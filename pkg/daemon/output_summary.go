package daemon

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/gesta-run/gesta-agent/pkg/model"
	"github.com/gesta-run/gesta-agent/pkg/util"
)

const outputCursorFile = "output-cursors.json"

type outputCursorStore struct {
	Sessions map[string]string `json:"sessions"`
}

type outputSummaryCommit func() error

type outputSummary struct {
	RepoRoot         string
	RepoHash         string
	SessionID        string
	GitSHABefore     string
	GitSHAAfter      string
	DiffHash         string
	MeasurementMode  string
	BaselineCaptured string
	FilesChanged     int64
	CodeLinesAdded   int64
	CodeLinesDeleted int64
	TestLinesAdded   int64
	TestLinesDeleted int64
	DocLinesAdded    int64
	DocLinesDeleted  int64
	DocWordsAdded    int64
	DocWordsDeleted  int64
	Files            []map[string]interface{}
}

func codexOutputSummaryEvent(ctx context.Context, cfg Config, transcript map[string]interface{}) (model.EventEnvelope, bool) {
	sessionID := firstString(transcript, "session_id", "session_id_hash")
	cwd := firstString(transcript, "_cwd")
	sessionStartedAt := parseOutputSummaryTime(firstString(transcript, "created_at"))
	return outputSummaryEvent(ctx, cfg, "codex", sessionID, cwd, sessionStartedAt, firstString(transcript, "title"), firstString(transcript, "model"))
}

// outputSummaryEvent builds an output.summary event for one agent session by
// diffing the current worktree against the session baseline (or HEAD when no
// baseline was captured). agentType tags the event ("codex", "claude_code").
// sessionID must be the hashed session id that both keys the baseline and matches
// the session-index event, so the control plane can correlate the two. It returns
// ok=false when cwd is not a git repo, there is no baseline delta, or no in-scope
// files changed.
func outputSummaryEvent(ctx context.Context, cfg Config, agentType, sessionID, cwd string, sessionStartedAt time.Time, title, modelName string) (model.EventEnvelope, bool) {
	if sessionID == "" || cwd == "" {
		return model.EventEnvelope{}, false
	}
	if cfg.DataDir == "" {
		cfg.DataDir = DefaultDataDir()
	}
	// A HEAD diff describes the current worktree, not the work produced by one
	// session. If the hook missed the session start, seed a baseline now and wait
	// for the next transcript change instead of attributing unrelated work.
	if !ensureOutputBaseline(ctx, cfg, cwd, sessionID) {
		return model.EventEnvelope{}, false
	}
	summary, ok := gitOutputSummaryWithConfig(ctx, cfg, cwd, sessionID)
	if !ok || summary.FilesChanged == 0 {
		return model.EventEnvelope{}, false
	}
	now := time.Now().UTC()
	payload := map[string]interface{}{
		"agent_type":         agentType,
		"metadata_only":      true,
		"session_id":         sessionID,
		"session_id_hash":    sessionID,
		"repo":               summary.RepoHash,
		"repo_path_hash":     summary.RepoHash,
		"git_sha_before":     summary.GitSHABefore,
		"git_sha_after":      summary.GitSHAAfter,
		"files_changed":      summary.FilesChanged,
		"code_lines_added":   summary.CodeLinesAdded,
		"code_lines_deleted": summary.CodeLinesDeleted,
		"test_lines_added":   summary.TestLinesAdded,
		"test_lines_deleted": summary.TestLinesDeleted,
		"doc_lines_added":    summary.DocLinesAdded,
		"doc_lines_deleted":  summary.DocLinesDeleted,
		"doc_words_added":    summary.DocWordsAdded,
		"doc_words_deleted":  summary.DocWordsDeleted,
		"diff_hash":          summary.DiffHash,
		"measurement_mode":   summary.MeasurementMode,
		"generated_at":       now.Format(time.RFC3339Nano),
		"session_started_at": outputSummaryTimeString(sessionStartedAt),
		"files":              summary.Files,
	}
	if summary.BaselineCaptured != "" {
		payload["baseline_captured_at"] = summary.BaselineCaptured
	}
	if strings.TrimSpace(title) != "" {
		payload["title"] = title
	}
	if strings.TrimSpace(modelName) != "" {
		payload["model"] = modelName
	}
	event := baseEvent(cfg, "output.summary", "git", agentType, payload)
	event.CreatedAt = now
	event.EventID = "evt_" + util.ShortHash(strings.Join([]string{
		"output.summary",
		sessionID,
		summary.RepoHash,
		summary.DiffHash,
	}, "\x00"))
	return event, true
}

func ensureOutputBaseline(ctx context.Context, cfg Config, cwd, sessionID string) bool {
	root, ok := gitRepoRoot(ctx, cwd)
	if !ok {
		return false
	}
	registerCommitScanRepo(cfg.DataDir, root)
	repoHash := util.ShortHash(root)
	if _, ok := loadOutputBaseline(cfg.DataDir, sessionID, repoHash); ok {
		return true
	}
	if err := CaptureOutputBaseline(ctx, cfg, cwd, sessionID); err != nil {
		return false
	}
	_, ok = loadOutputBaseline(cfg.DataDir, sessionID, repoHash)
	return ok
}

func FilterOutputSummaryEvents(cfg Config, events []model.EventEnvelope) ([]model.EventEnvelope, outputSummaryCommit, error) {
	store, err := loadOutputCursorStore(cfg.DataDir)
	if err != nil {
		return nil, nil, err
	}
	next := outputCursorStore{Sessions: map[string]string{}}
	for key, value := range store.Sessions {
		next.Sessions[key] = value
	}
	filtered := make([]model.EventEnvelope, 0, len(events))
	for _, event := range events {
		if event.EventType != "output.summary" {
			filtered = append(filtered, event)
			continue
		}
		sessionID := firstPayloadString(event.Payload, "session_id", "session_id_hash")
		repo := firstPayloadString(event.Payload, "repo", "repo_path_hash")
		diffHash := firstPayloadString(event.Payload, "diff_hash")
		if sessionID == "" || repo == "" || diffHash == "" {
			filtered = append(filtered, event)
			continue
		}
		key := outputCursorKey(sessionID, repo)
		if store.Sessions[key] == diffHash {
			continue
		}
		next.Sessions[key] = diffHash
		filtered = append(filtered, event)
	}
	commit := func() error {
		return saveOutputCursorStore(cfg.DataDir, next)
	}
	return filtered, commit, nil
}

func gitOutputSummary(ctx context.Context, cwd, sessionID string, sessionStartedAt time.Time) (outputSummary, bool) {
	root, ok := gitRepoRoot(ctx, cwd)
	if !ok {
		return outputSummary{}, false
	}
	return gitOutputSummaryFromHead(ctx, root, sessionID, sessionStartedAt)
}

func gitOutputSummaryWithConfig(ctx context.Context, cfg Config, cwd, sessionID string) (outputSummary, bool) {
	root, ok := gitRepoRoot(ctx, cwd)
	if !ok {
		return outputSummary{}, false
	}
	repoHash := util.ShortHash(root)
	if strings.TrimSpace(cfg.DataDir) == "" {
		return outputSummary{}, false
	}
	if baseline, ok := loadOutputBaseline(cfg.DataDir, sessionID, repoHash); ok {
		return gitOutputSummaryFromBaseline(ctx, root, sessionID, baseline)
	}
	return outputSummary{}, false
}

func gitOutputSummaryFromHead(ctx context.Context, root, sessionID string, sessionStartedAt time.Time) (outputSummary, bool) {
	head := gitHead(ctx, root)
	numstat, err := commandOutput(ctx, "git", "-C", root, "diff", "--numstat", "HEAD", "--")
	if err != nil {
		return outputSummary{}, false
	}
	rawDiff, _ := commandOutput(ctx, "git", "-C", root, "diff", "--raw", "HEAD", "--")
	summary := outputSummary{
		RepoRoot:        root,
		RepoHash:        util.ShortHash(root),
		SessionID:       sessionID,
		GitSHABefore:    head,
		GitSHAAfter:     head,
		MeasurementMode: "git_head_diff",
	}
	changed := map[string]bool{}
	for _, line := range strings.Split(numstat, "\n") {
		fields := strings.Split(line, "\t")
		if len(fields) < 3 {
			continue
		}
		path := strings.TrimSpace(fields[2])
		if path == "" {
			continue
		}
		if !worktreePathChangedSince(root, path, sessionStartedAt) {
			changed[path] = true
			continue
		}
		added := parseOutputCount(fields[0])
		deleted := parseOutputCount(fields[1])
		applyFileOutput(&summary, path, added, deleted)
		if fileKind(path) == "docs" {
			addedWords, deletedWords := gitDocWordDiff(ctx, root, path)
			summary.DocWordsAdded += addedWords
			summary.DocWordsDeleted += deletedWords
		}
		changed[path] = true
	}
	var untrackedIdentity []string
	for _, path := range gitUntrackedFiles(ctx, root) {
		if changed[path] {
			continue
		}
		if !worktreePathChangedSince(root, path, sessionStartedAt) {
			continue
		}
		added, words := countWorktreeFile(root, path)
		kind := fileKind(path)
		if kind == "docs" {
			summary.DocLinesAdded += added
			summary.DocWordsAdded += words
		} else if kind == "test" {
			summary.TestLinesAdded += added
		} else if kind == "code" {
			summary.CodeLinesAdded += added
		} else {
			continue
		}
		summary.Files = append(summary.Files, outputFilePayload(path, kind, added, 0, words, 0))
		untrackedIdentity = append(untrackedIdentity, util.ShortHash(path)+"="+worktreeFileHash(root, path))
	}
	sort.Strings(untrackedIdentity)
	summary.FilesChanged = int64(len(summary.Files))
	if summary.FilesChanged == 0 {
		return outputSummary{}, false
	}
	data, _ := json.Marshal(summary.Files)
	summary.DiffHash = util.HashString(strings.Join([]string{
		summary.RepoHash,
		summary.GitSHABefore,
		rawDiff,
		strings.Join(untrackedIdentity, "\n"),
		string(data),
	}, "\x00"))
	return summary, true
}

func gitOutputSummaryFromBaseline(ctx context.Context, root, sessionID string, baseline outputBaseline) (outputSummary, bool) {
	current := captureOutputFileSnapshots(ctx, root)
	head := gitHead(ctx, root)
	summary := outputSummary{
		RepoRoot:         root,
		RepoHash:         util.ShortHash(root),
		SessionID:        sessionID,
		GitSHABefore:     baseline.GitSHABefore,
		GitSHAAfter:      head,
		MeasurementMode:  "session_baseline",
		BaselineCaptured: baseline.CapturedAt,
	}
	paths := outputSnapshotPaths(baseline.Files, current)
	for _, path := range paths {
		before, beforeOK := baseline.Files[path]
		after, afterOK := current[path]
		kind := ""
		if afterOK {
			kind = after.Kind
		} else if beforeOK {
			kind = before.Kind
		}
		if kind == "" {
			continue
		}
		added, deleted := outputSequenceDiff(before.Lines, after.Lines)
		var wordsAdded, wordsDeleted int64
		if kind == "docs" {
			wordsAdded, wordsDeleted = outputSequenceDiff(before.Words, after.Words)
		}
		if added == 0 && deleted == 0 && wordsAdded == 0 && wordsDeleted == 0 {
			continue
		}
		applySnapshotFileOutput(&summary, path, kind, added, deleted, wordsAdded, wordsDeleted)
	}
	summary.FilesChanged = int64(len(summary.Files))
	if summary.FilesChanged == 0 {
		return outputSummary{}, false
	}
	identity, _ := json.Marshal(current)
	files, _ := json.Marshal(summary.Files)
	summary.DiffHash = util.HashString(strings.Join([]string{
		summary.RepoHash,
		baseline.CapturedAt,
		summary.GitSHABefore,
		summary.GitSHAAfter,
		string(identity),
		string(files),
	}, "\x00"))
	return summary, true
}

func outputSnapshotPaths(left, right map[string]outputFileSnapshot) []string {
	seen := map[string]struct{}{}
	for path := range left {
		seen[path] = struct{}{}
	}
	for path := range right {
		seen[path] = struct{}{}
	}
	paths := make([]string, 0, len(seen))
	for path := range seen {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	return paths
}

func applyFileOutput(summary *outputSummary, path string, added, deleted int64) {
	kind := fileKind(path)
	applySnapshotFileOutput(summary, path, kind, added, deleted, 0, 0)
}

func applySnapshotFileOutput(summary *outputSummary, path, kind string, added, deleted, wordsAdded, wordsDeleted int64) {
	switch kind {
	case "docs":
		summary.DocLinesAdded += added
		summary.DocLinesDeleted += deleted
		summary.DocWordsAdded += wordsAdded
		summary.DocWordsDeleted += wordsDeleted
	case "test":
		summary.TestLinesAdded += added
		summary.TestLinesDeleted += deleted
	case "code":
		summary.CodeLinesAdded += added
		summary.CodeLinesDeleted += deleted
	default:
		return
	}
	summary.Files = append(summary.Files, outputFilePayload(path, kind, added, deleted, wordsAdded, wordsDeleted))
}

func outputFilePayload(path, kind string, added, deleted, wordsAdded, wordsDeleted int64) map[string]interface{} {
	payload := map[string]interface{}{
		"path_hash":     util.ShortHash(path),
		"extension":     strings.TrimPrefix(strings.ToLower(filepath.Ext(path)), "."),
		"kind":          kind,
		"lines_added":   added,
		"lines_deleted": deleted,
	}
	if wordsAdded > 0 || wordsDeleted > 0 {
		payload["words_added"] = wordsAdded
		payload["words_deleted"] = wordsDeleted
	}
	return payload
}

func gitDocWordDiff(ctx context.Context, root, path string) (int64, int64) {
	out, err := commandOutput(ctx, "git", "-C", root, "diff", "--word-diff=porcelain", "--no-color", "HEAD", "--", path)
	if err != nil {
		return 0, 0
	}
	var added, deleted int64
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "+++") || strings.HasPrefix(line, "---") || line == "" {
			continue
		}
		switch line[0] {
		case '+':
			added += countWords(line[1:])
		case '-':
			deleted += countWords(line[1:])
		}
	}
	return added, deleted
}

func gitUntrackedFiles(ctx context.Context, root string) []string {
	out, err := commandOutput(ctx, "git", "-C", root, "ls-files", "--others", "--exclude-standard", "-z")
	if err != nil {
		return nil
	}
	var files []string
	for _, path := range strings.Split(out, "\x00") {
		path = strings.TrimSpace(path)
		if path != "" {
			files = append(files, path)
		}
	}
	return files
}

func countWorktreeFile(root, path string) (int64, int64) {
	file, err := os.Open(filepath.Join(root, path))
	if err != nil {
		return 0, 0
	}
	defer file.Close()
	var lines, words int64
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 16*1024), 1024*1024)
	for scanner.Scan() {
		lines++
		words += countWords(scanner.Text())
	}
	return lines, words
}

func worktreePathChangedSince(root, path string, since time.Time) bool {
	if since.IsZero() {
		return true
	}
	info, err := os.Stat(filepath.Join(root, path))
	if err != nil {
		return false
	}
	return !info.ModTime().Before(since)
}

func worktreeFileHash(root, path string) string {
	data, err := os.ReadFile(filepath.Join(root, path))
	if err != nil {
		return ""
	}
	return util.HashString(string(data))
}

func parseOutputSummaryTime(value string) time.Time {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339} {
		if parsed, err := time.Parse(layout, value); err == nil {
			return parsed.UTC()
		}
	}
	return time.Time{}
}

func outputSummaryTimeString(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.UTC().Format(time.RFC3339Nano)
}

func fileKind(path string) string {
	lower := strings.ToLower(filepath.ToSlash(path))
	ext := strings.ToLower(filepath.Ext(lower))
	base := filepath.Base(lower)
	if strings.Contains(base, ".test.") || strings.Contains(base, ".spec.") || strings.HasSuffix(base, "_test.go") {
		return "test"
	}
	switch ext {
	case ".md", ".mdx", ".txt", ".rst", ".adoc":
		return "docs"
	case ".go", ".ts", ".tsx", ".js", ".jsx", ".py", ".rs", ".java", ".kt", ".swift", ".c", ".cc", ".cpp", ".h", ".hpp", ".cs", ".rb", ".php", ".sh", ".sql", ".css", ".scss", ".sass", ".less", ".html", ".vue", ".svelte":
		return "code"
	default:
		return ""
	}
}

func gitRepoRoot(ctx context.Context, cwd string) (string, bool) {
	root, err := commandOutput(ctx, "git", "-C", cwd, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", false
	}
	root = strings.TrimSpace(root)
	return root, root != ""
}

func gitHead(ctx context.Context, root string) string {
	head, _ := commandOutput(ctx, "git", "-C", root, "rev-parse", "HEAD")
	return strings.TrimSpace(head)
}

func parseOutputCount(value string) int64 {
	value = strings.TrimSpace(value)
	if value == "" || value == "-" {
		return 0
	}
	var out int64
	for _, r := range value {
		if r < '0' || r > '9' {
			return 0
		}
		out = out*10 + int64(r-'0')
	}
	return out
}

func countWords(value string) int64 {
	var count int64
	inWord := false
	for _, r := range value {
		isWord := unicode.IsLetter(r) || unicode.IsDigit(r)
		if isWord && !inWord {
			count++
		}
		inWord = isWord
	}
	return count
}

func outputCursorKey(sessionID, repoRoot string) string {
	return util.HashString(strings.Join([]string{sessionID, repoRoot}, "\x00"))
}

func loadOutputCursorStore(dataDir string) (outputCursorStore, error) {
	if dataDir == "" {
		dataDir = DefaultDataDir()
	}
	path := filepath.Join(dataDir, outputCursorFile)
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return outputCursorStore{Sessions: map[string]string{}}, nil
	}
	if err != nil {
		return outputCursorStore{}, err
	}
	var store outputCursorStore
	if err := json.Unmarshal(data, &store); err != nil {
		return outputCursorStore{}, err
	}
	if store.Sessions == nil {
		store.Sessions = map[string]string{}
	}
	return store, nil
}

func saveOutputCursorStore(dataDir string, store outputCursorStore) error {
	if dataDir == "" {
		dataDir = DefaultDataDir()
	}
	if store.Sessions == nil {
		store.Sessions = map[string]string{}
	}
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(store, "", "  ")
	if err != nil {
		return err
	}
	path := filepath.Join(dataDir, outputCursorFile)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
