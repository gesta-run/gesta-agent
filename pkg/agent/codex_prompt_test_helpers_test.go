package agent

import (
	"encoding/json"
	"fmt"
	"testing"
)

func addCodexPromptProvenance(t *testing.T, event *agentHookEvent) {
	t.Helper()
	if event.TurnID == "" {
		event.TurnID = "turn-test"
	}
	event.TranscriptPath = writeCodexTranscript(t,
		fmt.Sprintf(`{"type":"event_msg","payload":{"type":"task_started","turn_id":%q}}`, event.TurnID),
		fmt.Sprintf(`{"type":"event_msg","payload":{"type":"user_message","message":%q}}`, event.Prompt),
	)
}

func marshalVerifiedCodexPrompt(t *testing.T, event agentHookEvent) []byte {
	t.Helper()
	addCodexPromptProvenance(t, &event)
	data, err := json.Marshal(event)
	if err != nil {
		t.Fatalf("marshal verified Codex prompt: %v", err)
	}
	return data
}
