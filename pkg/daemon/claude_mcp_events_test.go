package daemon

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gesta-run/gesta-agent/pkg/model"
	"github.com/gesta-run/gesta-agent/pkg/util"
)

func claudeAssistantToolLine(t *testing.T, sessionID, messageID, callID, name, timestamp, secret string) string {
	t.Helper()
	record := map[string]interface{}{
		"type":      "assistant",
		"sessionId": sessionID,
		"cwd":       "/Users/private/customer-repo",
		"gitBranch": "main",
		"timestamp": timestamp,
		"message": map[string]interface{}{
			"id":    messageID,
			"model": "claude-opus-4-8",
			"role":  "assistant",
			"content": []interface{}{
				map[string]interface{}{"type": "text", "text": "assistant text must not enter MCP telemetry"},
				map[string]interface{}{
					"type":  "tool_use",
					"id":    callID,
					"name":  name,
					"input": map[string]interface{}{"token": secret},
				},
			},
			"usage": map[string]interface{}{
				"input_tokens":                10,
				"output_tokens":               5,
				"cache_creation_input_tokens": 0,
				"cache_read_input_tokens":     0,
			},
		},
	}
	data, err := json.Marshal(record)
	if err != nil {
		t.Fatalf("marshal tool line: %v", err)
	}
	return string(data)
}

func TestParseClaudeTranscriptExtractsMCPMetadataAndDeduplicatesStreamingChunks(t *testing.T) {
	home := t.TempDir()
	mcpLine := claudeAssistantToolLine(
		t,
		claudeSessionUUID,
		"msg_mcp",
		"toolu_secret_raw_id",
		"mcp__GitHub__search_repositories",
		"2026-07-24T08:01:00Z",
		"secret-tool-input",
	)
	nonMCPLine := claudeAssistantToolLine(
		t,
		claudeSessionUUID,
		"msg_shell",
		"toolu_shell",
		"Bash",
		"2026-07-24T08:02:00Z",
		"secret-shell-input",
	)
	path := writeClaudeTranscript(t, home, claudeSessionUUID, []string{
		mcpLine,
		mcpLine,
		nonMCPLine,
		`{"type":"user","sessionId":"` + claudeSessionUUID + `","timestamp":"2026-07-24T08:03:00Z","message":{"content":[{"type":"tool_result","tool_use_id":"toolu_secret_raw_id","content":"secret-tool-result"}]}}`,
	})

	session, ok := parseClaudeTranscript(path)
	if !ok {
		t.Fatal("expected transcript to parse")
	}
	if len(session.MCPToolCalls) != 1 {
		t.Fatalf("MCP calls = %#v, want one de-duplicated call", session.MCPToolCalls)
	}
	call := session.MCPToolCalls[0]
	if call.MCPServer != "github" || call.MCPTool != "search_repositories" {
		t.Fatalf("MCP call = %#v", call)
	}
}

func TestClaudeMCPToolCallEventsAreMetadataOnlyAndDeterministic(t *testing.T) {
	cfg := NewDirectRuntimeConfig("http://127.0.0.1:1", "dtok_claude_mcp")
	cfg.DataDir = t.TempDir()
	session := claudeSessionUsage{
		SessionID: "/raw/private/session-id",
		MCPToolCalls: []claudeTranscriptToolCall{{
			Name:       "mcp__github__search_repositories",
			CallID:     "toolu_raw_secret_id",
			Timestamp:  "2026-07-24T08:01:00Z",
			MCPServer:  "github",
			MCPTool:    "search_repositories",
			BlockIndex: 1,
		}},
	}
	events := claudeMCPToolCallEvents(cfg, session)
	if len(events) != 1 {
		t.Fatalf("events = %#v, want one", events)
	}
	event := events[0]
	if event.EventType != "tool.call" || event.Source != "claude_code" || event.AgentType != "claude_code" {
		t.Fatalf("event envelope = %#v", event)
	}
	if event.Payload["tool_type"] != "mcp" ||
		event.Payload["mcp_server"] != "github" ||
		event.Payload["mcp_tool"] != "search_repositories" {
		t.Fatalf("event payload = %#v", event.Payload)
	}
	if event.Payload["session_id"] != util.ShortHash(session.SessionID) ||
		event.Payload["call_id_hash"] != util.ShortHash("toolu_raw_secret_id") {
		t.Fatalf("event identifiers are not hashed: %#v", event.Payload)
	}
	if event.EventID != claudeMCPToolCallEvents(cfg, session)[0].EventID {
		t.Fatal("event ID should be deterministic")
	}
	relocated := session
	relocated.MCPToolCalls = []claudeTranscriptToolCall{{
		Name:       "mcp__renamed__different_tool",
		CallID:     "toolu_raw_secret_id",
		Timestamp:  "2026-07-24T09:01:00Z",
		MCPServer:  "renamed",
		MCPTool:    "different_tool",
		BlockIndex: 99,
	}}
	if relocatedEventID := claudeMCPToolCallEvents(cfg, relocated)[0].EventID; relocatedEventID != event.EventID {
		t.Fatalf("stable call ID produced %q after transcript relocation, want %q", relocatedEventID, event.EventID)
	}

	serialized, err := json.Marshal(event)
	if err != nil {
		t.Fatalf("marshal event: %v", err)
	}
	text := string(serialized)
	for _, forbidden := range []string{
		session.SessionID,
		"toolu_raw_secret_id",
		"secret-tool-input",
		"secret-tool-result",
		"/Users/private",
	} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("event leaked %q: %s", forbidden, text)
		}
	}
}

func TestClaudeMCPEventsRespectInitialBaselineCutoff(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	cfg := Config{DataDir: t.TempDir()}
	projectsDir := filepath.Join(home, ".claude", "projects")
	baselineAt := time.Date(2026, 7, 24, 8, 0, 0, 0, time.UTC)

	historical := claudeAssistantToolLine(
		t,
		claudeSessionUUID,
		"msg_historical",
		"toolu_historical",
		"mcp__github__old_call",
		"2026-07-24T07:00:00Z",
		"historical-secret",
	)
	writeClaudeTranscript(t, home, claudeSessionUUID, []string{historical})
	first := claudeUsageEvents(cfg, projectsDir, baselineAt)
	if countEventsByType(first, "tool.call") != 0 {
		t.Fatalf("baseline cycle must not backfill historical calls: %#v", first)
	}

	current := claudeAssistantToolLine(
		t,
		claudeSessionUUID,
		"msg_current",
		"toolu_current",
		"mcp__notion__search",
		"2026-07-24T08:01:00Z",
		"current-secret",
	)
	writeClaudeTranscript(t, home, claudeSessionUUID, []string{historical, current})
	second := claudeUsageEvents(cfg, projectsDir, baselineAt.Add(2*time.Minute))
	if countEventsByType(second, "tool.call") != 1 {
		t.Fatalf("post-baseline cycle should emit only the new call: %#v", second)
	}
	for _, event := range second {
		if event.EventType == "tool.call" && event.Payload["mcp_server"] != "notion" {
			t.Fatalf("historical call escaped cutoff: %#v", event)
		}
	}

	third := claudeUsageEvents(cfg, projectsDir, baselineAt.Add(3*time.Minute))
	if countEventsByType(third, "tool.call") != 0 {
		t.Fatalf("unchanged transcript should not emit MCP calls: %#v", third)
	}
}

func TestMergeClaudeMCPToolCallsDeduplicatesResumedSession(t *testing.T) {
	call := claudeTranscriptToolCall{
		Name:      "mcp__github__search",
		CallID:    "toolu_shared",
		Timestamp: "2026-07-24T08:01:00Z",
		MCPServer: "github",
		MCPTool:   "search",
	}
	merged := mergeClaudeSessionsByID([]claudeSessionUsage{
		{SessionID: "session", ByModelDay: map[claudeModelDayKey]claudeAssistantUsage{}, MCPToolCalls: []claudeTranscriptToolCall{call}},
		{SessionID: "session", ByModelDay: map[claudeModelDayKey]claudeAssistantUsage{}, MCPToolCalls: []claudeTranscriptToolCall{call}},
	})
	if len(merged) != 1 || len(merged[0].MCPToolCalls) != 1 {
		t.Fatalf("merged sessions = %#v", merged)
	}
}

func countEventsByType(events []model.EventEnvelope, eventType string) int {
	count := 0
	for _, event := range events {
		if event.EventType == eventType {
			count++
		}
	}
	return count
}
