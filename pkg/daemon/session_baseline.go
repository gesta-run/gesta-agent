package daemon

import (
	"time"

	"github.com/gesta-run/gesta-agent/pkg/model"
	"github.com/gesta-run/gesta-agent/pkg/util"
)

const (
	internalCursorOnlyPayloadKey        = "_gesta_internal_cursor_only"
	internalInitialDeltaPayloadKey      = "_gesta_internal_initial_delta"
	internalPreviousTotalTokensKey      = "_gesta_internal_previous_total_tokens"
	internalPreviousInputTokensKey      = "_gesta_internal_previous_input_tokens"
	internalPreviousOutputTokensKey     = "_gesta_internal_previous_output_tokens"
	internalPreviousCacheReadTokensKey  = "_gesta_internal_previous_cache_read_tokens"
	internalPreviousCacheWriteTokensKey = "_gesta_internal_previous_cache_write_tokens"
	internalPreviousObservedAtKey       = "_gesta_internal_previous_observed_at"
	internalPreviousAccountingKey       = "_gesta_internal_previous_accounting"
	codexSessionBackfillModeValue       = "disabled"
)

type codexBaselineResult struct {
	UsageEvents      []map[string]interface{}
	TranscriptEvents []map[string]interface{}
	Meta             map[string]interface{}
	Commit           func() error
}

func filterCodexSessionBackfill(cfg Config, stateDB string, usageEvents, transcriptEvents []map[string]interface{}, observedAt time.Time) (codexBaselineResult, error) {
	if observedAt.IsZero() {
		observedAt = time.Now().UTC()
	}
	store, err := loadSessionBaselineStore(cfg.DataDir)
	if err != nil {
		return codexBaselineResult{}, err
	}
	stateDBHash := util.ShortHash(stateDB)
	baseline := store.Codex.StateDBs[stateDBHash]
	if baseline.Sessions == nil {
		baseline.Sessions = map[string]baselineSession{}
	}

	result := codexBaselineResult{Meta: map[string]interface{}{
		"session_backfill":               codexSessionBackfillModeValue,
		"session_baseline_enabled":       true,
		"session_baseline_state_db_hash": stateDBHash,
		"usage_summary_mode":             "cursor_only",
	}}
	if baseline.InitializedAt == "" {
		initializeCodexBaseline(&baseline, stateDBHash, usageEvents, transcriptEvents, observedAt)
		result.Meta["session_baseline_initialized"] = true
		result.Meta["historical_sessions_ignored"] = len(baseline.Sessions)
		result.Commit = commitCodexBaseline(cfg.DataDir, stateDBHash, baseline)
		return result, nil
	}

	var updates []map[string]interface{}
	var ignoredSessions map[string]struct{}
	result.UsageEvents, updates, _, ignoredSessions = filterCodexUsageBaseline(baseline, usageEvents)
	result.TranscriptEvents, updates = filterCodexTranscriptBaseline(
		baseline,
		transcriptEvents,
		updates,
		ignoredSessions,
		observedAt,
		dailyWorkLocation(cfg.DailyWorkTimezone),
	)
	baselineDirty := false
	for _, payload := range updates {
		if addBaselineSession(baseline.Sessions, payload) {
			baselineDirty = true
		}
	}
	if baselineDirty {
		result.Commit = commitCodexBaseline(cfg.DataDir, stateDBHash, baseline)
	}
	result.Meta["session_baseline_initialized"] = false
	result.Meta["session_baseline_ignored_sessions"] = len(baseline.Sessions)
	result.Meta["historical_sessions_ignored"] = len(ignoredSessions)
	result.Meta["usage_summary_events_cursor_only"] = len(result.UsageEvents)
	return result, nil
}

func initializeCodexBaseline(
	baseline *codexSessionBaseline,
	stateDBHash string,
	usageEvents, transcriptEvents []map[string]interface{},
	observedAt time.Time,
) {
	for _, payload := range usageEvents {
		addBaselineSession(baseline.Sessions, payload)
	}
	for _, payload := range transcriptEvents {
		addBaselineSession(baseline.Sessions, payload)
	}
	baseline.InitializedAt = observedAt.UTC().Format(time.RFC3339Nano)
	baseline.StateDBHash = stateDBHash
}

func filterCodexUsageBaseline(
	baseline codexSessionBaseline,
	usageEvents []map[string]interface{},
) (filtered, updates []map[string]interface{}, changed, ignored map[string]struct{}) {
	filtered = make([]map[string]interface{}, 0, len(usageEvents))
	updates = make([]map[string]interface{}, 0, len(usageEvents))
	changed = map[string]struct{}{}
	ignored = map[string]struct{}{}
	for _, payload := range usageEvents {
		sessionID := sessionIDFromPayload(payload)
		if sessionID == "" {
			continue
		}
		if existing, ok := baseline.Sessions[sessionID]; ok {
			total, hasTokens := payloadIntValue(payload, "total_tokens", "tokens_used")
			if !existing.TokensObserved {
				ignored[sessionID] = struct{}{}
				if hasTokens {
					updates = append(updates, payload)
				}
				continue
			}
			if baselineTokenAccountingChanged(existing, payload) {
				ignored[sessionID] = struct{}{}
				if hasTokens {
					updates = append(updates, payload)
				}
				continue
			}
			if !hasTokens {
				ignored[sessionID] = struct{}{}
				continue
			}
			if !baselineUsageAdvanced(existing, payload, total) {
				ignored[sessionID] = struct{}{}
				if total > existing.TotalTokens {
					updates = append(updates, payload)
				}
				continue
			}
			markInternalPreviousCursor(payload, existing)
		} else if forked, parent, ok := codexForkParentBaselineCursor(payload, baseline.Sessions); forked {
			if ok {
				markInternalPreviousCursor(payload, parent)
			}
		} else {
			markInternalInitialDelta(payload)
		}
		markInternalCursorOnly(payload)
		filtered = append(filtered, payload)
		changed[sessionID] = struct{}{}
		updates = append(updates, payload)
	}
	return filtered, updates, changed, ignored
}

func filterCodexTranscriptBaseline(
	baseline codexSessionBaseline,
	transcriptEvents, updates []map[string]interface{},
	ignoredSessions map[string]struct{},
	observedAt time.Time,
	location *time.Location,
) ([]map[string]interface{}, []map[string]interface{}) {
	filtered := make([]map[string]interface{}, 0, len(transcriptEvents))
	for _, payload := range transcriptEvents {
		sessionID := sessionIDFromPayload(payload)
		if sessionID == "" {
			continue
		}
		cursor, exists := baseline.Sessions[sessionID]
		if exists && !cursor.TranscriptCursorInitialized {
			cursor = seedTranscriptCursor(payload, cursor)
			updates = append(updates, transcriptCursorPayload(payload, cursor))
			ignoredSessions[sessionID] = struct{}{}
			continue
		}
		if shouldSkipBaselineTranscript(baseline, payload) {
			ignoredSessions[sessionID] = struct{}{}
			continue
		}
		chunks, cursor := prepareTranscriptChunks(payload, cursor, observedAt, location)
		filtered = append(filtered, chunks...)
		updates = append(updates, transcriptCursorPayload(payload, cursor))
	}
	return filtered, updates
}

func commitCodexBaseline(dataDir, stateDBHash string, baseline codexSessionBaseline) func() error {
	return func() error {
		store, err := loadSessionBaselineStore(dataDir)
		if err != nil {
			return err
		}
		store.Codex.StateDBs[stateDBHash] = baseline
		return saveSessionBaselineStore(dataDir, store)
	}
}

func addBaselineSession(sessions map[string]baselineSession, payload map[string]interface{}) bool {
	sessionID := sessionIDFromPayload(payload)
	if sessionID == "" {
		return false
	}
	existing, exists := sessions[sessionID]
	changed := !exists
	// Snapshot the pre-mutation observed-at so a genuine advance below can record
	// the cursor timestamp it is advancing FROM (UpdatedAt is overwritten next).
	priorObservedAt := existing.UpdatedAt
	if updatedAt := firstPayloadString(payload, "updated_at"); shouldReplaceBaselineUpdatedAt(existing.UpdatedAt, updatedAt) {
		existing.UpdatedAt = updatedAt
		changed = true
	}
	if transcriptHash := firstPayloadString(payload, "transcript_hash"); transcriptHash != "" && transcriptHash != existing.TranscriptHash {
		existing.TranscriptHash = transcriptHash
		changed = true
	}
	beforeMessageVersions := len(existing.TranscriptMessageVersions)
	beforeSequence := existing.TranscriptSequence
	beforeCursorInitialized := existing.TranscriptCursorInitialized
	if _, ok := payload["_gesta_transcript_cursor_initialized"]; ok {
		existing = transcriptCursorFromPayload(payload, existing)
	} else if _, ok := payload["messages"]; ok {
		existing = seedTranscriptCursor(payload, existing)
	}
	if len(existing.TranscriptMessageVersions) != beforeMessageVersions ||
		existing.TranscriptSequence != beforeSequence ||
		existing.TranscriptCursorInitialized != beforeCursorInitialized {
		changed = true
	}
	if total, ok := payloadIntValue(payload, "total_tokens", "tokens_used"); ok && (!existing.TokensObserved || total > existing.TotalTokens) {
		// Capture the value we are advancing FROM so a crash between the baseline
		// save and the usage-cursor commit can be recovered (see baselineSession).
		// On the very first observation (baseline init or backfill) there is no
		// emitted delta to recover, so pin Previous* to the same total — that
		// keeps the recovery condition (Previous < Total) false until a genuine
		// advance happens.
		if existing.TokensObserved {
			existing.PreviousTotalTokens = existing.TotalTokens
			existing.PreviousInputTokens = existing.InputTokens
			existing.PreviousOutputTokens = existing.OutputTokens
			existing.PreviousCacheReadTokens = existing.CacheReadTokens
			existing.PreviousCacheWriteTokens = existing.CacheWriteTokens
			existing.PreviousCacheObserved = existing.CacheObserved
			existing.PreviousObservedAt = priorObservedAt
		} else {
			existing.PreviousTotalTokens = total
		}
		existing.TotalTokens = total
		existing.TokensObserved = true
		changed = true
	}
	if input, ok := payloadIntValue(payload, "input_tokens", "prompt_tokens"); ok && (!existing.TokensObserved || input > existing.InputTokens) {
		existing.InputTokens = input
		changed = true
	}
	if output, ok := payloadIntValue(payload, "output_tokens", "completion_tokens"); ok && (!existing.TokensObserved || output > existing.OutputTokens) {
		existing.OutputTokens = output
		changed = true
	}
	if cacheRead, ok := payloadIntValue(payload, "cache_read_tokens", "cached_input_tokens", "cache_read_input_tokens"); ok && (!existing.TokensObserved || cacheRead > existing.CacheReadTokens) {
		existing.CacheReadTokens = cacheRead
		existing.CacheObserved = true
		changed = true
	}
	if cacheWrite, ok := payloadIntValue(payload, "cache_creation_tokens", "cache_write_tokens", "cache_creation_input_tokens"); ok && (!existing.TokensObserved || cacheWrite > existing.CacheWriteTokens) {
		existing.CacheWriteTokens = cacheWrite
		existing.CacheObserved = true
		changed = true
	}
	if accounting := firstPayloadString(payload, "token_accounting"); accounting != "" && accounting != existing.TokenAccounting {
		existing.TokenAccounting = accounting
		changed = true
	}
	sessions[sessionID] = existing
	return changed
}

func baselineTokenAccountingChanged(existing baselineSession, payload map[string]interface{}) bool {
	current := firstPayloadString(payload, "token_accounting")
	if current == "" {
		return false
	}
	return existing.TokenAccounting != current
}

func baselineUsageAdvanced(existing baselineSession, payload map[string]interface{}, total int64) bool {
	if total <= existing.TotalTokens {
		return false
	}
	updatedAt := firstPayloadString(payload, "updated_at")
	if updatedAt == "" || existing.UpdatedAt == "" {
		return true
	}
	return baselineTimestampAfter(updatedAt, existing.UpdatedAt)
}

func codexForkParentBaselineCursor(payload map[string]interface{}, sessions map[string]baselineSession) (bool, baselineSession, bool) {
	parentID := firstPayloadString(payload, "parent_session_id", "parent_session_id_hash", "forked_from_id", "forked_from")
	if parentID == "" {
		return false, baselineSession{}, false
	}
	if parent, ok := compatibleParentBaseline(parentID, payload, sessions); ok {
		return true, parent, true
	}
	if hashed := util.ShortHash(parentID); hashed != parentID {
		if parent, ok := compatibleParentBaseline(hashed, payload, sessions); ok {
			return true, parent, true
		}
	}
	return true, baselineSession{}, false
}

func compatibleParentBaseline(parentID string, payload map[string]interface{}, sessions map[string]baselineSession) (baselineSession, bool) {
	parent, ok := sessions[parentID]
	if !ok || !parent.TokensObserved {
		return baselineSession{}, false
	}
	currentAccounting := firstPayloadString(payload, "token_accounting")
	if parent.TokenAccounting != currentAccounting {
		return baselineSession{}, false
	}
	return parent, true
}

func shouldSkipBaselineTranscript(baseline codexSessionBaseline, payload map[string]interface{}) bool {
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
	if payloadUpdatedAfterBaseline(existing.UpdatedAt, firstPayloadString(payload, "updated_at")) {
		return false
	}
	if existing.UpdatedAt == "" && payloadUpdatedAfterBaseline(baseline.InitializedAt, firstPayloadString(payload, "updated_at")) {
		return false
	}
	return true
}

func shouldReplaceBaselineUpdatedAt(existing, candidate string) bool {
	if candidate == "" {
		return false
	}
	if existing == "" {
		return true
	}
	return baselineTimestampAfter(candidate, existing)
}

func payloadUpdatedAfterBaseline(existing, candidate string) bool {
	if existing == "" || candidate == "" {
		return false
	}
	return baselineTimestampAfter(candidate, existing)
}

func baselineTimestampAfter(candidate, existing string) bool {
	candidateTime, candidateOK := parseBaselineTimestamp(candidate)
	existingTime, existingOK := parseBaselineTimestamp(existing)
	if candidateOK && existingOK {
		return candidateTime.After(existingTime)
	}
	return candidate > existing
}

func parseBaselineTimestamp(value string) (time.Time, bool) {
	if value == "" {
		return time.Time{}, false
	}
	t, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}, false
	}
	return t, true
}

func sessionIDFromPayload(payload map[string]interface{}) string {
	if payload == nil {
		return ""
	}
	return firstPayloadString(payload, "session_id", "session_id_hash", "thread_id", "conversation_id")
}

func markInternalCursorOnly(payload map[string]interface{}) {
	if payload != nil {
		payload[internalCursorOnlyPayloadKey] = true
	}
}

func markInternalInitialDelta(payload map[string]interface{}) {
	if payload != nil {
		payload[internalInitialDeltaPayloadKey] = true
	}
}

func markInternalPreviousCursor(payload map[string]interface{}, previous baselineSession) {
	if payload == nil {
		return
	}
	payload[internalPreviousTotalTokensKey] = previous.TotalTokens
	payload[internalPreviousInputTokensKey] = previous.InputTokens
	payload[internalPreviousOutputTokensKey] = previous.OutputTokens
	// Only when the baseline actually recorded cache counters. Writing them
	// unconditionally would make a pre-upgrade baseline's missing counters look like
	// an observed zero, and the reconstructed cursor would then be diffed against it.
	if previous.CacheObserved {
		payload[internalPreviousCacheReadTokensKey] = previous.CacheReadTokens
		payload[internalPreviousCacheWriteTokensKey] = previous.CacheWriteTokens
	}
	if previous.UpdatedAt != "" {
		payload[internalPreviousObservedAtKey] = previous.UpdatedAt
	}
	if previous.TokenAccounting != "" {
		payload[internalPreviousAccountingKey] = previous.TokenAccounting
	}
}

// markInternalRecoveryCursor uses the pre-advance (Previous*) cursor recorded on
// the baseline. It is used when a bucket's reported total equals the baseline
// (so it would normally be suppressed) to emit a cursor-only recovery summary:
// if the real usage cursor already committed the advance, the delta machinery
// sees the cursor at the same total and produces zero delta; if a crash dropped
// the commit, the machinery falls back to this pre-advance hint and reconstructs
// the lost delta exactly once.
func markInternalRecoveryCursor(payload map[string]interface{}, previous baselineSession) {
	if payload == nil {
		return
	}
	payload[internalPreviousTotalTokensKey] = previous.PreviousTotalTokens
	payload[internalPreviousInputTokensKey] = previous.PreviousInputTokens
	payload[internalPreviousOutputTokensKey] = previous.PreviousOutputTokens
	// Same invariant as markInternalPreviousCursor, against the PRE-ADVANCE flag:
	// absence must survive recovery.
	if previous.PreviousCacheObserved {
		payload[internalPreviousCacheReadTokensKey] = previous.PreviousCacheReadTokens
		payload[internalPreviousCacheWriteTokensKey] = previous.PreviousCacheWriteTokens
	}
	if previous.PreviousObservedAt != "" {
		payload[internalPreviousObservedAtKey] = previous.PreviousObservedAt
	}
	if previous.TokenAccounting != "" {
		payload[internalPreviousAccountingKey] = previous.TokenAccounting
	}
}

func filterInternalEvents(events []model.EventEnvelope) ([]model.EventEnvelope, int) {
	filtered := make([]model.EventEnvelope, 0, len(events))
	filteredCount := 0
	for _, event := range events {
		if event.Payload != nil {
			if cursorOnly, _ := event.Payload[internalCursorOnlyPayloadKey].(bool); cursorOnly {
				filteredCount++
				continue
			}
		}
		filtered = append(filtered, event)
	}
	return filtered, filteredCount
}
