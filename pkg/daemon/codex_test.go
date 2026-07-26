package daemon

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gesta-run/gesta-agent/pkg/model"
	"github.com/gesta-run/gesta-agent/pkg/util"
)

func TestCodexBinaryPathUsesBundledCandidateWhenPATHMisses(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	binDir := filepath.Join(t.TempDir(), "ChatGPT.app", "Contents", "Resources")
	if err := os.MkdirAll(binDir, 0o700); err != nil {
		t.Fatalf("mkdir binary dir: %v", err)
	}
	binPath := filepath.Join(binDir, "codex")
	if err := os.WriteFile(binPath, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatalf("write codex binary: %v", err)
	}

	if got := codexBinaryPathWithCandidates([]string{filepath.Join(t.TempDir(), "missing"), binPath}); got != binPath {
		t.Fatalf("codex binary path = %q, want %q", got, binPath)
	}
}

func TestCodexBinaryPathIgnoresNonExecutableCandidate(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	binPath := filepath.Join(t.TempDir(), "codex")
	if err := os.WriteFile(binPath, []byte("#!/bin/sh\nexit 0\n"), 0o600); err != nil {
		t.Fatalf("write codex binary: %v", err)
	}

	if got := codexBinaryPathWithCandidates([]string{binPath}); got != "" {
		t.Fatalf("codex binary path = %q, want empty for non-executable candidate", got)
	}
}

func TestCodexUsagePayloadIsMetadataOnly(t *testing.T) {
	payload := codexUsagePayload(map[string]interface{}{
		"id":          "raw-session-id",
		"cwd":         "/Users/alice/private/repo",
		"model":       "gpt-5-codex",
		"tokens_used": float64(42),
		"git_branch":  "main",
		"git_sha":     "abc123",
	}, nil)

	if payload["session_id"] == "raw-session-id" {
		t.Fatal("expected session id to be hashed")
	}
	if _, ok := payload["cwd"]; ok {
		t.Fatal("payload included raw cwd")
	}
	if payload["cwd_hash"] == "" {
		t.Fatal("expected cwd hash")
	}
	if payload["total_tokens"] != int64(42) {
		t.Fatalf("unexpected total tokens: %#v", payload["total_tokens"])
	}
	if payload["metadata_only"] != true {
		t.Fatalf("expected metadata_only marker, got %#v", payload["metadata_only"])
	}

	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	serialized := string(data)
	if strings.Contains(serialized, "raw-session-id") || strings.Contains(serialized, "/Users/alice/private/repo") {
		t.Fatalf("payload leaked raw local identifiers: %s", serialized)
	}
}

func TestCodexUsagePayloadHashesForkParent(t *testing.T) {
	parentID := "019f125b-0251-7c42-9678-b80e9f630fc2"
	payload := codexUsagePayload(map[string]interface{}{
		"id":                "raw-child-session-id",
		"parent_session_id": parentID,
		"tokens_used":       int64(42),
	}, nil)

	parentHash := util.ShortHash(parentID)
	if payload["parent_session_id"] != parentHash || payload["parent_session_id_hash"] != parentHash {
		t.Fatalf("parent hash fields = %#v, want %s", payload, parentHash)
	}
	if payload["parent_session_id_is_hashed"] != true {
		t.Fatalf("parent_session_id_is_hashed = %#v, want true", payload["parent_session_id_is_hashed"])
	}
	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	if strings.Contains(string(data), parentID) {
		t.Fatalf("payload leaked raw parent id: %s", data)
	}
}

func TestCodexUsagePayloadReadsForkParentFromRollout(t *testing.T) {
	rolloutPath := filepath.Join(t.TempDir(), "rollout.jsonl")
	parentID := "019f125b-0251-7c42-9678-b80e9f630fc2"
	line := `{"type":"session_meta","payload":{"session_id":"child","forked_from_id":"` + parentID + `"}}`
	if err := os.WriteFile(rolloutPath, []byte(line+"\n"), 0o600); err != nil {
		t.Fatalf("write rollout: %v", err)
	}

	payload := codexUsagePayload(map[string]interface{}{
		"id":           "raw-child-session-id",
		"rollout_path": rolloutPath,
		"tokens_used":  int64(42),
	}, nil)

	parentHash := util.ShortHash(parentID)
	if payload["parent_session_id"] != parentHash {
		t.Fatalf("parent_session_id = %#v, want %s", payload["parent_session_id"], parentHash)
	}
}

func TestCodexUsagePayloadUsesRawTokensFromRollout(t *testing.T) {
	rolloutPath := filepath.Join(t.TempDir(), "rollout.jsonl")
	line := `{"type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":200000,"cached_input_tokens":180000,"output_tokens":7000,"total_tokens":207000}}}}`
	if err := os.WriteFile(rolloutPath, []byte(line+"\n"), 0o600); err != nil {
		t.Fatalf("write rollout: %v", err)
	}

	payload := codexUsagePayload(map[string]interface{}{
		"id":           "raw-session-id",
		"model":        "gpt-5-codex",
		"tokens_used":  int64(207000),
		"rollout_path": rolloutPath,
	}, nil)

	if payload["total_tokens"] != int64(207000) {
		t.Fatalf("total_tokens = %#v, want 207000", payload["total_tokens"])
	}
	if payload["tokens_used"] != int64(207000) {
		t.Fatalf("tokens_used = %#v, want 207000", payload["tokens_used"])
	}
	if payload["input_tokens"] != int64(200000) {
		t.Fatalf("input_tokens = %#v, want raw input 200000", payload["input_tokens"])
	}
	if payload["cached_input_tokens"] != int64(180000) {
		t.Fatalf("cached_input_tokens = %#v, want 180000", payload["cached_input_tokens"])
	}
	if payload["effective_tokens"] != int64(27000) {
		t.Fatalf("effective_tokens = %#v, want 27000", payload["effective_tokens"])
	}
	if payload["raw_total_tokens"] != int64(207000) {
		t.Fatalf("raw_total_tokens = %#v, want 207000", payload["raw_total_tokens"])
	}
	if payload["token_accounting"] != "raw_total" {
		t.Fatalf("token_accounting = %#v, want raw_total", payload["token_accounting"])
	}
}

func TestCodexEffectiveTokenUsageSkipsHugeLines(t *testing.T) {
	rolloutPath := filepath.Join(t.TempDir(), "rollout.jsonl")
	first := `{"type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":100,"cached_input_tokens":10,"output_tokens":20,"total_tokens":120}}}}`
	huge := `{"type":"response_item","payload":"` + strings.Repeat("x", codexTokenUsageMaxLineBytes+1024) + `"}`
	second := `{"type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":200,"cached_input_tokens":20,"output_tokens":30,"total_tokens":230}}}}`
	if err := os.WriteFile(rolloutPath, []byte(first+"\n"+huge+"\n"+second+"\n"), 0o600); err != nil {
		t.Fatalf("write rollout: %v", err)
	}

	usage, ok := codexEffectiveTokenUsage(rolloutPath)
	if !ok {
		t.Fatal("expected token usage")
	}
	if usage.TotalTokens != 230 {
		t.Fatalf("total tokens = %d, want 230", usage.TotalTokens)
	}
	if usage.InputTokens != 200 || usage.CachedInputTokens != 20 || usage.OutputTokens != 30 {
		t.Fatalf("usage = %+v, want latest token_count", usage)
	}
}

func TestCodexUsagePayloadConvertsNumericTimestamps(t *testing.T) {
	payload := codexUsagePayload(map[string]interface{}{
		"id":            "raw-session-id",
		"tokens_used":   int64(10),
		"created_at":    int64(1782101675),
		"updated_at_ms": int64(1782101681267),
	}, nil)

	if payload["created_at"] != "2026-06-22T04:14:35Z" {
		t.Fatalf("created_at = %#v, want RFC3339 timestamp", payload["created_at"])
	}
	if payload["updated_at"] != "2026-06-22T04:14:41.267Z" {
		t.Fatalf("updated_at = %#v, want RFC3339 timestamp from milliseconds", payload["updated_at"])
	}
}

func TestCodexTranscriptPayloadExtractsRedactedMessages(t *testing.T) {
	rolloutPath := filepath.Join(t.TempDir(), "rollout.jsonl")
	lines := []string{
		`{"timestamp":"2026-06-12T00:00:00Z","type":"response_item","payload":{"type":"message","role":"developer","content":[{"type":"text","text":"do not store this"}]}}`,
		`{"timestamp":"2026-06-12T00:00:30Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"text","text":"<environment_context><cwd>/private</cwd><current_date>2026-06-12</current_date></environment_context>"}]}}`,
		`{"timestamp":"2026-06-12T00:01:00Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"text","text":"please inspect api_key=super-secret"}]}}`,
		`{"timestamp":"2026-06-12T00:02:00Z","type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"text","text":"done"}]}}`,
		`{"timestamp":"2026-06-12T00:03:00Z","type":"response_item","payload":{"type":"function_call","name":"exec_command","arguments":"{\"cmd\":\"nl -ba console/src/app/page.tsx\"}","call_id":"call_abc123"}}`,
		`{"timestamp":"2026-06-12T00:04:00Z","type":"response_item","payload":{"type":"function_call_output","call_id":"call_abc123","output":"do not store command output"}}`,
	}
	if err := os.WriteFile(rolloutPath, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatalf("write rollout: %v", err)
	}

	usagePayload := codexUsagePayload(map[string]interface{}{
		"id":           "raw-session-id",
		"model":        "gpt-5-codex",
		"rollout_path": rolloutPath,
	}, nil)
	payload := codexTranscriptPayload(map[string]interface{}{
		"id":           "raw-session-id",
		"model":        "gpt-5-codex",
		"rollout_path": rolloutPath,
		"updated_at":   "2026-06-12T00:02:00Z",
	}, usagePayload, nil)

	if payload["metadata_only"] != false {
		t.Fatalf("metadata_only = %#v, want false", payload["metadata_only"])
	}
	messages, ok := payload["messages"].([]map[string]interface{})
	if !ok || len(messages) != 2 {
		t.Fatalf("messages = %#v", payload["messages"])
	}
	if payload["_rollout_path"] != rolloutPath {
		t.Fatalf("internal rollout path = %#v, want local path", payload["_rollout_path"])
	}
	publicPayload := codexPublicTranscriptPayload(payload)
	if _, ok := publicPayload["_rollout_path"]; ok {
		t.Fatalf("public payload leaked internal rollout path: %#v", publicPayload)
	}
	serialized, err := json.Marshal(publicPayload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	text := string(serialized)
	if strings.Contains(text, "do not store this") || strings.Contains(text, "environment_context") || strings.Contains(text, "do not store command output") || strings.Contains(text, "raw-session-id") || strings.Contains(text, rolloutPath) {
		t.Fatalf("payload leaked skipped text or raw identifiers: %s", text)
	}
	if !strings.Contains(text, "api_key=[REDACTED]") {
		t.Fatalf("payload did not redact secret: %s", text)
	}
	if strings.Contains(text, "nl -ba console/src/app/page.tsx") {
		t.Fatalf("payload included command text: %s", text)
	}
	var equivalentPayload map[string]interface{}
	if err := json.Unmarshal(serialized, &equivalentPayload); err != nil {
		t.Fatalf("unmarshal equivalent payload: %v", err)
	}
	firstEventID := codexTranscriptEventID(publicPayload)
	if firstEventID == "" || firstEventID != codexTranscriptEventID(equivalentPayload) {
		t.Fatal("transcript event id should be stable for equivalent payloads")
	}
}

func TestCodexToolCallEventsFromTranscriptKeepMetadataOnly(t *testing.T) {
	rolloutPath := filepath.Join(t.TempDir(), "rollout.jsonl")
	lines := []string{
		`{"timestamp":"2026-06-12T00:01:00Z","type":"response_item","payload":{"type":"function_call","name":"mcp__github__delete_repo","arguments":"{\"repo\":\"secret/private\"}","call_id":"call_mcp_1"}}`,
		`{"timestamp":"2026-06-12T00:01:30Z","type":"response_item","payload":{"type":"function_call","name":"search","namespace":"mcp__anysearch__","arguments":"{\"query\":\"secret\"}","call_id":"call_mcp_2"}}`,
		`{"timestamp":"2026-06-12T00:01:31Z","type":"event_msg","payload":{"type":"mcp_tool_call_end","call_id":"call_mcp_2","invocation":{"server":"anysearch","tool":"search","arguments":{"query":"secret"}}}}`,
		`{"timestamp":"2026-06-12T00:01:40Z","type":"response_item","payload":{"type":"function_call","name":"notion_search","namespace":"mcp__notion__","arguments":"{}","call_id":"call_mcp_3"}}`,
		`{"timestamp":"2026-06-12T00:02:00Z","type":"response_item","payload":{"type":"function_call","name":"exec_command","arguments":"{\"cmd\":\"cat .env\"}","call_id":"call_shell_1"}}`,
		`{"timestamp":"2026-06-12T00:03:00Z","type":"response_item","payload":{"type":"function_call_output","call_id":"call_shell_1","output":"do not store this"}}`,
		`{"timestamp":"2026-06-12T00:04:00Z","type":"response_item","payload":{"type":"function_call","name":"codex_apps.github.merge_pull_request","arguments":"{}","call_id":"call_app_1"}}`,
		`{"timestamp":"2026-06-12T00:04:30Z","type":"response_item","payload":{"type":"function_call","name":"mcp__codex_apps__github.merge_pull_request","arguments":"{}","call_id":"call_app_2"}}`,
		`{"timestamp":"2026-06-12T00:05:00Z","type":"event_msg","payload":{"type":"mcp_tool_call_end","call_id":"call_app_3","invocation":{"server":"codex_apps","tool":"notion.search","arguments":{}}}}`,
	}
	if err := os.WriteFile(rolloutPath, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatalf("write rollout: %v", err)
	}

	cfg := NewDirectRuntimeConfig("http://127.0.0.1:1", "dtok_tool_calls")
	cfg.DataDir = t.TempDir()
	events := codexToolCallEventsFromTranscript(cfg, map[string]interface{}{
		"session_id":    "hashed-session",
		"_rollout_path": rolloutPath,
	})
	if len(events) != 7 {
		t.Fatalf("events = %d, want 7", len(events))
	}
	first := events[0]
	if first.EventType != "tool.call" || first.Payload["tool_type"] != "mcp" ||
		first.Payload["mcp_server_id"] != "github" ||
		first.Payload["mcp_server_name"] != "github" ||
		first.Payload["mcp_tool"] != "delete_repo" {
		t.Fatalf("unexpected MCP tool event: %+v", first)
	}
	if first.CreatedAt.Format(time.RFC3339) != "2026-06-12T00:01:00Z" {
		t.Fatalf("created_at = %s", first.CreatedAt.Format(time.RFC3339))
	}
	second := events[1]
	if second.Payload["tool_type"] != "mcp" ||
		second.Payload["mcp_server_id"] != "anysearch" ||
		second.Payload["mcp_server_name"] != "anysearch" ||
		second.Payload["mcp_tool"] != "search" {
		t.Fatalf("unexpected MCP end event: %+v", second)
	}
	if second.CreatedAt.Format(time.RFC3339) != "2026-06-12T00:01:31Z" {
		t.Fatalf("created_at = %s", second.CreatedAt.Format(time.RFC3339))
	}
	third := events[2]
	if third.Payload["tool_type"] != "mcp" ||
		third.Payload["mcp_server_id"] != "notion" ||
		third.Payload["mcp_server_name"] != "notion" ||
		third.Payload["mcp_tool"] != "notion_search" {
		t.Fatalf("unexpected namespace MCP event: %+v", third)
	}
	fourth := events[3]
	if fourth.Payload["tool_name"] != "exec_command" || fourth.Payload["tool_type"] != "agent_tool" {
		t.Fatalf("unexpected agent tool event: %+v", fourth)
	}
	for _, event := range events[4:] {
		if event.Payload["tool_type"] != "agent_tool" ||
			event.Payload["mcp_server_id"] != nil ||
			event.Payload["mcp_server_name"] != nil ||
			event.Payload["mcp_tool"] != nil {
			t.Fatalf("codex app should be an agent tool, not MCP: %+v", event)
		}
	}
	serialized, err := json.Marshal(events)
	if err != nil {
		t.Fatalf("marshal events: %v", err)
	}
	text := string(serialized)
	if strings.Contains(text, "secret/private") || strings.Contains(text, "cat .env") || strings.Contains(text, "do not store this") || strings.Contains(text, rolloutPath) {
		t.Fatalf("tool call metadata leaked arguments, output, or local path: %s", text)
	}
}

func TestCodexSensitiveFindingEventsUseOriginalTranscriptText(t *testing.T) {
	rolloutPath := filepath.Join(t.TempDir(), "rollout.jsonl")
	secret := "sk-" + strings.Repeat("a", 24)
	lines := []string{
		`{"timestamp":"2026-06-12T00:01:00Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"text","text":"please inspect ` + secret + `"}]}}`,
		`{"timestamp":"2026-06-12T00:02:00Z","type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"text","text":"done"}]}}`,
	}
	if err := os.WriteFile(rolloutPath, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatalf("write rollout: %v", err)
	}

	cfg := NewDirectRuntimeConfig("http://127.0.0.1:1", "dtok_sensitive_transcript")
	cfg.DataDir = t.TempDir()
	usagePayload := codexUsagePayload(map[string]interface{}{
		"id":           "raw-session-id",
		"model":        "gpt-5-codex",
		"rollout_path": rolloutPath,
	}, nil)
	transcript := codexTranscriptPayload(map[string]interface{}{
		"id":           "raw-session-id",
		"model":        "gpt-5-codex",
		"rollout_path": rolloutPath,
		"updated_at":   "2026-06-12T00:02:00Z",
	}, usagePayload, nil)

	publicTranscript, err := json.Marshal(codexPublicTranscriptPayload(transcript))
	if err != nil {
		t.Fatalf("marshal public transcript: %v", err)
	}
	if strings.Contains(string(publicTranscript), secret) {
		t.Fatalf("public transcript leaked raw secret: %s", publicTranscript)
	}
	if !strings.Contains(string(publicTranscript), "[REDACTED]") {
		t.Fatalf("public transcript should keep redacted copy: %s", publicTranscript)
	}

	rules := []model.SensitiveRule{
		{
			RuleID:       "srule_test_openai",
			Name:         "Test OpenAI",
			Status:       "active",
			Source:       "user_prompt",
			DetectorType: "regex",
			Pattern:      `sk-[A-Za-z0-9_-]+`,
			Category:     "sensitive_data",
			Severity:     "high",
			Action:       "warn",
			SampleMode:   "original",
			Confidence:   0.9,
			Priority:     1,
		},
	}
	observedAt := time.Date(2026, 6, 12, 0, 3, 0, 0, time.UTC)
	events := codexSensitiveFindingEventsAt(cfg, transcript, rules, observedAt)
	if len(events) != 1 {
		t.Fatalf("events = %#v, want one sensitive finding", events)
	}
	event := events[0]
	if event.EventType != "sensitive.finding" || event.Source != "codex" || event.AgentType != "codex" {
		t.Fatalf("unexpected event envelope: %#v", event)
	}
	if event.CreatedAt.Format("2006-01-02T15:04:05Z") != "2026-06-12T00:01:00Z" {
		t.Fatalf("created_at = %s", event.CreatedAt)
	}
	payload := event.Payload
	if payload["detection_source"] != "session_transcript" ||
		payload["action"] != "warn" ||
		payload["sample_mode"] != "original" ||
		payload["raw_content_stored"] != true ||
		payload["rule_id"] != "srule_test_openai" {
		t.Fatalf("unexpected finding payload: %#v", payload)
	}
	if sample, ok := payload["sample"].(string); !ok || !strings.Contains(sample, secret) {
		t.Fatalf("finding should keep original sample: %#v", payload)
	}
	if payload["fingerprint"] == "" || payload["message_hash"] == "" || payload["transcript_path_hash"] == "" {
		t.Fatalf("missing derived metadata: %#v", payload)
	}
	if events[0].EventID != codexSensitiveFindingEventsAt(cfg, transcript, rules, observedAt)[0].EventID {
		t.Fatal("sensitive finding event id should be deterministic")
	}
}

func TestCodexSensitiveTranscriptFallbackSkipsWhenUserPromptHookActive(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home")
	codexDir := filepath.Join(home, ".codex")
	if err := os.MkdirAll(codexDir, 0o700); err != nil {
		t.Fatalf("mkdir codex dir: %v", err)
	}
	t.Setenv("HOME", home)

	hookPath := filepath.Join(codexDir, "hooks.json")
	hooks := `{
  "hooks": {
    "UserPromptSubmit": [
      {
        "hooks": [
          {"type": "command", "command": "'/tmp/gesta-agent' codex-hook"}
        ]
      }
    ]
  }
}`
	if err := os.WriteFile(hookPath, []byte(hooks), 0o600); err != nil {
		t.Fatalf("write hooks: %v", err)
	}
	config := `[features]
hooks = true

[hooks.state.` + tomlBasicString(hookPath+":user_prompt_submit:0:0") + `]
trusted_hash = "sha256:test"
`
	if err := os.WriteFile(filepath.Join(codexDir, "config.toml"), []byte(config), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	rolloutPath := filepath.Join(t.TempDir(), "rollout.jsonl")
	secret := "sk-" + strings.Repeat("b", 24)
	now := time.Now().UTC().Format(time.RFC3339Nano)
	line := `{"timestamp":"` + now + `","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"text","text":"please inspect ` + secret + `"}]}}`
	if err := os.WriteFile(rolloutPath, []byte(line+"\n"), 0o600); err != nil {
		t.Fatalf("write rollout: %v", err)
	}

	cfg := NewDirectRuntimeConfig("http://127.0.0.1:1", "dtok_sensitive_transcript_active_hook")
	cfg.DataDir = t.TempDir()
	transcript := codexTranscriptPayload(map[string]interface{}{
		"id":           "raw-session-id",
		"model":        "gpt-5-codex",
		"rollout_path": rolloutPath,
		"updated_at":   now,
	}, map[string]interface{}{}, nil)
	rules := []model.SensitiveRule{
		{
			RuleID:       "srule_test_openai",
			Name:         "Test OpenAI",
			Status:       "active",
			Source:       "user_prompt",
			DetectorType: "regex",
			Pattern:      `sk-[A-Za-z0-9_-]+`,
			Category:     "sensitive_data",
			Severity:     "high",
			Action:       "warn",
			SampleMode:   "original",
			Confidence:   0.9,
			Priority:     1,
		},
	}
	if events := codexSensitiveFindingEventsFromTranscripts(cfg, []map[string]interface{}{transcript}, rules); len(events) != 0 {
		t.Fatalf("fallback produced events while prompt hook is active: %#v", events)
	}
}

func TestReadCodexTranscriptReadsTailOfLargeRollout(t *testing.T) {
	rolloutPath := filepath.Join(t.TempDir(), "large-rollout.jsonl")
	largePrefix := strings.Repeat("x", codexTranscriptTailBytes+1024)
	lines := []string{
		`{"type":"session_meta","payload":"` + largePrefix + `"}`,
		`{"timestamp":"2026-06-12T00:01:00Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"text","text":"recent question"}]}}`,
		`{"timestamp":"2026-06-12T00:02:00Z","type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"text","text":"recent answer"}]}}`,
	}
	if err := os.WriteFile(rolloutPath, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatalf("write rollout: %v", err)
	}

	messages, truncated, err := readCodexTranscript(rolloutPath)
	if err != nil {
		t.Fatalf("readCodexTranscript: %v", err)
	}
	if truncated {
		t.Fatal("did not expect truncation")
	}
	if len(messages) != 2 {
		t.Fatalf("messages = %#v", messages)
	}
	if messages[0]["role"] != "user" || messages[0]["text"] != "recent question" {
		t.Fatalf("unexpected first message: %#v", messages[0])
	}
	if messages[1]["role"] != "assistant" || messages[1]["text"] != "recent answer" {
		t.Fatalf("unexpected second message: %#v", messages[1])
	}
}

func TestReadCodexTranscriptKeepsLatestMessages(t *testing.T) {
	rolloutPath := filepath.Join(t.TempDir(), "long-rollout.jsonl")
	lines := make([]string, 0, codexMaxTranscriptMessages+5)
	for i := 0; i < codexMaxTranscriptMessages+5; i++ {
		lines = append(lines, fmt.Sprintf(`{"timestamp":"2026-06-12T00:%02d:00Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"text","text":"message %03d"}]}}`, i, i))
	}
	if err := os.WriteFile(rolloutPath, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatalf("write rollout: %v", err)
	}

	messages, truncated, err := readCodexTranscript(rolloutPath)
	if err != nil {
		t.Fatalf("readCodexTranscript: %v", err)
	}
	if !truncated {
		t.Fatal("expected truncation")
	}
	if len(messages) != codexMaxTranscriptMessages {
		t.Fatalf("messages = %d, want %d", len(messages), codexMaxTranscriptMessages)
	}
	if messages[0]["text"] != "message 005" {
		t.Fatalf("first message = %#v, want message 005", messages[0])
	}
	if messages[len(messages)-1]["text"] != "message 084" {
		t.Fatalf("last message = %#v, want message 084", messages[len(messages)-1])
	}
}

func TestCodexSessionIndexTitleUsesThreadName(t *testing.T) {
	indexPath := filepath.Join(t.TempDir(), "session_index.jsonl")
	rawID := "019db82f-0ff3-7fc3-9763-cebfae60b634"
	line := `{"id":"` + rawID + `","thread_name":"设计支持用户一键安装的项目安装脚本方案","updated_at":"2026-04-23T02:33:32.005719Z"}`
	if err := os.WriteFile(indexPath, []byte(line+"\n"), 0o600); err != nil {
		t.Fatalf("write index: %v", err)
	}

	titles := codexSessionIndexTitles(indexPath)
	payload := codexUsagePayload(map[string]interface{}{
		"id":          rawID,
		"title":       "现在设计这个项目的安装脚步，需要用户一键都能安装，你觉得怎么样好点",
		"tokens_used": int64(10),
	}, titles)

	if payload["title"] != "设计支持用户一键安装的项目安装脚本方案" {
		t.Fatalf("title = %#v", payload["title"])
	}
	if payload["title_source"] != "codex_session_index" {
		t.Fatalf("title_source = %#v", payload["title_source"])
	}
}

func TestCodexUsageSQLScansAllTokenRows(t *testing.T) {
	query := codexUsageSQL(map[string]bool{
		"id":          true,
		"session_id":  true,
		"updated_at":  true,
		"tokens_used": true,
		"model":       true,
	})
	if !strings.Contains(query, "from threads where") {
		t.Fatalf("query does not filter token-bearing rows: %s", query)
	}
	if !strings.Contains(query, "coalesce(\"tokens_used\",0) > 0") {
		t.Fatalf("query does not include token predicate: %s", query)
	}
	if strings.Contains(strings.ToLower(query), "limit") {
		t.Fatalf("query should scan all token rows, got: %s", query)
	}
}

func TestCommandOutputMetadataDoesNotIncludeOutput(t *testing.T) {
	payload := commandOutputMetadata("first line\nsecret second line")
	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	if strings.Contains(string(data), "secret second line") {
		t.Fatalf("payload leaked command output: %s", data)
	}
	if payload["line_count"] != 2 {
		t.Fatalf("unexpected line count: %#v", payload["line_count"])
	}
}
