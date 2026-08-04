package daemon

import (
	"context"
	"time"

	"github.com/gesta-run/gesta-agent/pkg/model"
	"github.com/gesta-run/gesta-agent/pkg/privacy"
	turnusage "github.com/gesta-run/gesta-agent/pkg/turn"
	"github.com/gesta-run/gesta-agent/pkg/util"
)

func collectCodexStateEvents(ctx context.Context, cfg Config, stateDB string, observedAt time.Time) ([]model.EventEnvelope, []func() error) {
	aggregate, usageEvents, transcriptEvents, turnSessions, err := readCodexState(ctx, stateDB)
	if err != nil {
		return []model.EventEnvelope{snapshotEvent(cfg, "adapter.warning", "codex", "codex", map[string]interface{}{
			"state_db_hash": util.ShortHash(stateDB),
			"error":         privacy.RedactAndTruncate(err.Error(), 2048),
		})}, nil
	}

	var events []model.EventEnvelope
	var commits []func() error
	turnEvents, turnCommit, turnErr := turnusage.CollectCodex(turnusage.Config{
		DataDir:  cfg.DataDir,
		DaemonID: cfg.DaemonID,
	}, turnSessions, observedAt)
	if turnErr != nil {
		events = append(events, snapshotEvent(cfg, "adapter.warning", "codex", "codex", map[string]interface{}{
			"state_db_hash": util.ShortHash(stateDB),
			"error":         privacy.RedactAndTruncate(turnErr.Error(), 2048),
			"scope":         "turn_usage",
		}))
	} else {
		for _, usage := range turnEvents {
			event := baseEvent(cfg, turnusage.EventType, "codex", "codex", usage.Payload())
			event.EventID = usage.EventID
			event.CreatedAt = usage.EndedAt
			events = append(events, event)
		}
		if turnCommit != nil {
			commits = append(commits, turnCommit)
		}
	}

	sensitiveRules := codexSensitiveRulesForCollection(cfg)
	sensitiveEvents := codexSensitiveFindingEventsFromTranscripts(cfg, transcriptEvents, sensitiveRules)
	baselineResult, baselineErr := filterCodexSessionBackfill(cfg, stateDB, usageEvents, transcriptEvents, observedAt)
	for key, value := range baselineResult.Meta {
		aggregate[key] = value
	}
	if baselineErr != nil {
		events = append(events, snapshotEvent(cfg, "adapter.warning", "codex", "codex", map[string]interface{}{
			"state_db_hash": util.ShortHash(stateDB),
			"error":         privacy.RedactAndTruncate(baselineErr.Error(), 2048),
			"scope":         "session_baseline",
		}))
		transcriptEvents = nil
	} else {
		transcriptEvents = baselineResult.TranscriptEvents
		if baselineResult.Commit != nil {
			commits = append(commits, baselineResult.Commit)
		}
	}

	events = append(events, snapshotEvent(cfg, "codex.usage_summary", "codex", "codex", aggregate))
	toolCallsCollected := map[string]struct{}{}
	for _, transcript := range transcriptEvents {
		sessionID := firstString(transcript, "session_id", "session_id_hash")
		if _, ok := toolCallsCollected[sessionID]; !ok {
			events = append(events, codexToolCallEventsFromTranscript(cfg, transcript)...)
			toolCallsCollected[sessionID] = struct{}{}
		}
		publicTranscript := codexPublicTranscriptPayload(transcript)
		event := baseEvent(cfg, transcriptChunkEventType, "codex", "codex", publicTranscript)
		event.EventID = transcriptChunkEventID(publicTranscript)
		events = append(events, event)
	}
	events = append(events, sensitiveEvents...)
	return events, commits
}
