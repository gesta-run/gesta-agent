package daemon

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"time"

	"github.com/gesta-run/gesta-agent/pkg/model"
	"github.com/gesta-run/gesta-agent/pkg/privacy"
	turnusage "github.com/gesta-run/gesta-agent/pkg/turn"
	"github.com/gesta-run/gesta-agent/pkg/util"
)

func collectCodexStateEvents(ctx context.Context, cfg Config, stateDB string, observedAt time.Time) ([]model.EventEnvelope, []func() error) {
	aggregate := map[string]interface{}{
		"state_db_present": stateDB != "",
	}
	var events []model.EventEnvelope
	var commits []func() error
	var counterResets []turnusage.CounterReset
	var usageEvents, transcriptEvents []map[string]interface{}
	var stateSessions []turnusage.CodexSession
	stateReadOK := stateDB == ""
	if stateDB != "" {
		var err error
		aggregate, usageEvents, transcriptEvents, stateSessions, err = readCodexState(ctx, stateDB)
		if err != nil {
			aggregate = map[string]interface{}{
				"state_db_present": true,
				"state_db_hash":    util.ShortHash(stateDB),
			}
			events = append(events, codexWarningEvent(cfg, stateDB, "state_db", err))
		} else {
			stateReadOK = true
		}
	}
	aggregate["transcript_fallback"] = true

	discoveredSessions, discoveryErr := discoverCodexTurnSessions(defaultCodexSessionsRoot())
	if discoveryErr != nil {
		events = append(events, codexWarningEvent(cfg, stateDB, "turn_discovery", discoveryErr))
	}
	aggregate["transcript_fallback_sessions"] = len(discoveredSessions)
	turnSessions, invalidTurnIdentity := filterInvalidCodexTurnSessions(mergeCodexTurnSessions(stateSessions, discoveredSessions))
	if invalidTurnIdentity {
		events = append(events, codexWarningEvent(cfg, stateDB, "turn_identity", errors.New("ignored codex session with self-referential fork parent")))
	}
	var transcriptFallbacks int
	transcriptEvents, transcriptFallbacks = mergeCodexTranscriptFallbacks(transcriptEvents, turnSessions)
	aggregate["transcript_fallback_payloads"] = transcriptFallbacks
	turnEvents, turnCommit, turnErr := turnusage.CollectCodex(turnusage.Config{
		DataDir:       cfg.DataDir,
		DaemonID:      cfg.DaemonID,
		TotalEncoding: cfg.TurnUsageTotal,
		OnCounterReset: func(reset turnusage.CounterReset) {
			counterResets = append(counterResets, reset)
		},
	}, turnSessions, observedAt)
	if turnErr != nil {
		events = append(events, codexWarningEvent(cfg, stateDB, "turn_usage", turnErr))
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
	for _, reset := range counterResets {
		events = append(events, snapshotEvent(cfg, "adapter.warning", "codex", "codex", map[string]interface{}{
			"scope":           "turn_usage_counter_reset",
			"session_id_hash": reset.SessionIDHash,
			"turn_id_hash":    reset.TurnIDHash,
		}))
	}

	baselineSource := codexBaselineSourceForCollection(stateDB, stateReadOK, transcriptFallbacks, transcriptEvents)
	if baselineSource != "" {
		baselineResult, baselineErr := filterCodexSessionBackfill(cfg, baselineSource, usageEvents, transcriptEvents, observedAt)
		for key, value := range baselineResult.Meta {
			aggregate[key] = value
		}
		if baselineErr != nil {
			events = append(events, codexWarningEvent(cfg, stateDB, "session_baseline", baselineErr))
			transcriptEvents = nil
		} else {
			transcriptEvents = baselineResult.TranscriptEvents
			if baselineResult.Commit != nil {
				commits = append(commits, baselineResult.Commit)
			}
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
	return events, commits
}

func codexBaselineSourceForCollection(
	stateDB string,
	stateReadOK bool,
	transcriptFallbacks int,
	transcriptEvents []map[string]interface{},
) string {
	if !stateReadOK && transcriptFallbacks == 0 {
		return ""
	}
	if stateDB != "" {
		return stateDB
	}
	if len(transcriptEvents) > 0 {
		return codexRolloutBaselineSource
	}
	return ""
}

func codexWarningEvent(cfg Config, stateDB, scope string, err error) model.EventEnvelope {
	payload := map[string]interface{}{
		"error": privacy.RedactAndTruncate(err.Error(), 2048),
		"scope": scope,
	}
	if stateDB != "" {
		payload["state_db_hash"] = util.ShortHash(stateDB)
	}
	return snapshotEvent(cfg, "adapter.warning", "codex", "codex", payload)
}

func mergeCodexTurnSessions(stateSessions, discoveredSessions []turnusage.CodexSession) []turnusage.CodexSession {
	byID := make(map[string]turnusage.CodexSession, len(stateSessions)+len(discoveredSessions))
	byRollout := make(map[string]string, len(stateSessions)+len(discoveredSessions))
	for _, session := range stateSessions {
		byID[session.SessionID] = session
		if key := codexRolloutKey(session.RolloutPath); key != "" {
			byRollout[key] = session.SessionID
		}
	}
	for _, discovered := range discoveredSessions {
		existingID := discovered.SessionID
		existing, ok := byID[existingID]
		if !ok {
			existingID, ok = byRollout[codexRolloutKey(discovered.RolloutPath)]
			existing = byID[existingID]
		}
		if ok {
			delete(byID, existingID)
			delete(byRollout, codexRolloutKey(existing.RolloutPath))
			discovered = mergeCodexTurnSession(existing, discovered)
		}
		byID[discovered.SessionID] = discovered
		if key := codexRolloutKey(discovered.RolloutPath); key != "" {
			byRollout[key] = discovered.SessionID
		}
	}
	sessions := make([]turnusage.CodexSession, 0, len(byID))
	for _, session := range byID {
		sessions = append(sessions, session)
	}
	return sessions
}

func mergeCodexTurnSession(state, discovered turnusage.CodexSession) turnusage.CodexSession {
	if state.SessionID == discovered.SessionID {
		if discovered.LegacySessionID == "" {
			discovered.LegacySessionID = state.LegacySessionID
		}
	} else if state.LegacySessionID == discovered.SessionID {
		discovered.SessionID = state.SessionID
		discovered.LegacySessionID = state.LegacySessionID
	}
	if discovered.ParentSessionID == "" {
		discovered.ParentSessionID = state.ParentSessionID
	}
	if discovered.Title == "" {
		discovered.Title = state.Title
	}
	if discovered.Model == "" {
		discovered.Model = state.Model
	}
	if discovered.Repo == "" {
		discovered.Repo = state.Repo
	}
	if discovered.ModelProvider == "" {
		discovered.ModelProvider = state.ModelProvider
	}
	return discovered
}

func codexRolloutKey(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	return filepath.Clean(path)
}

func filterInvalidCodexTurnSessions(sessions []turnusage.CodexSession) ([]turnusage.CodexSession, bool) {
	filtered := make([]turnusage.CodexSession, 0, len(sessions))
	invalid := false
	for _, session := range sessions {
		if session.SessionID != "" && session.ParentSessionID == session.SessionID {
			invalid = true
			continue
		}
		filtered = append(filtered, session)
	}
	return filtered, invalid
}
