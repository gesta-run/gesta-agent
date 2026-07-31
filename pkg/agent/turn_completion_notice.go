package agent

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/gesta-run/gesta-agent/pkg/activitydetail"
	"github.com/gesta-run/gesta-agent/pkg/contextmatch"
	"github.com/gesta-run/gesta-agent/pkg/daemon"
	"github.com/gesta-run/gesta-agent/pkg/localactivity"
	"github.com/gesta-run/gesta-agent/pkg/turnreceipt"
)

var localActivityUIHealthy = localactivity.Healthy

func beginTurnReceiptBestEffort(cfg daemon.Config, event agentHookEvent, agentType string) {
	sessionID, turnID := turnReceiptIdentity(event)
	if err := turnreceipt.NewStore(cfg.DataDir).Begin(agentType, sessionID, turnID); err != nil {
		fmt.Fprintf(os.Stderr, "gesta-agent hook: turn receipt was not started: %v\n", err)
	}
}

func recordTurnContextMatchesBestEffort(
	cfg daemon.Config,
	event agentHookEvent,
	agentType string,
	result contextmatch.Result,
) {
	matches := make([]turnreceipt.ContextRuleMatch, 0, len(result.Rules))
	for _, rule := range result.Rules {
		if strings.EqualFold(strings.TrimSpace(rule.MatchType), "always") {
			continue
		}
		matches = append(matches, turnreceipt.ContextRuleMatch{
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
	sessionID, turnID := turnReceiptIdentity(event)
	if err := turnreceipt.NewStore(cfg.DataDir).RecordContextMatches(
		agentType,
		sessionID,
		turnID,
		matches,
	); err != nil {
		fmt.Fprintf(os.Stderr, "gesta-agent hook: context match receipt was not recorded: %v\n", err)
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
		receipt,
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
	sessionID, _ := turnReceiptIdentity(event)
	pending, found, err := turnreceipt.NewStore(cfg.DataDir).ConsumePending(agentType, sessionID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "gesta-agent hook: pending turn notice was not consumed: %v\n", err)
		return response
	}
	if !found {
		return response
	}
	receipt := turnreceipt.Receipt{
		ContextMatches: pending.ContextMatches,
		Output:         pending.Output,
	}
	detailURL := ""
	if len(receipt.ContextMatches) > 0 && localActivityUIHealthy(ctx) {
		detail, detailErr := activitydetail.NewStore(cfg.DataDir).Create(
			agentType,
			receipt.ContextMatches,
			receipt.Output,
		)
		if detailErr != nil {
			fmt.Fprintf(os.Stderr, "gesta-agent hook: local activity detail was not saved: %v\n", detailErr)
		} else {
			detailURL = localactivity.ActivityURL(detail.ActivityID)
		}
	}
	message := formatTurnCompletionNoticeWithDetails(receipt, detailURL)
	if message == "" {
		return response
	}
	return mergeUserPromptAdditionalContext(response, pendingTurnNoticeContext(message))
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
