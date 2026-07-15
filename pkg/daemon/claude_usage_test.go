package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gesta-run/gesta-agent/pkg/util"
)

// writeClaudeTranscript writes a transcript jsonl under
// <home>/.claude/projects/<enc>/<uuid>.jsonl and returns the file path.
func writeClaudeTranscript(t *testing.T, home, sessionUUID string, lines []string) string {
	t.Helper()
	dir := filepath.Join(home, ".claude", "projects", "-Users-test-repo")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir transcripts: %v", err)
	}
	path := filepath.Join(dir, sessionUUID+".jsonl")
	body := ""
	for _, line := range lines {
		body += line + "\n"
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write transcript: %v", err)
	}
	return path
}

const (
	claudeSessionUUID = "11111111-1111-1111-1111-111111111111"
)

func claudeUserLine(text string) string {
	return `{"type":"user","sessionId":"` + claudeSessionUUID + `","cwd":"/Users/test/repo","gitBranch":"main","timestamp":"2026-06-20T10:00:00.000Z","message":{"role":"user","content":` + jsonString(text) + `}}`
}

func claudeAssistantLine(model, ts string, in, out, cacheCreate, cacheRead int) string {
	return `{"type":"assistant","sessionId":"` + claudeSessionUUID + `","cwd":"/Users/test/repo","gitBranch":"main","timestamp":"` + ts + `","message":{"model":"` + model + `","role":"assistant","usage":{"input_tokens":` + itoa(in) + `,"output_tokens":` + itoa(out) + `,"cache_creation_input_tokens":` + itoa(cacheCreate) + `,"cache_read_input_tokens":` + itoa(cacheRead) + `}}}`
}

// claudeAssistantLineWithID mirrors a real Claude Code transcript record where a
// single LLM turn is split across multiple JSONL records that share one
// message.id and repeat the SAME cumulative usage.
func claudeAssistantLineWithID(id, model, ts string, in, out, cacheCreate, cacheRead int) string {
	return `{"type":"assistant","sessionId":"` + claudeSessionUUID + `","cwd":"/Users/test/repo","gitBranch":"main","timestamp":"` + ts + `","message":{"id":"` + id + `","model":"` + model + `","role":"assistant","usage":{"input_tokens":` + itoa(in) + `,"output_tokens":` + itoa(out) + `,"cache_creation_input_tokens":` + itoa(cacheCreate) + `,"cache_read_input_tokens":` + itoa(cacheRead) + `}}}`
}

func claudeAssistantTextLineWithID(id, model, ts, text string, in, out, cacheCreate, cacheRead int) string {
	return `{"type":"assistant","sessionId":"` + claudeSessionUUID + `","cwd":"/Users/test/repo","gitBranch":"main","timestamp":"` + ts + `","message":{"id":"` + id + `","model":"` + model + `","role":"assistant","content":[{"type":"text","text":` + jsonString(text) + `}],"usage":{"input_tokens":` + itoa(in) + `,"output_tokens":` + itoa(out) + `,"cache_creation_input_tokens":` + itoa(cacheCreate) + `,"cache_read_input_tokens":` + itoa(cacheRead) + `}}}`
}

func claudeSyntheticLine() string {
	return `{"type":"assistant","sessionId":"` + claudeSessionUUID + `","cwd":"/Users/test/repo","gitBranch":"main","timestamp":"2026-06-20T10:05:00.000Z","message":{"model":"<synthetic>","role":"assistant","usage":{"input_tokens":0,"output_tokens":0,"cache_creation_input_tokens":0,"cache_read_input_tokens":0}}}`
}

func claudeSyntheticTextLineWithID(id, ts, text string) string {
	return `{"type":"assistant","sessionId":"` + claudeSessionUUID + `","cwd":"/Users/test/repo","gitBranch":"main","timestamp":"` + ts + `","message":{"id":"` + id + `","model":"<synthetic>","role":"assistant","content":[{"type":"text","text":` + jsonString(text) + `}],"usage":{"input_tokens":0,"output_tokens":0,"cache_creation_input_tokens":0,"cache_read_input_tokens":0}}}`
}

func itoa(v int) string { return stringInt(int64(v)) }
func jsonString(s string) string {
	// minimal json string quoting for test fixtures (no special chars expected)
	return `"` + s + `"`
}

func TestParseClaudeTranscriptSkipsSyntheticAndSumsUsage(t *testing.T) {
	home := t.TempDir()
	path := writeClaudeTranscript(t, home, claudeSessionUUID, []string{
		claudeUserLine("implement the feature"),
		claudeAssistantLine("claude-opus-4-8", "2026-06-20T10:01:00.000Z", 100, 200, 50, 25),
		claudeSyntheticLine(),
		claudeAssistantLine("claude-opus-4-8", "2026-06-20T10:02:00.000Z", 10, 20, 5, 0),
		claudeAssistantLine("claude-haiku-4-5", "2026-06-20T10:03:00.000Z", 1, 2, 0, 0),
	})

	session, ok := parseClaudeTranscript(path)
	if !ok {
		t.Fatal("expected transcript to parse")
	}
	if session.SessionID != claudeSessionUUID {
		t.Fatalf("session id = %q", session.SessionID)
	}
	// 3 real assistant messages, synthetic skipped.
	if session.AssistantEvents != 3 {
		t.Fatalf("assistant events = %d, want 3", session.AssistantEvents)
	}
	// Total tokens = input+output+cacheCreate+cacheRead over the 3 real msgs.
	// opus: (100+200+50+25)+(10+20+5+0) = 375+35 = 410
	// haiku: 1+2+0+0 = 3
	wantTotal := int64(410 + 3)
	if session.totalTokens() != wantTotal {
		t.Fatalf("total tokens = %d, want %d", session.totalTokens(), wantTotal)
	}
	if len(session.Models) != 2 {
		t.Fatalf("models = %#v, want 2 distinct", session.Models)
	}
	// per-model/day breakdown on 2026-06-20
	opus := session.ByModelDay[claudeModelDayKey{Model: "claude-opus-4-8", Day: "2026-06-20"}]
	if opus.TotalTokens() != 410 {
		t.Fatalf("opus model/day total = %d, want 410", opus.TotalTokens())
	}
	haiku := session.ByModelDay[claudeModelDayKey{Model: "claude-haiku-4-5", Day: "2026-06-20"}]
	if haiku.TotalTokens() != 3 {
		t.Fatalf("haiku model/day total = %d, want 3", haiku.TotalTokens())
	}
	if session.Title != "implement the feature" {
		t.Fatalf("title = %q", session.Title)
	}
}

func TestParseClaudeTranscriptCrossDayBuckets(t *testing.T) {
	home := t.TempDir()
	path := writeClaudeTranscript(t, home, claudeSessionUUID, []string{
		claudeAssistantLine("claude-opus-4-8", "2026-06-20T23:30:00.000Z", 100, 100, 0, 0),
		claudeAssistantLine("claude-opus-4-8", "2026-06-21T00:30:00.000Z", 50, 50, 0, 0),
	})
	session, ok := parseClaudeTranscript(path)
	if !ok {
		t.Fatal("expected transcript to parse")
	}
	day20 := session.ByModelDay[claudeModelDayKey{Model: "claude-opus-4-8", Day: "2026-06-20"}]
	day21 := session.ByModelDay[claudeModelDayKey{Model: "claude-opus-4-8", Day: "2026-06-21"}]
	if day20.TotalTokens() != 200 {
		t.Fatalf("2026-06-20 bucket = %d, want 200", day20.TotalTokens())
	}
	if day21.TotalTokens() != 100 {
		t.Fatalf("2026-06-21 bucket = %d, want 100", day21.TotalTokens())
	}
	if len(session.ByModelDay) != 2 {
		t.Fatalf("expected 2 model/day buckets, got %d", len(session.ByModelDay))
	}
}

// TestParseClaudeTranscriptDeduplicatesByMessageID reproduces the real-world
// shape where one LLM turn emits multiple records sharing a single message.id,
// each repeating the SAME cumulative usage. The turn must be counted exactly
// once (tokens and AssistantEvents), not once per record.
func TestParseClaudeTranscriptDeduplicatesByMessageID(t *testing.T) {
	home := t.TempDir()
	path := writeClaudeTranscript(t, home, claudeSessionUUID, []string{
		claudeUserLine("do the work"),
		// Turn 1: three records, one message.id, identical cumulative usage.
		claudeAssistantLineWithID("msg_aaa", "claude-opus-4-8", "2026-06-20T10:01:00.000Z", 100, 200, 50, 25),
		claudeAssistantLineWithID("msg_aaa", "claude-opus-4-8", "2026-06-20T10:01:01.000Z", 100, 200, 50, 25),
		claudeAssistantLineWithID("msg_aaa", "claude-opus-4-8", "2026-06-20T10:01:02.000Z", 100, 200, 50, 25),
		// Turn 2: a different message.id, counted independently.
		claudeAssistantLineWithID("msg_bbb", "claude-opus-4-8", "2026-06-20T10:02:00.000Z", 10, 20, 5, 0),
		claudeAssistantLineWithID("msg_bbb", "claude-opus-4-8", "2026-06-20T10:02:01.000Z", 10, 20, 5, 0),
	})

	session, ok := parseClaudeTranscript(path)
	if !ok {
		t.Fatal("expected transcript to parse")
	}
	// Two distinct turns despite five assistant records.
	if session.AssistantEvents != 2 {
		t.Fatalf("assistant events = %d, want 2 (one per distinct message.id)", session.AssistantEvents)
	}
	// turn1 = 100+200+50+25 = 375, turn2 = 10+20+5+0 = 35; total 410, NOT
	// 375*3 + 35*2 = 1195 which the pre-fix code would have produced.
	wantTotal := int64(375 + 35)
	if session.totalTokens() != wantTotal {
		t.Fatalf("total tokens = %d, want %d (duplicate message.id records must be counted once)", session.totalTokens(), wantTotal)
	}
	bucket := session.ByModelDay[claudeModelDayKey{Model: "claude-opus-4-8", Day: "2026-06-20"}]
	if bucket.TotalTokens() != wantTotal {
		t.Fatalf("model/day bucket total = %d, want %d", bucket.TotalTokens(), wantTotal)
	}
}

func TestClaudeSessionIndexPayloadIncludesFullTranscript(t *testing.T) {
	home := t.TempDir()
	path := writeClaudeTranscript(t, home, claudeSessionUUID, []string{
		claudeUserLine("first prompt"),
		claudeAssistantTextLineWithID("msg_aaa", "claude-opus-4-8", "2026-06-20T10:01:00.000Z", "first answer", 100, 200, 0, 0),
		claudeUserLine("second prompt"),
		claudeAssistantTextLineWithID("msg_bbb", "claude-opus-4-8", "2026-06-20T10:02:00.000Z", "second answer", 10, 20, 0, 0),
	})

	session, ok := parseClaudeTranscript(path)
	if !ok {
		t.Fatal("expected transcript to parse")
	}
	payload := claudeSessionIndexPayload(session)
	messages, ok := payload["messages"].([]map[string]interface{})
	if !ok {
		t.Fatalf("messages = %#v", payload["messages"])
	}
	if len(messages) != 4 {
		t.Fatalf("messages = %d, want 4: %#v", len(messages), messages)
	}
	wantTexts := []string{"first prompt", "first answer", "second prompt", "second answer"}
	for i, want := range wantTexts {
		if messages[i]["text"] != want {
			t.Fatalf("message %d text = %#v, want %q", i, messages[i]["text"], want)
		}
	}
	if count, _ := payloadIntValue(payload, "message_count"); count != 4 {
		t.Fatalf("message_count = %d, want 4", count)
	}
	if payload["title"] != "first prompt" {
		t.Fatalf("title = %#v, want first prompt", payload["title"])
	}
	if payload["transcript_hash"] == "" {
		t.Fatal("transcript_hash should be set")
	}
}

func TestParseClaudeTranscriptKeepsZeroUsageChatTranscript(t *testing.T) {
	home := t.TempDir()
	path := writeClaudeTranscript(t, home, claudeSessionUUID, []string{
		claudeUserLine("hello"),
		claudeAssistantTextLineWithID("resp_zero", "gpt-5.5", "2026-06-20T10:01:00.000Z", "zero usage answer", 0, 0, 0, 0),
	})

	session, ok := parseClaudeTranscript(path)
	if !ok {
		t.Fatal("expected zero-usage transcript with readable chat to parse")
	}
	if session.AssistantEvents != 1 {
		t.Fatalf("assistant events = %d, want 1", session.AssistantEvents)
	}
	if session.totalTokens() != 0 {
		t.Fatalf("total tokens = %d, want 0", session.totalTokens())
	}
	if len(session.ByModelDay) != 0 {
		t.Fatalf("zero-usage transcript should not create usage buckets: %#v", session.ByModelDay)
	}
	if len(session.Models) != 1 || session.Models[0] != "gpt-5.5" {
		t.Fatalf("models = %#v, want gpt-5.5", session.Models)
	}
	payload := claudeSessionIndexPayload(session)
	messages, ok := payload["messages"].([]map[string]interface{})
	if !ok || len(messages) != 2 {
		t.Fatalf("messages = %#v, want 2 transcript messages", payload["messages"])
	}
	if messages[1]["text"] != "zero usage answer" {
		t.Fatalf("assistant message text = %#v", messages[1])
	}
	if payload["model"] != "gpt-5.5" {
		t.Fatalf("payload model = %#v, want gpt-5.5", payload["model"])
	}
	if total, _ := payloadIntValue(payload, "total_tokens"); total != 0 {
		t.Fatalf("payload total tokens = %d, want 0", total)
	}
}

func TestParseClaudeTranscriptKeepsChatTurnsAndFiltersCommandNoise(t *testing.T) {
	home := t.TempDir()
	path := writeClaudeTranscript(t, home, claudeSessionUUID, []string{
		claudeUserLine("Hi"),
		claudeSyntheticTextLineWithID("synthetic_visible", "2026-06-20T10:01:00.000Z", "Pick a different model."),
		claudeUserLine("<command-name>/model</command-name>\n<command-message>model</command-message>"),
		claudeUserLine("<local-command-stdout>Set model to gpt-5.5</local-command-stdout>"),
		claudeUserLine("Hello"),
		claudeAssistantTextLineWithID("resp_zero", "gpt-5.5", "2026-06-20T10:02:00.000Z", "Hello! How can I help?", 0, 0, 0, 0),
	})

	session, ok := parseClaudeTranscript(path)
	if !ok {
		t.Fatal("expected transcript to parse")
	}
	if session.AssistantEvents != 1 {
		t.Fatalf("assistant events = %d, want only the real model assistant counted", session.AssistantEvents)
	}
	if session.totalTokens() != 0 {
		t.Fatalf("total tokens = %d, want 0", session.totalTokens())
	}
	payload := claudeSessionIndexPayload(session)
	messages, ok := payload["messages"].([]map[string]interface{})
	if !ok {
		t.Fatalf("messages = %#v", payload["messages"])
	}
	got := make([]string, 0, len(messages))
	for _, message := range messages {
		got = append(got, firstString(message, "role")+":"+firstString(message, "text"))
	}
	want := []string{
		"user:Hi",
		"assistant:Pick a different model.",
		"user:Hello",
		"assistant:Hello! How can I help?",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("messages = %#v, want %#v", got, want)
	}
}

// claudeAssistantLineForSession mirrors claudeAssistantLine but lets the test
// pin an explicit sessionId so a single logical session can be split across
// multiple transcript files (the resume case).
func claudeAssistantLineForSession(sessionID, model, ts string, in, out, cacheCreate, cacheRead int) string {
	return `{"type":"assistant","sessionId":"` + sessionID + `","cwd":"/Users/test/repo","gitBranch":"main","timestamp":"` + ts + `","message":{"model":"` + model + `","role":"assistant","usage":{"input_tokens":` + itoa(in) + `,"output_tokens":` + itoa(out) + `,"cache_creation_input_tokens":` + itoa(cacheCreate) + `,"cache_read_input_tokens":` + itoa(cacheRead) + `}}}`
}

// TestMergeClaudeSessionsByIDSumsAcrossFiles reproduces the resume case: one
// logical session split across two transcript files (different file names, same
// sessionId), feeding the same (session, model, day) bucket. The merged result
// must SUM tokens (200 + 600 = 800) and assistant events, not collapse to MAX or
// emit two thrashing session-index records.
func TestMergeClaudeSessionsByIDSumsAcrossFiles(t *testing.T) {
	home := t.TempDir()
	const sessionID = "22222222-2222-2222-2222-222222222222"

	// File A: 200 tokens (100 in + 100 out) on 2026-06-20.
	writeClaudeTranscript(t, home, "file-a", []string{
		claudeUserLine("start the work"),
		claudeAssistantLineForSession(sessionID, "claude-opus-4-8", "2026-06-20T10:00:00.000Z", 100, 100, 0, 0),
	})
	// File B: 600 tokens (300 in + 300 out) same session+model+day, after resume.
	writeClaudeTranscript(t, home, "file-b", []string{
		claudeUserLine("continue the work"),
		claudeAssistantLineForSession(sessionID, "claude-opus-4-8", "2026-06-20T11:00:00.000Z", 300, 300, 0, 0),
	})

	// writeClaudeTranscript hardcodes claudeSessionUUID in the user line; rewrite
	// both files so EVERY record (user + assistant) carries the resume sessionId.
	projectsDir := filepath.Join(home, ".claude", "projects", "-Users-test-repo")
	for _, name := range []string{"file-a", "file-b"} {
		path := filepath.Join(projectsDir, name+".jsonl")
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		fixed := strings.ReplaceAll(string(raw), claudeSessionUUID, sessionID)
		if err := os.WriteFile(path, []byte(fixed), 0o600); err != nil {
			t.Fatalf("rewrite %s: %v", name, err)
		}
	}

	var sessions []claudeSessionUsage
	for _, name := range []string{"file-a", "file-b"} {
		path := filepath.Join(projectsDir, name+".jsonl")
		session, ok := parseClaudeTranscript(path)
		if !ok {
			t.Fatalf("expected %s to parse", name)
		}
		if session.SessionID != sessionID {
			t.Fatalf("%s session id = %q, want %q", name, session.SessionID, sessionID)
		}
		sessions = append(sessions, session)
	}

	merged := mergeClaudeSessionsByID(sessions)
	if len(merged) != 1 {
		t.Fatalf("expected 1 merged session, got %d", len(merged))
	}
	got := merged[0]
	if got.SessionID != sessionID {
		t.Fatalf("merged session id = %q", got.SessionID)
	}
	// SUM across files, not MAX (the bug emitted a single 600 bucket).
	if got.totalTokens() != 800 {
		t.Fatalf("merged total tokens = %d, want 800 (200 + 600)", got.totalTokens())
	}
	if got.AssistantEvents != 2 {
		t.Fatalf("merged assistant events = %d, want 2", got.AssistantEvents)
	}
	bucket := got.ByModelDay[claudeModelDayKey{Model: "claude-opus-4-8", Day: "2026-06-20"}]
	if bucket.TotalTokens() != 800 {
		t.Fatalf("merged model/day bucket = %d, want 800", bucket.TotalTokens())
	}
	if len(got.ByModelDay) != 1 {
		t.Fatalf("expected 1 model/day bucket, got %d", len(got.ByModelDay))
	}
	// Title seeds from the earliest file (FirstEventAt order).
	if got.Title != "start the work" {
		t.Fatalf("merged title = %q, want %q", got.Title, "start the work")
	}
	if got.FirstEventAt.Format(time.RFC3339) != "2026-06-20T10:00:00Z" {
		t.Fatalf("merged first event = %s", got.FirstEventAt.Format(time.RFC3339))
	}
	if got.LastEventAt.Format(time.RFC3339) != "2026-06-20T11:00:00Z" {
		t.Fatalf("merged last event = %s", got.LastEventAt.Format(time.RFC3339))
	}

	// End-to-end: exactly one usage bucket and one session-index record, summed.
	usagePayload := claudeUsageSummaryPayload(got, claudeModelDayKey{Model: "claude-opus-4-8", Day: "2026-06-20"}, bucket)
	if total, _ := payloadIntValue(usagePayload, "total_tokens"); total != 800 {
		t.Fatalf("usage payload total = %d, want 800", total)
	}
	indexPayload := claudeSessionIndexPayload(got)
	if total, _ := payloadIntValue(indexPayload, "total_tokens"); total != 800 {
		t.Fatalf("session index total = %d, want 800", total)
	}
	if count, _ := payloadIntValue(indexPayload, "event_count"); count != 2 {
		t.Fatalf("session index event_count = %d, want 2", count)
	}
	// Regression guard: the usage summary must expose the real session-index id as
	// index_session_id so the control plane reconciles agent_sessions.total_tokens
	// by session_id. The per-bucket session_id stays salted for cursor independence
	// and must differ from the index id.
	if usagePayload["index_session_id"] != indexPayload["session_id"] {
		t.Fatalf("usage index_session_id %v != session index id %v", usagePayload["index_session_id"], indexPayload["session_id"])
	}
	if usagePayload["session_id"] == indexPayload["session_id"] {
		t.Fatalf("usage bucket session_id %v should be salted, not equal to the session index id", usagePayload["session_id"])
	}
}

func TestCollectClaudeUsageEventsIdempotency(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dataDir := t.TempDir()
	cfg := Config{DataDir: dataDir, UserName: "tester"}
	projectsDir := filepath.Join(home, ".claude", "projects")

	writeClaudeTranscript(t, home, claudeSessionUUID, []string{
		claudeUserLine("first task"),
		claudeAssistantLine("claude-opus-4-8", "2026-06-20T10:01:00.000Z", 100, 200, 0, 0),
	})
	observed := time.Date(2026, 6, 20, 12, 0, 0, 0, time.UTC)

	// First cycle initializes the baseline; historical sessions are ignored
	// (no usage / session events emitted), mirroring Codex.
	usage, sessions, meta, err := collectClaudeUsageEvents(cfg, projectsDir, observed)
	if err != nil {
		t.Fatalf("collect cycle 1: %v", err)
	}
	if len(usage) != 0 || len(sessions) != 0 {
		t.Fatalf("cycle 1 should emit nothing (baseline init): usage=%d sessions=%d", len(usage), len(sessions))
	}
	if initialized, _ := meta["session_baseline_initialized"].(bool); !initialized {
		t.Fatalf("cycle 1 should initialize baseline, meta=%#v", meta)
	}

	// Second cycle over the unchanged transcript must emit ZERO new deltas.
	usage, sessions, _, err = collectClaudeUsageEvents(cfg, projectsDir, observed.Add(time.Minute))
	if err != nil {
		t.Fatalf("collect cycle 2: %v", err)
	}
	if len(usage) != 0 {
		t.Fatalf("cycle 2 over unchanged transcript should emit no usage, got %d: %#v", len(usage), usage)
	}
	if len(sessions) != 0 {
		t.Fatalf("cycle 2 over unchanged transcript should emit no session events, got %d", len(sessions))
	}

	// Append a new assistant message, then re-run: only the new delta is emitted.
	path := writeClaudeTranscript(t, home, claudeSessionUUID, []string{
		claudeUserLine("first task"),
		claudeAssistantLine("claude-opus-4-8", "2026-06-20T10:01:00.000Z", 100, 200, 0, 0),
		claudeAssistantLine("claude-opus-4-8", "2026-06-20T10:10:00.000Z", 10, 20, 0, 0),
	})
	_ = path
	usage, sessions, _, err = collectClaudeUsageEvents(cfg, projectsDir, observed.Add(2*time.Minute))
	if err != nil {
		t.Fatalf("collect cycle 3: %v", err)
	}
	if len(usage) != 1 {
		t.Fatalf("cycle 3 should emit exactly 1 advanced usage bucket, got %d: %#v", len(usage), usage)
	}
	// Cumulative bucket total = 300 + 30 = 330; baseline previous was 300.
	if total, _ := payloadIntValue(usage[0], "total_tokens"); total != 330 {
		t.Fatalf("cycle 3 cumulative bucket total = %d, want 330", total)
	}
	if cursorOnly, _ := usage[0][internalCursorOnlyPayloadKey].(bool); !cursorOnly {
		t.Fatalf("advanced usage should be cursor-only: %#v", usage[0])
	}
	if len(sessions) != 1 {
		t.Fatalf("cycle 3 should re-emit the changed session, got %d", len(sessions))
	}
}

func TestCollectClaudeUsageEventsReemitsUserOnlyTranscriptChange(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dataDir := t.TempDir()
	cfg := Config{DataDir: dataDir, UserName: "tester"}
	projectsDir := filepath.Join(home, ".claude", "projects")
	observed := time.Date(2026, 6, 20, 12, 0, 0, 0, time.UTC)

	writeClaudeTranscript(t, home, claudeSessionUUID, []string{
		claudeUserLine("first task"),
		claudeAssistantTextLineWithID("msg_aaa", "claude-opus-4-8", "2026-06-20T10:01:00.000Z", "first answer", 100, 200, 0, 0),
	})
	if _, _, _, err := collectClaudeUsageEvents(cfg, projectsDir, observed); err != nil {
		t.Fatalf("baseline init: %v", err)
	}
	usage, sessions, _, err := collectClaudeUsageEvents(cfg, projectsDir, observed.Add(time.Minute))
	if err != nil {
		t.Fatalf("unchanged collect: %v", err)
	}
	if len(usage) != 0 || len(sessions) != 0 {
		t.Fatalf("unchanged collect should emit nothing, usage=%d sessions=%d", len(usage), len(sessions))
	}

	writeClaudeTranscript(t, home, claudeSessionUUID, []string{
		claudeUserLine("first task"),
		claudeAssistantTextLineWithID("msg_aaa", "claude-opus-4-8", "2026-06-20T10:01:00.000Z", "first answer", 100, 200, 0, 0),
		claudeUserLine("new prompt before assistant reply"),
	})
	usage, sessions, _, err = collectClaudeUsageEvents(cfg, projectsDir, observed.Add(2*time.Minute))
	if err != nil {
		t.Fatalf("changed collect: %v", err)
	}
	if len(usage) != 0 {
		t.Fatalf("user-only transcript change should not emit usage, got %d", len(usage))
	}
	if len(sessions) != 1 {
		t.Fatalf("user-only transcript change should re-emit session, got %d", len(sessions))
	}
	messages, ok := sessions[0]["messages"].([]map[string]interface{})
	if !ok || len(messages) != 3 {
		t.Fatalf("messages = %#v, want 3 messages", sessions[0]["messages"])
	}
	if messages[2]["text"] != "new prompt before assistant reply" {
		t.Fatalf("last message = %#v", messages[2])
	}
}

func TestCollectClaudeUsageEventsEmitsZeroUsageTranscriptWithoutUsageSummary(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dataDir := t.TempDir()
	cfg := Config{DataDir: dataDir, UserName: "tester"}
	projectsDir := filepath.Join(home, ".claude", "projects")
	observed := time.Date(2026, 6, 20, 12, 0, 0, 0, time.UTC)

	if _, _, _, err := collectClaudeUsageEvents(cfg, projectsDir, observed); err != nil {
		t.Fatalf("baseline init: %v", err)
	}
	writeClaudeTranscript(t, home, claudeSessionUUID, []string{
		claudeUserLine("hello"),
		claudeAssistantTextLineWithID("resp_zero", "gpt-5.5", "2026-06-20T10:01:00.000Z", "zero usage answer", 0, 0, 0, 0),
	})

	usage, sessions, _, err := collectClaudeUsageEvents(cfg, projectsDir, observed.Add(time.Minute))
	if err != nil {
		t.Fatalf("collect zero-usage transcript: %v", err)
	}
	if len(usage) != 0 {
		t.Fatalf("zero-usage transcript should not emit usage summaries, got %d: %#v", len(usage), usage)
	}
	if len(sessions) != 1 {
		t.Fatalf("zero-usage transcript should emit one session transcript, got %d", len(sessions))
	}
	messages, ok := sessions[0]["messages"].([]map[string]interface{})
	if !ok || len(messages) != 2 {
		t.Fatalf("messages = %#v, want 2 transcript messages", sessions[0]["messages"])
	}
	if sessions[0]["model"] != "gpt-5.5" {
		t.Fatalf("session model = %#v, want gpt-5.5", sessions[0]["model"])
	}
}

func TestCollectClaudeUsageEventsDeltaPipeline(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dataDir := t.TempDir()
	cfg := Config{DataDir: dataDir, UserName: "tester", UsageWindow: "10m"}
	projectsDir := filepath.Join(home, ".claude", "projects")

	// Initialize baseline with an empty-ish state, then add a brand-new session
	// so it produces an initial delta.
	if _, _, _, err := collectClaudeUsageEvents(cfg, projectsDir, time.Date(2026, 6, 20, 9, 0, 0, 0, time.UTC)); err != nil {
		t.Fatalf("baseline init: %v", err)
	}

	writeClaudeTranscript(t, home, claudeSessionUUID, []string{
		claudeAssistantLine("claude-opus-4-8", "2026-06-20T23:30:00.000Z", 100, 100, 0, 0),
		claudeAssistantLine("claude-opus-4-8", "2026-06-21T00:30:00.000Z", 50, 50, 0, 0),
	})
	observed := time.Date(2026, 6, 21, 1, 0, 0, 0, time.UTC)

	// Emit usage.summary events (this advances the baseline once) and run the
	// generic delta machinery over them.
	events := claudeUsageEvents(cfg, projectsDir, observed)
	usageSummaryCount := 0
	for _, event := range events {
		if event.EventType == "usage.summary" {
			usageSummaryCount++
		}
	}
	if usageSummaryCount != 2 {
		t.Fatalf("expected 2 usage.summary events (one per day bucket), got %d", usageSummaryCount)
	}
	deltas, commit, err := BuildUsageDeltaEvents(cfg, events, observed)
	if err != nil {
		t.Fatalf("build deltas: %v", err)
	}
	if len(deltas) != 2 {
		t.Fatalf("expected 2 usage.delta events (one per day bucket), got %d", len(deltas))
	}
	days := map[string]int64{}
	for _, delta := range deltas {
		day := delta.CreatedAt.UTC().Format("2006-01-02")
		days[day] = payloadInt(delta.Payload, "total_tokens")
		if delta.AgentType != claudeCodeAgentType {
			t.Fatalf("delta agent type = %q", delta.AgentType)
		}
	}
	if days["2026-06-20"] != 200 {
		t.Fatalf("2026-06-20 delta = %d, want 200", days["2026-06-20"])
	}
	if days["2026-06-21"] != 100 {
		t.Fatalf("2026-06-21 delta = %d, want 100", days["2026-06-21"])
	}
	// Both day buckets must emit their delta under the SAME real session id (the
	// session-index id), not the salted per-bucket cursor id, so the control plane
	// reconciles agent_sessions.total_tokens across buckets by session_id.
	indexSessionID := util.ShortHash(claudeSessionUUID)
	for _, delta := range deltas {
		if sid := firstPayloadString(delta.Payload, "session_id"); sid != indexSessionID {
			t.Fatalf("delta session_id = %q, want real session-index id %q (control-plane reconcile would miss it)", sid, indexSessionID)
		}
	}
	if commit != nil {
		if err := commit(); err != nil {
			t.Fatalf("commit cursors: %v", err)
		}
	}

	// Re-running the SAME events after commit must yield zero new deltas.
	events2 := claudeUsageEvents(cfg, projectsDir, observed.Add(time.Minute))
	deltas2, _, err := BuildUsageDeltaEvents(cfg, events2, observed.Add(time.Minute))
	if err != nil {
		t.Fatalf("build deltas (2): %v", err)
	}
	if len(deltas2) != 0 {
		t.Fatalf("re-run should yield no new deltas, got %d", len(deltas2))
	}
}

// TestCollectClaudeUsageEventsRecoversAfterCrashBetweenBaselineAndCursor
// reproduces the two-phase-commit data-loss window: the baseline is saved a
// phase earlier than the usage cursor (runner.go), so a crash after the baseline
// advance but before commitUsageDeltas() must NOT permanently drop the delta.
func TestCollectClaudeUsageEventsRecoversAfterCrashBetweenBaselineAndCursor(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dataDir := t.TempDir()
	cfg := Config{DataDir: dataDir, UserName: "tester", UsageWindow: "10m"}
	projectsDir := filepath.Join(home, ".claude", "projects")

	// Cycle 1: initialize the baseline over an existing session (nothing emitted).
	writeClaudeTranscript(t, home, claudeSessionUUID, []string{
		claudeUserLine("first task"),
		claudeAssistantLine("claude-opus-4-8", "2026-06-20T10:01:00.000Z", 100, 100, 0, 0),
	})
	if _, _, _, err := collectClaudeUsageEvents(cfg, projectsDir, time.Date(2026, 6, 20, 9, 0, 0, 0, time.UTC)); err != nil {
		t.Fatalf("baseline init: %v", err)
	}

	// Append a real new turn, then run the producing pipeline. This advances the
	// baseline (persisted now) and yields a 200-token delta. We deliberately do
	// NOT call the cursor commit — simulating a crash between the two phases.
	writeClaudeTranscript(t, home, claudeSessionUUID, []string{
		claudeUserLine("first task"),
		claudeAssistantLine("claude-opus-4-8", "2026-06-20T10:01:00.000Z", 100, 100, 0, 0),
		claudeAssistantLine("claude-opus-4-8", "2026-06-20T10:10:00.000Z", 100, 100, 0, 0),
	})
	observed := time.Date(2026, 6, 20, 12, 0, 0, 0, time.UTC)
	events := claudeUsageEvents(cfg, projectsDir, observed)
	deltas, commit, err := BuildUsageDeltaEvents(cfg, events, observed)
	if err != nil {
		t.Fatalf("build deltas (pre-crash): %v", err)
	}
	if len(deltas) != 1 {
		t.Fatalf("pre-crash cycle should build 1 delta, got %d", len(deltas))
	}
	if got := payloadInt(deltas[0].Payload, "total_tokens"); got != 200 {
		t.Fatalf("pre-crash delta = %d, want 200", got)
	}
	_ = commit // CRASH: commitUsageDeltas() is never called, cursor stays stale.

	// Restart: re-run over the UNCHANGED transcript. The baseline already sits at
	// the advanced total, so without recovery the summary would be suppressed and
	// the 200-token delta lost forever. Recovery must re-emit it exactly once.
	observed2 := observed.Add(time.Minute)
	events2 := claudeUsageEvents(cfg, projectsDir, observed2)
	deltas2, commit2, err := BuildUsageDeltaEvents(cfg, events2, observed2)
	if err != nil {
		t.Fatalf("build deltas (post-crash): %v", err)
	}
	if len(deltas2) != 1 {
		t.Fatalf("post-crash restart should recover exactly 1 delta, got %d", len(deltas2))
	}
	if got := payloadInt(deltas2[0].Payload, "total_tokens"); got != 200 {
		t.Fatalf("recovered delta = %d, want 200", got)
	}
	if commit2 != nil {
		if err := commit2(); err != nil {
			t.Fatalf("commit recovered cursor: %v", err)
		}
	}

	// After the cursor finally commits, a subsequent unchanged cycle must NOT
	// re-emit the delta (no double counting).
	observed3 := observed2.Add(time.Minute)
	events3 := claudeUsageEvents(cfg, projectsDir, observed3)
	deltas3, _, err := BuildUsageDeltaEvents(cfg, events3, observed3)
	if err != nil {
		t.Fatalf("build deltas (post-commit): %v", err)
	}
	if len(deltas3) != 0 {
		t.Fatalf("post-commit unchanged cycle should emit no deltas, got %d", len(deltas3))
	}
}

func TestClaudeUsageSummaryPayloadHashesIdentifiers(t *testing.T) {
	session := claudeSessionUsage{
		SessionID:  claudeSessionUUID,
		CWD:        "/Users/secret/private-repo",
		GitBranch:  "main",
		Title:      "do the thing",
		ByModelDay: map[claudeModelDayKey]claudeAssistantUsage{},
	}
	key := claudeModelDayKey{Model: "claude-opus-4-8", Day: "2026-06-20"}
	payload := claudeUsageSummaryPayload(session, key, claudeAssistantUsage{InputTokens: 5, OutputTokens: 7})

	if payload["session_id"] == claudeSessionUUID {
		t.Fatal("session id should be hashed")
	}
	if _, ok := payload["cwd"]; ok {
		t.Fatal("payload leaked raw cwd")
	}
	if payload["repo_path_hash"] != util.ShortHash("/Users/secret/private-repo") {
		t.Fatalf("repo hash = %#v", payload["repo_path_hash"])
	}
	if payload["model"] != "claude-opus-4-8" {
		t.Fatalf("model = %#v", payload["model"])
	}
	if payload["usage_day"] != "2026-06-20" {
		t.Fatalf("usage_day = %#v", payload["usage_day"])
	}
	if total, _ := payloadIntValue(payload, "total_tokens"); total != 12 {
		t.Fatalf("total tokens = %d, want 12", total)
	}
}
