package turn

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestCollectCodexEmitsCompletedTurnsAfterInitialization(t *testing.T) {
	dataDir := t.TempDir()
	rollout := filepath.Join(t.TempDir(), "rollout.jsonl")
	writeLines(t, rollout, []string{
		turnLine("2026-08-04T00:00:00Z", "task_started", "old"),
		tokenLine("2026-08-04T00:00:01Z", 80, 20, 20, 0),
		turnLine("2026-08-04T00:00:02Z", "task_complete", "old"),
	})
	session := CodexSession{SessionID: "session-hash", RolloutPath: rollout, Model: "gpt-5.6"}
	_, commit, err := CollectCodex(Config{DataDir: dataDir, DaemonID: "daemon"}, []CodexSession{session}, time.Now())
	if err != nil || commit == nil {
		t.Fatalf("initialize collector: commit=%v err=%v", commit != nil, err)
	}
	if err := commit(); err != nil {
		t.Fatalf("commit initialization: %v", err)
	}
	appendLines(t, rollout, []string{
		turnLine("2026-08-04T00:01:00Z", "task_started", "deploy"),
		`{"timestamp":"2026-08-04T00:01:01Z","type":"event_msg","payload":{"type":"user_message","message":"deploy to production"}}`,
		`{"timestamp":"2026-08-04T00:01:02Z","type":"response_item","payload":{"type":"function_call","name":"exec_command","arguments":"{\"cmd\":\"kubectl apply -f app.yaml\"}"}}`,
		tokenLine("2026-08-04T00:01:03Z", 180, 50, 70, 0),
		turnLine("2026-08-04T00:01:04Z", "task_complete", "deploy"),
	})
	events, commit, err := CollectCodex(Config{DataDir: dataDir, DaemonID: "daemon"}, []CodexSession{session}, time.Now())
	if err != nil || len(events) != 1 {
		t.Fatalf("events=%d err=%v, want one", len(events), err)
	}
	if events[0].WorkType != "SRE" || events[0].Tokens.Total() != 80 || events[0].Tokens.BilledTotal() != 130 {
		t.Fatalf("event=%+v, want SRE with 80 effective and 130 billed tokens", events[0])
	}
	if err := commit(); err != nil {
		t.Fatalf("commit turn: %v", err)
	}
	cursorData, err := os.ReadFile(filepath.Join(dataDir, cursorFile))
	if err != nil {
		t.Fatalf("read cursor: %v", err)
	}
	if strings.Contains(string(cursorData), "kubectl") || strings.Contains(string(cursorData), "production") {
		t.Fatalf("cursor leaked classifier input: %s", cursorData)
	}
	again, _, err := CollectCodex(Config{DataDir: dataDir, DaemonID: "daemon"}, []CodexSession{session}, time.Now())
	if err != nil || len(again) != 0 {
		t.Fatalf("retry events=%d err=%v, want zero", len(again), err)
	}
	encoded, _ := json.Marshal(events[0].Payload())
	if strings.Contains(string(encoded), "kubectl") || strings.Contains(string(encoded), "production") {
		t.Fatalf("event leaked classifier input: %s", encoded)
	}
}

func TestCollectCodexPersistsActiveTurnAcrossCollections(t *testing.T) {
	dataDir := t.TempDir()
	rollout := filepath.Join(t.TempDir(), "rollout.jsonl")
	writeLines(t, rollout, []string{tokenLine("2026-08-04T00:00:00Z", 100, 20, 20, 0)})
	session := CodexSession{SessionID: "session-hash", RolloutPath: rollout}
	_, commit, err := CollectCodex(Config{DataDir: dataDir, DaemonID: "daemon"}, []CodexSession{session}, time.Now())
	if err != nil {
		t.Fatalf("initialize collector: %v", err)
	}
	if err := commit(); err != nil {
		t.Fatalf("commit initialization: %v", err)
	}
	appendLines(t, rollout, []string{
		turnLine("2026-08-04T00:01:00Z", "task_started", "turn"),
		`{"timestamp":"2026-08-04T00:01:01Z","type":"event_msg","payload":{"type":"user_message","message":"roll back the production deployment"}}`,
		tokenLine("2026-08-04T00:01:02Z", 130, 25, 25, 0),
	})
	events, commit, err := CollectCodex(Config{DataDir: dataDir, DaemonID: "daemon"}, []CodexSession{session}, time.Now())
	if err != nil || len(events) != 0 {
		t.Fatalf("active collection events=%d err=%v", len(events), err)
	}
	if err := commit(); err != nil {
		t.Fatalf("commit active turn: %v", err)
	}
	appendLines(t, rollout, []string{
		tokenLine("2026-08-04T00:01:03Z", 180, 40, 40, 0),
		turnLine("2026-08-04T00:01:04Z", "turn_aborted", "turn"),
	})
	events, _, err = CollectCodex(Config{DataDir: dataDir, DaemonID: "daemon"}, []CodexSession{session}, time.Now())
	if err != nil || len(events) != 1 {
		t.Fatalf("terminal collection events=%d err=%v", len(events), err)
	}
	if events[0].Status != "aborted" || events[0].WorkType != "SRE" || events[0].Tokens.Total() != 80 || events[0].Tokens.BilledTotal() != 100 {
		t.Fatalf("event = %+v, want aborted SRE turn with 80 effective and 100 billed tokens", events[0])
	}
}

func TestCollectCodexKeepsTurnsFromMultipleNewSessions(t *testing.T) {
	dataDir := t.TempDir()
	cutover := time.Date(2026, 8, 4, 0, 0, 0, 0, time.UTC)
	seedRollout := filepath.Join(t.TempDir(), "seed.jsonl")
	writeLines(t, seedRollout, []string{tokenLine("2026-08-04T00:00:00Z", 10, 1, 0, 0)})
	seed := CodexSession{SessionID: "seed-session", RolloutPath: seedRollout}
	_, commit, err := CollectCodex(Config{DataDir: dataDir, DaemonID: "daemon"}, []CodexSession{seed}, cutover)
	if err != nil {
		t.Fatalf("initialize collector: %v", err)
	}
	if err := commit(); err != nil {
		t.Fatalf("commit initialization: %v", err)
	}

	var sessions []CodexSession
	for _, suffix := range []string{"one", "two"} {
		rollout := filepath.Join(t.TempDir(), suffix+".jsonl")
		writeLines(t, rollout, []string{
			turnLine("2026-08-04T00:01:00Z", "task_started", suffix),
			`{"timestamp":"2026-08-04T00:01:01Z","type":"event_msg","payload":{"type":"user_message","message":"write documentation"}}`,
			tokenLine("2026-08-04T00:01:02Z", 20, 5, 5, 0),
			turnLine("2026-08-04T00:01:03Z", "task_complete", suffix),
		})
		sessions = append(sessions, CodexSession{SessionID: "session-" + suffix, RolloutPath: rollout})
	}
	events, _, err := CollectCodex(Config{DataDir: dataDir, DaemonID: "daemon"}, append([]CodexSession{seed}, sessions...), cutover.Add(2*time.Minute))
	if err != nil || len(events) != 2 {
		t.Fatalf("events=%d err=%v, want both new sessions", len(events), err)
	}
	for _, event := range events {
		if event.WorkType != "Docs" {
			t.Fatalf("event = %+v, want Docs", event)
		}
	}
}

func TestCollectCodexDoesNotBackfillOldSessionDiscoveredAfterCutover(t *testing.T) {
	dataDir := t.TempDir()
	cutover := time.Date(2026, 8, 4, 0, 0, 0, 0, time.UTC)
	seedRollout := filepath.Join(t.TempDir(), "seed.jsonl")
	writeLines(t, seedRollout, []string{tokenLine(cutover.Format(time.RFC3339), 10, 1, 0, 0)})
	seed := CodexSession{SessionID: "seed-session", RolloutPath: seedRollout}
	_, commit, err := CollectCodex(Config{DataDir: dataDir, DaemonID: "daemon"}, []CodexSession{seed}, cutover)
	if err != nil {
		t.Fatalf("initialize collector: %v", err)
	}
	if err := commit(); err != nil {
		t.Fatalf("commit initialization: %v", err)
	}

	oldRollout := filepath.Join(t.TempDir(), "old.jsonl")
	writeLines(t, oldRollout, []string{
		turnLine("2026-08-03T23:00:00Z", "task_started", "old"),
		tokenLine("2026-08-03T23:00:01Z", 100, 20, 20, 0),
		turnLine("2026-08-03T23:00:02Z", "task_complete", "old"),
	})
	events, _, err := CollectCodex(Config{DataDir: dataDir, DaemonID: "daemon"}, []CodexSession{
		seed,
		{SessionID: "old-session", RolloutPath: oldRollout},
	}, cutover.Add(time.Minute))
	if err != nil || len(events) != 0 {
		t.Fatalf("events=%d err=%v, want no historical backfill", len(events), err)
	}
}

func TestCollectCodexEmitsMultipleTurnsBetweenCollections(t *testing.T) {
	dataDir := t.TempDir()
	rollout := filepath.Join(t.TempDir(), "rollout.jsonl")
	writeLines(t, rollout, []string{tokenLine("2026-08-04T00:00:00Z", 100, 20, 20, 0)})
	session := CodexSession{SessionID: "session-hash", RolloutPath: rollout}
	_, commit, err := CollectCodex(Config{DataDir: dataDir, DaemonID: "daemon"}, []CodexSession{session}, time.Now())
	if err != nil {
		t.Fatalf("initialize collector: %v", err)
	}
	if err := commit(); err != nil {
		t.Fatalf("commit initialization: %v", err)
	}
	appendLines(t, rollout, []string{
		turnLine("2026-08-04T00:01:00Z", "task_started", "one"),
		tokenLine("2026-08-04T00:01:01Z", 130, 30, 20, 0),
		turnLine("2026-08-04T00:01:02Z", "task_complete", "one"),
		turnLine("2026-08-04T00:02:00Z", "task_started", "two"),
		tokenLine("2026-08-04T00:02:01Z", 150, 40, 30, 0),
		turnLine("2026-08-04T00:02:02Z", "task_complete", "two"),
	})

	events, _, err := CollectCodex(Config{DataDir: dataDir, DaemonID: "daemon"}, []CodexSession{session}, time.Now())
	if err != nil || len(events) != 2 {
		t.Fatalf("events=%d err=%v, want two", len(events), err)
	}
}

func TestCollectCodexRetriesPartialFinalLine(t *testing.T) {
	dataDir := t.TempDir()
	rollout := filepath.Join(t.TempDir(), "rollout.jsonl")
	writeLines(t, rollout, []string{tokenLine("2026-08-04T00:00:00Z", 100, 20, 20, 0)})
	session := CodexSession{SessionID: "session-hash", RolloutPath: rollout}
	_, commit, err := CollectCodex(Config{DataDir: dataDir, DaemonID: "daemon"}, []CodexSession{session}, time.Now())
	if err != nil {
		t.Fatalf("initialize collector: %v", err)
	}
	if err := commit(); err != nil {
		t.Fatalf("commit initialization: %v", err)
	}
	appendLines(t, rollout, []string{
		turnLine("2026-08-04T00:01:00Z", "task_started", "turn"),
		tokenLine("2026-08-04T00:01:01Z", 130, 30, 20, 0),
	})
	appendRaw(t, rollout, turnLine("2026-08-04T00:01:02Z", "task_complete", "turn"))

	events, commit, err := CollectCodex(Config{DataDir: dataDir, DaemonID: "daemon"}, []CodexSession{session}, time.Now())
	if err != nil || len(events) != 0 {
		t.Fatalf("partial collection events=%d err=%v", len(events), err)
	}
	if err := commit(); err != nil {
		t.Fatalf("commit partial collection: %v", err)
	}
	appendRaw(t, rollout, "\n")
	events, _, err = CollectCodex(Config{DataDir: dataDir, DaemonID: "daemon"}, []CodexSession{session}, time.Now())
	if err != nil || len(events) != 1 {
		t.Fatalf("completed collection events=%d err=%v, want one", len(events), err)
	}
}

func TestCollectCodexSeedsReplacedRolloutWithoutReplay(t *testing.T) {
	dataDir := t.TempDir()
	rollout := filepath.Join(t.TempDir(), "rollout.jsonl")
	originalLines := make([]string, 0, 10)
	for index := int64(0); index < 10; index++ {
		originalLines = append(originalLines, tokenLine("2026-08-04T00:00:00Z", 100+index, 20, 20, 0))
	}
	writeLines(t, rollout, originalLines)
	session := CodexSession{SessionID: "session-hash", RolloutPath: rollout}
	_, commit, err := CollectCodex(Config{DataDir: dataDir, DaemonID: "daemon"}, []CodexSession{session}, time.Now())
	if err != nil {
		t.Fatalf("initialize collector: %v", err)
	}
	if err := commit(); err != nil {
		t.Fatalf("commit initialization: %v", err)
	}
	writeLines(t, rollout, []string{
		turnLine("2026-08-04T00:01:00Z", "task_started", "historical"),
		tokenLine("2026-08-04T00:01:01Z", 20, 5, 0, 0),
		turnLine("2026-08-04T00:01:02Z", "task_complete", "historical"),
	})

	events, commit, err := CollectCodex(Config{DataDir: dataDir, DaemonID: "daemon"}, []CodexSession{session}, time.Now())
	if err != nil || len(events) != 0 {
		t.Fatalf("replacement events=%d err=%v, want no replay", len(events), err)
	}
	if err := commit(); err != nil {
		t.Fatalf("commit replacement: %v", err)
	}
	appendLines(t, rollout, []string{
		turnLine("2026-08-04T00:02:00Z", "task_started", "new"),
		tokenLine("2026-08-04T00:02:01Z", 40, 10, 0, 0),
		turnLine("2026-08-04T00:02:02Z", "task_complete", "new"),
	})
	events, _, err = CollectCodex(Config{DataDir: dataDir, DaemonID: "daemon"}, []CodexSession{session}, time.Now())
	if err != nil || len(events) != 1 {
		t.Fatalf("new turn events=%d err=%v, want one", len(events), err)
	}
}

func TestClassifierUsesWordBoundaries(t *testing.T) {
	scores := map[string]int{}
	scoreText(scores, "product planning for a digital workspace", 7)
	if scores["SRE"] != 0 || scores["Coding"] != 0 {
		t.Fatalf("false positive scores: %+v", scores)
	}
}

func writeLines(t *testing.T, path string, lines []string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatalf("write rollout: %v", err)
	}
}

func appendLines(t *testing.T, path string, lines []string) {
	t.Helper()
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatalf("open rollout: %v", err)
	}
	defer file.Close()
	if _, err := file.WriteString(strings.Join(lines, "\n") + "\n"); err != nil {
		t.Fatalf("append rollout: %v", err)
	}
}

func appendRaw(t *testing.T, path, value string) {
	t.Helper()
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatalf("open rollout: %v", err)
	}
	defer file.Close()
	if _, err := file.WriteString(value); err != nil {
		t.Fatalf("append rollout: %v", err)
	}
}

func turnLine(timestamp, eventType, turnID string) string {
	return `{"timestamp":"` + timestamp + `","type":"event_msg","payload":{"type":"` + eventType + `","turn_id":"` + turnID + `"}}`
}

func tokenLine(timestamp string, input, output, cached, cacheWrite int64) string {
	return `{"timestamp":"` + timestamp + `","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":` + number(input) + `,"output_tokens":` + number(output) + `,"cached_input_tokens":` + number(cached) + `,"cache_write_input_tokens":` + number(cacheWrite) + `}}}}`
}

func number(value int64) string {
	return strconv.FormatInt(value, 10)
}
