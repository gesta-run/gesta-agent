package app

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/gesta-run/gesta-agent/pkg/daemon"
	"github.com/gesta-run/gesta-agent/pkg/model"
	"github.com/gesta-run/gesta-agent/pkg/policy"
	"github.com/gesta-run/gesta-agent/pkg/privacy"
	"github.com/gesta-run/gesta-agent/pkg/util"
)

const gestaSensitivePromptDeniedMessage = "Gesta blocked this prompt because it appears to contain secret material. Remove secrets or replace them with placeholders, then retry."
const hookSensitiveRulesCacheMaxAge = time.Minute

type agentHookEvent struct {
	HookEventName  string                 `json:"hook_event_name"`
	Prompt         string                 `json:"prompt"`
	ToolName       string                 `json:"tool_name"`
	ToolInput      map[string]interface{} `json:"tool_input"`
	SessionID      string                 `json:"session_id"`
	ConversationID string                 `json:"conversation_id"`
	TurnID         string                 `json:"turn_id"`
	CWD            string                 `json:"cwd"`
	Model          string                 `json:"model"`
	TranscriptPath string                 `json:"transcript_path"`
	PermissionMode string                 `json:"permission_mode"`
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

// processCodexHook is a thin wrapper kept for backward compatibility with tests.
func processCodexHook(ctx context.Context, data []byte) map[string]interface{} {
	return processAgentHook(ctx, data, "codex", "codex")
}

func processAgentHook(ctx context.Context, data []byte, agentType, source string) map[string]interface{} {
	var event agentHookEvent
	if err := json.Unmarshal(data, &event); err != nil {
		return map[string]interface{}{}
	}
	switch event.HookEventName {
	case "SessionStart":
		cfg, _ := guardConfig()
		captureOutputBaselineBestEffort(ctx, cfg, event)
		return map[string]interface{}{}
	case "UserPromptSubmit":
		return processUserPromptSubmit(ctx, event, agentType, source)
	case "PreToolUse":
		return processPreToolUse(ctx, event, agentType, source)
	default:
		return map[string]interface{}{}
	}
}

func processUserPromptSubmit(ctx context.Context, event agentHookEvent, agentType, source string) map[string]interface{} {
	if strings.TrimSpace(event.Prompt) == "" {
		return map[string]interface{}{}
	}

	cfg, shouldFlush := guardConfig()
	captureOutputBaselineBestEffort(ctx, cfg, event)
	var findings []privacy.SensitiveFinding
	if rules, ok := hookSensitiveRules(ctx, cfg, shouldFlush); ok {
		findings = privacy.DetectSensitiveTextWithRules(event.Prompt, sensitiveFingerprintKey(cfg), rules)
	}
	if len(findings) == 0 {
		return map[string]interface{}{}
	}

	recordSensitiveFindingsBestEffortWithConfig(cfg, shouldFlush, event, findings, source, agentType)
	if !sensitiveFindingsShouldBlock(findings) {
		return map[string]interface{}{}
	}
	return map[string]interface{}{
		"decision": "block",
		"reason":   gestaSensitivePromptDeniedMessage,
	}
}

func processPreToolUse(ctx context.Context, event agentHookEvent, agentType, source string) map[string]interface{} {
	_ = source
	cfg, shouldFlush := guardConfig()
	captureOutputBaselineBestEffort(ctx, cfg, event)
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

func captureOutputBaselineBestEffort(ctx context.Context, cfg daemon.Config, event agentHookEvent) {
	sessionID := firstNonEmpty(event.SessionID, event.ConversationID)
	cwd := strings.TrimSpace(event.CWD)
	if sessionID == "" || cwd == "" {
		return
	}
	if err := daemon.CaptureOutputBaseline(ctx, cfg, cwd, util.ShortHash(sessionID)); err != nil {
		fmt.Fprintf(os.Stderr, "gesta-agent hook: output baseline was not captured: %v\n", err)
	}
}

func sensitiveFindingsShouldBlock(findings []privacy.SensitiveFinding) bool {
	for _, finding := range findings {
		if strings.EqualFold(strings.TrimSpace(finding.Action), "block") {
			return true
		}
	}
	return false
}

func recordSensitiveFindingsBestEffortWithConfig(cfg daemon.Config, shouldFlush bool, hookEvent agentHookEvent, findings []privacy.SensitiveFinding, source, agentType string) {
	if err := recordSensitiveFindingsWithConfig(cfg, shouldFlush, hookEvent, findings, source, agentType); err != nil {
		fmt.Fprintf(os.Stderr, "gesta-agent hook: sensitive finding was not recorded: %v\n", err)
	}
}

func recordSensitiveFindingsWithConfig(cfg daemon.Config, shouldFlush bool, hookEvent agentHookEvent, findings []privacy.SensitiveFinding, source, agentType string) error {
	if len(findings) == 0 {
		return nil
	}
	events := make([]model.EventEnvelope, 0, len(findings))
	for _, finding := range findings {
		payload := map[string]interface{}{
			"source":             "user_prompt",
			"hook_event_name":    hookEvent.HookEventName,
			"category":           finding.Category,
			"severity":           finding.Severity,
			"confidence":         finding.Confidence,
			"fingerprint":        finding.Fingerprint,
			"sample":             finding.Sample,
			"sample_mode":        finding.SampleMode,
			"action":             firstNonEmpty(finding.Action, "block"),
			"metadata_only":      finding.Sample == "",
			"raw_content_stored": finding.SampleMode == "original" && finding.Sample != "",
		}
		if finding.RuleID != "" {
			payload["rule_id"] = finding.RuleID
		}
		if finding.RuleName != "" {
			payload["rule_name"] = finding.RuleName
		}
		if sessionID := firstNonEmpty(hookEvent.SessionID, hookEvent.ConversationID); sessionID != "" {
			payload["session_id_hash"] = util.ShortHash(sessionID)
			payload["session_id_is_hashed"] = true
		}
		if hookEvent.TurnID != "" {
			payload["turn_id_hash"] = util.ShortHash(hookEvent.TurnID)
		}
		if hookEvent.CWD != "" {
			payload["cwd_hash"] = util.ShortHash(hookEvent.CWD)
		}
		if hookEvent.Model != "" {
			payload["model"] = privacy.RedactAndTruncate(hookEvent.Model, 128)
		}

		events = append(events, model.EventEnvelope{
			EventID:      util.NewID("evt"),
			CustomerID:   cfg.CustomerID,
			DeploymentID: cfg.DeploymentID,
			DaemonID:     cfg.DaemonID,
			DeviceID:     cfg.DeviceID,
			UserID:       cfg.UserID,
			UserName:     cfg.EffectiveUserName(),
			TeamID:       cfg.TeamID,
			EventType:    "sensitive.finding",
			Source:       source,
			AgentType:    agentType,
			CreatedAt:    time.Now().UTC(),
			Payload:      payload,
		})
	}
	queue := daemon.NewQueue(cfg.DataDir)
	if err := queue.Append(events); err != nil {
		return fmt.Errorf("queue sensitive finding: %w", err)
	}
	if !shouldFlush {
		return nil
	}
	client := daemon.NewClient(cfg.EffectiveServerURL(), cfg.Token)
	if err := queue.Drain(client.SendEvents); err != nil {
		fmt.Fprintf(os.Stderr, "gesta-agent hook: queued sensitive finding locally; flush failed: %v\n", err)
		return nil
	}
	return nil
}

func sensitiveFingerprintKey(cfg daemon.Config) string {
	for _, candidate := range []string{
		cfg.Token,
		cfg.APIKey,
		cfg.DaemonID,
		cfg.DeviceID,
		cfg.UserID,
		cfg.DataDir,
	} {
		if strings.TrimSpace(candidate) != "" {
			return candidate
		}
	}
	return "gesta-local-sensitive-finding"
}

func consumeApprovedPolicyGrant(ctx context.Context, cfg daemon.Config, evaluation policy.Evaluation) (model.PolicyApproval, bool) {
	if cfg.Token == "" || cfg.EffectiveServerURL() == "" || evaluation.CommandHash == "" {
		return model.PolicyApproval{}, false
	}
	client := daemon.NewClient(cfg.EffectiveServerURL(), cfg.Token)
	type result struct {
		resp model.PolicyApprovalResolveResponse
		err  error
	}
	ch := make(chan result, 1)
	go func() {
		resp, err := client.ConsumePolicyApproval(model.ConsumePolicyApprovalRequest{
			DaemonID:    cfg.DaemonID,
			DeviceID:    cfg.DeviceID,
			UserID:      cfg.UserID,
			UserName:    cfg.EffectiveUserName(),
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
	client := daemon.NewClient(cfg.EffectiveServerURL(), cfg.Token)
	type result struct {
		approval model.PolicyApproval
		err      error
	}
	ch := make(chan result, 1)
	go func() {
		approval, err := client.CreatePolicyApproval(model.CreatePolicyApprovalRequest{
			DaemonID:       cfg.DaemonID,
			DeviceID:       cfg.DeviceID,
			UserID:         cfg.UserID,
			UserName:       cfg.EffectiveUserName(),
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
	if cached, err := daemon.LoadPolicyCache(cfg.DataDir); err == nil {
		return cached.Rules, true
	}
	if !shouldFetch {
		return nil, false
	}
	client := daemon.NewClient(cfg.EffectiveServerURL(), cfg.Token)
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

func hookSensitiveRules(ctx context.Context, cfg daemon.Config, shouldFetch bool) ([]model.SensitiveRule, bool) {
	var cachedRules []model.SensitiveRule
	hasCachedRules := false
	if cached, err := daemon.LoadSensitiveRuleCache(cfg.DataDir); err == nil {
		cachedRules = cached.Rules
		hasCachedRules = true
		if !shouldFetch || !sensitiveRuleCacheExpired(cached, time.Now()) {
			return cached.Rules, true
		}
	}
	if !shouldFetch {
		return nil, false
	}
	rules, ok := fetchHookSensitiveRules(ctx, cfg)
	if !ok {
		if hasCachedRules {
			return cachedRules, true
		}
		return nil, false
	}
	if err := daemon.SaveSensitiveRuleCache(cfg.DataDir, rules, time.Now().UTC()); err != nil {
		fmt.Fprintf(os.Stderr, "gesta-agent hook: sensitive rules cache was not updated: %v\n", err)
	}
	return rules, true
}

func sensitiveRuleCacheExpired(cache daemon.SensitiveRuleCache, now time.Time) bool {
	if cache.SyncedAt.IsZero() {
		return true
	}
	if now.IsZero() {
		now = time.Now()
	}
	if cache.SyncedAt.After(now) {
		return false
	}
	return now.Sub(cache.SyncedAt) >= hookSensitiveRulesCacheMaxAge
}

func fetchHookSensitiveRules(ctx context.Context, cfg daemon.Config) ([]model.SensitiveRule, bool) {
	client := daemon.NewClient(cfg.EffectiveServerURL(), cfg.Token)
	type result struct {
		rules []model.SensitiveRule
		err   error
	}
	ch := make(chan result, 1)
	go func() {
		rules, err := client.SensitiveRules()
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
	if path := firstStringField(event.ToolInput, "file_path", "path"); path != "" {
		return []string{toolName, path}
	}
	return []string{toolName, compactJSON(event.ToolInput)}
}

func hookShellCommand(event agentHookEvent) string {
	switch normalizeToolName(event.ToolName) {
	case "bash", "shell", "exec_command", "functions.exec_command":
		if command := stringField(event.ToolInput, "command"); command != "" {
			return command
		}
		return stringField(event.ToolInput, "cmd")
	default:
		return ""
	}
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
