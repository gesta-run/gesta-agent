package daemon

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/gesta-run/gesta-agent/pkg/model"
)

func TestBuildUsageDeltaEventsBaselinesThenEmitsDelta(t *testing.T) {
	cfg := Config{
		DataDir:      t.TempDir(),
		CustomerID:   "acme",
		DeploymentID: "prod",
		DaemonID:     "daemon_1",
		DeviceID:     "device_1",
		UsageWindow:  "10m",
	}
	firstObservedAt := time.Date(2026, 6, 10, 23, 58, 0, 0, time.UTC)
	first := usageSummaryEvent(100, 60, 40)
	deltas, commit, err := BuildUsageDeltaEvents(cfg, []model.EventEnvelope{first}, firstObservedAt)
	if err != nil {
		t.Fatalf("BuildUsageDeltaEvents first: %v", err)
	}
	if len(deltas) != 0 {
		t.Fatalf("first observation emitted deltas: %+v", deltas)
	}
	if err := commit(); err != nil {
		t.Fatalf("commit first: %v", err)
	}

	secondObservedAt := firstObservedAt.Add(12 * time.Minute)
	second := usageSummaryEvent(180, 110, 70)
	deltas, commit, err = BuildUsageDeltaEvents(cfg, []model.EventEnvelope{second}, secondObservedAt)
	if err != nil {
		t.Fatalf("BuildUsageDeltaEvents second: %v", err)
	}
	if len(deltas) != 1 {
		t.Fatalf("second observation emitted %d deltas, want 1", len(deltas))
	}
	payload := deltas[0].Payload
	if payload["total_tokens"] != int64(80) {
		t.Fatalf("total_tokens = %#v, want 80", payload["total_tokens"])
	}
	if payload["input_tokens"] != int64(50) {
		t.Fatalf("input_tokens = %#v, want 50", payload["input_tokens"])
	}
	if payload["output_tokens"] != int64(30) {
		t.Fatalf("output_tokens = %#v, want 30", payload["output_tokens"])
	}
	if payload["window_start"] != firstObservedAt.Format(time.RFC3339Nano) {
		t.Fatalf("window_start = %#v, want %s", payload["window_start"], firstObservedAt.Format(time.RFC3339Nano))
	}
	if payload["window_end"] != secondObservedAt.Format(time.RFC3339Nano) {
		t.Fatalf("window_end = %#v, want %s", payload["window_end"], secondObservedAt.Format(time.RFC3339Nano))
	}
	if err := commit(); err != nil {
		t.Fatalf("commit second: %v", err)
	}
}

func TestBuildUsageDeltaEventsSkipsNegativeDelta(t *testing.T) {
	cfg := Config{
		DataDir:      t.TempDir(),
		CustomerID:   "acme",
		DeploymentID: "prod",
		DaemonID:     "daemon_1",
		DeviceID:     "device_1",
		UsageWindow:  "10m",
	}
	observedAt := time.Date(2026, 6, 10, 12, 0, 0, 0, time.UTC)
	deltas, commit, err := BuildUsageDeltaEvents(cfg, []model.EventEnvelope{usageSummaryEvent(200, 100, 100)}, observedAt)
	if err != nil {
		t.Fatalf("BuildUsageDeltaEvents first: %v", err)
	}
	if len(deltas) != 0 {
		t.Fatalf("first observation emitted deltas: %+v", deltas)
	}
	if err := commit(); err != nil {
		t.Fatalf("commit first: %v", err)
	}

	deltas, commit, err = BuildUsageDeltaEvents(cfg, []model.EventEnvelope{usageSummaryEvent(150, 80, 70)}, observedAt.Add(10*time.Minute))
	if err != nil {
		t.Fatalf("BuildUsageDeltaEvents reset: %v", err)
	}
	if len(deltas) != 0 {
		t.Fatalf("negative observation emitted deltas: %+v", deltas)
	}
	if err := commit(); err != nil {
		t.Fatalf("commit reset: %v", err)
	}
}

func TestBuildUsageDeltaEventsEmitsInitialDeltaForNewPostBaselineSession(t *testing.T) {
	cfg := Config{DataDir: t.TempDir(), UsageWindow: "10m"}
	observedAt := time.Date(2026, 6, 22, 4, 30, 0, 0, time.UTC)
	event := usageSummaryEvent(21383, 12000, 9383)
	event.Payload[internalInitialDeltaPayloadKey] = true

	deltas, commit, err := BuildUsageDeltaEvents(cfg, []model.EventEnvelope{event}, observedAt)
	if err != nil {
		t.Fatalf("BuildUsageDeltaEvents: %v", err)
	}
	if len(deltas) != 1 {
		t.Fatalf("deltas = %#v, want one initial delta", deltas)
	}
	if deltas[0].EventType != "usage.delta" {
		t.Fatalf("event type = %s, want usage.delta", deltas[0].EventType)
	}
	if got := payloadInt(deltas[0].Payload, "total_tokens"); got != 21383 {
		t.Fatalf("total_tokens = %d, want 21383", got)
	}
	if initial, _ := deltas[0].Payload["initial_observation"].(bool); !initial {
		t.Fatalf("delta payload missing initial_observation: %#v", deltas[0].Payload)
	}
	if previous := payloadInt(deltas[0].Payload, "session_previous_total"); previous != 0 {
		t.Fatalf("session_previous_total = %d, want 0", previous)
	}
	if commit == nil {
		t.Fatal("expected commit callback")
	}
}

func TestBuildUsageDeltaEventsUsesPreviousCursorPayloadForBaselineSession(t *testing.T) {
	cfg := Config{DataDir: t.TempDir(), UsageWindow: "10m"}
	observedAt := time.Date(2026, 6, 22, 4, 30, 0, 0, time.UTC)
	event := usageSummaryEvent(140, 80, 60)
	event.Payload[internalPreviousTotalTokensKey] = int64(100)
	event.Payload[internalPreviousInputTokensKey] = int64(55)
	event.Payload[internalPreviousOutputTokensKey] = int64(45)
	event.Payload[internalPreviousObservedAtKey] = observedAt.Add(-time.Minute).Format(time.RFC3339Nano)

	deltas, _, err := BuildUsageDeltaEvents(cfg, []model.EventEnvelope{event}, observedAt)
	if err != nil {
		t.Fatalf("BuildUsageDeltaEvents: %v", err)
	}
	if len(deltas) != 1 {
		t.Fatalf("deltas = %#v, want one baseline delta", deltas)
	}
	payload := deltas[0].Payload
	if got := payloadInt(payload, "total_tokens"); got != 40 {
		t.Fatalf("total_tokens = %d, want 40", got)
	}
	if got := payloadInt(payload, "input_tokens"); got != 25 {
		t.Fatalf("input_tokens = %d, want 25", got)
	}
	if got := payloadInt(payload, "output_tokens"); got != 15 {
		t.Fatalf("output_tokens = %d, want 15", got)
	}
	if got := payloadInt(payload, "session_previous_total"); got != 100 {
		t.Fatalf("session_previous_total = %d, want 100", got)
	}
}

func TestBuildUsageDeltaEventsSeparatesAccountingModes(t *testing.T) {
	cfg := Config{DataDir: t.TempDir(), UsageWindow: "10m"}
	observedAt := time.Date(2026, 6, 22, 4, 30, 0, 0, time.UTC)

	legacy := usageSummaryEvent(100, 80, 20)
	deltas, commit, err := BuildUsageDeltaEvents(cfg, []model.EventEnvelope{legacy}, observedAt)
	if err != nil {
		t.Fatalf("BuildUsageDeltaEvents legacy: %v", err)
	}
	if len(deltas) != 0 {
		t.Fatalf("legacy deltas = %#v, want cursor seed only", deltas)
	}
	if err := commit(); err != nil {
		t.Fatalf("commit legacy: %v", err)
	}

	raw := usageSummaryEvent(1000, 900, 100)
	raw.Payload["token_accounting"] = "raw_total"
	deltas, commit, err = BuildUsageDeltaEvents(cfg, []model.EventEnvelope{raw}, observedAt.Add(time.Minute))
	if err != nil {
		t.Fatalf("BuildUsageDeltaEvents raw seed: %v", err)
	}
	if len(deltas) != 0 {
		t.Fatalf("raw seed deltas = %#v, want no historical accounting-mode jump", deltas)
	}
	if err := commit(); err != nil {
		t.Fatalf("commit raw seed: %v", err)
	}

	rawNext := usageSummaryEvent(1200, 1080, 120)
	rawNext.Payload["token_accounting"] = "raw_total"
	deltas, _, err = BuildUsageDeltaEvents(cfg, []model.EventEnvelope{rawNext}, observedAt.Add(2*time.Minute))
	if err != nil {
		t.Fatalf("BuildUsageDeltaEvents raw next: %v", err)
	}
	if len(deltas) != 1 {
		t.Fatalf("raw next deltas = %#v, want one delta", deltas)
	}
	if got := payloadInt(deltas[0].Payload, "total_tokens"); got != 200 {
		t.Fatalf("total_tokens = %d, want 200", got)
	}
}

func TestBuildUsageDeltaEventsComputesCacheTierDeltas(t *testing.T) {
	cfg := Config{
		DataDir:      t.TempDir(),
		CustomerID:   "acme",
		DeploymentID: "prod",
		DaemonID:     "daemon_1",
		DeviceID:     "device_1",
		UsageWindow:  "10m",
	}
	firstObservedAt := time.Date(2026, 6, 10, 12, 0, 0, 0, time.UTC)
	first := usageSummaryEvent(200, 100, 40)
	first.Payload["cached_input_tokens"] = int64(50)
	first.Payload["cache_creation_tokens"] = int64(10)
	deltas, commit, err := BuildUsageDeltaEvents(cfg, []model.EventEnvelope{first}, firstObservedAt)
	if err != nil {
		t.Fatalf("BuildUsageDeltaEvents first: %v", err)
	}
	if len(deltas) != 0 {
		t.Fatalf("first observation emitted deltas: %+v", deltas)
	}
	if err := commit(); err != nil {
		t.Fatalf("commit first: %v", err)
	}

	second := usageSummaryEvent(380, 180, 70)
	second.Payload["cached_input_tokens"] = int64(110)
	second.Payload["cache_creation_tokens"] = int64(20)
	deltas, commit, err = BuildUsageDeltaEvents(cfg, []model.EventEnvelope{second}, firstObservedAt.Add(5*time.Minute))
	if err != nil {
		t.Fatalf("BuildUsageDeltaEvents second: %v", err)
	}
	if len(deltas) != 1 {
		t.Fatalf("second observation emitted %d deltas, want 1", len(deltas))
	}
	payload := deltas[0].Payload
	if got := payloadInt(payload, "cache_read_tokens"); got != 60 {
		t.Fatalf("cache_read_tokens = %d, want 60", got)
	}
	if got := payloadInt(payload, "cache_write_tokens"); got != 10 {
		t.Fatalf("cache_write_tokens = %d, want 10", got)
	}
	if got := payloadInt(payload, "input_tokens"); got != 80 {
		t.Fatalf("input_tokens = %d, want 80", got)
	}
	if err := commit(); err != nil {
		t.Fatalf("commit second: %v", err)
	}
}

func usageSummaryEvent(total, input, output int64) model.EventEnvelope {
	return model.EventEnvelope{
		EventID:      "evt_summary",
		CustomerID:   "acme",
		DeploymentID: "prod",
		DaemonID:     "daemon_1",
		DeviceID:     "device_1",
		EventType:    "usage.summary",
		Source:       "codex",
		AgentType:    "codex",
		Payload: map[string]interface{}{
			"session_id":    "session_1",
			"model":         "gpt-5-codex",
			"source_hash":   "repo_hash",
			"total_tokens":  total,
			"input_tokens":  input,
			"output_tokens": output,
		},
	}
}

// cursorEventWithCache is a cumulative usage summary carrying cache counters.
func cursorEventWithCache(total, input, output, cacheRead, cacheWrite int64) model.EventEnvelope {
	return model.EventEnvelope{
		EventID:      "evt_cache",
		CustomerID:   "acme",
		DeploymentID: "prod",
		DaemonID:     "daemon_1",
		DeviceID:     "device_1",
		EventType:    "usage.summary",
		Source:       "codex",
		AgentType:    "codex",
		Payload: map[string]interface{}{
			"session_id":            "session_cache",
			"model":                 "gpt-5-codex",
			"total_tokens":          total,
			"input_tokens":          input,
			"output_tokens":         output,
			"cache_read_tokens":     cacheRead,
			"cache_creation_tokens": cacheWrite,
		},
	}
}

// An agent that upgrades with a session already in flight finds a cursor file
// written before cache accounting existed: it has no cache fields, so they
// deserialize as zero. Diffing the session's cumulative cache against that zero
// would emit its ENTIRE cache history as one window's delta — for a Codex cursor,
// which is session-lifetime, that is weeks of cache reads landing on a single day.
// The first post-upgrade poll must SEED the cache cursor instead, and the poll
// after it must resume reporting real increments.
func TestUsageDeltaDoesNotSpikeWhenUpgradingWithInFlightSession(t *testing.T) {
	cfg := Config{
		DataDir:      t.TempDir(),
		CustomerID:   "acme",
		DeploymentID: "prod",
		DaemonID:     "daemon_1",
		DeviceID:     "device_1",
		UsageWindow:  "10m",
	}
	t0 := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)

	// A session already being tracked before the upgrade.
	_, commit, err := BuildUsageDeltaEvents(cfg, []model.EventEnvelope{
		cursorEventWithCache(1_000, 600, 400, 5_000_000, 200_000),
	}, t0)
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := commit(); err != nil {
		t.Fatalf("commit seed: %v", err)
	}

	// Rewrite the cursor file the way an agent BUILT BEFORE cache accounting would
	// have left it: the cache fields simply are not there.
	path := filepath.Join(cfg.DataDir, usageCursorFile)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read cursor file: %v", err)
	}
	var legacy map[string]map[string]map[string]interface{}
	if err := json.Unmarshal(raw, &legacy); err != nil {
		t.Fatalf("unmarshal cursor file: %v", err)
	}
	for _, cursor := range legacy["sessions"] {
		delete(cursor, "cache_read_tokens")
		delete(cursor, "cache_write_tokens")
		delete(cursor, "cache_observed")
	}
	rewritten, err := json.Marshal(legacy)
	if err != nil {
		t.Fatalf("marshal legacy cursor: %v", err)
	}
	if err := os.WriteFile(path, rewritten, 0o600); err != nil {
		t.Fatalf("write legacy cursor file: %v", err)
	}

	// First poll after the upgrade. The session's cumulative cache is large; none of
	// it is new, and none of it may be emitted as this window's delta.
	deltas, commit, err := BuildUsageDeltaEvents(cfg, []model.EventEnvelope{
		cursorEventWithCache(1_500, 900, 600, 5_400_000, 220_000),
	}, t0.Add(12*time.Minute))
	if err != nil {
		t.Fatalf("post-upgrade poll: %v", err)
	}
	if len(deltas) != 1 {
		t.Fatalf("expected one delta after upgrade, got %d", len(deltas))
	}
	if got := payloadInt(deltas[0].Payload, "cache_read_tokens"); got != 0 {
		t.Fatalf("post-upgrade cache_read delta = %d, want 0 — the session's cumulative cache history was emitted as one window's delta", got)
	}
	if got := payloadInt(deltas[0].Payload, "cache_write_tokens"); got != 0 {
		t.Fatalf("post-upgrade cache_write delta = %d, want 0", got)
	}
	// Token deltas themselves must still be reported normally.
	if got := payloadInt(deltas[0].Payload, "total_tokens"); got != 500 {
		t.Fatalf("total delta = %d, want 500", got)
	}
	if err := commit(); err != nil {
		t.Fatalf("commit post-upgrade: %v", err)
	}

	// The poll after that must resume reporting REAL cache increments — the guard
	// must seed the cursor once, not suppress cache accounting forever.
	deltas, _, err = BuildUsageDeltaEvents(cfg, []model.EventEnvelope{
		cursorEventWithCache(2_000, 1_200, 800, 5_500_000, 230_000),
	}, t0.Add(24*time.Minute))
	if err != nil {
		t.Fatalf("steady-state poll: %v", err)
	}
	if len(deltas) != 1 {
		t.Fatalf("expected one delta in steady state, got %d", len(deltas))
	}
	if got := payloadInt(deltas[0].Payload, "cache_read_tokens"); got != 100_000 {
		t.Fatalf("steady-state cache_read delta = %d, want 100000 — cache accounting must resume, not stay suppressed", got)
	}
	if got := payloadInt(deltas[0].Payload, "cache_write_tokens"); got != 10_000 {
		t.Fatalf("steady-state cache_write delta = %d, want 10000", got)
	}
}

// The cursor-file guard is not enough on its own. If the usage cursor entry is
// missing but the BASELINE survives — the crash-between-phases case the Previous*
// machinery exists for, or a lost usage-cursors.json — the delta machinery
// reconstructs a cursor from the baseline payload instead. A baseline written
// before cache accounting has no cache counters, and writing them into that
// payload as zeros would make them look OBSERVED, bypassing the seeding guard and
// emitting the session's whole cumulative cache history as one window's delta.
// Absence has to survive the recovery path too.
func TestRecoveredCursorFromPreUpgradeBaselineDoesNotSpikeCache(t *testing.T) {
	observedAt := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)

	// A baseline persisted by a pre-upgrade agent: token counters, but no cache
	// counters and no record that cache was ever observed.
	legacy := baselineSession{
		TotalTokens:    1_000,
		InputTokens:    600,
		OutputTokens:   400,
		TokensObserved: true,
		UpdatedAt:      observedAt.Format(time.RFC3339Nano),
	}

	payload := map[string]interface{}{}
	markInternalPreviousCursor(payload, legacy)

	if _, present := payload[internalPreviousCacheReadTokensKey]; present {
		t.Fatal("a pre-upgrade baseline must not write cache counters into the recovery payload — their absence is what tells the delta machinery to seed rather than diff")
	}

	cursor, ok := previousUsageCursorFromPayload(payload, observedAt)
	if !ok {
		t.Fatal("recovery cursor should still be reconstructable from the token counters")
	}
	if cursor.CacheObserved {
		t.Fatal("recovered cursor claims cache was observed; the seeding guard in usageDeltaFromEvent would be bypassed and the session's cumulative cache emitted as one delta")
	}

	// A baseline that DID record cache counters must still round-trip them, or the
	// guard would suppress cache accounting forever instead of seeding once.
	current := legacy
	current.CacheReadTokens = 5_000_000
	current.CacheWriteTokens = 200_000
	current.CacheObserved = true

	payload = map[string]interface{}{}
	markInternalPreviousCursor(payload, current)
	cursor, ok = previousUsageCursorFromPayload(payload, observedAt)
	if !ok || !cursor.CacheObserved {
		t.Fatal("a baseline that observed cache must round-trip it as observed")
	}
	if cursor.CacheReadTokens != 5_000_000 {
		t.Fatalf("recovered cache read = %d, want 5000000", cursor.CacheReadTokens)
	}
}

// The seeding guard is only as good as the cursor it reads. A poll whose payload
// carries NO cache keys must not rewrite the cursor to an OBSERVED zero: the next
// poll that does carry them would diff the session's cumulative cache against that
// zero and emit the whole history as one window's delta — permanently, once in
// ClickHouse. Codex makes this reachable: its reader only emits cached_input_tokens
// when the rollout file parses, so the key can vanish for a cycle while the token
// total keeps advancing.
func TestKeylessPollDoesNotArmACacheSpikeOnTheNextPoll(t *testing.T) {
	cfg := Config{
		DataDir:      t.TempDir(),
		CustomerID:   "acme",
		DeploymentID: "prod",
		DaemonID:     "daemon_1",
		DeviceID:     "device_1",
		UsageWindow:  "10m",
	}
	t0 := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)

	// Establish a tracked session with real cache counters.
	_, commit, err := BuildUsageDeltaEvents(cfg, []model.EventEnvelope{
		cursorEventWithCache(1_000, 600, 400, 5_000_000, 200_000),
	}, t0)
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := commit(); err != nil {
		t.Fatalf("commit seed: %v", err)
	}

	// A poll whose payload lost the cache keys (unparseable rollout file) while the
	// token total still advanced.
	keyless := cursorEventWithCache(1_500, 900, 600, 0, 0)
	delete(keyless.Payload, "cache_read_tokens")
	delete(keyless.Payload, "cache_creation_tokens")
	_, commit, err = BuildUsageDeltaEvents(cfg, []model.EventEnvelope{keyless}, t0.Add(12*time.Minute))
	if err != nil {
		t.Fatalf("keyless poll: %v", err)
	}
	if err := commit(); err != nil {
		t.Fatalf("commit keyless: %v", err)
	}

	// The next poll carries the keys again, with the cache counters where they always
	// were. None of that is new, and none of it may be emitted as this window's delta.
	deltas, _, err := BuildUsageDeltaEvents(cfg, []model.EventEnvelope{
		cursorEventWithCache(2_000, 1_200, 800, 5_100_000, 210_000),
	}, t0.Add(24*time.Minute))
	if err != nil {
		t.Fatalf("recovery poll: %v", err)
	}
	if len(deltas) != 1 {
		t.Fatalf("expected one delta, got %d", len(deltas))
	}
	if got := payloadInt(deltas[0].Payload, "cache_read_tokens"); got != 0 {
		t.Fatalf("cache_read delta = %d, want 0 — a keyless poll rewrote the cursor to an observed zero and armed a cumulative-history spike", got)
	}
	if got := payloadInt(deltas[0].Payload, "cache_write_tokens"); got != 0 {
		t.Fatalf("cache_write delta = %d, want 0", got)
	}
}
