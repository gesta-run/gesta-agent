package daemon

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestOutputRepoExcluded(t *testing.T) {
	cases := map[string]bool{
		"github.com/cloudpilot-ai/karpenter-provider-aws-pro":      true,
		"github.com/cloudpilot-ai/karpenter-provider-gcp":          true,
		"github.com/cloudpilot-ai/karpenter-provider-gcp-pro":      true,
		"github.com/cloudpilot-ai/karpenter-provider-azure-pro":    true,
		"github.com/cloudpilot-ai/karpenter-provider-alibabacloud": true,
		"github.com/cloudpilot-ai/gesta":                           false,
		"github.com/cloudpilot-ai/crm":                             false,
		"github.com/cloudpilot-ai/cloudpilot":                      false,
		// substring safety: a repo merely mentioning the word must not match.
		"github.com/cloudpilot-ai/karpenter-docs": false,
		"": false,
	}
	for remote, want := range cases {
		if got := outputRepoExcluded(remote); got != want {
			t.Errorf("outputRepoExcluded(%q) = %v, want %v", remote, got, want)
		}
	}
}

func TestOutputRepoExcludedEnvOverride(t *testing.T) {
	t.Setenv("GESTA_OUTPUT_EXCLUDED_REPOS", " my-fork , acme/thing ")
	if !outputRepoExcluded("github.com/acme/my-fork") {
		t.Error("env-configured pattern my-fork should be excluded")
	}
	if !outputRepoExcluded("gitlab.com/acme/thing") {
		t.Error("env-configured pattern acme/thing should be excluded")
	}
	// The env override replaces the default list, so the built-in karpenter
	// pattern is no longer active while the override is set.
	if outputRepoExcluded("github.com/cloudpilot-ai/karpenter-provider-aws-pro") {
		t.Error("env override should replace defaults; karpenter no longer excluded")
	}
}

func TestNormalizeGitRemoteURL(t *testing.T) {
	cases := []struct {
		raw  string
		want string
	}{
		{"git@github.com:cloudpilot-ai/gesta.git", "github.com/cloudpilot-ai/gesta"},
		{"https://github.com/cloudpilot-ai/gesta.git", "github.com/cloudpilot-ai/gesta"},
		{"https://user:token@GitHub.com/cloudpilot-ai/gesta", "github.com/cloudpilot-ai/gesta"},
		{"ssh://git@gitlab.example.com/team/repo.git", "gitlab.example.com/team/repo"},
		// Explicit ports are not part of repo identity: a self-hosted GitLab on
		// a non-standard SSH port must dedupe against its HTTPS clones.
		{"ssh://git@gitlab.example.com:2222/team/repo.git", "gitlab.example.com/team/repo"},
		{"https://gitlab.example.com:8443/team/repo.git", "gitlab.example.com/team/repo"},
		{"git://Host.io/repo.git", "host.io/repo"},
		// Local-path remotes have no shared identity to dedupe on: skipped.
		{"file:///home/user/origin", ""},
		{"", ""},
		{"not-a-remote", ""},
	}
	for _, tc := range cases {
		if got := normalizeGitRemoteURL(tc.raw); got != tc.want {
			t.Fatalf("normalizeGitRemoteURL(%q) = %q, want %q", tc.raw, got, tc.want)
		}
	}
	if normalizeGitRemoteURL("ssh://git@gitlab.example.com:2222/team/repo.git") != normalizeGitRemoteURL("https://gitlab.example.com/team/repo") {
		t.Fatal("ssh-with-port and https forms of the same repo must normalize identically")
	}
}

func TestNormalizeNumstatPath(t *testing.T) {
	cases := []struct {
		raw  string
		want string
	}{
		{"pkg/daemon/commit_scan.go", "pkg/daemon/commit_scan.go"},
		{"pkg/{old.go => new.go}", "pkg/new.go"},
		{"pkg/{daemon => scanner}/scan.go", "pkg/scanner/scan.go"},
		{"old.go => new.go", "new.go"},
		{"{old => new}/main.go", "new/main.go"},
	}
	for _, tc := range cases {
		if got := normalizeNumstatPath(tc.raw); got != tc.want {
			t.Fatalf("normalizeNumstatPath(%q) = %q, want %q", tc.raw, got, tc.want)
		}
	}
}

// Classification keys on the trailer email, never the display name: a human
// co-author named Claude must not mark a commit AI-assisted.
func TestDetectAITrailer(t *testing.T) {
	cases := []struct {
		trailers string
		wantTool string
		wantOK   bool
	}{
		{"Claude Opus 4.8 <noreply@anthropic.com>", "claude", true},
		{"Co-Authored-By: Claude <noreply@anthropic.com>", "claude", true},
		{"ChatGPT <codex@openai.com>", "codex", true},
		{"Codex <codex@cloudpilot.ai>", "codex", true},
		{"Copilot <175728472+Copilot@users.noreply.github.com>", "copilot", true},
		{"Claude Dubois <claude.dubois@corp.fr>", "", false},
		{"Ada Lovelace <ada@cloudpilot.ai>", "", false},
		// Domain matching is label-anchored: lookalike domains must not count.
		{"Evil <bot@anthropic.com.evil.io>", "", false},
		{"Evil <bot@not-anthropic.community.org>", "", false},
		// Gemini is a human given name; only the agent's own domain counts.
		{"Gemini Rossi <gemini.rossi@corp.it>", "", false},
		{"gemini-cli <gemini-cli@google.com>", "gemini", true},
		{"mentions claude in prose but no trailer", "", false},
		{"Claude without an email address", "", false},
		{"", "", false},
	}
	for _, tc := range cases {
		tool, ok := detectAITrailer(tc.trailers)
		if tool != tc.wantTool || ok != tc.wantOK {
			t.Fatalf("detectAITrailer(%q) = (%q, %v), want (%q, %v)", tc.trailers, tool, ok, tc.wantTool, tc.wantOK)
		}
	}
}

func TestClampCommitTimePinsTheFuture(t *testing.T) {
	now := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)
	past := now.Add(-time.Hour)
	if got := clampCommitTime(past, now); !got.Equal(past) {
		t.Fatalf("past commit time changed: %v", got)
	}
	if got := clampCommitTime(now.Add(48*time.Hour), now); !got.Equal(now) {
		t.Fatalf("future commit time not clamped: %v", got)
	}
	if got := clampCommitTime(time.Time{}, now); !got.Equal(now) {
		t.Fatalf("zero commit time not defaulted: %v", got)
	}
}

func TestCommitScanPathExcluded(t *testing.T) {
	excluded := []string{
		"api/service.pb.go",
		"api/service.pb.gw.go",
		"pkg/schema_generated.ts",
		"web/bundle.min.js",
		"vendor/lib/lib.go",
		"node_modules/x/index.js",
		"pnpm-lock.yaml",
	}
	for _, path := range excluded {
		if !commitScanPathExcluded(path) {
			t.Fatalf("%s should be excluded", path)
		}
	}
	included := []string{"pkg/control/store.go", "console/src/app.tsx", "README.md", "pkg/store_test.go"}
	for _, path := range included {
		if commitScanPathExcluded(path) {
			t.Fatalf("%s should not be excluded", path)
		}
	}
}

const (
	testSHA1 = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	testSHA2 = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

// The counts stream contains nothing but git-generated shas and numstat lines,
// so message bytes cannot forge counts by construction; this pins the parser's
// handling of renames, binaries, exclusions, and malformed records.
func TestParseNumstatLog(t *testing.T) {
	out := strings.Join([]string{
		"\x1e" + testSHA1,
		"10\t2\tpkg/feature.go",
		"5\t0\tpkg/feature_test.go",
		"3\t1\tREADME.md",
		"7\t0\tapi/service.pb.go",
		"-\t-\tassets/logo.png",
		"4\t0\t\"path with\ttab.go\"", // git still quotes tab/newline paths; unclassifiable, skipped
		"",
		"\x1e" + testSHA2,
		"4\t4\tpkg/{old.go => new.go}",
		"\x1enot-a-sha",
		"999\t0\tforged.go",
	}, "\n")
	facts := parseNumstatLog(out)
	if len(facts) != 2 {
		t.Fatalf("parsed %d commits, want 2 (invalid-sha record dropped)", len(facts))
	}
	first := facts[testSHA1]
	if first.CodeAdded != 10 || first.CodeDeleted != 2 || first.TestAdded != 5 || first.DocAdded != 3 {
		t.Fatalf("first commit counts wrong: %+v", first)
	}
	if first.FilesChanged != 3 {
		t.Fatalf("generated and binary files must not count: files=%d", first.FilesChanged)
	}
	second := facts[testSHA2]
	if second.CodeAdded != 4 || second.CodeDeleted != 4 {
		t.Fatalf("rename numstat not parsed: %+v", second)
	}
}

func TestParseCommitMetaLog(t *testing.T) {
	now := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)
	out := strings.Join([]string{
		"\x1e" + testSHA1 + "\x1fJJK@CloudPilot.ai\x1f2026-07-13T10:00:00+00:00\x1fClaude <noreply@anthropic.com>",
		"\x1e" + testSHA2 + "\x1fzrt@cloudpilot.ai\x1f2099-01-01T00:00:00+00:00\x1f",
	}, "\n")
	meta := parseCommitMetaLog(out, now)
	first := meta[testSHA1]
	if first.AuthorEmail != "jjk@cloudpilot.ai" {
		t.Fatalf("author email not lowercased: %q", first.AuthorEmail)
	}
	if !first.AIAssisted || first.AITool != "claude" {
		t.Fatalf("AI trailer lost: %+v", first)
	}
	second := meta[testSHA2]
	if second.AIAssisted {
		t.Fatalf("no trailer must not read as AI: %+v", second)
	}
	if !second.CommittedAt.Equal(now) {
		t.Fatalf("future committer date not clamped: %v", second.CommittedAt)
	}
}

// A hostile record-separator byte inside the (user-controlled) trailer field
// must not fabricate a commit: the spill-over chunk fails sha validation.
func TestParseCommitMetaLogDropsForgedRecords(t *testing.T) {
	now := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)
	out := "\x1e" + testSHA1 + "\x1fa@b.c\x1f2026-07-13T10:00:00+00:00\x1fevil\x1eforged-not-a-sha\x1fx@y.z\x1f2026-07-13T10:00:00+00:00\x1fClaude <noreply@anthropic.com>"
	meta := parseCommitMetaLog(out, now)
	if len(meta) != 1 {
		t.Fatalf("forged record survived: %d entries", len(meta))
	}
	if _, ok := meta[testSHA1]; !ok {
		t.Fatal("legitimate record lost")
	}
}

// End-to-end against a real origin+clone pair: only commits that reached
// origin's default branch are reported; the cursor advances only via
// CommitScanCursorCommit (the runner's post-queue contract); a commit message
// carrying fake numstat bytes cannot inflate counts; AI trailers mark
// ai_assisted.
func TestGitCommitsAdapterScansMergedCommits(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	origin := t.TempDir()
	clone := t.TempDir()
	dataDir := t.TempDir()
	run := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, string(out))
		}
	}
	run(origin, "init", "--bare", "--initial-branch=main")
	run(clone, "init", "--initial-branch=main")
	run(clone, "config", "user.email", "jjk@cloudpilot.ai")
	run(clone, "config", "user.name", "JJK")
	run(clone, "remote", "add", "origin", "file://"+origin)

	mustWriteFile(t, filepath.Join(clone, "main.go"), "package main\n\nfunc main() {}\n")
	run(clone, "add", ".")
	run(clone, "commit", "-m", "initial\n\nCo-Authored-By: Claude <noreply@anthropic.com>")
	mustWriteFile(t, filepath.Join(clone, "notes.md"), "one\ntwo\n")
	run(clone, "add", ".")
	run(clone, "commit", "-m", "handwritten docs")
	// A hostile message: a field separator followed by numstat-shaped bytes.
	// The two-pass scan must report this commit's REAL counts (1 line).
	mustWriteFile(t, filepath.Join(clone, "tiny.go"), "package main\n")
	run(clone, "add", ".")
	run(clone, "commit", "-m", "sneaky\x1f999999\t0\tmain.go")
	run(clone, "push", "origin", "main")
	run(clone, "fetch", "origin")

	// file:// remotes normalize to "", which the adapter skips by design. For
	// the test, record a host-shaped URL AFTER pushing.
	run(clone, "config", "remote.origin.url", "git@example.com:team/demo.git")

	registerCommitScanRepo(dataDir, clone)
	cfg := Config{DataDir: dataDir}

	collect := func() []map[string]interface{} {
		t.Helper()
		_, events := GitCommitsAdapter{}.Collect(context.Background(), cfg)
		var commits []map[string]interface{}
		for _, event := range events {
			if event.EventType != "repo.commits" {
				continue
			}
			raw, ok := event.Payload["commits"].([]map[string]interface{})
			if !ok {
				t.Fatalf("commits payload has unexpected type %T", event.Payload["commits"])
			}
			commits = append(commits, raw...)
			if event.Payload["repo_remote"] != "example.com/team/demo" {
				t.Fatalf("repo_remote = %v", event.Payload["repo_remote"])
			}
		}
		// Mirror the runner: cursors advance only after a durable queue/ship.
		if commit := CommitScanCursorCommit(cfg, events); commit != nil {
			if err := commit(); err != nil {
				t.Fatalf("commit scan cursors: %v", err)
			}
		}
		return commits
	}

	commits := collect()
	if len(commits) != 3 {
		t.Fatalf("got %d commits, want 3", len(commits))
	}
	var aiCount int
	for _, commit := range commits {
		if commit["ai_assisted"] == true {
			aiCount++
		}
		if commit["code_lines_added"].(int64) > 10 {
			t.Fatalf("forged numstat leaked into counts: %+v", commit)
		}
	}
	if aiCount != 1 {
		t.Fatalf("ai_assisted commits = %d, want 1", aiCount)
	}

	// Cursor committed → unchanged tip reports nothing.
	if commits := collect(); len(commits) != 0 {
		t.Fatalf("unchanged tip re-reported %d commits", len(commits))
	}

	// New commit pushed and fetched: only the delta comes back.
	mustWriteFile(t, filepath.Join(clone, "more.go"), "package main\n\nfunc more() {}\n")
	run(clone, "config", "remote.origin.url", "file://"+origin)
	run(clone, "add", ".")
	run(clone, "commit", "-m", "increment")
	run(clone, "push", "origin", "main")
	run(clone, "fetch", "origin")
	run(clone, "config", "remote.origin.url", "git@example.com:team/demo.git")

	if commits := collect(); len(commits) != 1 {
		t.Fatalf("incremental scan reported %d commits, want 1", len(commits))
	}
}

// A capped scan reports the OLDEST chunk and the cursor lands on that chunk's
// newest commit, so successive cycles walk the backlog instead of skipping it.
func TestListRepoCommitsChunksOldestFirst(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	root := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, string(out))
		}
	}
	run("init", "--initial-branch=main")
	run("config", "user.email", "t@example.com")
	run("config", "user.name", "T")
	for i := 0; i < 5; i++ {
		mustWriteFile(t, filepath.Join(root, "f.txt"), strings.Repeat("x", i+1))
		run("add", ".")
		run("commit", "-m", "c")
	}
	now := time.Now().UTC()

	first, truncated, ok := listRepoCommits(context.Background(), root, "HEAD", "", now, 2)
	if !ok || len(first) != 2 || !truncated {
		t.Fatalf("first chunk = %d commits (truncated=%v, ok=%v), want 2/true/true", len(first), truncated, ok)
	}
	second, truncated, ok := listRepoCommits(context.Background(), root, "HEAD", first[len(first)-1], now, 2)
	if !ok || len(second) != 2 || !truncated {
		t.Fatalf("second chunk = %d commits (truncated=%v), want 2/true", len(second), truncated)
	}
	third, truncated, ok := listRepoCommits(context.Background(), root, "HEAD", second[len(second)-1], now, 2)
	if !ok || len(third) != 1 || truncated {
		t.Fatalf("third chunk = %d commits (truncated=%v), want 1/false", len(third), truncated)
	}
	seen := map[string]bool{}
	for _, sha := range append(append(append([]string{}, first...), second...), third...) {
		if seen[sha] {
			t.Fatalf("sha %s reported twice across chunks", sha)
		}
		seen[sha] = true
	}
}

func TestRegisteredCommitScanReposRoundTrip(t *testing.T) {
	dataDir := t.TempDir()
	if repos := registeredCommitScanRepos(dataDir); len(repos) != 0 {
		t.Fatalf("empty registry returned %v", repos)
	}
	registerCommitScanRepo(dataDir, "/tmp/repo-a")
	registerCommitScanRepo(dataDir, "/tmp/repo-b")
	registerCommitScanRepo(dataDir, "/tmp/repo-a") // idempotent
	repos := registeredCommitScanRepos(dataDir)
	if len(repos) != 2 {
		t.Fatalf("registry has %d entries, want 2: %v", len(repos), repos)
	}
	if _, err := os.Stat(filepath.Join(dataDir, commitScanRepoDir)); err != nil {
		t.Fatalf("registry dir missing: %v", err)
	}
}

// TestParseMergeBranch covers the two anchored merge-subject formats and the
// non-matches that must NOT be read as a branch (Signal 2, CLO-1732).
func TestParseMergeBranch(t *testing.T) {
	cases := []struct{ subject, want string }{
		// GitHub PR merge: branch is everything after "<owner>/".
		{"Merge pull request #123 from cloudpilot-ai/codex/add-widget", "codex/add-widget"},
		{"Merge pull request #7 from someone/feature/login", "feature/login"},
		{"Merge pull request #9 from org/codex/a/b/c", "codex/a/b/c"},
		// git CLI branch merge, with and without an "into <base>" tail.
		{"Merge branch 'codex/quick-fix'", "codex/quick-fix"},
		{"Merge branch 'release-0.4' into main", "release-0.4"},
		{"Merge branch 'codex/x' of github.com:o/r into main", "codex/x"},
		// Not merge subjects at all — must never yield a branch.
		{"feat: add codex/ prefix support", ""},
		{"fix bug in codex/foo", ""},
		// Anchoring: the prefix must be at the very start.
		{"xMerge pull request #1 from o/codex/y", ""},
		// Degenerate merge subjects.
		{"Merge pull request #1 from owner/", ""},
		{"Merge pull request #1 from ownerbranch", ""},
		{"Merge branch 'unterminated", ""},
		{"", ""},
	}
	for _, c := range cases {
		if got := parseMergeBranch(c.subject); got != c.want {
			t.Errorf("parseMergeBranch(%q) = %q, want %q", c.subject, got, c.want)
		}
	}
}

// A merge subject is user-controlled: control bytes in the "branch name" must be
// stripped before it becomes a stored fact.
func TestParseMergeBranchStripsHostileBytes(t *testing.T) {
	if got := parseMergeBranch("Merge branch 'codex/\x00\x07evil\x1f'"); got != "codex/evil" {
		t.Fatalf("ASCII control bytes not stripped: %q", got)
	}
	// Unicode format characters — U+202E (RTL override), U+200B (zero-width
	// space), U+2028 (line separator) — are display-spoofing vectors and must be
	// stripped too before the branch becomes a rendered fact.
	if got := parseMergeBranch("Merge branch 'codex/\u202e\u200bevil '"); got != "codex/evil" {
		t.Fatalf("unicode format chars not stripped: %q", got)
	}
}

// commitFactEvents emits merged_via_branch only when present — omitted keys keep
// pre-Signal-2 payload shape byte-for-byte.
func TestCommitFactEventsEmitMergedViaBranch(t *testing.T) {
	facts := []commitFact{
		{SHA: strings.Repeat("a", 40), MergedViaBranch: "codex/x"},
		{SHA: strings.Repeat("b", 40)},
	}
	events := commitFactEvents(Config{CustomerID: "c", DaemonID: "d"}, "repo", "ck", "example.com/o/r", "main", facts, time.Now().UTC())
	var commits []map[string]interface{}
	for _, event := range events {
		if raw, ok := event.Payload["commits"].([]map[string]interface{}); ok {
			commits = append(commits, raw...)
		}
	}
	if len(commits) != 2 {
		t.Fatalf("got %d commits, want 2", len(commits))
	}
	if commits[0]["merged_via_branch"] != "codex/x" {
		t.Fatalf("first commit merged_via_branch = %v, want codex/x", commits[0]["merged_via_branch"])
	}
	if _, present := commits[1]["merged_via_branch"]; present {
		t.Fatalf("unlabeled commit must omit merged_via_branch, got %v", commits[1]["merged_via_branch"])
	}
}

// mergeProvenanceMap labels the commits a codex/ PR merge introduced, and never
// the merge commit itself (merge commits are not output).
func TestMergeProvenanceMapLabelsMergedBranch(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	root := t.TempDir()
	run := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	run("init", "--initial-branch=main")
	run("config", "user.email", "t@example.com")
	run("config", "user.name", "T")
	mustWriteFile(t, filepath.Join(root, "base.go"), "package main\n")
	run("add", ".")
	run("commit", "-m", "base")
	run("checkout", "-b", "codex/add-thing")
	mustWriteFile(t, filepath.Join(root, "thing.go"), "package main\n\nfunc thing() {}\n")
	run("add", ".")
	run("commit", "-m", "add thing")
	childSHA := run("rev-parse", "HEAD")
	run("checkout", "main")
	run("merge", "--no-ff", "codex/add-thing", "-m", "Merge pull request #1 from cloudpilot-ai/codex/add-thing")
	mergeSHA := run("rev-parse", "HEAD")

	m, ok := mergeProvenanceMap(context.Background(), root, "HEAD", "", time.Now().UTC())
	if !ok {
		t.Fatalf("provenance pass reported not-ok on a healthy repo")
	}
	if m[childSHA] != "codex/add-thing" {
		t.Fatalf("child %s mapped to %q, want codex/add-thing (map=%v)", childSHA, m[childSHA], m)
	}
	if _, ok := m[mergeSHA]; ok {
		t.Fatalf("merge commit %s must not be labeled as output", mergeSHA)
	}
}

// The giant-merge circuit breaker: a merge that introduces more than the cap is
// a history import, not a PR, and must be skipped rather than mislabeled.
func TestMergeProvenanceCircuitBreakerSkipsGiantMerges(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	root := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "--initial-branch=main")
	run("config", "user.email", "t@example.com")
	run("config", "user.name", "T")
	mustWriteFile(t, filepath.Join(root, "base.go"), "package main\n")
	run("add", ".")
	run("commit", "-m", "base")
	run("checkout", "-b", "codex/big")
	for i := 0; i < 2; i++ {
		mustWriteFile(t, filepath.Join(root, "f.go"), strings.Repeat("x\n", i+1))
		run("add", ".")
		run("commit", "-m", "step")
	}
	run("checkout", "main")
	run("merge", "--no-ff", "codex/big", "-m", "Merge pull request #2 from cloudpilot-ai/codex/big")
	now := time.Now().UTC()

	if skipped, ok := mergeProvenanceMapCapped(context.Background(), root, "HEAD", "", now, 1); !ok || len(skipped) != 0 {
		t.Fatalf("merge of 2 commits must be skipped at cap=1 (ok=%v), got %v", ok, skipped)
	}
	if labeled, ok := mergeProvenanceMapCapped(context.Background(), root, "HEAD", "", now, 500); !ok || len(labeled) != 2 {
		t.Fatalf("both child commits should be labeled at cap=500 (ok=%v), got %d (%v)", ok, len(labeled), labeled)
	}
}
