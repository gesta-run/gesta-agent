package daemon

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gesta-run/gesta-agent/pkg/model"
	"github.com/gesta-run/gesta-agent/pkg/util"
)

const (
	sessionBaselineFile                 = "session-baseline.json"
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

type sessionBaselineStore struct {
	Version    int                            `json:"version"`
	Codex      codexSessionBaselineGroup      `json:"codex"`
	ClaudeCode claudeCodeSessionBaselineGroup `json:"claude_code"`
}

type codexSessionBaselineGroup struct {
	StateDBs map[string]codexSessionBaseline `json:"state_dbs"`
}

type claudeCodeSessionBaselineGroup struct {
	InitializedAt string                     `json:"initialized_at,omitempty"`
	Sessions      map[string]baselineSession `json:"sessions"`
}

type codexSessionBaseline struct {
	InitializedAt string                     `json:"initialized_at"`
	StateDBHash   string                     `json:"state_db_hash,omitempty"`
	Sessions      map[string]baselineSession `json:"sessions"`
}

type baselineSession struct {
	UpdatedAt        string `json:"updated_at,omitempty"`
	TranscriptHash   string `json:"transcript_hash,omitempty"`
	TotalTokens      int64  `json:"total_tokens,omitempty"`
	InputTokens      int64  `json:"input_tokens,omitempty"`
	OutputTokens     int64  `json:"output_tokens,omitempty"`
	CacheReadTokens  int64  `json:"cache_read_tokens,omitempty"`
	CacheWriteTokens int64  `json:"cache_write_tokens,omitempty"`
	TokenAccounting  string `json:"token_accounting,omitempty"`
	TokensObserved   bool   `json:"tokens_observed,omitempty"`
	// CacheObserved separates "this session's cache counters were genuinely zero"
	// from "this baseline predates cache accounting and never recorded them". A
	// baseline written by an older agent has neither field, so both read back as
	// zero — but only the first may be diffed against. Without this, the recovery
	// path below would hand the delta machinery a cursor claiming an OBSERVED zero,
	// defeating its seeding guard and emitting the session's whole cumulative cache
	// history as one window's delta.
	CacheObserved bool `json:"cache_observed,omitempty"`
	// Previous* records the cursor value this session last advanced FROM (i.e.
	// the value before the most recent advance). The usage cursor is committed in
	// a later phase than the baseline (see runner.go), so a crash between the two
	// can leave the baseline advanced while the cursor is stale. Carrying the
	// pre-advance cursor lets a suppressed (total == baseline) summary still be
	// re-emitted as a cursor-only recovery hint, so the delta machinery can
	// reconstruct the lost delta on restart without double counting when the
	// cursor did commit. Populated only when a real advance happens.
	PreviousTotalTokens      int64  `json:"previous_total_tokens,omitempty"`
	PreviousInputTokens      int64  `json:"previous_input_tokens,omitempty"`
	PreviousOutputTokens     int64  `json:"previous_output_tokens,omitempty"`
	PreviousCacheReadTokens  int64  `json:"previous_cache_read_tokens,omitempty"`
	PreviousCacheWriteTokens int64  `json:"previous_cache_write_tokens,omitempty"`
	PreviousObservedAt       string `json:"previous_observed_at,omitempty"`
	// PreviousCacheObserved is CacheObserved for that pre-advance snapshot. The
	// recovery cursor is reconstructed from the Previous* fields, so it needs its
	// own record of whether those cache counters were ever real.
	PreviousCacheObserved bool `json:"previous_cache_observed,omitempty"`
}

func filterCodexSessionBackfill(cfg Config, stateDB string, usageEvents, transcriptEvents []map[string]interface{}, observedAt time.Time) ([]map[string]interface{}, []map[string]interface{}, map[string]interface{}, error) {
	if observedAt.IsZero() {
		observedAt = time.Now().UTC()
	}
	store, err := loadSessionBaselineStore(cfg.DataDir)
	if err != nil {
		return nil, nil, nil, err
	}
	stateDBHash := util.ShortHash(stateDB)
	baseline := store.Codex.StateDBs[stateDBHash]
	if baseline.Sessions == nil {
		baseline.Sessions = map[string]baselineSession{}
	}

	meta := map[string]interface{}{
		"session_backfill":               codexSessionBackfillModeValue,
		"session_baseline_enabled":       true,
		"session_baseline_state_db_hash": stateDBHash,
		"usage_summary_mode":             "cursor_only",
	}
	if baseline.InitializedAt == "" {
		for _, payload := range usageEvents {
			addBaselineSession(baseline.Sessions, payload)
		}
		for _, payload := range transcriptEvents {
			addBaselineSession(baseline.Sessions, payload)
		}
		baseline.InitializedAt = observedAt.UTC().Format(time.RFC3339Nano)
		baseline.StateDBHash = stateDBHash
		store.Codex.StateDBs[stateDBHash] = baseline
		if err := saveSessionBaselineStore(cfg.DataDir, store); err != nil {
			return nil, nil, nil, err
		}
		meta["session_baseline_initialized"] = true
		meta["historical_sessions_ignored"] = len(baseline.Sessions)
		return nil, nil, meta, nil
	}

	filteredUsage := make([]map[string]interface{}, 0, len(usageEvents))
	filteredTranscripts := make([]map[string]interface{}, 0, len(transcriptEvents))
	baselineUpdates := make([]map[string]interface{}, 0, len(usageEvents)+len(transcriptEvents))
	changedUsageSessions := map[string]struct{}{}
	ignoredSessions := map[string]struct{}{}
	for _, payload := range usageEvents {
		sessionID := sessionIDFromPayload(payload)
		if sessionID == "" {
			continue
		}
		if existing, ok := baseline.Sessions[sessionID]; ok {
			total, hasTokens := payloadIntValue(payload, "total_tokens", "tokens_used")
			if !existing.TokensObserved {
				ignoredSessions[sessionID] = struct{}{}
				if hasTokens {
					baselineUpdates = append(baselineUpdates, payload)
				}
				continue
			}
			if baselineTokenAccountingChanged(existing, payload) {
				ignoredSessions[sessionID] = struct{}{}
				if hasTokens {
					baselineUpdates = append(baselineUpdates, payload)
				}
				continue
			}
			if !hasTokens {
				ignoredSessions[sessionID] = struct{}{}
				continue
			}
			if !baselineUsageAdvanced(existing, payload, total) {
				ignoredSessions[sessionID] = struct{}{}
				if total > existing.TotalTokens {
					baselineUpdates = append(baselineUpdates, payload)
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
		filteredUsage = append(filteredUsage, payload)
		changedUsageSessions[sessionID] = struct{}{}
		baselineUpdates = append(baselineUpdates, payload)
	}
	for _, payload := range transcriptEvents {
		sessionID := sessionIDFromPayload(payload)
		if sessionID == "" {
			continue
		}
		if shouldSkipBaselineTranscript(baseline, payload) {
			if _, usageChanged := changedUsageSessions[sessionID]; usageChanged {
				filteredTranscripts = append(filteredTranscripts, payload)
				baselineUpdates = append(baselineUpdates, payload)
				continue
			}
			ignoredSessions[sessionID] = struct{}{}
			continue
		}
		filteredTranscripts = append(filteredTranscripts, payload)
		baselineUpdates = append(baselineUpdates, payload)
	}
	baselineDirty := false
	for _, payload := range baselineUpdates {
		if addBaselineSession(baseline.Sessions, payload) {
			baselineDirty = true
		}
	}
	if baselineDirty {
		store.Codex.StateDBs[stateDBHash] = baseline
		if err := saveSessionBaselineStore(cfg.DataDir, store); err != nil {
			return nil, nil, nil, err
		}
	}
	meta["session_baseline_initialized"] = false
	meta["session_baseline_ignored_sessions"] = len(baseline.Sessions)
	meta["historical_sessions_ignored"] = len(ignoredSessions)
	meta["usage_summary_events_cursor_only"] = len(filteredUsage)
	return filteredUsage, filteredTranscripts, meta, nil
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

func loadSessionBaselineStore(dataDir string) (sessionBaselineStore, error) {
	if dataDir == "" {
		dataDir = DefaultDataDir()
	}
	path := filepath.Join(dataDir, sessionBaselineFile)
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return newSessionBaselineStore(), nil
	}
	if err != nil {
		return sessionBaselineStore{}, err
	}
	var store sessionBaselineStore
	if err := json.Unmarshal(data, &store); err != nil {
		recoveredStore, recovered, recoverErr := decodeSessionBaselineStorePrefix(data)
		if recoverErr != nil {
			return sessionBaselineStore{}, err
		}
		if recovered {
			_ = saveSessionBaselineStore(dataDir, recoveredStore)
		}
		return recoveredStore, nil
	}
	normalizeSessionBaselineStore(&store)
	return store, nil
}

func decodeSessionBaselineStorePrefix(data []byte) (sessionBaselineStore, bool, error) {
	var store sessionBaselineStore
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := decoder.Decode(&store); err != nil {
		return sessionBaselineStore{}, false, err
	}
	remainder := string(data[decoder.InputOffset():])
	recovered := strings.TrimSpace(remainder) != ""
	normalizeSessionBaselineStore(&store)
	return store, recovered, nil
}

func saveSessionBaselineStore(dataDir string, store sessionBaselineStore) error {
	if dataDir == "" {
		dataDir = DefaultDataDir()
	}
	normalizeSessionBaselineStore(&store)
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(store, "", "  ")
	if err != nil {
		return err
	}
	path := filepath.Join(dataDir, sessionBaselineFile)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func newSessionBaselineStore() sessionBaselineStore {
	store := sessionBaselineStore{}
	normalizeSessionBaselineStore(&store)
	return store
}

func normalizeSessionBaselineStore(store *sessionBaselineStore) {
	if store.Version == 0 {
		store.Version = 1
	}
	if store.Codex.StateDBs == nil {
		store.Codex.StateDBs = map[string]codexSessionBaseline{}
	}
	if store.ClaudeCode.Sessions == nil {
		store.ClaudeCode.Sessions = map[string]baselineSession{}
	}
}
