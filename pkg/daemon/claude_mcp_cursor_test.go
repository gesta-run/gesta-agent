package daemon

import (
	"testing"
	"time"
)

func TestClaudeMCPCursorCommitsOnlyAfterDurableQueueAppend(t *testing.T) {
	cfg := Config{DataDir: t.TempDir()}
	baselineAt := time.Date(2026, 7, 24, 8, 0, 0, 0, time.UTC)
	historical := claudeMCPTestSession("toolu_historical", "2026-07-24T07:00:00Z")

	initial, err := collectClaudeEventsFromSessions(cfg, []claudeSessionUsage{historical}, baselineAt)
	if err != nil {
		t.Fatalf("prepare initial baseline: %v", err)
	}
	if len(initial.MCPEvents) != 0 || initial.Commit == nil {
		t.Fatalf("initial collection = %#v, want a deferred baseline commit", initial)
	}
	if err := initial.Commit(); err != nil {
		t.Fatalf("commit initial baseline: %v", err)
	}

	current := historical
	current.MCPToolCalls = append(current.MCPToolCalls, claudeMCPTestCall(
		"toolu_current",
		"2026-07-24T08:01:00Z",
	))
	prepared, err := collectClaudeEventsFromSessions(cfg, []claudeSessionUsage{current}, baselineAt.Add(2*time.Minute))
	if err != nil {
		t.Fatalf("prepare current call: %v", err)
	}
	if len(prepared.MCPEvents) != 1 || prepared.Commit == nil {
		t.Fatalf("current collection = %#v, want one event and a deferred commit", prepared)
	}

	retry, err := collectClaudeEventsFromSessions(cfg, []claudeSessionUsage{current}, baselineAt.Add(3*time.Minute))
	if err != nil {
		t.Fatalf("retry before commit: %v", err)
	}
	if len(retry.MCPEvents) != 1 || retry.MCPEvents[0].EventID != prepared.MCPEvents[0].EventID {
		t.Fatalf("uncommitted event was not retried deterministically: first=%#v retry=%#v", prepared.MCPEvents, retry.MCPEvents)
	}

	if err := prepared.Commit(); err != nil {
		t.Fatalf("commit current cursor: %v", err)
	}
	afterCommit, err := collectClaudeEventsFromSessions(cfg, []claudeSessionUsage{current}, baselineAt.Add(4*time.Minute))
	if err != nil {
		t.Fatalf("collect after commit: %v", err)
	}
	if len(afterCommit.MCPEvents) != 0 {
		t.Fatalf("committed event was re-emitted: %#v", afterCommit.MCPEvents)
	}
}

func TestClaudeMCPCursorRetainsOnlyLatestTimestampEventIDs(t *testing.T) {
	cfg := Config{DataDir: t.TempDir()}
	baselineAt := time.Date(2026, 7, 24, 8, 0, 0, 0, time.UTC)
	session := claudeMCPTestSession("toolu_historical", "2026-07-24T07:00:00Z")

	initial, err := collectClaudeEventsFromSessions(cfg, []claudeSessionUsage{session}, baselineAt)
	if err != nil {
		t.Fatalf("prepare initial baseline: %v", err)
	}
	if err := initial.Commit(); err != nil {
		t.Fatalf("commit initial baseline: %v", err)
	}

	session.MCPToolCalls = append(session.MCPToolCalls,
		claudeMCPTestCall("toolu_same_time_a", "2026-07-24T08:01:00Z"),
		claudeMCPTestCall("toolu_same_time_b", "2026-07-24T08:01:00Z"),
	)
	current, err := collectClaudeEventsFromSessions(cfg, []claudeSessionUsage{session}, baselineAt.Add(2*time.Minute))
	if err != nil {
		t.Fatalf("prepare same-time calls: %v", err)
	}
	if len(current.MCPEvents) != 2 {
		t.Fatalf("same-time events = %#v, want two", current.MCPEvents)
	}
	if err := current.Commit(); err != nil {
		t.Fatalf("commit same-time cursor: %v", err)
	}

	store, err := loadSessionBaselineStore(cfg.DataDir)
	if err != nil {
		t.Fatalf("load committed baseline: %v", err)
	}
	cursor := store.ClaudeCode.Sessions[current.MCPEvents[0].Payload["session_id"].(string)]
	if cursor.MCPToolCallCursorAt != "2026-07-24T08:01:00Z" {
		t.Fatalf("cursor timestamp = %q", cursor.MCPToolCallCursorAt)
	}
	if len(cursor.MCPToolCallCursorEventIDs) != 2 {
		t.Fatalf("cursor event IDs = %#v, want two IDs at latest timestamp", cursor.MCPToolCallCursorEventIDs)
	}

	retry, err := collectClaudeEventsFromSessions(cfg, []claudeSessionUsage{session}, baselineAt.Add(3*time.Minute))
	if err != nil {
		t.Fatalf("retry same-time calls: %v", err)
	}
	if len(retry.MCPEvents) != 0 {
		t.Fatalf("same-time calls were re-emitted: %#v", retry.MCPEvents)
	}
}

func TestClaudeBaselineCommitPreservesLatestCodexState(t *testing.T) {
	cfg := Config{DataDir: t.TempDir()}
	observedAt := time.Date(2026, 7, 24, 8, 0, 0, 0, time.UTC)
	collection, err := collectClaudeEventsFromSessions(
		cfg,
		[]claudeSessionUsage{claudeMCPTestSession("toolu_historical", "2026-07-24T07:00:00Z")},
		observedAt,
	)
	if err != nil {
		t.Fatalf("prepare Claude baseline: %v", err)
	}

	store := newSessionBaselineStore()
	store.Codex.StateDBs["state_db"] = codexSessionBaseline{
		InitializedAt: observedAt.Format(time.RFC3339Nano),
		StateDBHash:   "state_db",
		Sessions: map[string]baselineSession{
			"codex_session": {TranscriptHash: "latest"},
		},
	}
	if err := saveSessionBaselineStore(cfg.DataDir, store); err != nil {
		t.Fatalf("save concurrent Codex state: %v", err)
	}
	if err := collection.Commit(); err != nil {
		t.Fatalf("commit Claude baseline: %v", err)
	}

	committed, err := loadSessionBaselineStore(cfg.DataDir)
	if err != nil {
		t.Fatalf("load combined baseline: %v", err)
	}
	if got := committed.Codex.StateDBs["state_db"].Sessions["codex_session"].TranscriptHash; got != "latest" {
		t.Fatalf("Claude commit overwrote Codex state: transcript hash = %q", got)
	}
	if committed.ClaudeCode.MCPInitializedAt == "" {
		t.Fatal("Claude state was not committed")
	}
}

func claudeMCPTestSession(callID, timestamp string) claudeSessionUsage {
	return claudeSessionUsage{
		SessionID:    claudeSessionUUID,
		ByModelDay:   map[claudeModelDayKey]claudeAssistantUsage{},
		MCPToolCalls: []claudeTranscriptToolCall{claudeMCPTestCall(callID, timestamp)},
	}
}

func claudeMCPTestCall(callID, timestamp string) claudeTranscriptToolCall {
	return claudeTranscriptToolCall{
		Name:      "mcp__github__search",
		CallID:    callID,
		Timestamp: timestamp,
		MCPServer: "github",
		MCPTool:   "search",
	}
}
