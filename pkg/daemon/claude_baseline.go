package daemon

import (
	"time"
)

// filterClaudeSessionBaseline mirrors filterCodexSessionBackfill for Claude Code.
// It persists a per-session cursor/baseline in cfg.DataDir so repeated collection
// cycles and daemon restarts never double count.
//
//   - On the very first run it records every existing usage bucket + session as a
//     baseline and emits nothing (historical backfill is ignored, exactly like
//     Codex).
//   - On later runs it only emits buckets whose cumulative tokens advanced (new
//     model+day buckets or growth in an existing one) and sessions whose
//     transcript fingerprint changed. Each emitted usage summary is marked
//     cursor-only / initial-delta so the generic usage-delta machinery turns it
//     into exactly one incremental usage.delta.
//
// usageEvents are keyed per (session, model, day) bucket via session_id; session
// events are keyed per real session via session_id_hash.
func filterClaudeSessionBaseline(cfg Config, usageEvents, sessionEvents []map[string]interface{}, observedAt time.Time) ([]map[string]interface{}, []map[string]interface{}, map[string]interface{}, error) {
	if observedAt.IsZero() {
		observedAt = time.Now().UTC()
	}
	store, err := loadSessionBaselineStore(cfg.DataDir)
	if err != nil {
		return nil, nil, nil, err
	}
	baseline := store.ClaudeCode
	if baseline.Sessions == nil {
		baseline.Sessions = map[string]baselineSession{}
	}

	meta := map[string]interface{}{
		"session_backfill":         codexSessionBackfillModeValue,
		"session_baseline_enabled": true,
		"usage_summary_mode":       "cursor_only",
	}

	if baseline.InitializedAt == "" {
		for _, payload := range usageEvents {
			addBaselineSession(baseline.Sessions, payload)
		}
		for _, payload := range sessionEvents {
			addBaselineSession(baseline.Sessions, payload)
		}
		baseline.InitializedAt = observedAt.UTC().Format(time.RFC3339Nano)
		store.ClaudeCode = baseline
		if err := saveSessionBaselineStore(cfg.DataDir, store); err != nil {
			return nil, nil, nil, err
		}
		meta["session_baseline_initialized"] = true
		meta["historical_sessions_ignored"] = len(baseline.Sessions)
		return nil, nil, meta, nil
	}

	filteredUsage := make([]map[string]interface{}, 0, len(usageEvents))
	filteredSessions := make([]map[string]interface{}, 0, len(sessionEvents))
	baselineUpdates := make([]map[string]interface{}, 0, len(usageEvents)+len(sessionEvents))
	ignoredSessions := map[string]struct{}{}

	for _, payload := range usageEvents {
		sessionID := sessionIDFromPayload(payload)
		if sessionID == "" {
			continue
		}
		total, hasTokens := payloadIntValue(payload, "total_tokens", "tokens_used")
		if existing, ok := baseline.Sessions[sessionID]; ok {
			if !existing.TokensObserved {
				ignoredSessions[sessionID] = struct{}{}
				if hasTokens {
					baselineUpdates = append(baselineUpdates, payload)
				}
				continue
			}
			if !hasTokens || total <= existing.TotalTokens {
				// The bucket has not advanced past the baseline. Normally we drop
				// it, but the baseline is saved a phase earlier than the usage
				// cursor (runner.go), so a crash in between can leave the baseline
				// at this total while the cursor never recorded the delta. When the
				// baseline still carries an unconfirmed advance (its pre-advance
				// value is below the current total), re-emit a cursor-only recovery
				// summary that hints at the pre-advance cursor. The delta machinery
				// then yields zero delta if the cursor already committed, or
				// reconstructs the lost delta exactly once if the commit was lost.
				if hasTokens && total == existing.TotalTokens && existing.PreviousTotalTokens < existing.TotalTokens {
					markInternalRecoveryCursor(payload, existing)
					markInternalCursorOnly(payload)
					filteredUsage = append(filteredUsage, payload)
					continue
				}
				ignoredSessions[sessionID] = struct{}{}
				continue
			}
			markInternalPreviousCursor(payload, existing)
		} else {
			markInternalInitialDelta(payload)
		}
		markInternalCursorOnly(payload)
		filteredUsage = append(filteredUsage, payload)
		baselineUpdates = append(baselineUpdates, payload)
	}

	for _, payload := range sessionEvents {
		sessionID := sessionIDFromPayload(payload)
		if sessionID == "" {
			continue
		}
		if claudeShouldSkipBaselineSession(baseline, payload) {
			ignoredSessions[sessionID] = struct{}{}
			continue
		}
		filteredSessions = append(filteredSessions, payload)
		baselineUpdates = append(baselineUpdates, payload)
	}

	baselineDirty := false
	for _, payload := range baselineUpdates {
		if addBaselineSession(baseline.Sessions, payload) {
			baselineDirty = true
		}
	}
	if baselineDirty {
		store.ClaudeCode = baseline
		if err := saveSessionBaselineStore(cfg.DataDir, store); err != nil {
			return nil, nil, nil, err
		}
	}

	meta["session_baseline_initialized"] = false
	meta["session_baseline_ignored_sessions"] = len(baseline.Sessions)
	meta["historical_sessions_ignored"] = len(ignoredSessions)
	meta["usage_summary_events_cursor_only"] = len(filteredUsage)
	return filteredUsage, filteredSessions, meta, nil
}

func claudeShouldSkipBaselineSession(baseline claudeCodeSessionBaselineGroup, payload map[string]interface{}) bool {
	sessionID := sessionIDFromPayload(payload)
	if sessionID == "" {
		return false
	}
	existing, ok := baseline.Sessions[sessionID]
	if !ok {
		return false
	}
	transcriptHash := firstPayloadString(payload, "transcript_hash")
	if existing.TranscriptHash != "" && transcriptHash != "" {
		return existing.TranscriptHash == transcriptHash
	}
	return true
}
