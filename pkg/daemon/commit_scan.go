package daemon

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/gesta-run/gesta-agent/pkg/model"
	"github.com/gesta-run/gesta-agent/pkg/util"
)

// Merged-commit scanning: the durable output pipeline (CLO-1733). The snapshot
// pipeline (output_summary.go) measures a session's working tree and stays as a
// per-session diagnostic; the numbers the console bills against come from here —
// commits that actually landed on a repo's default branch, read from the local
// clone the developer already has. Nothing here talks to a git host and no code
// content leaves the machine: each commit is reduced to (sha, author email,
// committer time, line counts per file kind, AI trailer flag).
//
// Because these numbers are money-facing, the scan is structured so that no
// user-controlled byte can influence the counts: commit lists come from
// rev-list (hex shas only), line counts come from a `git log --numstat` stream
// whose pretty format prints nothing but the sha, and commit-message content
// (the AI trailer) is read in a separate metadata pass where it is the final,
// never-parsed-as-counts field.
const (
	commitScanRepoDir = "commit-repos.d"
	commitCursorFile  = "commit-cursors.json"
	// First scan of a repo is bounded to the efficiency composer's widest
	// lookback (120 days: the trailing-30d window plus the prior-90d trend
	// window). Deeper history is an explicit operator action, not a default.
	commitScanHorizonDays = 120
	// Per-cycle chunk size. A scan that hits this cap reports the OLDEST chunk
	// and leaves the cursor at that chunk's newest commit, so the next cycle
	// continues where this one stopped — a busy backfill catches up across
	// cycles instead of silently skipping history.
	commitScanMaxCommits = 4000
	// Shas per git invocation inside a cycle. Each -M numstat call stays far
	// below the timeout even on a cold cache, so a large first scan degrades
	// into more cheap calls instead of one call that can never finish.
	commitScanGitChunk   = 500
	commitScanGitTimeout = 60 * time.Second
	// Commits per repo.commits event. Bounds payload size; a backfill simply
	// emits several events.
	commitsPerEvent = 100
)

// shallowChecked tracks which clones this daemon process has already probed
// for shallowness, so the probe (and its warning event) fires once per clone
// per run rather than every scan cycle.
var shallowChecked = struct {
	mu   sync.Mutex
	seen map[string]bool
}{seen: map[string]bool{}}

// shallowCheckOnce reports whether this is the first shallow probe for root in
// this daemon process.
func shallowCheckOnce(root string) bool {
	shallowChecked.mu.Lock()
	defer shallowChecked.mu.Unlock()
	if shallowChecked.seen[root] {
		return false
	}
	shallowChecked.seen[root] = true
	return true
}

// commitFact is one merged commit reduced to reportable numbers.
type commitFact struct {
	SHA          string
	AuthorEmail  string
	CommittedAt  time.Time
	FilesChanged int64
	CodeAdded    int64
	CodeDeleted  int64
	TestAdded    int64
	TestDeleted  int64
	DocAdded     int64
	DocDeleted   int64
	AIAssisted   bool
	AITool       string
}

// GitCommitsAdapter scans registered repos for commits newly merged to the
// default branch and reports them as repo.commits events. It does NOT persist
// scan cursors itself: each event carries the cursor it justifies, and the
// runner commits cursors via CommitScanCursorCommit only after the events are
// durably queued — the same ordering contract the usage and output cursors
// follow, so a queue failure can never silently skip commits forever.
type GitCommitsAdapter struct{}

func (GitCommitsAdapter) Name() string { return "git_commits" }

func (a GitCommitsAdapter) Collect(ctx context.Context, cfg Config) (AdapterResult, []model.EventEnvelope) {
	now := time.Now().UTC()
	if cfg.DataDir == "" {
		cfg.DataDir = DefaultDataDir()
	}
	repos := registeredCommitScanRepos(cfg.DataDir)
	status := model.AdapterStatus{Name: a.Name(), Detected: len(repos) > 0, Status: "ok", UpdatedAt: now.Format(time.RFC3339)}
	if len(repos) == 0 {
		status.Status = "no_repos"
		return AdapterResult{Status: status}, nil
	}
	cursors, err := loadCommitCursorStore(cfg.DataDir)
	if err != nil {
		status.Status = "cursor_error"
		return AdapterResult{Status: status}, nil
	}
	var events []model.EventEnvelope
	catchingUp := false
	scanErrors := 0
	for _, root := range repos {
		if _, err := os.Stat(root); err != nil {
			continue
		}
		remote := normalizeGitRemoteURL(gitOriginURL(ctx, root))
		if remote == "" {
			// A clone without an origin remote cannot be deduplicated across
			// machines and has no shared default branch to have merged to.
			continue
		}
		if outputRepoExcluded(remote) {
			// Upstream OSS forks are skipped entirely: their history is
			// dominated by upstream syncs, not authored product work
			// (see defaultOutputExcludedRepoPatterns).
			continue
		}
		// A shallow clone silently hides everything behind its boundary from
		// the scan, so it must not look like a complete history. One warning
		// event per clone per daemon run; the fix (`git fetch --unshallow`) is
		// the developer's call, never an unrequested network fetch.
		if shallowCheckOnce(root) && gitIsShallowClone(ctx, root) {
			events = append(events, snapshotEvent(cfg, "adapter.warning", "git", "git_commits", map[string]interface{}{
				"repo_remote": remote,
				"warning":     "shallow_clone",
				"detail":      "history behind the shallow boundary is invisible to the commit scan; run `git fetch --unshallow` in this clone",
			}))
		}
		repoID := util.ShortHash("git-remote\x00" + remote)
		// Cursors are keyed per CLONE, not per remote: two local clones of the
		// same repo have different tips and object stores, and a shared cursor
		// would ping-pong between them, full-rescanning the stale clone every
		// other cycle forever.
		cursorKey := util.ShortHash(root)
		ref, ok := resolveDefaultBranchRef(ctx, root)
		if !ok {
			scanErrors++
			continue
		}
		tip := gitRevParse(ctx, root, ref)
		if tip == "" {
			scanErrors++
			continue
		}
		cursor := cursors.Repos[cursorKey]
		if cursor.LastSHA == tip {
			continue
		}
		shas, truncated, ok := listRepoCommits(ctx, root, ref, cursor.LastSHA, now, commitScanMaxCommits)
		if !ok {
			scanErrors++
			continue
		}
		if len(shas) == 0 {
			continue
		}
		if truncated {
			catchingUp = true
		}
		facts, ok := commitFactsForSHAs(ctx, root, shas, now)
		if !ok {
			scanErrors++
			continue
		}
		events = append(events, commitFactEvents(cfg, repoID, cursorKey, remote, ref, facts, now)...)
	}
	// Never a silent undercount: a repo that keeps failing (timeouts, git
	// errors) or a cycle that only covered a chunk is visible in the status.
	if scanErrors > 0 {
		status.Status = "scan_errors"
	} else if catchingUp {
		status.Status = "catching_up"
	}
	return AdapterResult{Status: status}, events
}

// CommitScanCursorCommit returns a callback that persists the commit-scan
// cursors carried in repo.commits events. The caller must invoke it only after
// the events are durably queued (or shipped); until then the cursors on disk
// still describe the last successfully queued scan, so a crash or queue
// failure simply rescans — and the control plane dedupes by (repo, sha).
// Returns nil when the events carry no cursor updates.
func CommitScanCursorCommit(cfg Config, events []model.EventEnvelope) func() error {
	if cfg.DataDir == "" {
		cfg.DataDir = DefaultDataDir()
	}
	updates := map[string]commitRepoCursor{}
	for _, event := range events {
		if event.EventType != "repo.commits" {
			continue
		}
		cursorKey := firstPayloadString(event.Payload, "cursor_key")
		cursorSHA := firstPayloadString(event.Payload, "cursor_sha")
		if cursorKey == "" || !isHexSHA(cursorSHA) {
			continue
		}
		// Batches arrive oldest-first per clone, so the last one wins.
		updates[cursorKey] = commitRepoCursor{LastSHA: cursorSHA, ScannedAt: event.CreatedAt.UTC().Format(time.RFC3339)}
	}
	if len(updates) == 0 {
		return nil
	}
	dataDir := cfg.DataDir
	return func() error {
		store, err := loadCommitCursorStore(dataDir)
		if err != nil {
			return err
		}
		for repoID, cursor := range updates {
			store.Repos[repoID] = cursor
		}
		return saveCommitCursorStore(dataDir, store)
	}
}

// listRepoCommits returns the oldest-first shas of non-merge commits reachable
// from ref that the cursor has not covered, capped at max. truncated reports
// whether older-than-cap history remains for a later cycle. A cursor git no
// longer accepts (rewritten branch, pruned object) falls back to the bounded
// horizon scan; re-reported commits dedupe at the control plane.
func listRepoCommits(ctx context.Context, root, ref, cursorSHA string, now time.Time, max int) ([]string, bool, bool) {
	base := []string{"rev-list", "--reverse", "--no-merges"}
	if cursorSHA != "" {
		out, err := gitStdout(ctx, root, "", append(base, cursorSHA+".."+ref)...)
		if err == nil {
			shas, truncated := capShaList(parseShaLines(out), max)
			return shas, truncated, true
		}
	}
	horizon := now.AddDate(0, 0, -commitScanHorizonDays)
	out, err := gitStdout(ctx, root, "", append(base, "--since="+horizon.Format(time.RFC3339), ref)...)
	if err != nil {
		return nil, false, false
	}
	shas, truncated := capShaList(parseShaLines(out), max)
	return shas, truncated, true
}

func parseShaLines(out string) []string {
	var shas []string
	for _, line := range strings.Split(out, "\n") {
		sha := strings.TrimSpace(line)
		if isHexSHA(sha) {
			shas = append(shas, sha)
		}
	}
	return shas
}

func capShaList(shas []string, max int) ([]string, bool) {
	if max > 0 && len(shas) > max {
		return shas[:max], true
	}
	return shas, false
}

func isHexSHA(value string) bool {
	if len(value) != 40 && len(value) != 64 {
		return false
	}
	for _, r := range value {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
}

// commitFactsForSHAs resolves the given commits (oldest-first) into facts using
// two git passes, sub-chunked so no single git invocation carries more than
// commitScanGitChunk commits — a large first scan becomes many cheap calls
// rather than one call that can outlive the timeout forever.
//
// The counts pass prints nothing but hex shas and numstat lines — no
// commit-message byte can reach it, so counts cannot be forged from a message —
// and disables path quoting so non-ASCII filenames keep their extension. The
// metadata pass carries the raw (%ae, deliberately NOT mailmap-resolved: a
// .mailmap in one clone's working tree must not rewrite attribution for the
// whole org) author email, committer date, and the Co-authored-by trailer
// values; the trailer text is user-controlled and is therefore the final
// field, never parsed as counts, and any record whose sha fails hex validation
// (e.g. framing broken by a hostile byte) is dropped.
func commitFactsForSHAs(ctx context.Context, root string, shas []string, now time.Time) ([]commitFact, bool) {
	counts := map[string]*commitFact{}
	meta := map[string]commitMeta{}
	for start := 0; start < len(shas); start += commitScanGitChunk {
		end := start + commitScanGitChunk
		if end > len(shas) {
			end = len(shas)
		}
		stdin := strings.Join(shas[start:end], "\n") + "\n"
		countsOut, err := gitStdout(ctx, root, stdin,
			"-c", "core.quotePath=false",
			"log", "--no-walk=unsorted", "-M", "--numstat", "--pretty=format:%x1e%H", "--stdin")
		if err != nil {
			return nil, false
		}
		for sha, fact := range parseNumstatLog(countsOut) {
			counts[sha] = fact
		}
		metaOut, err := gitStdout(ctx, root, stdin,
			"log", "--no-walk=unsorted", "--pretty=format:%x1e%H%x1f%ae%x1f%cI%x1f%(trailers:key=Co-authored-by,valueonly)", "--stdin")
		if err != nil {
			return nil, false
		}
		for sha, entry := range parseCommitMetaLog(metaOut, now) {
			meta[sha] = entry
		}
	}

	facts := make([]commitFact, 0, len(shas))
	for _, sha := range shas {
		fact, ok := counts[sha]
		if !ok {
			continue
		}
		if m, ok := meta[sha]; ok {
			fact.AuthorEmail = m.AuthorEmail
			fact.CommittedAt = m.CommittedAt
			fact.AIAssisted = m.AIAssisted
			fact.AITool = m.AITool
		} else {
			fact.CommittedAt = clampCommitTime(time.Time{}, now)
		}
		facts = append(facts, *fact)
	}
	return facts, true
}

// parseNumstatLog parses `git log --numstat --pretty=format:%x1e%H` output:
// each \x1e-framed record is a hex sha line followed by that commit's numstat
// lines. Records with a non-sha first line are dropped.
func parseNumstatLog(out string) map[string]*commitFact {
	facts := map[string]*commitFact{}
	for _, chunk := range strings.Split(out, "\x1e") {
		lines := strings.Split(chunk, "\n")
		sha := strings.TrimSpace(lines[0])
		if !isHexSHA(sha) {
			continue
		}
		fact := &commitFact{SHA: sha}
		for _, line := range lines[1:] {
			fields := strings.Split(line, "\t")
			if len(fields) < 3 {
				continue
			}
			path := normalizeNumstatPath(strings.TrimSpace(fields[2]))
			// With core.quotePath=false only paths containing tabs/newlines
			// still arrive quoted; those can't be classified reliably — skip.
			if path == "" || strings.HasPrefix(path, `"`) || commitScanPathExcluded(path) {
				continue
			}
			kind := fileKind(path)
			if kind == "" {
				continue
			}
			added := parseOutputCount(fields[0])
			deleted := parseOutputCount(fields[1])
			if added == 0 && deleted == 0 {
				continue
			}
			fact.FilesChanged++
			switch kind {
			case "code":
				fact.CodeAdded += added
				fact.CodeDeleted += deleted
			case "test":
				fact.TestAdded += added
				fact.TestDeleted += deleted
			case "docs":
				fact.DocAdded += added
				fact.DocDeleted += deleted
			}
		}
		facts[sha] = fact
	}
	return facts
}

type commitMeta struct {
	AuthorEmail string
	CommittedAt time.Time
	AIAssisted  bool
	AITool      string
}

// parseCommitMetaLog parses the metadata pass: \x1e-framed records of
// sha \x1f author-email \x1f committer-iso \x1f trailer-values. The trailer
// field is the rest of the record; a hostile \x1e inside it merely produces a
// follow-on chunk whose first field fails sha validation and is dropped.
func parseCommitMetaLog(out string, now time.Time) map[string]commitMeta {
	meta := map[string]commitMeta{}
	for _, chunk := range strings.Split(out, "\x1e") {
		parts := strings.SplitN(chunk, "\x1f", 4)
		if len(parts) < 4 {
			continue
		}
		sha := strings.TrimSpace(parts[0])
		if !isHexSHA(sha) {
			continue
		}
		entry := commitMeta{
			AuthorEmail: strings.ToLower(strings.TrimSpace(parts[1])),
			CommittedAt: clampCommitTime(parseOutputSummaryTime(strings.TrimSpace(parts[2])), now),
		}
		entry.AITool, entry.AIAssisted = detectAITrailer(parts[3])
		meta[sha] = entry
	}
	return meta
}

// commitFactEvents batches facts (oldest-first) into repo.commits events. Each
// event carries cursor_key (the clone it was scanned from) and cursor_sha (the
// newest commit it covers), which is what CommitScanCursorCommit persists after
// the batch is durably queued. The event id is derived from the exact sha set
// so a re-queued batch stays idempotent at the control plane's session_events
// layer too.
func commitFactEvents(cfg Config, repoID, cursorKey, remote, ref string, facts []commitFact, now time.Time) []model.EventEnvelope {
	var events []model.EventEnvelope
	for start := 0; start < len(facts); start += commitsPerEvent {
		end := start + commitsPerEvent
		if end > len(facts) {
			end = len(facts)
		}
		batch := facts[start:end]
		commits := make([]map[string]interface{}, 0, len(batch))
		var shas []string
		for _, fact := range batch {
			shas = append(shas, fact.SHA)
			commit := map[string]interface{}{
				"sha":                fact.SHA,
				"author_email":       fact.AuthorEmail,
				"committed_at":       fact.CommittedAt.Format(time.RFC3339),
				"files_changed":      fact.FilesChanged,
				"code_lines_added":   fact.CodeAdded,
				"code_lines_deleted": fact.CodeDeleted,
				"test_lines_added":   fact.TestAdded,
				"test_lines_deleted": fact.TestDeleted,
				"doc_lines_added":    fact.DocAdded,
				"doc_lines_deleted":  fact.DocDeleted,
				"ai_assisted":        fact.AIAssisted,
			}
			if fact.AITool != "" {
				commit["ai_tool"] = fact.AITool
			}
			commits = append(commits, commit)
		}
		event := baseEvent(cfg, "repo.commits", "git", "", map[string]interface{}{
			"repo_id":        repoID,
			"repo_remote":    remote,
			"default_branch": ref,
			"commit_count":   int64(len(commits)),
			"commits":        commits,
			"cursor_key":     cursorKey,
			"cursor_sha":     batch[len(batch)-1].SHA,
			"metadata_only":  true,
		})
		event.CreatedAt = now
		// The id spans customer/daemon as well as the sha set: a re-queued
		// batch (same daemon) stays idempotent, while an identical batch from
		// another machine or another tenant must NOT collide — the control
		// plane's event-level existence check is unscoped, and a collision
		// would silently drop the second tenant's facts.
		event.EventID = "evt_" + util.ShortHash(strings.Join(append([]string{"repo.commits", cfg.CustomerID, cfg.DaemonID, repoID}, shas...), "\x00"))
		events = append(events, event)
	}
	return events
}

// detectAITrailer reports whether any Co-authored-by trailer names a known
// coding agent. Classification keys on the trailer's EMAIL, not the display
// name: "Claude Dubois <claude.dubois@corp.fr>" is a person, while every agent
// writes an identifiable address (noreply@anthropic.com, codex@openai.com, …).
// Input lines may be bare trailer values (git's trailers:valueonly output) or
// full "Co-Authored-By: …" lines; both are handled.
func detectAITrailer(trailers string) (string, bool) {
	for _, line := range strings.Split(trailers, "\n") {
		value := strings.TrimSpace(line)
		if lower := strings.ToLower(value); strings.HasPrefix(lower, "co-authored-by:") {
			value = strings.TrimSpace(value[len("co-authored-by:"):])
		}
		email := trailerEmail(value)
		if email == "" {
			continue
		}
		local, domain, _ := strings.Cut(email, "@")
		switch {
		case domainIs(domain, "anthropic.com"):
			return "claude", true
		case domainIs(domain, "openai.com"), local == "codex", local == "chatgpt":
			return "codex", true
		case (domainIs(domain, "github.com") || domainIs(domain, "users.noreply.github.com")) && strings.Contains(local, "copilot"):
			return "copilot", true
		case domainIs(domain, "cursor.com"), domainIs(domain, "cursor.sh"), local == "cursoragent":
			return "cursor", true
		case domainIs(domain, "google.com") && strings.HasPrefix(local, "gemini"):
			return "gemini", true
		}
	}
	return "", false
}

// domainIs anchors domain matching at label boundaries: "anthropic.com" and
// "mail.anthropic.com" match, "not-anthropic.community.org" and
// "anthropic.com.evil.io" do not.
func domainIs(domain, target string) bool {
	return domain == target || strings.HasSuffix(domain, "."+target)
}

// trailerEmail extracts the lowercased address from a "Name <email>" trailer
// value. No angle brackets means no verifiable identity — no match.
func trailerEmail(value string) string {
	open := strings.LastIndex(value, "<")
	close := strings.LastIndex(value, ">")
	if open < 0 || close <= open+1 {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(value[open+1 : close]))
}

// commitScanPathExcluded extends the snapshot pipeline's directory/lockfile
// exclusions with generated files that carry code extensions — codegen must not
// read as authored output.
func commitScanPathExcluded(path string) bool {
	if outputPathExcluded(path) {
		return true
	}
	base := strings.ToLower(filepath.Base(filepath.ToSlash(path)))
	if strings.HasSuffix(base, ".pb.go") || strings.HasSuffix(base, ".pb.gw.go") {
		return true
	}
	if strings.Contains(base, "_generated.") || strings.Contains(base, ".generated.") {
		return true
	}
	if strings.HasSuffix(base, ".min.js") || strings.HasSuffix(base, ".min.css") {
		return true
	}
	return false
}

// normalizeNumstatPath resolves git's rename notation to the post-rename path:
// "dir/{old.go => new.go}" and "old.go => new.go" both become the new path.
func normalizeNumstatPath(path string) string {
	if start := strings.Index(path, "{"); start >= 0 {
		if arrow := strings.Index(path[start:], " => "); arrow >= 0 {
			if end := strings.Index(path[start:], "}"); end >= 0 {
				newPart := path[start+arrow+4 : start+end]
				normalized := path[:start] + newPart + path[start+end+1:]
				return strings.ReplaceAll(normalized, "//", "/")
			}
		}
	}
	if arrow := strings.Index(path, " => "); arrow >= 0 {
		return strings.TrimSpace(path[arrow+4:])
	}
	return path
}

// defaultOutputExcludedRepoPatterns lists substrings of a normalized remote URL
// whose repos are excluded from output measurement. These are upstream OSS
// forks (the karpenter provider forks): a single upstream sync adds tens of
// thousands of lines the person who ran it did not author — generated reference
// docs (e.g. website/content/**/instance-types.md) and synced upstream sources.
// Vendored third-party code is already dropped by outputPathExcluded, but
// generated docs and non-vendored upstream code are not, so without this the
// sync's author is credited with the entire upstream diff.
//
// This is a deliberately narrow, tactical denylist. First-class repo-scope
// classification (which repositories count toward output) is being designed
// separately; when it lands this list is removed.
var defaultOutputExcludedRepoPatterns = []string{
	"/karpenter-provider-",
}

// outputExcludedRepoPatterns returns the active exclusion substrings. The
// GESTA_OUTPUT_EXCLUDED_REPOS environment variable (comma-separated) replaces
// the default list, letting an operator adjust scope without an agent release.
func outputExcludedRepoPatterns() []string {
	if raw := strings.TrimSpace(os.Getenv("GESTA_OUTPUT_EXCLUDED_REPOS")); raw != "" {
		var patterns []string
		for _, part := range strings.Split(raw, ",") {
			if part = strings.ToLower(strings.TrimSpace(part)); part != "" {
				patterns = append(patterns, part)
			}
		}
		return patterns
	}
	return defaultOutputExcludedRepoPatterns
}

// outputRepoExcluded reports whether a repo's normalized remote is excluded from
// output measurement (see defaultOutputExcludedRepoPatterns).
func outputRepoExcluded(remote string) bool {
	remote = strings.ToLower(strings.TrimSpace(remote))
	if remote == "" {
		return false
	}
	for _, pattern := range outputExcludedRepoPatterns() {
		if pattern != "" && strings.Contains(remote, pattern) {
			return true
		}
	}
	return false
}

// normalizeGitRemoteURL reduces a remote URL to host/path identity so SSH and
// HTTPS clones of the same repo produce the same repo id: scheme, credentials,
// explicit ports, the ".git" suffix, and host case are all stripped.
