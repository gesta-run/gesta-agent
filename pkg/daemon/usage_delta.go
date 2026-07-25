package daemon

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gesta-run/gesta-agent/internal/atomicfile"
	"github.com/gesta-run/gesta-agent/pkg/model"
	"github.com/gesta-run/gesta-agent/pkg/util"
)

const usageCursorFile = "usage-cursors.json"

type usageCursorStore struct {
	Sessions map[string]usageCursor `json:"sessions"`
}

type usageCursor struct {
	TotalTokens      int64  `json:"total_tokens"`
	InputTokens      int64  `json:"input_tokens"`
	OutputTokens     int64  `json:"output_tokens"`
	CacheReadTokens  int64  `json:"cache_read_tokens"`
	CacheWriteTokens int64  `json:"cache_write_tokens"`
	ObservedAt       string `json:"observed_at"`

	// CacheObserved separates "this session's cache counters were genuinely zero"
	// from "this cursor predates cache accounting and never recorded them". Both
	// deserialize the counters as 0, but only the first may be diffed against:
	// see the seeding guard in usageDeltaFromEvent.
	CacheObserved bool `json:"cache_observed"`
}

type usageObservation struct {
	sessionID       string
	outputSessionID string
	total           int64
	input           int64
	output          int64
	cacheRead       int64
	cacheWrite      int64
	cacheObserved   bool
}

type usageTokenDelta struct {
	total      int64
	input      int64
	output     int64
	cacheRead  int64
	cacheWrite int64
}

type usageDeltaCommit func() error

func BuildUsageDeltaEvents(cfg Config, events []model.EventEnvelope, observedAt time.Time) ([]model.EventEnvelope, usageDeltaCommit, error) {
	if observedAt.IsZero() {
		observedAt = time.Now().UTC()
	}
	store, err := loadUsageCursorStore(cfg.DataDir)
	if err != nil {
		return nil, nil, err
	}
	next := usageCursorStore{Sessions: map[string]usageCursor{}}
	for key, value := range store.Sessions {
		next.Sessions[key] = value
	}

	var deltas []model.EventEnvelope
	for _, event := range events {
		delta, key, cursor, ok := usageDeltaFromEvent(cfg, event, store, observedAt)
		if !ok {
			continue
		}
		next.Sessions[key] = cursor
		if delta.EventID != "" {
			deltas = append(deltas, delta)
		}
	}

	commit := func() error {
		return saveUsageCursorStore(cfg.DataDir, next)
	}
	return deltas, commit, nil
}

func usageDeltaFromEvent(cfg Config, event model.EventEnvelope, store usageCursorStore, observedAt time.Time) (model.EventEnvelope, string, usageCursor, bool) {
	observation, ok := usageObservationFromEvent(event)
	if !ok {
		return model.EventEnvelope{}, "", usageCursor{}, false
	}
	key := usageCursorKey(event, observation.sessionID, firstPayloadString(event.Payload, "token_accounting"))
	nextCursor := usageCursor{
		TotalTokens:      observation.total,
		InputTokens:      observation.input,
		OutputTokens:     observation.output,
		CacheReadTokens:  observation.cacheRead,
		CacheWriteTokens: observation.cacheWrite,
		ObservedAt:       observedAt.UTC().Format(time.RFC3339Nano),
		CacheObserved:    observation.cacheObserved,
	}
	previous, seen := store.Sessions[key]
	if !seen {
		if baselineCursor, ok := previousUsageCursorFromPayload(event.Payload, observedAt.Add(-cfg.EffectiveUsageWindow())); ok {
			previous = baselineCursor
		} else if payloadBool(event.Payload, internalInitialDeltaPayloadKey) {
			// A session tracked from its start: its prior cache counters really were
			// zero, so they are safe to diff against.
			previous = usageCursor{
				ObservedAt:    observedAt.Add(-cfg.EffectiveUsageWindow()).UTC().Format(time.RFC3339Nano),
				CacheObserved: true,
			}
		} else {
			return model.EventEnvelope{}, key, nextCursor, true
		}
	}

	// A cursor written before cache-tier accounting carries no cache counters, so
	// they read back as zero. Diffing against that zero would emit the session's
	// ENTIRE cumulative cache history as a single window's delta on the first poll
	// after the agent upgrades — and a Codex cursor is session-lifetime, so that
	// can be weeks of cache reads landing on one day. Seed the cache cursor from
	// the current cumulative value instead and report no cache movement for this
	// poll, exactly as a newly-tracked session is seeded rather than back-filled.
	// It costs one window of cache attribution, once, per in-flight session.
	if !previous.CacheObserved {
		previous.CacheReadTokens = observation.cacheRead
		previous.CacheWriteTokens = observation.cacheWrite
	}

	tokenDelta, ok := calculateUsageTokenDelta(observation, previous)
	if !ok {
		return model.EventEnvelope{}, key, nextCursor, true
	}
	windowStart, windowEnd := usageDeltaWindow(cfg, event.Payload, previous, observedAt)
	payload := map[string]interface{}{
		"accounting":             "delta",
		"source_event_id":        event.EventID,
		"source_session_id":      observation.sessionID,
		"session_id":             observation.outputSessionID,
		"window_start":           windowStart.Format(time.RFC3339Nano),
		"window_end":             windowEnd.Format(time.RFC3339Nano),
		"configured_window_sec":  int64(cfg.EffectiveUsageWindow().Seconds()),
		"total_tokens":           tokenDelta.total,
		"tokens_used":            tokenDelta.total,
		"input_tokens":           tokenDelta.input,
		"output_tokens":          tokenDelta.output,
		"cache_read_tokens":      tokenDelta.cacheRead,
		"cache_write_tokens":     tokenDelta.cacheWrite,
		"session_total_tokens":   observation.total,
		"session_input_tokens":   observation.input,
		"session_output_tokens":  observation.output,
		"session_previous_total": previous.TotalTokens,
	}
	if !seen {
		payload["initial_observation"] = true
	}
	copyPayloadString(payload, event.Payload, "model", "model_name", "model_id")
	copyPayloadString(payload, event.Payload, "repo", "repository", "repo_name", "repo_path_hash", "workspace", "workspace_hash", "source_hash")
	copyPayloadString(payload, event.Payload, "model_provider")
	copyPayloadString(payload, event.Payload, "token_accounting")
	copyPayloadIntAs(payload, event.Payload, "raw_total_tokens", "session_raw_total_tokens")

	delta := baseEvent(cfg, "usage.delta", event.Source, event.AgentType, payload)
	delta.CreatedAt = windowEnd
	return delta, key, nextCursor, true
}

func calculateUsageTokenDelta(observation usageObservation, previous usageCursor) (usageTokenDelta, bool) {
	delta := usageTokenDelta{
		total:      observation.total - previous.TotalTokens,
		input:      max(observation.input-previous.InputTokens, 0),
		output:     max(observation.output-previous.OutputTokens, 0),
		cacheRead:  max(observation.cacheRead-previous.CacheReadTokens, 0),
		cacheWrite: max(observation.cacheWrite-previous.CacheWriteTokens, 0),
	}
	return delta, delta.total > 0
}

func usageDeltaWindow(cfg Config, payload map[string]interface{}, previous usageCursor, observedAt time.Time) (time.Time, time.Time) {
	windowStart := parseUsageCursorTime(previous.ObservedAt, observedAt.Add(-cfg.EffectiveUsageWindow()))
	windowEnd := observedAt.UTC()
	// Pre-bucketed adapters can pin usage to the day when work happened.
	if day := firstPayloadString(payload, "usage_day"); day != "" {
		if parsedDay, err := time.Parse("2006-01-02", day); err == nil {
			windowEnd = time.Date(parsedDay.Year(), parsedDay.Month(), parsedDay.Day(), 23, 59, 59, 0, time.UTC)
			if windowEnd.After(observedAt.UTC()) {
				windowEnd = observedAt.UTC()
			}
			if windowStart.After(windowEnd) {
				windowStart = windowEnd
			}
		}
	}
	return windowStart, windowEnd
}

func usageObservationFromEvent(event model.EventEnvelope) (usageObservation, bool) {
	if (event.EventType != "usage.summary" && event.EventType != "session.summary") || event.Payload == nil {
		return usageObservation{}, false
	}
	sessionID := strings.TrimSpace(firstPayloadString(event.Payload, "session_id", "session_id_hash", "thread_id", "conversation_id"))
	if sessionID == "" {
		return usageObservation{}, false
	}
	total := payloadInt(event.Payload, "total_tokens", "tokens_used")
	if total <= 0 {
		return usageObservation{}, false
	}
	cacheRead, cacheReadOK := payloadIntValue(event.Payload, "cache_read_tokens", "cached_input_tokens", "cache_read_input_tokens")
	cacheWrite, cacheWriteOK := payloadIntValue(event.Payload, "cache_creation_tokens", "cache_write_tokens", "cache_creation_input_tokens")
	return usageObservation{
		sessionID:       sessionID,
		outputSessionID: firstNonEmptyString(firstPayloadString(event.Payload, "index_session_id"), sessionID),
		total:           total,
		input:           payloadInt(event.Payload, "input_tokens", "prompt_tokens"),
		output:          payloadInt(event.Payload, "output_tokens", "completion_tokens"),
		cacheRead:       cacheRead,
		cacheWrite:      cacheWrite,
		cacheObserved:   cacheReadOK || cacheWriteOK,
	}, true
}

func usageCursorKey(event model.EventEnvelope, sessionID, tokenAccounting string) string {
	parts := []string{
		event.DaemonID,
		event.Source,
		event.AgentType,
		sessionID,
	}
	if strings.TrimSpace(tokenAccounting) != "" {
		parts = append(parts, strings.TrimSpace(tokenAccounting))
	}
	return util.HashString(strings.Join(parts, "\x00"))
}

func loadUsageCursorStore(dataDir string) (usageCursorStore, error) {
	if dataDir == "" {
		dataDir = DefaultDataDir()
	}
	path := filepath.Join(dataDir, usageCursorFile)
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return usageCursorStore{Sessions: map[string]usageCursor{}}, nil
	}
	if err != nil {
		return usageCursorStore{}, err
	}
	var store usageCursorStore
	if err := json.Unmarshal(data, &store); err != nil {
		return usageCursorStore{}, err
	}
	if store.Sessions == nil {
		store.Sessions = map[string]usageCursor{}
	}
	return store, nil
}

func saveUsageCursorStore(dataDir string, store usageCursorStore) error {
	if dataDir == "" {
		dataDir = DefaultDataDir()
	}
	if store.Sessions == nil {
		store.Sessions = map[string]usageCursor{}
	}
	return atomicfile.WriteJSON(filepath.Join(dataDir, usageCursorFile), store)
}

func parseUsageCursorTime(value string, fallback time.Time) time.Time {
	if t, err := time.Parse(time.RFC3339Nano, value); err == nil {
		return t.UTC()
	}
	return fallback.UTC()
}

func firstPayloadString(payload map[string]interface{}, keys ...string) string {
	for _, key := range keys {
		value, ok := payload[key]
		if !ok || value == nil {
			continue
		}
		switch typed := value.(type) {
		case string:
			if strings.TrimSpace(typed) != "" {
				return strings.TrimSpace(typed)
			}
		case fmt.Stringer:
			if strings.TrimSpace(typed.String()) != "" {
				return strings.TrimSpace(typed.String())
			}
		}
	}
	return ""
}

func payloadInt(payload map[string]interface{}, keys ...string) int64 {
	value, _ := payloadIntValue(payload, keys...)
	return value
}

func payloadIntValue(payload map[string]interface{}, keys ...string) (int64, bool) {
	for _, key := range keys {
		if value, ok := asInt64(payload[key]); ok {
			return value, true
		}
	}
	return 0, false
}

func payloadBool(payload map[string]interface{}, key string) bool {
	if payload == nil {
		return false
	}
	value, _ := payload[key].(bool)
	return value
}

func previousUsageCursorFromPayload(payload map[string]interface{}, fallbackObservedAt time.Time) (usageCursor, bool) {
	total, ok := payloadIntValue(payload, internalPreviousTotalTokensKey)
	if !ok {
		return usageCursor{}, false
	}
	input, _ := payloadIntValue(payload, internalPreviousInputTokensKey)
	output, _ := payloadIntValue(payload, internalPreviousOutputTokensKey)
	// A baseline written by an older agent has no cache keys at all. Record their
	// absence rather than reading it as a zero the delta can be diffed against.
	cacheRead, cacheReadObserved := payloadIntValue(payload, internalPreviousCacheReadTokensKey)
	cacheWrite, _ := payloadIntValue(payload, internalPreviousCacheWriteTokensKey)
	observedAt := firstPayloadString(payload, internalPreviousObservedAtKey)
	if observedAt == "" {
		observedAt = fallbackObservedAt.UTC().Format(time.RFC3339Nano)
	}
	return usageCursor{
		TotalTokens:      total,
		InputTokens:      input,
		OutputTokens:     output,
		CacheReadTokens:  cacheRead,
		CacheWriteTokens: cacheWrite,
		ObservedAt:       observedAt,
		CacheObserved:    cacheReadObserved,
	}, true
}

func copyPayloadString(dst, src map[string]interface{}, keys ...string) {
	for _, key := range keys {
		if value := firstPayloadString(src, key); value != "" {
			dst[key] = value
			return
		}
	}
}

func copyPayloadIntAs(dst, src map[string]interface{}, from, to string) {
	if value, ok := asInt64(src[from]); ok {
		dst[to] = value
	}
}
