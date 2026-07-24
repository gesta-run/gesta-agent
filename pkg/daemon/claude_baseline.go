package daemon

import (
	"sort"
	"time"

	"github.com/gesta-run/gesta-agent/pkg/model"
)

type claudeBaselineResult struct {
	UsageEvents   []map[string]interface{}
	SessionEvents []map[string]interface{}
	MCPEvents     []model.EventEnvelope
	Meta          map[string]interface{}
	Commit        func() error
}

// filterClaudeSessionBaseline prepares incremental Claude events and an
// after-queue commit. It never advances persistent state before the caller has
// durably appended the returned events.
func filterClaudeSessionBaseline(
	cfg Config,
	usageEvents, sessionEvents []map[string]interface{},
	mcpEvents []model.EventEnvelope,
	observedAt time.Time,
) (claudeBaselineResult, error) {
	if observedAt.IsZero() {
		observedAt = time.Now().UTC()
	}
	store, err := loadSessionBaselineStore(cfg.DataDir)
	if err != nil {
		return claudeBaselineResult{}, err
	}
	baseline := store.ClaudeCode
	if baseline.Sessions == nil {
		baseline.Sessions = map[string]baselineSession{}
	}
	result := claudeBaselineResult{Meta: map[string]interface{}{
		"session_backfill":         codexSessionBackfillModeValue,
		"session_baseline_enabled": true,
		"usage_summary_mode":       "cursor_only",
	}}

	dirty := false
	if baseline.InitializedAt == "" {
		dirty = initializeClaudeUsageBaseline(&baseline, usageEvents, sessionEvents, observedAt)
		result.Meta["session_baseline_initialized"] = true
		result.Meta["historical_sessions_ignored"] = len(baseline.Sessions)
	} else {
		var updates []map[string]interface{}
		var ignored int
		result.UsageEvents, updates, ignored = filterClaudeUsageBaseline(baseline, usageEvents)
		result.SessionEvents, updates = filterClaudeSessionPayloads(baseline, sessionEvents, updates)
		for _, payload := range updates {
			if addBaselineSession(baseline.Sessions, payload) {
				dirty = true
			}
		}
		result.Meta["session_baseline_initialized"] = false
		result.Meta["historical_sessions_ignored"] = ignored
		result.Meta["usage_summary_events_cursor_only"] = len(result.UsageEvents)
	}
	result.Meta["session_baseline_initialized_at"] = baseline.InitializedAt
	result.Meta["session_baseline_ignored_sessions"] = len(baseline.Sessions)

	var mcpDirty bool
	result.MCPEvents, mcpDirty = filterClaudeMCPBaseline(&baseline, mcpEvents, observedAt, result.Meta)
	dirty = dirty || mcpDirty
	if dirty {
		result.Commit = commitClaudeBaseline(cfg.DataDir, baseline)
	}
	return result, nil
}

func initializeClaudeUsageBaseline(
	baseline *claudeCodeSessionBaselineGroup,
	usageEvents, sessionEvents []map[string]interface{},
	observedAt time.Time,
) bool {
	for _, payload := range usageEvents {
		addBaselineSession(baseline.Sessions, payload)
	}
	for _, payload := range sessionEvents {
		addBaselineSession(baseline.Sessions, payload)
	}
	baseline.InitializedAt = observedAt.UTC().Format(time.RFC3339Nano)
	return true
}

func filterClaudeUsageBaseline(
	baseline claudeCodeSessionBaselineGroup,
	usageEvents []map[string]interface{},
) (filtered, updates []map[string]interface{}, ignoredCount int) {
	ignoredSessions := map[string]struct{}{}
	for _, payload := range usageEvents {
		sessionID := sessionIDFromPayload(payload)
		if sessionID == "" {
			continue
		}
		total, hasTokens := payloadIntValue(payload, "total_tokens", "tokens_used")
		existing, exists := baseline.Sessions[sessionID]
		if exists && (!existing.TokensObserved || !hasTokens || total <= existing.TotalTokens) {
			if existing.TokensObserved && hasTokens &&
				total == existing.TotalTokens &&
				existing.PreviousTotalTokens < existing.TotalTokens {
				markInternalRecoveryCursor(payload, existing)
				markInternalCursorOnly(payload)
				filtered = append(filtered, payload)
				continue
			}
			ignoredSessions[sessionID] = struct{}{}
			if !existing.TokensObserved && hasTokens {
				updates = append(updates, payload)
			}
			continue
		}
		if exists {
			markInternalPreviousCursor(payload, existing)
		} else {
			markInternalInitialDelta(payload)
		}
		markInternalCursorOnly(payload)
		filtered = append(filtered, payload)
		updates = append(updates, payload)
	}
	return filtered, updates, len(ignoredSessions)
}

func filterClaudeSessionPayloads(
	baseline claudeCodeSessionBaselineGroup,
	sessionEvents, updates []map[string]interface{},
) ([]map[string]interface{}, []map[string]interface{}) {
	filtered := make([]map[string]interface{}, 0, len(sessionEvents))
	for _, payload := range sessionEvents {
		if sessionIDFromPayload(payload) == "" || claudeShouldSkipBaselineSession(baseline, payload) {
			continue
		}
		filtered = append(filtered, payload)
		updates = append(updates, payload)
	}
	return filtered, updates
}

func filterClaudeMCPBaseline(
	baseline *claudeCodeSessionBaselineGroup,
	events []model.EventEnvelope,
	observedAt time.Time,
	meta map[string]interface{},
) ([]model.EventEnvelope, bool) {
	ordered := append([]model.EventEnvelope(nil), events...)
	sort.SliceStable(ordered, func(i, j int) bool {
		leftSession := firstPayloadString(ordered[i].Payload, "session_id", "session_id_hash")
		rightSession := firstPayloadString(ordered[j].Payload, "session_id", "session_id_hash")
		if leftSession != rightSession {
			return leftSession < rightSession
		}
		if !ordered[i].CreatedAt.Equal(ordered[j].CreatedAt) {
			return ordered[i].CreatedAt.Before(ordered[j].CreatedAt)
		}
		return ordered[i].EventID < ordered[j].EventID
	})

	if baseline.MCPInitializedAt == "" {
		for _, event := range ordered {
			advanceClaudeMCPCursor(baseline.Sessions, event)
		}
		baseline.MCPInitializedAt = observedAt.UTC().Format(time.RFC3339Nano)
		meta["mcp_baseline_initialized"] = true
		meta["mcp_baseline_initialized_at"] = baseline.MCPInitializedAt
		meta["historical_mcp_calls_ignored"] = len(ordered)
		return nil, true
	}

	cutoff, ok := parseClaudeTimestamp(baseline.MCPInitializedAt)
	if !ok {
		cutoff = observedAt.UTC()
	}
	filtered := make([]model.EventEnvelope, 0, len(ordered))
	dirty := false
	for _, event := range ordered {
		sessionID := firstPayloadString(event.Payload, "session_id", "session_id_hash")
		if sessionID == "" || event.CreatedAt.IsZero() {
			continue
		}
		cursor := baseline.Sessions[sessionID]
		if claudeMCPEventAfterCursor(event, cursor, cutoff) {
			filtered = append(filtered, event)
		}
		if advanceClaudeMCPCursor(baseline.Sessions, event) {
			dirty = true
		}
	}
	meta["mcp_baseline_initialized"] = false
	meta["mcp_baseline_initialized_at"] = baseline.MCPInitializedAt
	meta["mcp_tool_call_events"] = len(filtered)
	return filtered, dirty
}

func claudeMCPEventAfterCursor(event model.EventEnvelope, cursor baselineSession, cutoff time.Time) bool {
	cursorAt, ok := parseClaudeTimestamp(cursor.MCPToolCallCursorAt)
	if !ok {
		return event.CreatedAt.After(cutoff)
	}
	if event.CreatedAt.After(cursorAt) {
		return true
	}
	if !event.CreatedAt.Equal(cursorAt) {
		return false
	}
	for _, eventID := range cursor.MCPToolCallCursorEventIDs {
		if eventID == event.EventID {
			return false
		}
	}
	return true
}

func advanceClaudeMCPCursor(sessions map[string]baselineSession, event model.EventEnvelope) bool {
	sessionID := firstPayloadString(event.Payload, "session_id", "session_id_hash")
	if sessionID == "" || event.EventID == "" || event.CreatedAt.IsZero() {
		return false
	}
	cursor := sessions[sessionID]
	cursorAt, ok := parseClaudeTimestamp(cursor.MCPToolCallCursorAt)
	switch {
	case !ok || event.CreatedAt.After(cursorAt):
		cursor.MCPToolCallCursorAt = event.CreatedAt.UTC().Format(time.RFC3339Nano)
		cursor.MCPToolCallCursorEventIDs = []string{event.EventID}
	case event.CreatedAt.Equal(cursorAt):
		for _, eventID := range cursor.MCPToolCallCursorEventIDs {
			if eventID == event.EventID {
				return false
			}
		}
		cursor.MCPToolCallCursorEventIDs = append(cursor.MCPToolCallCursorEventIDs, event.EventID)
		sort.Strings(cursor.MCPToolCallCursorEventIDs)
	default:
		return false
	}
	sessions[sessionID] = cursor
	return true
}

func commitClaudeBaseline(dataDir string, baseline claudeCodeSessionBaselineGroup) func() error {
	return func() error {
		store, err := loadSessionBaselineStore(dataDir)
		if err != nil {
			return err
		}
		store.ClaudeCode = baseline
		return saveSessionBaselineStore(dataDir, store)
	}
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
