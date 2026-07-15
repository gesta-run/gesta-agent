package daemon

import (
	"context"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/gesta-run/gesta-agent/pkg/util"
)

func newOutputTestRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	root := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, string(out))
		}
	}
	git("init")
	git("config", "user.email", "test@example.com")
	git("config", "user.name", "Test User")
	mustWriteFile(t, filepath.Join(root, "main.go"), "package main\n")
	git("add", ".")
	git("commit", "-m", "initial")
	return root
}

// TestClaudeOutputSummaryEventsFromBaseline verifies that an active Claude Code
// session with a hook-captured baseline emits one output.summary event carrying
// the per-kind line deltas. Without the Claude output path this returned nothing,
// so the console "Output produced" ledger stayed empty for Claude Code users.
func TestClaudeOutputSummaryEventsFromBaseline(t *testing.T) {
	root := newOutputTestRepo(t)

	const rawSessionID = "claude-session-123"
	cfg := Config{DataDir: t.TempDir()}
	// The hook captures the baseline keyed by the hashed session id; the collection
	// path must look it up with the same hash, so mirror that here.
	if err := CaptureOutputBaseline(context.Background(), cfg, root, util.ShortHash(rawSessionID)); err != nil {
		t.Fatalf("CaptureOutputBaseline: %v", err)
	}

	// Session edits after the baseline: add code, a test, and docs.
	mustWriteFile(t, filepath.Join(root, "main.go"), "package main\n\nfunc added() {}\n")
	mustWriteFile(t, filepath.Join(root, "main_test.go"), "package main\n\nfunc TestAdded(t *testing.T) {}\n")
	mustWriteFile(t, filepath.Join(root, "README.md"), "hello brave new world\n")

	sessions := []claudeSessionUsage{{
		SessionID:    rawSessionID,
		CWD:          root,
		FirstEventAt: time.Now().Add(-time.Hour).UTC(),
		Models:       []string{"claude-opus-4-8"},
		Title:        "add a function",
	}}
	active := map[string]bool{util.ShortHash(rawSessionID): true}

	events := claudeOutputSummaryEvents(context.Background(), cfg, sessions, active)
	if len(events) != 1 {
		t.Fatalf("output summary events = %d, want 1", len(events))
	}
	event := events[0]
	if event.EventType != "output.summary" {
		t.Fatalf("event type = %q, want output.summary", event.EventType)
	}
	if got := event.Payload["agent_type"]; got != "claude_code" {
		t.Fatalf("agent_type = %v, want claude_code", got)
	}
	// session_id must be the hashed id so the control plane correlates it with the
	// Claude session-index event (which also emits the hashed id).
	if got := event.Payload["session_id"]; got != util.ShortHash(rawSessionID) {
		t.Fatalf("session_id = %v, want %s", got, util.ShortHash(rawSessionID))
	}
	if event.Payload["measurement_mode"] != "session_baseline" {
		t.Fatalf("measurement_mode = %v, want session_baseline", event.Payload["measurement_mode"])
	}
	if added, _ := event.Payload["code_lines_added"].(int64); added <= 0 {
		t.Fatalf("code_lines_added = %v, want > 0", event.Payload["code_lines_added"])
	}
	if added, _ := event.Payload["test_lines_added"].(int64); added <= 0 {
		t.Fatalf("test_lines_added = %v, want > 0", event.Payload["test_lines_added"])
	}
	if added, _ := event.Payload["doc_lines_added"].(int64); added <= 0 {
		t.Fatalf("doc_lines_added = %v, want > 0", event.Payload["doc_lines_added"])
	}
}

// TestClaudeOutputSummaryEventsGatesDormantSessions covers the multi-session case:
// an active session (B) and a dormant session (A) share a git worktree. A has no
// fresh baseline, so if it were processed it would fall through to the HEAD-diff
// fallback and be credited the repo's entire current diff — double-counting B's
// output. Gating on the active set must emit exactly one event, for B only.
func TestClaudeOutputSummaryEventsGatesDormantSessions(t *testing.T) {
	root := newOutputTestRepo(t)

	const activeID = "claude-session-active"
	const dormantID = "claude-session-dormant"
	cfg := Config{DataDir: t.TempDir()}
	// Only the active session has a fresh baseline.
	if err := CaptureOutputBaseline(context.Background(), cfg, root, util.ShortHash(activeID)); err != nil {
		t.Fatalf("CaptureOutputBaseline: %v", err)
	}
	// Shared worktree change after the baseline.
	mustWriteFile(t, filepath.Join(root, "main.go"), "package main\n\nfunc added() {}\n")

	sessions := []claudeSessionUsage{
		{SessionID: dormantID, CWD: root, FirstEventAt: time.Now().Add(-72 * time.Hour).UTC()},
		{SessionID: activeID, CWD: root, FirstEventAt: time.Now().Add(-time.Minute).UTC(), Models: []string{"claude-opus-4-8"}},
	}
	// Only the active session is flagged active this cycle.
	active := map[string]bool{util.ShortHash(activeID): true}

	events := claudeOutputSummaryEvents(context.Background(), cfg, sessions, active)
	if len(events) != 1 {
		t.Fatalf("output summary events = %d, want 1 (dormant session must be skipped)", len(events))
	}
	if got := events[0].Payload["session_id"]; got != util.ShortHash(activeID) {
		t.Fatalf("session_id = %v, want the active session %s", got, util.ShortHash(activeID))
	}
}

// TestClaudeOutputSummaryEventsSkipsNonGitAndEmpty verifies active sessions that
// cannot yield output (no cwd, or a cwd outside any git repo) produce no events
// rather than erroring.
func TestClaudeOutputSummaryEventsSkipsNonGitAndEmpty(t *testing.T) {
	cfg := Config{DataDir: t.TempDir()}
	sessions := []claudeSessionUsage{
		{SessionID: "no-cwd"},
		{SessionID: "not-a-repo", CWD: t.TempDir()},
	}
	active := map[string]bool{
		util.ShortHash("no-cwd"):     true,
		util.ShortHash("not-a-repo"): true,
	}
	if events := claudeOutputSummaryEvents(context.Background(), cfg, sessions, active); len(events) != 0 {
		t.Fatalf("output summary events = %d, want 0", len(events))
	}
}
