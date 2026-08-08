package daemon

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/gesta-run/gesta-agent/pkg/model"
)

func TestClaudeTurnUsageGroupsToolLoop(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	cfg := Config{DataDir: t.TempDir(), DaemonID: "daemon"}
	initializedAt := time.Date(2026, 8, 4, 9, 0, 0, 0, time.UTC)
	_, commit := claudeEventsFromSessions(cfg, nil, initializedAt)
	if commit == nil || commit() != nil {
		t.Fatal("initialize Claude cursors")
	}

	path := writeClaudeTranscript(t, home, claudeSessionUUID, []string{
		`{"type":"user","uuid":"user-turn-1","sessionId":"` + claudeSessionUUID + `","cwd":"/Users/test/repo","timestamp":"2026-08-04T10:00:00Z","message":{"role":"user","content":"deploy the service to production"}}`,
		`{"type":"assistant","sessionId":"` + claudeSessionUUID + `","cwd":"/Users/test/repo","timestamp":"2026-08-04T10:01:00Z","message":{"id":"msg-tool","model":"claude-opus-4-1","role":"assistant","stop_reason":"tool_use","content":[{"type":"tool_use","name":"Bash","input":{"command":"kubectl apply -f app.yaml"}}],"usage":{"input_tokens":10,"output_tokens":5,"cache_creation_input_tokens":3,"cache_read_input_tokens":2}}}`,
		`{"type":"user","sessionId":"` + claudeSessionUUID + `","timestamp":"2026-08-04T10:01:30Z","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"tool-1","content":"ok"}]}}`,
		`{"type":"assistant","sessionId":"` + claudeSessionUUID + `","cwd":"/Users/test/repo","timestamp":"2026-08-04T10:02:00Z","message":{"id":"msg-final","model":"claude-opus-4-1","role":"assistant","stop_reason":"end_turn","content":[{"type":"text","text":"deployment complete"}],"usage":{"input_tokens":4,"output_tokens":6,"cache_creation_input_tokens":0,"cache_read_input_tokens":1}}}`,
	})
	session, ok := parseClaudeTranscript(path)
	if !ok || len(session.Turns) != 1 {
		t.Fatalf("turns=%d parsed=%v, want one grouped turn", len(session.Turns), ok)
	}
	if session.Turns[0].Usage.TotalTokens() != 31 {
		t.Fatalf("turn tokens=%d, want 31", session.Turns[0].Usage.TotalTokens())
	}
	if session.Turns[0].Status != "completed" {
		t.Fatalf("turn status=%q, want completed", session.Turns[0].Status)
	}

	events, commit := claudeEventsFromSessions(cfg, []claudeSessionUsage{session}, initializedAt.Add(2*time.Hour))
	var usageEvents []model.EventEnvelope
	for _, event := range events {
		if event.EventType == "turn.usage" {
			usageEvents = append(usageEvents, event)
		}
	}
	if len(usageEvents) != 1 {
		t.Fatalf("turn usage events=%d, want one", len(usageEvents))
	}
	payload := usageEvents[0].Payload
	if payload["work_type"] != "SRE" || payloadInt(payload, "total_tokens") != 31 {
		t.Fatalf("payload=%#v", payload)
	}
	encoded := string(mustJSON(payload))
	if strings.Contains(encoded, "kubectl") || strings.Contains(encoded, "production") {
		t.Fatalf("turn event leaked classifier evidence: %s", encoded)
	}
	if commit == nil || commit() != nil {
		t.Fatal("commit Claude turn cursor")
	}
}

func TestClaudeTurnUsageIncludesMetadataAndSidechainsExactlyOnce(t *testing.T) {
	home := t.TempDir()
	path := writeClaudeTranscript(t, home, claudeSessionUUID, []string{
		`{"type":"user","uuid":"meta","isMeta":true,"sessionId":"` + claudeSessionUUID + `","timestamp":"2026-08-04T10:00:00Z","message":{"content":"generated metadata"}}`,
		`{"type":"user","uuid":"human","sessionId":"` + claudeSessionUUID + `","timestamp":"2026-08-04T10:02:00Z","message":{"content":"write documentation"}}`,
		`{"type":"assistant","isMeta":true,"sessionId":"` + claudeSessionUUID + `","timestamp":"2026-08-04T10:02:00.100Z","message":{"id":"meta-response","model":"claude-opus-4-1","stop_reason":"end_turn","usage":{"input_tokens":10,"output_tokens":2}}}`,
		`{"type":"user","uuid":"sidechain","isSidechain":true,"sessionId":"` + claudeSessionUUID + `","timestamp":"2026-08-04T10:02:00.200Z","message":{"content":"delegate internal work"}}`,
		`{"type":"assistant","isSidechain":true,"sessionId":"` + claudeSessionUUID + `","timestamp":"2026-08-04T10:02:00.300Z","message":{"id":"sidechain-response","model":"claude-opus-4-1","stop_reason":"end_turn","usage":{"input_tokens":20,"output_tokens":4}}}`,
		`{"type":"assistant","sessionId":"` + claudeSessionUUID + `","timestamp":"2026-08-04T10:02:01Z","message":{"id":"human-response","model":"claude-opus-4-1","stop_reason":"end_turn","usage":{"input_tokens":5,"output_tokens":3}}}`,
	})
	session, ok := parseClaudeTranscript(path)
	if !ok || len(session.Turns) != 3 {
		t.Fatalf("turns=%d parsed=%v, want metadata, sidechain, and human turns", len(session.Turns), ok)
	}
	want := map[string]int64{
		"internal:meta-response":      12,
		"internal:sidechain-response": 24,
		"human":                       8,
	}
	var total int64
	for _, turn := range session.Turns {
		tokens, exists := want[turn.TurnID]
		if !exists || turn.Usage.TotalTokens() != tokens {
			t.Fatalf("unexpected turn=%+v", turn)
		}
		total += turn.Usage.TotalTokens()
	}
	if total != session.Total.TotalTokens() || total != 44 {
		t.Fatalf("turn total=%d session total=%d, want 44", total, session.Total.TotalTokens())
	}
}

func TestClaudeTurnUsageMarksInterruptedToolLoopAborted(t *testing.T) {
	home := t.TempDir()
	path := writeClaudeTranscript(t, home, claudeSessionUUID, []string{
		`{"type":"user","uuid":"first","sessionId":"` + claudeSessionUUID + `","timestamp":"2026-08-04T10:00:00Z","message":{"content":"deploy the service"}}`,
		`{"type":"assistant","sessionId":"` + claudeSessionUUID + `","timestamp":"2026-08-04T10:00:01Z","message":{"id":"first-tool","model":"claude-opus-4-1","stop_reason":"tool_use","content":[{"type":"tool_use","name":"Bash","input":{"command":"kubectl apply"}}],"usage":{"input_tokens":10,"output_tokens":2}}}`,
		`{"type":"user","uuid":"second","sessionId":"` + claudeSessionUUID + `","timestamp":"2026-08-04T10:01:00Z","message":{"content":"write the runbook instead"}}`,
		`{"type":"assistant","sessionId":"` + claudeSessionUUID + `","timestamp":"2026-08-04T10:01:01Z","message":{"id":"second-final","model":"claude-opus-4-1","stop_reason":"end_turn","usage":{"input_tokens":4,"output_tokens":3}}}`,
	})
	session, ok := parseClaudeTranscript(path)
	if !ok || len(session.Turns) != 2 {
		t.Fatalf("turns=%d parsed=%v, want interrupted and completed turns", len(session.Turns), ok)
	}
	if session.Turns[0].Status != "aborted" || session.Turns[0].Usage.TotalTokens() != 12 {
		t.Fatalf("first turn=%+v, want aborted with 12 tokens", session.Turns[0])
	}
	if session.Turns[1].Status != "completed" || session.Turns[1].Usage.TotalTokens() != 7 {
		t.Fatalf("second turn=%+v, want completed with 7 tokens", session.Turns[1])
	}
}

func mustJSON(value interface{}) []byte {
	data, _ := json.Marshal(value)
	return data
}
