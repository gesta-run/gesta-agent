package daemon

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/gesta-run/gesta-agent/pkg/model"
	"github.com/gesta-run/gesta-agent/pkg/util"
)

func TestGitOutputSummaryCountsTrackedAndUntrackedFiles(t *testing.T) {
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
	mustWriteFile(t, filepath.Join(root, "main.go"), "package main\n\nfunc main() {}\n")
	mustWriteFile(t, filepath.Join(root, "README.md"), "hello world\n")
	git("add", ".")
	git("commit", "-m", "initial")

	mustWriteFile(t, filepath.Join(root, "main.go"), "package main\n\nfunc main() {}\nfunc added() {}\n")
	mustWriteFile(t, filepath.Join(root, "main_test.go"), "package main\n\nfunc TestAdded(t *testing.T) {}\n")
	mustWriteFile(t, filepath.Join(root, "README.md"), "hello brave new world\n")
	mustWriteFile(t, filepath.Join(root, "docs.md"), "one two three\nfour five\n")

	summary, ok := gitOutputSummary(context.Background(), root, "sess_output", time.Time{})
	if !ok {
		t.Fatal("gitOutputSummary returned no summary")
	}
	if summary.FilesChanged != 4 {
		t.Fatalf("files changed = %d, want 4", summary.FilesChanged)
	}
	if summary.CodeLinesAdded == 0 {
		t.Fatalf("code lines added = %d, want > 0", summary.CodeLinesAdded)
	}
	if summary.TestLinesAdded == 0 {
		t.Fatalf("test lines added = %d, want > 0", summary.TestLinesAdded)
	}
	if summary.DocWordsAdded < 5 {
		t.Fatalf("doc words added = %d, want at least 5", summary.DocWordsAdded)
	}
	if summary.DiffHash == "" {
		t.Fatal("diff hash is empty")
	}
}

func TestGitOutputSummaryUsesSessionBaselineAcrossCommit(t *testing.T) {
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
	before := gitHead(context.Background(), root)

	cfg := Config{DataDir: t.TempDir()}
	if err := CaptureOutputBaseline(context.Background(), cfg, root, "sess_output"); err != nil {
		t.Fatalf("CaptureOutputBaseline: %v", err)
	}
	mustWriteFile(t, filepath.Join(root, "main.go"), "package main\n\nfunc added() {}\n")
	git("add", ".")
	git("commit", "-m", "session change")
	after := gitHead(context.Background(), root)

	summary, ok := gitOutputSummaryWithConfig(context.Background(), cfg, root, "sess_output")
	if !ok {
		t.Fatal("gitOutputSummaryWithConfig returned no summary")
	}
	if summary.MeasurementMode != "session_baseline" {
		t.Fatalf("measurement mode = %q, want session_baseline", summary.MeasurementMode)
	}
	if summary.GitSHABefore != before || summary.GitSHAAfter != after {
		t.Fatalf("git sha range = %q..%q, want %q..%q", summary.GitSHABefore, summary.GitSHAAfter, before, after)
	}
	if summary.FilesChanged != 1 || summary.CodeLinesAdded != 2 || summary.CodeLinesDeleted != 0 {
		t.Fatalf("summary = %+v, want 1 file and 2 added code lines", summary)
	}
}

func TestGitOutputSummaryBaselineExcludesPreexistingDirtyWork(t *testing.T) {
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
	mustWriteFile(t, filepath.Join(root, "old.go"), "package main\n")
	mustWriteFile(t, filepath.Join(root, "new.go"), "package main\n")
	git("add", ".")
	git("commit", "-m", "initial")

	mustWriteFile(t, filepath.Join(root, "old.go"), "package main\n\nfunc existingDirtyChange() {}\n")
	cfg := Config{DataDir: t.TempDir()}
	if err := CaptureOutputBaseline(context.Background(), cfg, root, "sess_output"); err != nil {
		t.Fatalf("CaptureOutputBaseline: %v", err)
	}
	mustWriteFile(t, filepath.Join(root, "new.go"), "package main\n\nfunc sessionChange() {}\n")

	summary, ok := gitOutputSummaryWithConfig(context.Background(), cfg, root, "sess_output")
	if !ok {
		t.Fatal("gitOutputSummaryWithConfig returned no summary")
	}
	if summary.FilesChanged != 1 || summary.CodeLinesAdded != 2 {
		t.Fatalf("summary = %+v, want only the post-baseline file change", summary)
	}
}

func TestGitOutputSummaryBaselineNoChangeDoesNotFallbackToHeadDiff(t *testing.T) {
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
	mustWriteFile(t, filepath.Join(root, "old.go"), "package main\n")
	git("add", ".")
	git("commit", "-m", "initial")

	mustWriteFile(t, filepath.Join(root, "old.go"), "package main\n\nfunc existingDirtyChange() {}\n")
	cfg := Config{DataDir: t.TempDir()}
	if err := CaptureOutputBaseline(context.Background(), cfg, root, "sess_output"); err != nil {
		t.Fatalf("CaptureOutputBaseline: %v", err)
	}

	if summary, ok := gitOutputSummaryWithConfig(context.Background(), cfg, root, "sess_output"); ok {
		t.Fatalf("summary = %+v, want no output when baseline has no changes", summary)
	}
}

func TestLoadOutputBaselineIgnoresExpiredSnapshots(t *testing.T) {
	cfg := Config{DataDir: t.TempDir()}
	store := outputBaselineStore{Sessions: map[string]outputBaseline{
		outputCursorKey("sess_output", "repo_hash"): {
			SessionID:    "sess_output",
			RepoHash:     "repo_hash",
			GitSHABefore: "abc123",
			CapturedAt:   time.Now().Add(-(outputBaselineTTL + time.Hour)).UTC().Format(time.RFC3339Nano),
			Files:        map[string]outputFileSnapshot{},
		},
	}}
	if err := saveOutputBaselineStore(cfg.DataDir, store); err != nil {
		t.Fatalf("saveOutputBaselineStore: %v", err)
	}
	if baseline, ok := loadOutputBaseline(cfg.DataDir, "sess_output", "repo_hash"); ok {
		t.Fatalf("loaded expired baseline = %+v, want none", baseline)
	}
}

func TestGitOutputSummarySkipsFilesOlderThanSessionStart(t *testing.T) {
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
	mustWriteFile(t, filepath.Join(root, "old.go"), "package main\n")
	mustWriteFile(t, filepath.Join(root, "new.go"), "package main\n")
	git("add", ".")
	git("commit", "-m", "initial")

	oldTime := time.Now().Add(-2 * time.Hour)
	mustWriteFile(t, filepath.Join(root, "old.go"), "package main\nfunc oldChange() {}\n")
	if err := os.Chtimes(filepath.Join(root, "old.go"), oldTime, oldTime); err != nil {
		t.Fatalf("chtimes old.go: %v", err)
	}
	sessionStart := time.Now().Add(-time.Hour)
	mustWriteFile(t, filepath.Join(root, "new.go"), "package main\nfunc newChange() {}\n")

	summary, ok := gitOutputSummary(context.Background(), root, "sess_output", sessionStart)
	if !ok {
		t.Fatal("gitOutputSummary returned no summary")
	}
	if summary.FilesChanged != 1 {
		t.Fatalf("files changed = %d, want 1", summary.FilesChanged)
	}
}

func TestOutputSummaryEventSeedsMissingBaselineWithoutAttributingExistingWork(t *testing.T) {
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

	// This represents work completed before Gesta first observes a still-active
	// session. It must not be credited to the session without a start baseline.
	mustWriteFile(t, filepath.Join(root, "main.go"), "package main\n\nfunc existing() {}\n")
	cfg := Config{DataDir: t.TempDir()}
	if event, ok := outputSummaryEvent(context.Background(), cfg, "codex", "sess_output", root, time.Now().Add(-time.Hour), "", ""); ok {
		t.Fatalf("event = %+v, want no unbased output", event)
	}

	resolvedRoot, ok := gitRepoRoot(context.Background(), root)
	if !ok {
		t.Fatal("resolve repository root")
	}
	if _, ok := loadOutputBaseline(cfg.DataDir, "sess_output", util.ShortHash(resolvedRoot)); !ok {
		t.Fatal("missing baseline seeded for active session")
	}

	mustWriteFile(t, filepath.Join(root, "main.go"), "package main\n\nfunc existing() {}\n\nfunc tracked() {}\n")
	event, ok := outputSummaryEvent(context.Background(), cfg, "codex", "sess_output", root, time.Now().Add(-time.Hour), "", "")
	if !ok {
		t.Fatal("output summary event missing after a baseline-backed change")
	}
	if got, _ := event.Payload["code_lines_added"].(int64); got != 2 {
		t.Fatalf("code lines added = %d, want 2", got)
	}
}

func TestFilterOutputSummaryEventsCommitsCursorAfterQueue(t *testing.T) {
	cfg := Config{DataDir: t.TempDir()}
	event := model.EventEnvelope{
		EventID:   "evt_output",
		EventType: "output.summary",
		Payload: map[string]interface{}{
			"session_id": "sess",
			"repo":       "repo_hash",
			"diff_hash":  "diff_a",
		},
	}

	filtered, commit, err := FilterOutputSummaryEvents(cfg, []model.EventEnvelope{event})
	if err != nil {
		t.Fatalf("FilterOutputSummaryEvents first: %v", err)
	}
	if len(filtered) != 1 {
		t.Fatalf("filtered first = %d, want 1", len(filtered))
	}

	filtered, _, err = FilterOutputSummaryEvents(cfg, []model.EventEnvelope{event})
	if err != nil {
		t.Fatalf("FilterOutputSummaryEvents before commit: %v", err)
	}
	if len(filtered) != 1 {
		t.Fatalf("filtered before commit = %d, want 1", len(filtered))
	}
	if err := commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}
	filtered, _, err = FilterOutputSummaryEvents(cfg, []model.EventEnvelope{event})
	if err != nil {
		t.Fatalf("FilterOutputSummaryEvents after commit: %v", err)
	}
	if len(filtered) != 0 {
		t.Fatalf("filtered after commit = %d, want 0", len(filtered))
	}
}

func mustWriteFile(t *testing.T, path, data string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
