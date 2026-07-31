package agent

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/gesta-run/gesta-agent/pkg/controlclient"
	"github.com/gesta-run/gesta-agent/pkg/daemon"
	"github.com/gesta-run/gesta-agent/pkg/model"
	"github.com/gesta-run/gesta-agent/pkg/policy"
	"github.com/gesta-run/gesta-agent/pkg/promptscope"
	"github.com/gesta-run/gesta-agent/pkg/rulecache"
)

const gestaSensitivePromptDeniedMessage = "Gesta blocked this prompt because it appears to contain secret material. Remove secrets or replace them with placeholders, then retry."

type agentHookEvent struct {
	HookEventName  string      `json:"hook_event_name"`
	Prompt         string      `json:"prompt"`
	ToolName       string      `json:"tool_name"`
	ToolInput      interface{} `json:"tool_input"`
	ToolUseID      string      `json:"tool_use_id"`
	SessionID      string      `json:"session_id"`
	ConversationID string      `json:"conversation_id"`
	TurnID         string      `json:"turn_id"`
	CWD            string      `json:"cwd"`
	Model          string      `json:"model"`
	TranscriptPath string      `json:"transcript_path"`
	PermissionMode string      `json:"permission_mode"`
}

func codexHook(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("codex-hook", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	if err := fs.Parse(args); err != nil {
		return err
	}
	data, err := io.ReadAll(os.Stdin)
	if err != nil {
		return err
	}
	response := processAgentHook(ctx, data, "codex", "codex")
	return json.NewEncoder(os.Stdout).Encode(response)
}

func claudeHook(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("claude-hook", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	if err := fs.Parse(args); err != nil {
		return err
	}
	data, err := io.ReadAll(os.Stdin)
	if err != nil {
		return err
	}
	response := processAgentHook(ctx, data, "claude_code", "claude_code")
	return json.NewEncoder(os.Stdout).Encode(response)
}

func processAgentHook(ctx context.Context, data []byte, agentType, source string) map[string]interface{} {
	var event agentHookEvent
	if err := json.Unmarshal(data, &event); err != nil {
		return map[string]interface{}{}
	}
	switch event.HookEventName {
	case "SessionStart":
		cfg, _ := guardConfig()
		cleanupTurnReceiptsBestEffort(cfg)
		return map[string]interface{}{}
	case "UserPromptSubmit":
		return processUserPromptSubmit(ctx, event, agentType, source)
	case "PreToolUse":
		return processPreToolUse(ctx, event, agentType, source)
	case "PostToolUse":
		if agentType == "claude_code" {
			cfg, _ := guardConfig()
			recordGrossToolUseBestEffortWithConfig(cfg, event, agentType, source)
		}
		return map[string]interface{}{}
	case "Stop":
		return processTurnStop(ctx, event, agentType)
	default:
		return map[string]interface{}{}
	}
}

func processUserPromptSubmit(ctx context.Context, event agentHookEvent, agentType, source string) map[string]interface{} {
	if strings.TrimSpace(event.Prompt) == "" {
		return map[string]interface{}{}
	}

	cfg, _ := guardConfig()
	beginTurnReceiptBestEffort(cfg, event, agentType)
	findings := detectSensitivePrompt(cfg, event.Prompt)
	if len(findings) > 0 {
		recordSensitiveFindingsBestEffortWithConfig(cfg, event, findings, source, agentType)
		if sensitiveFindingsShouldBlock(findings) {
			return map[string]interface{}{
				"decision": "block",
				"reason":   gestaSensitivePromptDeniedMessage,
			}
		}
	}
	matchPrompt := promptscope.Extract(agentType, event.Prompt)
	response := processOrganizationContext(cfg, event, matchPrompt, agentType, source)
	return injectPendingTurnNoticeBestEffort(ctx, cfg, event, agentType, response)
}

func processPreToolUse(ctx context.Context, event agentHookEvent, agentType, source string) map[string]interface{} {
	cfg, shouldFlush := guardConfig()
	if !hookEventIsShellCommand(event) {
		return map[string]interface{}{}
	}

	evaluation := evaluateHookEvent(ctx, event, cfg, shouldFlush, agentType)
	if evaluation.Decision == policy.DecisionAllow {
		if len(evaluation.RuleIDs) > 0 {
			recordGuardDecisionBestEffortWithConfig(cfg, shouldFlush, evaluation, false, 0)
		}
		return map[string]interface{}{}
	}
	if evaluation.Decision == policy.DecisionWarn {
		recordGuardDecisionBestEffortWithConfig(cfg, shouldFlush, evaluation, false, 0)
		return map[string]interface{}{}
	}
	if evaluation.Decision == policy.DecisionApproval {
		if _, approved := consumeApprovedPolicyGrant(ctx, cfg, evaluation); approved {
			approvedEvaluation := evaluation
			approvedEvaluation.Decision = policy.DecisionAllow
			approvedEvaluation.Reason = "approved by Gesta"
			recordGuardDecisionBestEffortWithConfig(cfg, shouldFlush, approvedEvaluation, false, 0)
			return map[string]interface{}{}
		}
	}

	exitCode := GuardBlockedExitCode
	if evaluation.Decision == policy.DecisionApproval {
		exitCode = GuardApprovalExitCode
	}
	recordGuardDecisionBestEffortWithConfig(cfg, shouldFlush, evaluation, false, exitCode)

	reason := gestaHighRiskCommandDeniedMessage
	if evaluation.Decision == policy.DecisionApproval {
		reason = gestaHighRiskCommandApprovalMessage
		if approval, ok := createPolicyApprovalRequest(ctx, cfg, evaluation); ok && approval.ApprovalID != "" {
			reason = fmt.Sprintf("%s. Approval request: %s. Approve it in Gesta, then retry the command.", reason, approval.ApprovalID)
		}
	}

	return map[string]interface{}{
		"hookSpecificOutput": map[string]interface{}{
			"hookEventName":            "PreToolUse",
			"permissionDecision":       "deny",
			"permissionDecisionReason": reason,
			"additionalContext":        reason,
		},
	}
}

func consumeApprovedPolicyGrant(ctx context.Context, cfg daemon.Config, evaluation policy.Evaluation) (model.PolicyApproval, bool) {
	if cfg.Token == "" || cfg.EffectiveServerURL() == "" || evaluation.CommandHash == "" {
		return model.PolicyApproval{}, false
	}
	client := controlclient.NewClient(cfg.EffectiveServerURL(), cfg.Token)
	type result struct {
		resp model.PolicyApprovalResolveResponse
		err  error
	}
	ch := make(chan result, 1)
	go func() {
		resp, err := client.ConsumePolicyApproval(model.ConsumePolicyApprovalRequest{
			DaemonID:    cfg.DaemonID,
			DeviceID:    cfg.DeviceID,
			TeamID:      cfg.TeamID,
			AgentType:   evaluation.AgentType,
			CommandHash: evaluation.CommandHash,
		})
		ch <- result{resp: resp, err: err}
	}()
	select {
	case <-ctx.Done():
		return model.PolicyApproval{}, false
	case <-time.After(3 * time.Second):
		return model.PolicyApproval{}, false
	case res := <-ch:
		if res.err != nil || !res.resp.Approved || res.resp.Approval == nil {
			return model.PolicyApproval{}, false
		}
		return *res.resp.Approval, true
	}
}

func createPolicyApprovalRequest(ctx context.Context, cfg daemon.Config, evaluation policy.Evaluation) (model.PolicyApproval, bool) {
	if cfg.Token == "" || cfg.EffectiveServerURL() == "" || evaluation.CommandHash == "" {
		return model.PolicyApproval{}, false
	}
	client := controlclient.NewClient(cfg.EffectiveServerURL(), cfg.Token)
	type result struct {
		approval model.PolicyApproval
		err      error
	}
	ch := make(chan result, 1)
	go func() {
		approval, err := client.CreatePolicyApproval(model.CreatePolicyApprovalRequest{
			DaemonID:       cfg.DaemonID,
			DeviceID:       cfg.DeviceID,
			TeamID:         cfg.TeamID,
			AgentType:      evaluation.AgentType,
			CommandHash:    evaluation.CommandHash,
			CommandPreview: evaluation.CommandPreview,
			RuleIDs:        evaluation.RuleIDs,
			Reason:         evaluation.Reason,
		})
		ch <- result{approval: approval, err: err}
	}()
	select {
	case <-ctx.Done():
		return model.PolicyApproval{}, false
	case <-time.After(3 * time.Second):
		return model.PolicyApproval{}, false
	case res := <-ch:
		return res.approval, res.err == nil
	}
}

func evaluateHookEvent(ctx context.Context, event agentHookEvent, cfg daemon.Config, shouldFetch bool, agentType string) policy.Evaluation {
	args := hookEventArgs(event)
	rules, ok := hookPolicyRules(ctx, cfg, shouldFetch)
	if ok {
		return policy.EvaluateCommandWithRules(agentType, args, rules)
	}
	return policy.EvaluateCommand(agentType, args)
}

func hookEventIsShellCommand(event agentHookEvent) bool {
	return hookShellCommand(event) != ""
}

func hookPolicyRules(ctx context.Context, cfg daemon.Config, shouldFetch bool) ([]model.PolicyRule, bool) {
	if cached, err := rulecache.LoadPolicyCache(cfg.DataDir); err == nil {
		return cached.Rules, true
	}
	if !shouldFetch {
		return nil, false
	}
	client := controlclient.NewClient(cfg.EffectiveServerURL(), cfg.Token)
	type result struct {
		rules []model.PolicyRule
		err   error
	}
	ch := make(chan result, 1)
	go func() {
		rules, err := client.PolicyRules()
		ch <- result{rules: rules, err: err}
	}()
	select {
	case <-ctx.Done():
		return nil, false
	case <-time.After(3 * time.Second):
		return nil, false
	case res := <-ch:
		if res.err != nil {
			return nil, false
		}
		return res.rules, true
	}
}

func hookEventArgs(event agentHookEvent) []string {
	toolName := strings.TrimSpace(event.ToolName)
	if toolName == "" {
		toolName = "unknown_tool"
	}
	if command := hookShellCommand(event); command != "" {
		return []string{"sh", "-c", command}
	}
	if path := firstStringField(toolInputMap(event.ToolInput), "file_path", "path"); path != "" {
		return []string{toolName, path}
	}
	return []string{toolName, compactJSON(event.ToolInput)}
}

func hookShellCommand(event agentHookEvent) string {
	input := toolInputMap(event.ToolInput)
	switch normalizeToolName(event.ToolName) {
	case "bash", "shell", "exec_command", "functions.exec_command":
		if command := stringField(input, "command"); command != "" {
			return command
		}
		return stringField(input, "cmd")
	default:
		return ""
	}
}

func toolInputMap(value interface{}) map[string]interface{} {
	values, _ := value.(map[string]interface{})
	return values
}

func normalizeToolName(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

func firstStringField(values map[string]interface{}, keys ...string) string {
	for _, key := range keys {
		if value := stringField(values, key); value != "" {
			return value
		}
	}
	return ""
}

func stringField(values map[string]interface{}, key string) string {
	if values == nil {
		return ""
	}
	value, ok := values[key]
	if !ok {
		return ""
	}
	text, ok := value.(string)
	if !ok {
		return ""
	}
	return strings.TrimSpace(text)
}

func compactJSON(value interface{}) string {
	data, err := json.Marshal(value)
	if err != nil {
		return fmt.Sprint(value)
	}
	return string(data)
}
