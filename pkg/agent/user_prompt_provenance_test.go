package agent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestVerifyUserPromptSubmissionMatchesPersistedCodexMessage(t *testing.T) {
	transcriptPath := writeCodexTranscript(t,
		`{"type":"event_msg","payload":{"type":"task_started","turn_id":"turn-real"}}`,
		`{"type":"event_msg","payload":{"type":"user_message","message":"hello"}}`,
	)
	event := agentHookEvent{TranscriptPath: transcriptPath, TurnID: "turn-real"}

	if err := verifyUserPromptSubmission(context.Background(), event, "codex", "hello"); err != nil {
		t.Fatalf("verify persisted prompt: %v", err)
	}
}

func TestVerifyUserPromptSubmissionRejectsSyntheticPayloadForRealTurn(t *testing.T) {
	transcriptPath := writeCodexTranscript(t,
		`{"type":"event_msg","payload":{"type":"task_started","turn_id":"turn-real"}}`,
		`{"type":"event_msg","payload":{"type":"user_message","message":"real user prompt"}}`,
	)
	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()

	err := verifyUserPromptSubmission(ctx, agentHookEvent{
		TranscriptPath: transcriptPath,
		TurnID:         "turn-real",
	}, "codex", "synthetic payload")
	if !errors.Is(err, errCodexPromptNotPersisted) {
		t.Fatalf("expected prompt provenance rejection, got %v", err)
	}
}

func TestVerifyUserPromptSubmissionUsesTurnMetadata(t *testing.T) {
	transcriptPath := writeCodexTranscript(t,
		`{"type":"event_msg","payload":{"type":"task_started","turn_id":"turn-real"}}`,
		`{"type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"hello"}],"internal_chat_message_metadata_passthrough":{"turn_id":"turn-other"}}}`,
		`{"type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"hello"}],"internal_chat_message_metadata_passthrough":{"turn_id":"turn-real"}}}`,
	)

	matched, err := codexTranscriptHasUserPrompt(transcriptPath, "turn-real", "hello")
	if err != nil {
		t.Fatalf("scan transcript: %v", err)
	}
	if !matched {
		t.Fatal("message carrying the target turn metadata should match")
	}
}

func TestVerifyUserPromptSubmissionRequiresCodexProvenance(t *testing.T) {
	if err := verifyUserPromptSubmission(context.Background(), agentHookEvent{}, "codex", "hello"); err == nil {
		t.Fatal("Codex events without provenance should be rejected")
	}
	if err := verifyUserPromptSubmission(context.Background(), agentHookEvent{}, "claude_code", "hello"); err != nil {
		t.Fatalf("Claude events should not use Codex transcript validation: %v", err)
	}
}

func TestCodexPromptLookupUsesRecentTail(t *testing.T) {
	transcriptPath := filepath.Join(t.TempDir(), "rollout.jsonl")
	prefix := strings.Repeat("x", int(codexPromptProvenanceTailBytes)+1024) + "\n"
	records := strings.Join([]string{
		`{"type":"event_msg","payload":{"type":"task_started","turn_id":"turn-recent"}}`,
		`{"type":"event_msg","payload":{"type":"user_message","message":"recent prompt"}}`,
	}, "\n") + "\n"
	if err := os.WriteFile(transcriptPath, []byte(prefix+records), 0o600); err != nil {
		t.Fatalf("write transcript: %v", err)
	}

	matched, err := codexTranscriptHasUserPrompt(transcriptPath, "turn-recent", "recent prompt")
	if err != nil {
		t.Fatalf("scan recent transcript tail: %v", err)
	}
	if !matched {
		t.Fatal("prompt in the recent transcript tail should be found")
	}
}

func TestVerifyUserPromptSubmissionWaitsForDelayedTranscriptWrite(t *testing.T) {
	transcriptPath := writeCodexTranscript(t,
		`{"type":"event_msg","payload":{"type":"task_started","turn_id":"turn-delayed"}}`,
	)
	done := make(chan struct{})
	go func() {
		defer close(done)
		time.Sleep(75 * time.Millisecond)
		file, err := os.OpenFile(transcriptPath, os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			return
		}
		defer file.Close()
		_, _ = file.WriteString(`{"type":"event_msg","payload":{"type":"user_message","message":"delayed prompt"}}` + "\n")
	}()

	err := verifyUserPromptSubmission(context.Background(), agentHookEvent{
		TranscriptPath: transcriptPath,
		TurnID:         "turn-delayed",
	}, "codex", "delayed prompt")
	<-done
	if err != nil {
		t.Fatalf("verify delayed prompt: %v", err)
	}
}

func writeCodexTranscript(t *testing.T, records ...string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "rollout.jsonl")
	if err := os.WriteFile(path, []byte(strings.Join(records, "\n")+"\n"), 0o600); err != nil {
		t.Fatalf("write transcript: %v", err)
	}
	return path
}
