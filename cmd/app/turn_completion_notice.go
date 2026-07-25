package app

import (
	"context"
	"fmt"
	"os"
	"strings"

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

func recordTurnPolicyMatchesBestEffort(
	cfg daemon.Config,
	event agentHookEvent,
	agentType string,
	result contextmatch.Result,
) {
	count := 0
	for _, rule := range result.Rules {
		if !strings.EqualFold(strings.TrimSpace(rule.MatchType), "always") {
			count++
		}
	}
	if count == 0 {
		return
	}
	sessionID, turnID := turnReceiptIdentity(event)
	if err := turnreceipt.NewStore(cfg.DataDir).RecordPolicyMatches(
		agentType,
		sessionID,
		turnID,
		count,
	); err != nil {
		fmt.Fprintf(os.Stderr, "gesta-agent hook: policy match receipt was not recorded: %v\n", err)
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
	message := formatTurnCompletionNotice(receipt)
	if message == "" {
		return map[string]interface{}{}
	}
	if err := turnreceipt.NewStore(cfg.DataDir).SavePending(
		agentType,
		sessionID,
		message,
	); err != nil {
		fmt.Fprintf(os.Stderr, "gesta-agent hook: pending turn notice was not saved: %v\n", err)
	}
	return map[string]interface{}{}
}

func injectPendingTurnNoticeBestEffort(
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
	return mergeUserPromptAdditionalContext(response, pendingTurnNoticeContext(pending.Notice))
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
