package app

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/gesta-run/gesta-agent/pkg/codexapp"
	"github.com/gesta-run/gesta-agent/pkg/daemon"
	"github.com/gesta-run/gesta-agent/pkg/model"
	"github.com/gesta-run/gesta-agent/pkg/privacy"
	"github.com/gesta-run/gesta-agent/pkg/toolinput"
	"github.com/gesta-run/gesta-agent/pkg/util"
)

var readCodexTurn = codexapp.ReadTurn

func recordGrossToolUseBestEffortWithConfig(cfg daemon.Config, event agentHookEvent, agentType, source string) {
	if err := recordGrossToolUseWithConfig(cfg, event, agentType, source); err != nil {
		fmt.Fprintf(os.Stderr, "gesta-agent hook: output was not recorded: %v\n", err)
	}
}

func recordGrossToolUseWithConfig(cfg daemon.Config, event agentHookEvent, agentType, source string) error {
	var measurements []toolinput.Measurement
	switch agentType {
	case "claude_code":
		measurements = toolinput.MeasureClaudeToolUse(event.ToolName, event.ToolInput)
	default:
		return nil
	}
	if len(measurements) == 0 {
		return nil
	}
	return appendGrossMeasurements(cfg, grossObservation{
		CallID:       event.ToolUseID,
		SessionID:    firstNonEmpty(event.SessionID, event.ConversationID),
		TurnID:       event.TurnID,
		ToolName:     event.ToolName,
		AgentType:    agentType,
		Source:       source,
		Origin:       "post_tool_use",
		ObservedAt:   time.Now().UTC(),
		Measurements: measurements,
	})
}

func recordCodexTurnBestEffort(ctx context.Context, cfg daemon.Config, event agentHookEvent) {
	if err := recordCodexTurn(ctx, cfg, event); err != nil {
		fmt.Fprintf(os.Stderr, "gesta-agent hook: Codex turn output was not recorded: %v\n", err)
	}
}

func recordCodexTurn(ctx context.Context, cfg daemon.Config, event agentHookEvent) error {
	sessionID := firstNonEmpty(event.SessionID, event.ConversationID)
	turnID := strings.TrimSpace(event.TurnID)
	if strings.TrimSpace(sessionID) == "" || turnID == "" {
		return nil
	}
	turn, err := readCodexTurn(ctx, sessionID, turnID)
	if err != nil {
		return err
	}
	observedAt := time.Now().UTC()
	if turn.CompletedAt != nil && *turn.CompletedAt > 0 {
		observedAt = time.Unix(*turn.CompletedAt, 0).UTC()
	}
	for _, item := range turn.Items {
		if item.Status != "completed" {
			continue
		}
		switch item.Type {
		case "fileChange":
			changes := make([]toolinput.FileChange, 0, len(item.Changes))
			for _, change := range item.Changes {
				changes = append(changes, toolinput.FileChange{Path: change.Path, Kind: change.Kind.Type, Diff: change.Diff})
			}
			if err := appendGrossMeasurements(cfg, grossObservation{
				CallID:       item.ID,
				SessionID:    sessionID,
				TurnID:       turnID,
				ToolName:     "fileChange",
				AgentType:    "codex",
				Source:       "codex",
				Origin:       "thread_read_file_change",
				ObservedAt:   observedAt,
				Measurements: toolinput.MeasureCodexFileChanges(changes),
			}); err != nil {
				return err
			}
		case "mcpToolCall":
			toolName := "mcp__" + strings.TrimSpace(item.Server) + "__" + strings.TrimSpace(item.Tool)
			if err := appendGrossMeasurements(cfg, grossObservation{
				CallID:       item.ID,
				SessionID:    sessionID,
				TurnID:       turnID,
				ToolName:     toolName,
				AgentType:    "codex",
				Source:       "codex",
				Origin:       "thread_read_mcp_reconciliation",
				ObservedAt:   observedAt,
				Measurements: toolinput.MeasureCodexMCP(toolName, item.Arguments),
			}); err != nil {
				return err
			}
		}
	}
	return nil
}

type grossObservation struct {
	CallID       string
	SessionID    string
	TurnID       string
	ToolName     string
	AgentType    string
	Source       string
	Origin       string
	ObservedAt   time.Time
	Measurements []toolinput.Measurement
}

func appendGrossMeasurements(cfg daemon.Config, observation grossObservation) error {
	callID := strings.TrimSpace(observation.CallID)
	sessionID := strings.TrimSpace(observation.SessionID)
	turnID := strings.TrimSpace(observation.TurnID)
	if callID == "" || sessionID == "" || len(observation.Measurements) == 0 {
		return nil
	}
	if observation.AgentType == "codex" && turnID == "" {
		return nil
	}
	createdAt := observation.ObservedAt.UTC()
	if createdAt.IsZero() {
		createdAt = time.Now().UTC()
	}
	events := make([]model.EventEnvelope, 0, len(observation.Measurements))
	for _, measurement := range observation.Measurements {
		targetHash := ""
		if strings.TrimSpace(measurement.Target) != "" {
			targetHash = util.ShortHash(measurement.Target)
		}
		identity := strings.Join([]string{
			"tool.input.gross.v2",
			cfg.DaemonID,
			observation.Source,
			sessionID,
			turnID,
			callID,
			measurement.Category,
			targetHash,
		}, "\x00")
		payload := map[string]interface{}{
			"schema_version":   2,
			"call_id_hash":     util.ShortHash(callID),
			"tool_name":        privacy.RedactAndTruncate(strings.TrimSpace(observation.ToolName), 256),
			"tool_class":       measurement.ToolClass,
			"category":         measurement.Category,
			"characters":       measurement.Counts.Characters,
			"lines":            measurement.Counts.Lines,
			"words":            measurement.Counts.Words,
			"raw_input_stored": false,
			"origin":           observation.Origin,
			"observed_at":      createdAt.Format(time.RFC3339Nano),
			"session_id":       util.ShortHash(sessionID),
		}
		if turnID != "" {
			payload["turn_id_hash"] = util.ShortHash(turnID)
		}
		if targetHash != "" {
			payload["target_path_hash"] = targetHash
		}
		events = append(events, model.EventEnvelope{
			EventID:      "evt_" + util.ShortHash(identity),
			CustomerID:   cfg.CustomerID,
			DeploymentID: cfg.DeploymentID,
			DaemonID:     cfg.DaemonID,
			DeviceID:     cfg.DeviceID,
			TeamID:       cfg.TeamID,
			EventType:    "tool.input.gross",
			Source:       observation.Source,
			AgentType:    observation.AgentType,
			CreatedAt:    createdAt,
			Payload:      payload,
		})
	}
	if err := daemon.NewQueue(cfg.DataDir).Append(events); err != nil {
		return fmt.Errorf("queue Gross Ink: %w", err)
	}
	return nil
}
