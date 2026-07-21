package daemon

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestRepoDiscoveryObserveCwdRegistersRepoRoot(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	repo := t.TempDir()
	dataDir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, string(out))
		}
	}
	run("init", "--initial-branch=main")
	sub := filepath.Join(repo, "pkg", "deep")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}

	d := newRepoDiscovery()
	d.observeCwd(context.Background(), dataDir, sub)

	repos := registeredCommitScanRepos(dataDir)
	if len(repos) != 1 {
		t.Fatalf("registered %d repos, want 1: %v", len(repos), repos)
	}
	// git prints the symlink-resolved root (macOS TempDir lives behind
	// /var → /private/var), so compare resolved paths.
	want, err := filepath.EvalSymlinks(repo)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := filepath.EvalSymlinks(repos[0]); err != nil || got != want {
		t.Fatalf("registered root = %q (%v), want %q", repos[0], err, want)
	}

	// Observing the same cwd again stays registered exactly once.
	d.observeCwd(context.Background(), dataDir, sub)
	if repos := registeredCommitScanRepos(dataDir); len(repos) != 1 {
		t.Fatalf("re-observe registered %d repos, want 1", len(repos))
	}
}

func TestRepoDiscoveryIgnoresNonGitCwd(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	dataDir := t.TempDir()
	d := newRepoDiscovery()
	d.observeCwd(context.Background(), dataDir, t.TempDir())
	d.observeCwd(context.Background(), dataDir, "")
	if repos := registeredCommitScanRepos(dataDir); len(repos) != 0 {
		t.Fatalf("non-git cwd registered %d repos, want 0: %v", len(repos), repos)
	}
}

func TestCodexRolloutCwd(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rollout.jsonl")
	lines := []string{
		`not json at all`,
		`{"timestamp":"t","type":"session_meta","payload":{"id":"s1"}}`,
		`{"timestamp":"t","type":"turn_context","payload":{"cwd":"/work/repo"}}`,
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := codexRolloutCwd(path); got != "/work/repo" {
		t.Fatalf("codexRolloutCwd = %q, want /work/repo", got)
	}
	if got := codexRolloutCwd(filepath.Join(t.TempDir(), "missing.jsonl")); got != "" {
		t.Fatalf("missing file returned %q, want empty", got)
	}
}

func TestCodexRolloutCwdBoundsTheProbe(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rollout.jsonl")
	var lines []string
	for i := 0; i < codexRolloutCwdMaxRecords; i++ {
		lines = append(lines, fmt.Sprintf(`{"type":"response_item","payload":{"seq":%d}}`, i))
	}
	lines = append(lines, `{"type":"turn_context","payload":{"cwd":"/too/late"}}`)
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := codexRolloutCwd(path); got != "" {
		t.Fatalf("probe read past its bound: %q", got)
	}
}

func TestRepoDiscoveryObserveRolloutRegistersRepo(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	repo := t.TempDir()
	dataDir := t.TempDir()
	cmd := exec.Command("git", "-C", repo, "init", "--initial-branch=main")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, string(out))
	}
	rollout := filepath.Join(t.TempDir(), "rollout.jsonl")
	record := fmt.Sprintf(`{"type":"session_meta","payload":{"cwd":%q}}`, repo)
	if err := os.WriteFile(rollout, []byte(record+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	d := newRepoDiscovery()
	d.observeRollout(context.Background(), dataDir, rollout)

	repos := registeredCommitScanRepos(dataDir)
	if len(repos) != 1 {
		t.Fatalf("registered %d repos, want 1: %v", len(repos), repos)
	}

	// The rollout file is probed once per process.
	d.observeRollout(context.Background(), dataDir, rollout)
	if repos := registeredCommitScanRepos(dataDir); len(repos) != 1 {
		t.Fatalf("re-observe registered %d repos, want 1", len(repos))
	}
}

func TestGitCommitsAdapterWarnsOnShallowClone(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	origin := t.TempDir()
	seed := t.TempDir()
	dataDir := t.TempDir()
	run := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, string(out))
		}
	}
	run(origin, "init", "--bare", "--initial-branch=main")
	run(seed, "init", "--initial-branch=main")
	run(seed, "config", "user.email", "jjk@cloudpilot.ai")
	run(seed, "config", "user.name", "JJK")
	run(seed, "remote", "add", "origin", "file://"+origin)
	mustWriteFile(t, filepath.Join(seed, "a.go"), "package a\n")
	run(seed, "add", ".")
	run(seed, "commit", "-m", "first")
	mustWriteFile(t, filepath.Join(seed, "b.go"), "package a\n\nfunc B() {}\n")
	run(seed, "add", ".")
	run(seed, "commit", "-m", "second")
	run(seed, "push", "origin", "main")

	shallow := filepath.Join(t.TempDir(), "clone")
	clone := exec.Command("git", "clone", "--depth", "1", "file://"+origin, shallow)
	if out, err := clone.CombinedOutput(); err != nil {
		t.Fatalf("git clone --depth 1: %v\n%s", err, string(out))
	}
	run(shallow, "config", "remote.origin.url", "git@example.com:team/shallow.git")

	registerCommitScanRepo(dataDir, shallow)
	cfg := Config{DataDir: dataDir}

	_, events := GitCommitsAdapter{}.Collect(context.Background(), cfg)
	warnings := 0
	commits := 0
	for _, event := range events {
		switch event.EventType {
		case "adapter.warning":
			if event.Payload["warning"] == "shallow_clone" {
				warnings++
				if event.Payload["repo_remote"] != "example.com/team/shallow" {
					t.Fatalf("warning repo_remote = %v", event.Payload["repo_remote"])
				}
			}
		case "repo.commits":
			commits++
		}
	}
	if warnings != 1 {
		t.Fatalf("shallow warnings = %d, want 1", warnings)
	}
	if commits == 0 {
		t.Fatal("shallow clone reported no repo.commits; the scan must still cover reachable history")
	}

	// The probe and its warning fire once per clone per daemon run.
	_, events = GitCommitsAdapter{}.Collect(context.Background(), cfg)
	for _, event := range events {
		if event.EventType == "adapter.warning" && event.Payload["warning"] == "shallow_clone" {
			t.Fatal("shallow warning repeated on the next cycle")
		}
	}
}
