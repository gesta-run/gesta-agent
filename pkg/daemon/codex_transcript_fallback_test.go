package daemon

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	turnusage "github.com/gesta-run/gesta-agent/pkg/turn"
	"github.com/gesta-run/gesta-agent/pkg/util"
)

func TestMergeCodexTranscriptFallbacksReadsRolloutWithoutSQLite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rollout.jsonl")
	contents := "" +
		`{"timestamp":"2026-08-10T01:00:00Z","type":"session_meta","payload":{"id":"raw-session","cwd":"C:\\repo"}}` + "\n" +
		`{"timestamp":"2026-08-10T01:01:00Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"fix Windows transcripts"}]}}` + "\n" +
		`{"timestamp":"2026-08-10T01:02:00Z","type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"implemented"}],"phase":"final_answer"}}` + "\n"
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	session := turnusage.CodexSession{
		SessionID:     "session-hash",
		RolloutPath:   path,
		Title:         "Windows transcripts",
		Model:         "gpt-5.6",
		ModelProvider: "openai",
		Repo:          "repo-hash",
	}

	transcripts, added := mergeCodexTranscriptFallbacks(nil, []turnusage.CodexSession{session})
	if added != 1 || len(transcripts) != 1 {
		t.Fatalf("fallback transcripts = %#v, added=%d", transcripts, added)
	}
	payload := transcripts[0]
	messages, _ := payload["messages"].([]map[string]interface{})
	if sessionIDFromPayload(payload) != session.SessionID || len(messages) != 2 ||
		firstString(messages[0], "text") != "fix Windows transcripts" ||
		!transcriptFallbackPayload(payload) {
		t.Fatalf("fallback payload = %#v", payload)
	}

	existing := []map[string]interface{}{{"session_id": session.SessionID}}
	transcripts, added = mergeCodexTranscriptFallbacks(existing, []turnusage.CodexSession{session})
	if added != 0 || len(transcripts) != 1 {
		t.Fatalf("deduplicated transcripts = %#v, added=%d", transcripts, added)
	}
}

func TestCodexBaselineSourceUsesFallbackAfterStateReadFailure(t *testing.T) {
	transcripts := []map[string]interface{}{{"session_id": "fallback-session"}}
	stateDB := filepath.Join(t.TempDir(), "state.sqlite")
	if got := codexBaselineSourceForCollection(stateDB, false, 1, transcripts); got != stateDB {
		t.Fatalf("baseline source after state read failure = %q, want %q", got, stateDB)
	}
	if got := codexBaselineSourceForCollection("", true, 1, transcripts); got != codexRolloutBaselineSource {
		t.Fatalf("rollout-only baseline source = %q, want %q", got, codexRolloutBaselineSource)
	}
	if got := codexBaselineSourceForCollection(stateDB, false, 0, nil); got != "" {
		t.Fatalf("baseline source without readable state or fallback = %q, want empty", got)
	}
}

func TestCodexTranscriptFallbackDoesNotBackfillBeforeBaseline(t *testing.T) {
	cfg := Config{DataDir: t.TempDir(), DailyWorkTimezone: "UTC"}
	stateDB := filepath.Join(t.TempDir(), "state.sqlite")
	stateDBHash := util.ShortHash(stateDB)
	store := newSessionBaselineStore()
	store.Codex.StateDBs[stateDBHash] = codexSessionBaseline{
		InitializedAt: "2026-08-10T02:00:00Z",
		StateDBHash:   stateDBHash,
		Sessions:      map[string]baselineSession{},
	}
	if err := saveSessionBaselineStore(cfg.DataDir, store); err != nil {
		t.Fatal(err)
	}
	payload := transcriptTestPayload("fallback-hash", []map[string]interface{}{
		transcriptTestMessage("user", "historical prompt", time.Date(2026, 8, 10, 1, 0, 0, 0, time.UTC)),
		transcriptTestMessage("assistant", "new response", time.Date(2026, 8, 10, 3, 0, 0, 0, time.UTC)),
	})
	payload[internalTranscriptFallbackPayloadKey] = true

	result, err := filterCodexSessionBackfill(cfg, stateDB, nil, []map[string]interface{}{payload}, time.Date(2026, 8, 10, 3, 1, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if len(result.TranscriptEvents) != 1 {
		t.Fatalf("fallback chunks = %#v", result.TranscriptEvents)
	}
	messages, _ := result.TranscriptEvents[0]["messages"].([]map[string]interface{})
	if len(messages) != 1 || firstString(messages[0], "text") != "new response" {
		t.Fatalf("fallback messages = %#v", messages)
	}
}
