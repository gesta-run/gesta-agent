package agent

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/gesta-run/gesta-agent/pkg/activitydetail"
	"github.com/gesta-run/gesta-agent/pkg/contextmatch"
	"github.com/gesta-run/gesta-agent/pkg/daemon"
	"github.com/gesta-run/gesta-agent/pkg/turnreceipt"
)

func beginTurnReceiptBestEffort(cfg daemon.Config, event agentHookEvent, agentType string) {
	sessionID, turnID := turnReceiptIdentity(event)
	if err := turnreceipt.NewStore(cfg.DataDir).Begin(agentType, sessionID, turnID); err != nil {
		fmt.Fprintf(os.Stderr, "gesta-agent hook: turn receipt was not started: %v\n", err)
	}
}

func recordTurnContextMatchesBestEffort(
	cfg daemon.Config,
	event agentHookEvent,
	result contextmatch.Result,
) {
	matches := make([]activitydetail.ContextRuleMatch, 0, len(result.Rules))
	for _, rule := range result.Rules {
		if strings.EqualFold(strings.TrimSpace(rule.MatchType), "always") {
			continue
		}
		matches = append(matches, activitydetail.ContextRuleMatch{
			RuleID:    rule.RuleID,
			Name:      rule.Name,
			MatchType: rule.MatchType,
			Priority:  rule.Priority,
			Content:   strings.TrimSpace(rule.ContextContent),
		})
	}
	if len(matches) == 0 {
		return
	}
	if event.ActivityID != "" {
		if err := activitydetail.NewStore(cfg.DataDir).RecordContext(event.ActivityID, matches); err != nil {
			fmt.Fprintf(os.Stderr, "gesta-agent hook: current context activity was not recorded: %v\n", err)
		}
	}
}

func recordTurnOutputBestEffort(
	cfg daemon.Config,
	event agentHookEvent,
	agentType string,
	output turnreceipt.OutputSummary,
) {
	sessionID, turnID := turnReceiptIdentity(event)
	if err := turnreceipt.NewStore(cfg.DataDir).RecordOutput(
		agentType,
		sessionID,
		turnID,
		event.ToolUseID,
		output,
	); err != nil {
		fmt.Fprintf(os.Stderr, "gesta-agent hook: output completion receipt was not recorded: %v\n", err)
	}
}

func cleanupTurnReceiptsBestEffort(cfg daemon.Config) {
	if err := turnreceipt.NewStore(cfg.DataDir).CleanupExpired(); err != nil {
		fmt.Fprintf(os.Stderr, "gesta-agent hook: expired turn receipts were not cleaned: %v\n", err)
	}
	if err := activitydetail.NewStore(cfg.DataDir).Cleanup(); err != nil {
		fmt.Fprintf(os.Stderr, "gesta-agent hook: expired activity details were not cleaned: %v\n", err)
	}
}

func processTurnStop(
	ctx context.Context,
	event agentHookEvent,
	agentType string,
) map[string]interface{} {
	cfg, _ := guardConfig()
	var output turnreceipt.OutputSummary
	if agentType == "codex" {
		output = recordCodexTurnBestEffort(ctx, cfg, event)
	}

	sessionID, turnID := turnReceiptIdentity(event)
	receipt, found, err := turnreceipt.NewStore(cfg.DataDir).Consume(agentType, sessionID, turnID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "gesta-agent hook: turn receipt was not consumed: %v\n", err)
		return map[string]interface{}{}
	}
	if !found {
		return map[string]interface{}{}
	}
	receipt.Output.Add(output)
	if err := turnreceipt.NewStore(cfg.DataDir).SavePending(
		agentType,
		sessionID,
		receipt.Output,
	); err != nil {
		fmt.Fprintf(os.Stderr, "gesta-agent hook: pending turn notice was not saved: %v\n", err)
	}
	return map[string]interface{}{}
}

func injectPendingTurnNoticeBestEffort(
	ctx context.Context,
	cfg daemon.Config,
	event agentHookEvent,
	agentType string,
	response map[string]interface{},
) map[string]interface{} {
	contexts := make([]string, 0, 2)
	sessionID, _ := turnReceiptIdentity(event)
	pending, found, err := turnreceipt.NewStore(cfg.DataDir).ConsumePending(agentType, sessionID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "gesta-agent hook: pending turn notice was not consumed: %v\n", err)
	} else if found && event.ActivityID != "" {
		if recordErr := activitydetail.NewStore(cfg.DataDir).RecordOutput(event.ActivityID, pending.Output); recordErr != nil {
			fmt.Fprintf(os.Stderr, "gesta-agent hook: previous output activity was not recorded: %v\n", recordErr)
		}
	}
	if event.ActivityID != "" {
		contexts = append(contexts, activityNoticeEndpointContext(event.ActivityID))
	}
	if message := dailyRecapNoticeBestEffort(cfg); message != "" {
		contexts = append(contexts, pendingTurnNoticeContext(message))
	}
	if len(contexts) == 0 {
		return response
	}
	return mergeUserPromptAdditionalContext(response, strings.Join(contexts, "\n\n"))
}

func mergeUserPromptAdditionalContext(
	response map[string]interface{},
	additionalContext string,
) map[string]interface{} {
	if response == nil {
		response = map[string]interface{}{}
	}
	output, ok := response["hookSpecificOutput"].(map[string]interface{})
	if !ok {
		output = map[string]interface{}{}
		response["hookSpecificOutput"] = output
	}
	output["hookEventName"] = "UserPromptSubmit"
	existing, _ := output["additionalContext"].(string)
	if existing = strings.TrimSpace(existing); existing != "" {
		output["additionalContext"] = existing + "\n\n" + additionalContext
	} else {
		output["additionalContext"] = additionalContext
	}
	return response
}

func turnReceiptIdentity(event agentHookEvent) (string, string) {
	return firstNonEmpty(event.SessionID, event.ConversationID), strings.TrimSpace(event.TurnID)
}
