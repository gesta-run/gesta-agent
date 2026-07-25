package daemon

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/gesta-run/gesta-agent/pkg/model"
	"github.com/gesta-run/gesta-agent/pkg/util"
)

func filterAndCommitCodexSessionBackfill(
	cfg Config,
	stateDB string,
	usageEvents, transcriptEvents []map[string]interface{},
	observedAt time.Time,
) ([]map[string]interface{}, []map[string]interface{}, map[string]interface{}, error) {
	result, err := filterCodexSessionBackfill(cfg, stateDB, usageEvents, transcriptEvents, observedAt)
	if err != nil {
		return nil, nil, nil, err
	}
	if result.Commit != nil {
		if err := result.Commit(); err != nil {
			return nil, nil, nil, err
		}
	}
	return result.UsageEvents, result.TranscriptEvents, result.Meta, nil
}

func TestFilterCodexSessionBackfillStagesBaselineCommit(t *testing.T) {
	cfg := Config{DataDir: t.TempDir()}
	stateDB := filepath.Join(t.TempDir(), "state.sqlite")
	result, err := filterCodexSessionBackfill(
		cfg,
		stateDB,
		[]map[string]interface{}{{
			"session_id": "old-session", "total_tokens": int64(100),
			"updated_at": "2026-06-21T00:00:00Z",
		}},
		nil,
		time.Date(2026, 6, 22, 3, 0, 0, 0, time.UTC),
	)
	if err != nil {
		t.Fatalf("filterCodexSessionBackfill: %v", err)
	}
	if result.Commit == nil {
		t.Fatal("baseline initialization should return a staged commit")
	}
	if _, err := os.Stat(filepath.Join(cfg.DataDir, sessionBaselineFile)); !os.IsNotExist(err) {
		t.Fatalf("baseline was persisted before commit: %v", err)
	}
	if err := result.Commit(); err != nil {
		t.Fatalf("commit baseline: %v", err)
	}
	if _, err := os.Stat(filepath.Join(cfg.DataDir, sessionBaselineFile)); err != nil {
		t.Fatalf("baseline was not persisted after commit: %v", err)
	}
}

func TestAdapterBaselineCommitsPreserveEachOther(t *testing.T) {
	cfg := Config{DataDir: t.TempDir()}
	observedAt := time.Date(2026, 6, 22, 3, 0, 0, 0, time.UTC)
	codexResult, err := filterCodexSessionBackfill(
		cfg,
		filepath.Join(t.TempDir(), "state.sqlite"),
		[]map[string]interface{}{{"session_id": "codex-session", "total_tokens": int64(10)}},
		nil,
		observedAt,
	)
	if err != nil {
		t.Fatalf("filter Codex baseline: %v", err)
	}
	claudeResult, err := filterClaudeSessionBaseline(
		cfg,
		[]map[string]interface{}{{"session_id": "claude-session", "total_tokens": int64(20)}},
		nil,
		nil,
		observedAt,
	)
	if err != nil {
		t.Fatalf("filter Claude baseline: %v", err)
	}
	if err := claudeResult.Commit(); err != nil {
		t.Fatalf("commit Claude baseline: %v", err)
	}
	if err := codexResult.Commit(); err != nil {
		t.Fatalf("commit Codex baseline: %v", err)
	}

	store, err := loadSessionBaselineStore(cfg.DataDir)
	if err != nil {
		t.Fatalf("load merged baseline: %v", err)
	}
	if _, ok := store.ClaudeCode.Sessions["claude-session"]; !ok {
		t.Fatalf("Claude baseline was lost: %#v", store.ClaudeCode.Sessions)
	}
	var codexSessionFound bool
	for _, baseline := range store.Codex.StateDBs {
		if _, ok := baseline.Sessions["codex-session"]; ok {
			codexSessionFound = true
		}
	}
	if !codexSessionFound {
		t.Fatalf("Codex baseline was lost: %#v", store.Codex.StateDBs)
	}
}

func TestFilterCodexSessionBackfillInitializesBaseline(t *testing.T) {
	cfg := Config{DataDir: t.TempDir()}
	stateDB := filepath.Join(t.TempDir(), "state.sqlite")
	observedAt := time.Date(2026, 6, 22, 3, 0, 0, 0, time.UTC)

	usage := []map[string]interface{}{
		{"session_id": "old-session", "total_tokens": int64(100), "updated_at": "2026-06-21T00:00:00Z"},
	}
	transcripts := []map[string]interface{}{
		{"session_id": "old-session", "updated_at": "2026-06-21T00:00:00Z", "transcript_hash": "old-hash"},
	}

	filteredUsage, filteredTranscripts, meta, err := filterAndCommitCodexSessionBackfill(cfg, stateDB, usage, transcripts, observedAt)
	if err != nil {
		t.Fatalf("filterCodexSessionBackfill: %v", err)
	}
	if len(filteredUsage) != 0 || len(filteredTranscripts) != 0 {
		t.Fatalf("initial collection should not emit historical sessions: usage=%#v transcripts=%#v", filteredUsage, filteredTranscripts)
	}
	if initialized, _ := meta["session_baseline_initialized"].(bool); !initialized {
		t.Fatalf("expected session baseline to initialize, meta=%#v", meta)
	}
	if ignored, _ := meta["historical_sessions_ignored"].(int); ignored != 1 {
		t.Fatalf("historical_sessions_ignored = %#v, want 1", meta["historical_sessions_ignored"])
	}

	store, err := loadSessionBaselineStore(cfg.DataDir)
	if err != nil {
		t.Fatalf("load baseline: %v", err)
	}
	stateDBHash := util.ShortHash(stateDB)
	baseline := store.Codex.StateDBs[stateDBHash]
	if baseline.InitializedAt == "" {
		t.Fatal("baseline was not persisted")
	}
	session, ok := baseline.Sessions["old-session"]
	if !ok {
		t.Fatalf("baseline sessions = %#v, want old-session", baseline.Sessions)
	}
	if session.TranscriptHash != "old-hash" {
		t.Fatalf("baseline transcript hash = %q, want old-hash", session.TranscriptHash)
	}
	if !session.TokensObserved || session.TotalTokens != 100 {
		t.Fatalf("baseline tokens = %+v, want observed total 100", session)
	}
}

func TestFilterCodexSessionBackfillSkipsBaselineAndKeepsNewSessions(t *testing.T) {
	cfg := Config{DataDir: t.TempDir()}
	stateDB := filepath.Join(t.TempDir(), "state.sqlite")
	observedAt := time.Date(2026, 6, 22, 3, 0, 0, 0, time.UTC)

	if _, _, _, err := filterAndCommitCodexSessionBackfill(cfg, stateDB, []map[string]interface{}{
		{"session_id": "old-session", "total_tokens": int64(100), "updated_at": "2026-06-21T00:00:00Z"},
	}, []map[string]interface{}{
		{"session_id": "old-session", "updated_at": "2026-06-21T00:00:00Z", "transcript_hash": "old-hash"},
	}, observedAt); err != nil {
		t.Fatalf("initialize baseline: %v", err)
	}

	usage := []map[string]interface{}{
		{"session_id": "old-session", "total_tokens": int64(100), "updated_at": "2026-06-21T00:00:00Z"},
		{"session_id": "new-session", "total_tokens": int64(10), "updated_at": "2026-06-22T00:00:00Z"},
	}
	transcripts := []map[string]interface{}{
		{"session_id": "old-session", "updated_at": "2026-06-21T00:00:00Z", "transcript_hash": "old-hash"},
		{"session_id": "new-session", "updated_at": "2026-06-22T00:00:00Z", "transcript_hash": "new-hash"},
	}

	filteredUsage, filteredTranscripts, meta, err := filterAndCommitCodexSessionBackfill(cfg, stateDB, usage, transcripts, observedAt.Add(time.Minute))
	if err != nil {
		t.Fatalf("filterCodexSessionBackfill: %v", err)
	}
	if len(filteredUsage) != 1 || filteredUsage[0]["session_id"] != "new-session" {
		t.Fatalf("filtered usage = %#v, want only new-session", filteredUsage)
	}
	if cursorOnly, _ := filteredUsage[0][internalCursorOnlyPayloadKey].(bool); !cursorOnly {
		t.Fatalf("new usage summary should be cursor-only: %#v", filteredUsage[0])
	}
	if initialDelta, _ := filteredUsage[0][internalInitialDeltaPayloadKey].(bool); !initialDelta {
		t.Fatalf("new usage summary should emit an initial delta: %#v", filteredUsage[0])
	}
	if len(filteredTranscripts) != 1 || filteredTranscripts[0]["session_id"] != "new-session" {
		t.Fatalf("filtered transcripts = %#v, want only new-session", filteredTranscripts)
	}
	if ignored, _ := meta["historical_sessions_ignored"].(int); ignored != 1 {
		t.Fatalf("historical_sessions_ignored = %#v, want 1", meta["historical_sessions_ignored"])
	}

	store, err := loadSessionBaselineStore(cfg.DataDir)
	if err != nil {
		t.Fatalf("load baseline: %v", err)
	}
	baseline := store.Codex.StateDBs[util.ShortHash(stateDB)]
	if baseline.Sessions["new-session"].TranscriptHash != "new-hash" {
		t.Fatalf("new session baseline = %#v, want transcript hash new-hash", baseline.Sessions["new-session"])
	}
}

func TestFilterCodexSessionBackfillUsesForkParentBaselineForNewSession(t *testing.T) {
	cfg := Config{DataDir: t.TempDir(), UsageWindow: "10m"}
	stateDB := filepath.Join(t.TempDir(), "state.sqlite")
	observedAt := time.Date(2026, 6, 22, 3, 0, 0, 0, time.UTC)

	if _, _, _, err := filterAndCommitCodexSessionBackfill(cfg, stateDB, []map[string]interface{}{
		{"session_id": "parent-session", "total_tokens": int64(100), "input_tokens": int64(60), "output_tokens": int64(40), "updated_at": "2026-06-21T00:00:00Z"},
	}, nil, observedAt); err != nil {
		t.Fatalf("initialize baseline: %v", err)
	}

	filteredUsage, _, _, err := filterAndCommitCodexSessionBackfill(cfg, stateDB, []map[string]interface{}{
		{
			"session_id":        "child-session",
			"parent_session_id": "parent-session",
			"total_tokens":      int64(150),
			"input_tokens":      int64(90),
			"output_tokens":     int64(60),
			"updated_at":        "2026-06-22T00:00:00Z",
		},
	}, nil, observedAt.Add(time.Minute))
	if err != nil {
		t.Fatalf("filterCodexSessionBackfill: %v", err)
	}
	if len(filteredUsage) != 1 || filteredUsage[0]["session_id"] != "child-session" {
		t.Fatalf("filtered usage = %#v, want only child-session", filteredUsage)
	}
	if initialDelta, _ := filteredUsage[0][internalInitialDeltaPayloadKey].(bool); initialDelta {
		t.Fatalf("fork child should not emit from zero: %#v", filteredUsage[0])
	}
	if previous := payloadInt(filteredUsage[0], internalPreviousTotalTokensKey); previous != 100 {
		t.Fatalf("previous total = %d, want parent total 100", previous)
	}

	event := usageSummaryEvent(0, 0, 0)
	event.Payload = filteredUsage[0]
	deltas, _, err := BuildUsageDeltaEvents(cfg, []model.EventEnvelope{event}, observedAt.Add(2*time.Minute))
	if err != nil {
		t.Fatalf("BuildUsageDeltaEvents: %v", err)
	}
	if len(deltas) != 1 {
		t.Fatalf("deltas = %#v, want one fork delta", deltas)
	}
	if got := payloadInt(deltas[0].Payload, "total_tokens"); got != 50 {
		t.Fatalf("delta total = %d, want 50", got)
	}
	if got := payloadInt(deltas[0].Payload, "input_tokens"); got != 30 {
		t.Fatalf("delta input = %d, want 30", got)
	}
	if got := payloadInt(deltas[0].Payload, "output_tokens"); got != 20 {
		t.Fatalf("delta output = %d, want 20", got)
	}
	if got := payloadInt(deltas[0].Payload, "session_previous_total"); got != 100 {
		t.Fatalf("session_previous_total = %d, want 100", got)
	}
}

func TestFilterCodexSessionBackfillSeedsForkWithoutKnownParent(t *testing.T) {
	cfg := Config{DataDir: t.TempDir(), UsageWindow: "10m"}
	stateDB := filepath.Join(t.TempDir(), "state.sqlite")
	observedAt := time.Date(2026, 6, 22, 3, 0, 0, 0, time.UTC)

	if _, _, _, err := filterAndCommitCodexSessionBackfill(cfg, stateDB, []map[string]interface{}{
		{"session_id": "old-session", "total_tokens": int64(100), "updated_at": "2026-06-21T00:00:00Z"},
	}, nil, observedAt); err != nil {
		t.Fatalf("initialize baseline: %v", err)
	}

	filteredUsage, _, _, err := filterAndCommitCodexSessionBackfill(cfg, stateDB, []map[string]interface{}{
		{
			"session_id":        "child-session",
			"parent_session_id": "missing-parent",
			"total_tokens":      int64(150),
			"updated_at":        "2026-06-22T00:00:00Z",
		},
	}, nil, observedAt.Add(time.Minute))
	if err != nil {
		t.Fatalf("filterCodexSessionBackfill: %v", err)
	}
	if len(filteredUsage) != 1 {
		t.Fatalf("filtered usage = %#v, want one cursor seed", filteredUsage)
	}
	if initialDelta, _ := filteredUsage[0][internalInitialDeltaPayloadKey].(bool); initialDelta {
		t.Fatalf("fork child without known parent should not emit from zero: %#v", filteredUsage[0])
	}
	if _, ok := filteredUsage[0][internalPreviousTotalTokensKey]; ok {
		t.Fatalf("unknown parent should not synthesize a previous cursor: %#v", filteredUsage[0])
	}

	event := usageSummaryEvent(0, 0, 0)
	event.Payload = filteredUsage[0]
	deltas, _, err := BuildUsageDeltaEvents(cfg, []model.EventEnvelope{event}, observedAt.Add(2*time.Minute))
	if err != nil {
		t.Fatalf("BuildUsageDeltaEvents: %v", err)
	}
	if len(deltas) != 0 {
		t.Fatalf("deltas = %#v, want fork cursor seed only", deltas)
	}
}

func TestFilterCodexSessionBackfillKeepsChangedBaselineSession(t *testing.T) {
	cfg := Config{DataDir: t.TempDir()}
	stateDB := filepath.Join(t.TempDir(), "state.sqlite")
	observedAt := time.Date(2026, 6, 22, 3, 0, 0, 0, time.UTC)

	if _, _, _, err := filterAndCommitCodexSessionBackfill(cfg, stateDB, []map[string]interface{}{
		{"session_id": "old-session", "total_tokens": int64(100), "updated_at": "2026-06-21T00:00:00Z"},
	}, []map[string]interface{}{
		{"session_id": "old-session", "updated_at": "2026-06-21T00:00:00Z", "transcript_hash": "hash-1"},
	}, observedAt); err != nil {
		t.Fatalf("initialize baseline: %v", err)
	}

	usage := []map[string]interface{}{
		{"session_id": "old-session", "total_tokens": int64(120), "updated_at": "2026-06-22T00:00:00Z"},
	}
	transcripts := []map[string]interface{}{
		{"session_id": "old-session", "updated_at": "2026-06-22T00:00:00Z", "transcript_hash": "hash-2"},
	}

	filteredUsage, filteredTranscripts, _, err := filterAndCommitCodexSessionBackfill(cfg, stateDB, usage, transcripts, observedAt.Add(time.Minute))
	if err != nil {
		t.Fatalf("filterCodexSessionBackfill: %v", err)
	}
	if len(filteredUsage) != 1 || filteredUsage[0]["session_id"] != "old-session" {
		t.Fatalf("filtered usage = %#v, want changed old-session usage", filteredUsage)
	}
	if previous := payloadInt(filteredUsage[0], internalPreviousTotalTokensKey); previous != 100 {
		t.Fatalf("previous total = %d, want 100", previous)
	}
	if initialDelta, _ := filteredUsage[0][internalInitialDeltaPayloadKey].(bool); initialDelta {
		t.Fatalf("old baseline session should not be marked as fresh initial delta: %#v", filteredUsage[0])
	}
	if len(filteredTranscripts) != 1 || filteredTranscripts[0]["session_id"] != "old-session" {
		t.Fatalf("filtered transcripts = %#v, want changed old-session transcript", filteredTranscripts)
	}

	store, err := loadSessionBaselineStore(cfg.DataDir)
	if err != nil {
		t.Fatalf("load baseline: %v", err)
	}
	baseline := store.Codex.StateDBs[util.ShortHash(stateDB)]
	if baseline.Sessions["old-session"].TranscriptHash != "hash-2" {
		t.Fatalf("baseline transcript hash = %q, want hash-2", baseline.Sessions["old-session"].TranscriptHash)
	}
	if baseline.Sessions["old-session"].TotalTokens != 120 {
		t.Fatalf("baseline total tokens = %d, want 120", baseline.Sessions["old-session"].TotalTokens)
	}
}

func TestFilterCodexSessionBackfillSeedsLegacyTokenBaselineBeforeDelta(t *testing.T) {
	cfg := Config{DataDir: t.TempDir()}
	stateDB := filepath.Join(t.TempDir(), "state.sqlite")
	observedAt := time.Date(2026, 6, 22, 3, 0, 0, 0, time.UTC)

	if _, _, _, err := filterAndCommitCodexSessionBackfill(cfg, stateDB, nil, []map[string]interface{}{
		{"session_id": "old-session", "updated_at": "2026-06-21T00:00:00Z"},
	}, observedAt); err != nil {
		t.Fatalf("initialize baseline: %v", err)
	}

	filteredUsage, filteredTranscripts, _, err := filterAndCommitCodexSessionBackfill(cfg, stateDB, []map[string]interface{}{
		{"session_id": "old-session", "total_tokens": int64(100), "updated_at": "2026-06-22T00:00:00Z"},
	}, []map[string]interface{}{
		{"session_id": "old-session", "updated_at": "2026-06-22T00:00:00Z", "transcript_hash": "hash-after-upgrade"},
	}, observedAt.Add(time.Minute))
	if err != nil {
		t.Fatalf("filterCodexSessionBackfill: %v", err)
	}
	if len(filteredUsage) != 0 {
		t.Fatalf("filtered usage = %#v, want legacy baseline seeding to skip usage", filteredUsage)
	}
	if len(filteredTranscripts) != 1 || filteredTranscripts[0]["session_id"] != "old-session" {
		t.Fatalf("filtered transcripts = %#v, want newer old-session transcript", filteredTranscripts)
	}
	store, err := loadSessionBaselineStore(cfg.DataDir)
	if err != nil {
		t.Fatalf("load baseline: %v", err)
	}
	baseline := store.Codex.StateDBs[util.ShortHash(stateDB)]
	if !baseline.Sessions["old-session"].TokensObserved || baseline.Sessions["old-session"].TotalTokens != 100 {
		t.Fatalf("legacy baseline tokens = %+v, want seeded total 100", baseline.Sessions["old-session"])
	}
}

func TestFilterCodexSessionBackfillMigratesTokenAccountingWithoutBackfill(t *testing.T) {
	cfg := Config{DataDir: t.TempDir()}
	stateDB := filepath.Join(t.TempDir(), "state.sqlite")
	observedAt := time.Date(2026, 6, 22, 3, 0, 0, 0, time.UTC)

	if _, _, _, err := filterAndCommitCodexSessionBackfill(cfg, stateDB, []map[string]interface{}{
		{"session_id": "old-session", "total_tokens": int64(100), "input_tokens": int64(80), "output_tokens": int64(20), "updated_at": "2026-06-21T00:00:00Z"},
	}, nil, observedAt); err != nil {
		t.Fatalf("initialize baseline: %v", err)
	}

	filteredUsage, _, _, err := filterAndCommitCodexSessionBackfill(cfg, stateDB, []map[string]interface{}{
		{"session_id": "old-session", "total_tokens": int64(1000), "input_tokens": int64(900), "output_tokens": int64(100), "token_accounting": "raw_total", "updated_at": "2026-06-22T00:00:00Z"},
	}, nil, observedAt.Add(time.Minute))
	if err != nil {
		t.Fatalf("migrate accounting baseline: %v", err)
	}
	if len(filteredUsage) != 0 {
		t.Fatalf("filtered usage = %#v, want accounting migration to skip historical raw jump", filteredUsage)
	}

	filteredUsage, _, _, err = filterAndCommitCodexSessionBackfill(cfg, stateDB, []map[string]interface{}{
		{"session_id": "old-session", "total_tokens": int64(1200), "input_tokens": int64(1080), "output_tokens": int64(120), "token_accounting": "raw_total", "updated_at": "2026-06-22T00:01:00Z"},
	}, nil, observedAt.Add(2*time.Minute))
	if err != nil {
		t.Fatalf("post-migration delta: %v", err)
	}
	if len(filteredUsage) != 1 {
		t.Fatalf("filtered usage = %#v, want one post-migration usage event", filteredUsage)
	}
	if previous := payloadInt(filteredUsage[0], internalPreviousTotalTokensKey); previous != 1000 {
		t.Fatalf("previous total = %d, want migrated raw total 1000", previous)
	}
	if previousMode := firstPayloadString(filteredUsage[0], internalPreviousAccountingKey); previousMode != "raw_total" {
		t.Fatalf("previous accounting = %q, want raw_total", previousMode)
	}
}

func TestFilterCodexSessionBackfillSeedsParserCorrectionWithoutDelta(t *testing.T) {
	cfg := Config{DataDir: t.TempDir()}
	stateDB := filepath.Join(t.TempDir(), "state.sqlite")
	observedAt := time.Date(2026, 6, 22, 3, 0, 0, 0, time.UTC)

	if _, _, _, err := filterAndCommitCodexSessionBackfill(cfg, stateDB, []map[string]interface{}{
		{"session_id": "old-session", "total_tokens": int64(100), "input_tokens": int64(80), "output_tokens": int64(20), "token_accounting": "raw_total", "updated_at": "2026-06-21T00:00:00Z"},
	}, nil, observedAt); err != nil {
		t.Fatalf("initialize baseline: %v", err)
	}

	filteredUsage, _, _, err := filterAndCommitCodexSessionBackfill(cfg, stateDB, []map[string]interface{}{
		{"session_id": "old-session", "total_tokens": int64(150), "input_tokens": int64(120), "output_tokens": int64(30), "token_accounting": "raw_total", "updated_at": "2026-06-21T00:00:00Z"},
	}, nil, observedAt.Add(time.Minute))
	if err != nil {
		t.Fatalf("parser correction baseline: %v", err)
	}
	if len(filteredUsage) != 0 {
		t.Fatalf("filtered usage = %#v, want parser correction to seed baseline only", filteredUsage)
	}

	store, err := loadSessionBaselineStore(cfg.DataDir)
	if err != nil {
		t.Fatalf("load baseline: %v", err)
	}
	session := store.Codex.StateDBs[util.ShortHash(stateDB)].Sessions["old-session"]
	if session.TotalTokens != 150 {
		t.Fatalf("baseline total = %d, want corrected total 150", session.TotalTokens)
	}
}

func TestFilterCodexSessionBackfillKeepsPostCutoffTranscriptForLegacyBaseline(t *testing.T) {
	cfg := Config{DataDir: t.TempDir()}
	stateDB := filepath.Join(t.TempDir(), "state.sqlite")
	observedAt := time.Date(2026, 6, 22, 3, 0, 0, 0, time.UTC)

	if _, _, _, err := filterAndCommitCodexSessionBackfill(cfg, stateDB, []map[string]interface{}{
		{"session_id": "old-session", "total_tokens": int64(100)},
	}, nil, observedAt); err != nil {
		t.Fatalf("initialize baseline: %v", err)
	}

	_, filteredTranscripts, _, err := filterAndCommitCodexSessionBackfill(cfg, stateDB, nil, []map[string]interface{}{
		{"session_id": "old-session", "updated_at": "2026-06-22T03:01:00Z", "transcript_hash": "post-cutoff-hash"},
	}, observedAt.Add(2*time.Minute))
	if err != nil {
		t.Fatalf("filterCodexSessionBackfill: %v", err)
	}
	if len(filteredTranscripts) != 1 || filteredTranscripts[0]["session_id"] != "old-session" {
		t.Fatalf("filtered transcripts = %#v, want post-cutoff old-session transcript", filteredTranscripts)
	}
}

func TestFilterInternalEventsDropsCursorOnlyEvents(t *testing.T) {
	events := []model.EventEnvelope{
		{
			EventType: "usage.summary",
			Payload: map[string]interface{}{
				internalCursorOnlyPayloadKey: true,
			},
		},
		{
			EventType: "agent.discovery",
			Payload:   map[string]interface{}{"ok": true},
		},
	}

	filtered, count := filterInternalEvents(events)
	if count != 1 {
		t.Fatalf("filtered count = %d, want 1", count)
	}
	if len(filtered) != 1 || filtered[0].EventType != "agent.discovery" {
		t.Fatalf("filtered events = %#v, want only agent.discovery", filtered)
	}
}

func TestLoadSessionBaselineStoreRecoversTrailingGarbage(t *testing.T) {
	dataDir := t.TempDir()
	stateDB := filepath.Join(t.TempDir(), "state.sqlite")
	if _, _, _, err := filterAndCommitCodexSessionBackfill(Config{DataDir: dataDir}, stateDB, []map[string]interface{}{
		{"session_id": "old-session", "total_tokens": int64(100), "updated_at": "2026-06-21T00:00:00Z"},
	}, nil, time.Date(2026, 6, 22, 3, 0, 0, 0, time.UTC)); err != nil {
		t.Fatalf("initialize baseline: %v", err)
	}

	path := filepath.Join(dataDir, sessionBaselineFile)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read baseline: %v", err)
	}
	if err := os.WriteFile(path, append(data, '}'), 0o600); err != nil {
		t.Fatalf("corrupt baseline: %v", err)
	}

	store, err := loadSessionBaselineStore(dataDir)
	if err != nil {
		t.Fatalf("load corrupt baseline: %v", err)
	}
	if _, ok := store.Codex.StateDBs[util.ShortHash(stateDB)].Sessions["old-session"]; !ok {
		t.Fatalf("recovered sessions = %#v, want old-session", store.Codex.StateDBs[util.ShortHash(stateDB)].Sessions)
	}

	repaired, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read repaired baseline: %v", err)
	}
	var decoded sessionBaselineStore
	if err := json.Unmarshal(repaired, &decoded); err != nil {
		t.Fatalf("baseline was not repaired: %v", err)
	}
}
