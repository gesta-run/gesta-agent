package daemon

import "testing"

func TestParseClaudeTranscriptMapsSummaryPhasesAndPrefersFinalDuplicate(t *testing.T) {
	home := t.TempDir()
	path := writeClaudeTranscript(t, home, claudeSessionUUID, []string{
		claudeUserLine("do the work"),
		claudeAssistantTextLineWithPhase("msg_progress", "claude-opus-4-8", "2026-06-20T10:01:00.000Z", "working", "tool_use"),
		claudeAssistantTextLineWithPhase("msg_final", "claude-opus-4-8", "2026-06-20T10:02:00.000Z", "long partial response", ""),
		claudeAssistantTextLineWithPhase("msg_final", "claude-opus-4-8", "2026-06-20T10:02:01.000Z", "done", "end_turn"),
	})

	session, ok := parseClaudeTranscript(path)
	if !ok {
		t.Fatal("expected transcript to parse")
	}
	if len(session.Messages) != 3 {
		t.Fatalf("messages = %#v, want user, progress, and final", session.Messages)
	}
	if got := firstString(session.Messages[1], "summary_phase"); got != transcriptSummaryPhaseProgress {
		t.Fatalf("progress phase = %q, want progress", got)
	}
	if got := firstString(session.Messages[2], "summary_phase"); got != transcriptSummaryPhaseFinal {
		t.Fatalf("final phase = %q, want final", got)
	}
	if got := firstString(session.Messages[2], "text"); got != "done" {
		t.Fatalf("final duplicate text = %q, want done", got)
	}
}

func TestParseClaudeTranscriptMapsStopSequenceToFinal(t *testing.T) {
	home := t.TempDir()
	path := writeClaudeTranscript(t, home, claudeSessionUUID, []string{
		claudeUserLine("do the work"),
		claudeAssistantTextLineWithPhase("msg_final", "claude-opus-4-8", "2026-06-20T10:01:00.000Z", "done", "stop_sequence"),
		claudeSyntheticTextLineWithID("synthetic_notice", "2026-06-20T10:02:00.000Z", "Session notice."),
	})

	session, ok := parseClaudeTranscript(path)
	if !ok {
		t.Fatal("expected transcript to parse")
	}
	if got := firstString(session.Messages[1], "summary_phase"); got != transcriptSummaryPhaseFinal {
		t.Fatalf("stop_sequence phase = %q, want final", got)
	}
}

func claudeAssistantTextLineWithPhase(id, model, ts, text, stopReason string) string {
	return `{"type":"assistant","sessionId":"` + claudeSessionUUID + `","cwd":"/Users/test/repo","gitBranch":"main","timestamp":"` + ts + `","message":{"id":"` + id + `","model":"` + model + `","role":"assistant","stop_reason":"` + stopReason + `","content":[{"type":"text","text":` + jsonString(text) + `}]}}`
}
