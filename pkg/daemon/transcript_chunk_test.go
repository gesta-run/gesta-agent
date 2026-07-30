package daemon

import (
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/gesta-run/gesta-agent/pkg/util"
)

func TestPrepareTranscriptChunksEmitsOnlyUnseenMessages(t *testing.T) {
	observedAt := time.Date(2026, 7, 28, 10, 0, 0, 0, time.UTC)
	first := transcriptTestPayload("hash-1", []map[string]interface{}{
		transcriptTestMessage("user", "first", observedAt),
		transcriptTestMessage("assistant", "reply", observedAt.Add(time.Minute)),
	})
	chunks, cursor := prepareTranscriptChunks(first, baselineSession{}, observedAt, time.UTC)
	if len(chunks) != 1 || payloadInt(chunks[0], "message_count") != 2 {
		t.Fatalf("first chunks = %#v, want one chunk with two messages", chunks)
	}
	if cursor.TranscriptSequence != 1 || len(cursor.TranscriptMessageVersions) != 2 ||
		!cursor.TranscriptCursorInitialized {
		t.Fatalf("first cursor = %#v", cursor)
	}

	second := transcriptTestPayload("hash-2", []map[string]interface{}{
		transcriptTestMessage("user", "first", observedAt),
		transcriptTestMessage("assistant", "reply", observedAt.Add(time.Minute)),
		transcriptTestMessage("user", "second", observedAt.Add(2*time.Minute)),
	})
	chunks, cursor = prepareTranscriptChunks(second, cursor, observedAt.Add(2*time.Minute), time.UTC)
	if len(chunks) != 1 || payloadInt(chunks[0], "message_count") != 1 {
		t.Fatalf("second chunks = %#v, want one chunk with one message", chunks)
	}
	if payloadInt(chunks[0], "sequence") != 2 || cursor.TranscriptSequence != 2 {
		t.Fatalf("second sequence = chunk %d cursor %d, want 2", payloadInt(chunks[0], "sequence"), cursor.TranscriptSequence)
	}
}

func TestPrepareTranscriptChunksIsStableAcrossRetry(t *testing.T) {
	observedAt := time.Date(2026, 7, 28, 10, 0, 0, 0, time.UTC)
	payload := transcriptTestPayload("hash", []map[string]interface{}{
		transcriptTestMessage("user", "first", observedAt),
	})
	first, _ := prepareTranscriptChunks(payload, baselineSession{}, observedAt, time.UTC)
	retry, _ := prepareTranscriptChunks(payload, baselineSession{}, observedAt.Add(time.Hour), time.UTC)
	if len(first) != 1 || len(retry) != 1 {
		t.Fatalf("chunks = %d/%d, want 1/1", len(first), len(retry))
	}
	if firstString(first[0], "chunk_id") != firstString(retry[0], "chunk_id") {
		t.Fatalf("chunk ids differ: %q != %q", firstString(first[0], "chunk_id"), firstString(retry[0], "chunk_id"))
	}
	if transcriptChunkEventID(first[0]) != transcriptChunkEventID(retry[0]) {
		t.Fatal("event ids should be stable across retry")
	}
}

func TestPrepareTranscriptChunksEmitsMessageRevisionWithStableID(t *testing.T) {
	observedAt := time.Date(2026, 7, 28, 10, 0, 0, 0, time.UTC)
	firstPayload := transcriptTestPayload("hash-1", []map[string]interface{}{{
		"message_id": "source-message-1",
		"role":       "assistant",
		"text":       "partial",
		"timestamp":  observedAt.Format(time.RFC3339Nano),
	}})
	firstChunks, cursor := prepareTranscriptChunks(firstPayload, baselineSession{}, observedAt, time.UTC)

	revisedPayload := transcriptTestPayload("hash-2", []map[string]interface{}{{
		"message_id": "source-message-1",
		"role":       "assistant",
		"text":       "complete response",
		"timestamp":  observedAt.Format(time.RFC3339Nano),
	}})
	revisedChunks, _ := prepareTranscriptChunks(revisedPayload, cursor, observedAt.Add(time.Second), time.UTC)
	if len(firstChunks) != 1 || len(revisedChunks) != 1 {
		t.Fatalf("chunks first=%d revised=%d, want 1/1", len(firstChunks), len(revisedChunks))
	}
	firstMessages := firstChunks[0]["messages"].([]map[string]interface{})
	revisedMessages := revisedChunks[0]["messages"].([]map[string]interface{})
	if firstMessages[0]["message_id"] != revisedMessages[0]["message_id"] {
		t.Fatalf("revision changed message id: %q != %q", firstMessages[0]["message_id"], revisedMessages[0]["message_id"])
	}
	if firstString(revisedMessages[0], "text") != "complete response" {
		t.Fatalf("revised messages = %#v", revisedMessages)
	}
}

func TestPrepareTranscriptChunksEmitsSummaryPhaseRevision(t *testing.T) {
	observedAt := time.Date(2026, 7, 28, 10, 0, 0, 0, time.UTC)
	progressPayload := transcriptTestPayload("hash-1", []map[string]interface{}{{
		"message_id":    "source-message-1",
		"role":          "assistant",
		"text":          "same response",
		"timestamp":     observedAt.Format(time.RFC3339Nano),
		"summary_phase": "progress",
	}})
	firstChunks, cursor := prepareTranscriptChunks(progressPayload, baselineSession{}, observedAt, time.UTC)

	finalPayload := transcriptTestPayload("hash-2", []map[string]interface{}{{
		"message_id":    "source-message-1",
		"role":          "assistant",
		"text":          "same response",
		"timestamp":     observedAt.Format(time.RFC3339Nano),
		"summary_phase": "final",
	}})
	finalChunks, _ := prepareTranscriptChunks(finalPayload, cursor, observedAt.Add(time.Second), time.UTC)
	if len(firstChunks) != 1 || len(finalChunks) != 1 {
		t.Fatalf("chunks first=%d final=%d, want 1/1", len(firstChunks), len(finalChunks))
	}
	message := finalChunks[0]["messages"].([]map[string]interface{})[0]
	if got := firstString(message, "summary_phase"); got != transcriptSummaryPhaseFinal {
		t.Fatalf("summary phase = %q, want final", got)
	}
}

func TestPrepareTranscriptChunksRespectsMessageLimit(t *testing.T) {
	observedAt := time.Date(2026, 7, 28, 10, 0, 0, 0, time.UTC)
	messages := make([]map[string]interface{}, 0, transcriptChunkMaxMessages+1)
	for index := 0; index <= transcriptChunkMaxMessages; index++ {
		messages = append(messages, transcriptTestMessage(
			"user",
			fmt.Sprintf("message-%d", index),
			observedAt.Add(time.Duration(index)*time.Second),
		))
	}
	chunks, cursor := prepareTranscriptChunks(
		transcriptTestPayload("hash", messages),
		baselineSession{},
		observedAt,
		time.UTC,
	)
	if len(chunks) != 2 ||
		payloadInt(chunks[0], "message_count") != transcriptChunkMaxMessages ||
		payloadInt(chunks[1], "message_count") != 1 ||
		cursor.TranscriptSequence != 2 {
		t.Fatalf("chunks = %#v, cursor = %#v", chunks, cursor)
	}
}

func TestPrepareTranscriptChunksUsesConfiguredDailyWorkTimezone(t *testing.T) {
	location, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatalf("load location: %v", err)
	}
	observedAt := time.Date(2026, 7, 27, 16, 30, 0, 0, time.UTC)
	payload := transcriptTestPayload("hash", []map[string]interface{}{
		transcriptTestMessage("user", "local next day", observedAt),
	})
	chunks, _ := prepareTranscriptChunks(payload, baselineSession{}, observedAt, location)
	if len(chunks) != 1 || firstString(chunks[0], "local_date") != "2026-07-28" {
		t.Fatalf("chunks = %#v", chunks)
	}
}

func TestCodexTranscriptCursorInitializationDoesNotBackfill(t *testing.T) {
	cfg := Config{DataDir: t.TempDir(), DailyWorkTimezone: "UTC"}
	stateDB := filepath.Join(t.TempDir(), "state.sqlite")
	stateDBHash := util.ShortHash(stateDB)
	store := newSessionBaselineStore()
	store.Codex.StateDBs[stateDBHash] = codexSessionBaseline{
		InitializedAt: "2026-07-28T09:00:00Z",
		StateDBHash:   stateDBHash,
		Sessions: map[string]baselineSession{
			"session-1": {
				UpdatedAt:      "2026-07-28T09:00:00Z",
				TranscriptHash: "legacy-hash",
			},
		},
	}
	if err := saveSessionBaselineStore(cfg.DataDir, store); err != nil {
		t.Fatalf("save baseline: %v", err)
	}

	observedAt := time.Date(2026, 7, 28, 10, 0, 0, 0, time.UTC)
	historical := []map[string]interface{}{
		transcriptTestMessage("user", "historical prompt", observedAt.Add(-time.Hour)),
		transcriptTestMessage("assistant", "historical response", observedAt.Add(-30*time.Minute)),
	}
	first, err := filterCodexSessionBackfill(cfg, stateDB, nil, []map[string]interface{}{
		transcriptTestPayload("legacy-hash", historical),
	}, observedAt)
	if err != nil {
		t.Fatalf("first filter: %v", err)
	}
	if len(first.TranscriptEvents) != 0 {
		t.Fatalf("legacy messages were backfilled: %#v", first.TranscriptEvents)
	}
	if first.Commit == nil {
		t.Fatal("cursor initialization should be committed")
	}
	if err := first.Commit(); err != nil {
		t.Fatalf("commit migrated cursor: %v", err)
	}

	nextMessages := append(append([]map[string]interface{}{}, historical...),
		transcriptTestMessage("user", "new prompt", observedAt.Add(time.Minute)))
	second, err := filterCodexSessionBackfill(cfg, stateDB, nil, []map[string]interface{}{
		transcriptTestPayload("second-after-upgrade", nextMessages),
	}, observedAt.Add(2*time.Minute))
	if err != nil {
		t.Fatalf("second filter: %v", err)
	}
	if len(second.TranscriptEvents) != 1 {
		t.Fatalf("new chunks = %#v, want one", second.TranscriptEvents)
	}
	messages := second.TranscriptEvents[0]["messages"].([]map[string]interface{})
	if len(messages) != 1 || firstString(messages[0], "text") != "new prompt" {
		t.Fatalf("new chunk messages = %#v", messages)
	}
}

func TestClaudeTranscriptCursorInitializationDoesNotBackfill(t *testing.T) {
	cfg := Config{DataDir: t.TempDir(), DailyWorkTimezone: "UTC"}
	store := newSessionBaselineStore()
	store.ClaudeCode = claudeCodeSessionBaselineGroup{
		InitializedAt: "2026-07-28T09:00:00Z",
		Sessions: map[string]baselineSession{
			"session-1": {
				UpdatedAt:      "2026-07-28T09:00:00Z",
				TranscriptHash: "legacy-hash",
			},
		},
	}
	if err := saveSessionBaselineStore(cfg.DataDir, store); err != nil {
		t.Fatalf("save baseline: %v", err)
	}

	observedAt := time.Date(2026, 7, 28, 10, 0, 0, 0, time.UTC)
	historical := []map[string]interface{}{
		transcriptTestMessage("user", "historical prompt", observedAt.Add(-time.Hour)),
	}
	first, err := filterClaudeSessionBaseline(
		cfg,
		nil,
		[]map[string]interface{}{transcriptTestPayload("legacy-hash", historical)},
		nil,
		observedAt,
	)
	if err != nil {
		t.Fatalf("first filter: %v", err)
	}
	if len(first.SessionEvents) != 0 {
		t.Fatalf("legacy messages were backfilled: %#v", first.SessionEvents)
	}
	if first.Commit == nil {
		t.Fatal("cursor initialization should be committed")
	}
	if err := first.Commit(); err != nil {
		t.Fatalf("commit migrated cursor: %v", err)
	}

	nextMessages := append(append([]map[string]interface{}{}, historical...),
		transcriptTestMessage("assistant", "new response", observedAt.Add(time.Minute)))
	second, err := filterClaudeSessionBaseline(
		cfg,
		nil,
		[]map[string]interface{}{transcriptTestPayload("next-hash", nextMessages)},
		nil,
		observedAt.Add(2*time.Minute),
	)
	if err != nil {
		t.Fatalf("second filter: %v", err)
	}
	if len(second.SessionEvents) != 1 {
		t.Fatalf("new chunks = %#v, want one", second.SessionEvents)
	}
	messages := second.SessionEvents[0]["messages"].([]map[string]interface{})
	if len(messages) != 1 || firstString(messages[0], "text") != "new response" {
		t.Fatalf("new chunk messages = %#v", messages)
	}
}

func transcriptTestPayload(hash string, messages []map[string]interface{}) map[string]interface{} {
	return map[string]interface{}{
		"agent_type":      "codex",
		"session_id":      "session-1",
		"transcript_hash": hash,
		"updated_at":      "2026-07-28T10:00:00Z",
		"title":           "Transcript chunks",
		"model":           "gpt-test",
		"repo":            "gesta-agent",
		"messages":        messages,
	}
}

func transcriptTestMessage(role, text string, timestamp time.Time) map[string]interface{} {
	return map[string]interface{}{
		"role":      role,
		"text":      text,
		"timestamp": timestamp.UTC().Format(time.RFC3339Nano),
	}
}
