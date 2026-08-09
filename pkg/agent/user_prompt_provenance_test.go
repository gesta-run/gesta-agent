package agent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
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

func TestVerifyUserPromptSubmissionSupportsTurnContextAndLegacyTaskStarted(t *testing.T) {
	for _, test := range []struct {
		name      string
		turnStart string
	}{
		{name: "turn context", turnStart: `{"type":"turn_context","payload":{"turn_id":"turn-real"}}`},
		{name: "legacy task started", turnStart: `{"type":"event_msg","payload":{"type":"task_started","turn_id":"turn-real"}}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			transcriptPath := writeCodexTranscript(t,
				test.turnStart,
				`{"type":"event_msg","payload":{"type":"user_message","message":"hello"}}`,
			)
			event := agentHookEvent{TranscriptPath: transcriptPath, TurnID: "turn-real"}

			if err := verifyUserPromptSubmission(context.Background(), event, "codex", "hello"); err != nil {
				t.Fatalf("verify persisted prompt: %v", err)
			}
		})
	}
}

func TestVerifyUserPromptSubmissionRejectsSyntheticPayloadForRealTurn(t *testing.T) {
	transcriptPath := writeCodexTranscript(t,
		`{"type":"event_msg","payload":{"type":"task_started","turn_id":"turn-real"}}`,
		`{"type":"event_msg","payload":{"type":"user_message","message":"real user prompt"}}`,
	)
	err := verifyUserPromptSubmission(context.Background(), agentHookEvent{
		TranscriptPath: transcriptPath,
		TurnID:         "turn-real",
	}, "codex", "synthetic payload")
	if !errors.Is(err, errCodexPromptMismatch) {
		t.Fatalf("expected prompt provenance rejection, got %v", err)
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

	state, err := codexTranscriptPromptState(transcriptPath, "turn-recent", "recent prompt")
	if err != nil {
		t.Fatalf("scan recent transcript tail: %v", err)
	}
	if !state.canonicalPromptMatched {
		t.Fatal("prompt in the recent transcript tail should be found")
	}
}

func TestVerifyUserPromptSubmissionAllowsPendingActiveTurn(t *testing.T) {
	transcriptPath := writeCodexTranscript(t,
		`{"type":"event_msg","payload":{"type":"task_started","turn_id":"turn-pending"}}`,
	)

	err := verifyUserPromptSubmission(context.Background(), agentHookEvent{
		TranscriptPath: transcriptPath,
		TurnID:         "turn-pending",
	}, "codex", "pending prompt")
	if err != nil {
		t.Fatalf("verify pending prompt: %v", err)
	}
}

func TestVerifyUserPromptSubmissionRejectsSupersededTurn(t *testing.T) {
	transcriptPath := writeCodexTranscript(t,
		`{"type":"event_msg","payload":{"type":"task_started","turn_id":"turn-old"}}`,
		`{"type":"event_msg","payload":{"type":"task_started","turn_id":"turn-current"}}`,
	)

	err := verifyUserPromptSubmission(context.Background(), agentHookEvent{
		TranscriptPath: transcriptPath,
		TurnID:         "turn-old",
	}, "codex", "stale prompt")
	if !errors.Is(err, errCodexTurnNotActive) {
		t.Fatalf("expected superseded turn rejection, got %v", err)
	}
}

func TestVerifyUserPromptSubmissionRejectsCompletedTurn(t *testing.T) {
	transcriptPath := writeCodexTranscript(t,
		`{"type":"event_msg","payload":{"type":"task_started","turn_id":"turn-complete"}}`,
		`{"type":"event_msg","payload":{"type":"user_message","message":"hello"}}`,
		`{"type":"event_msg","payload":{"type":"task_complete","turn_id":"turn-complete"}}`,
	)

	err := verifyUserPromptSubmission(context.Background(), agentHookEvent{
		TranscriptPath: transcriptPath,
		TurnID:         "turn-complete",
	}, "codex", "hello")
	if !errors.Is(err, errCodexTurnNotActive) {
		t.Fatalf("expected completed turn rejection, got %v", err)
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
