package daemon

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/gesta-run/gesta-agent/pkg/util"
)

const (
	outputBaselineFile = "output-baselines.json"
	// Keep a session baseline for the full dashboard reporting window, with a
	// little slack for collection delay. Expiring it earlier drops coverage for
	// long-running sessions before their output can be attributed correctly.
	outputBaselineTTL               = 35 * 24 * time.Hour
	outputSnapshotMaxFileBytes      = 2 * 1024 * 1024
	outputSnapshotExactDiffMaxCells = 4_000_000
)

type outputBaselineStore struct {
	Sessions map[string]outputBaseline `json:"sessions"`
}

type outputBaseline struct {
	SessionID    string                        `json:"session_id"`
	RepoHash     string                        `json:"repo_hash"`
	RepoRootHash string                        `json:"repo_root_hash"`
	GitSHABefore string                        `json:"git_sha_before"`
	CapturedAt   string                        `json:"captured_at"`
	Files        map[string]outputFileSnapshot `json:"files"`
}

type outputFileSnapshot struct {
	Kind  string   `json:"kind"`
	Lines []string `json:"lines,omitempty"`
	Words []string `json:"words,omitempty"`
}

// CaptureOutputBaseline records the worktree state at the first hook signal for
// a session. It stores hashed line/word sequences only; source text is not
// persisted in Gesta's local state.
func CaptureOutputBaseline(ctx context.Context, cfg Config, cwd, sessionID string) error {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" || strings.TrimSpace(cwd) == "" {
		return nil
	}
	if cfg.DataDir == "" {
		cfg.DataDir = DefaultDataDir()
	}
	baseline, ok := outputBaselineFromWorktree(ctx, cwd, sessionID, time.Now().UTC())
	if !ok {
		return nil
	}
	if root, ok := gitRepoRoot(ctx, cwd); ok {
		registerCommitScanRepo(cfg.DataDir, root)
	}
	key := outputCursorKey(sessionID, baseline.RepoHash)
	return withOutputStateLock(cfg.DataDir, outputBaselineFile, func() error {
		store, err := loadOutputBaselineStore(cfg.DataDir)
		if err != nil {
			return err
		}
		if store.Sessions == nil {
			store.Sessions = map[string]outputBaseline{}
		}
		pruneOutputBaselines(store, time.Now().UTC())
		if _, exists := store.Sessions[key]; exists {
			return saveOutputBaselineStore(cfg.DataDir, store)
		}
		store.Sessions[key] = baseline
		return saveOutputBaselineStore(cfg.DataDir, store)
	})
}

func outputBaselineFromWorktree(ctx context.Context, cwd, sessionID string, capturedAt time.Time) (outputBaseline, bool) {
	root, ok := gitRepoRoot(ctx, cwd)
	if !ok {
		return outputBaseline{}, false
	}
	head := gitHead(ctx, root)
	repoHash := util.ShortHash(root)
	return outputBaseline{
		SessionID:    sessionID,
		RepoHash:     repoHash,
		RepoRootHash: repoHash,
		GitSHABefore: head,
		CapturedAt:   capturedAt.UTC().Format(time.RFC3339Nano),
		Files:        captureOutputFileSnapshots(ctx, root),
	}, true
}

func loadOutputBaseline(dataDir, sessionID, repoHash string) (outputBaseline, bool) {
	store, err := loadOutputBaselineStore(dataDir)
	if err != nil || store.Sessions == nil {
		return outputBaseline{}, false
	}
	baseline, ok := store.Sessions[outputCursorKey(sessionID, repoHash)]
	if !ok || !outputBaselineFresh(baseline, time.Now().UTC()) {
		return outputBaseline{}, false
	}
	return baseline, ok
}

func outputBaselineFresh(baseline outputBaseline, now time.Time) bool {
	capturedAt, err := time.Parse(time.RFC3339Nano, baseline.CapturedAt)
	return err == nil && !capturedAt.Before(now.Add(-outputBaselineTTL))
}

func loadOutputBaselineStore(dataDir string) (outputBaselineStore, error) {
	path := outputStatePath(dataDir, outputBaselineFile)
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return outputBaselineStore{Sessions: map[string]outputBaseline{}}, nil
	}
	if err != nil {
		return outputBaselineStore{}, err
	}
	var store outputBaselineStore
	if err := json.Unmarshal(data, &store); err != nil {
		return outputBaselineStore{}, err
	}
	if store.Sessions == nil {
		store.Sessions = map[string]outputBaseline{}
	}
	return store, nil
}

func saveOutputBaselineStore(dataDir string, store outputBaselineStore) error {
	path := outputStatePath(dataDir, outputBaselineFile)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(store, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func pruneOutputBaselines(store outputBaselineStore, now time.Time) {
	for key, baseline := range store.Sessions {
		if !outputBaselineFresh(baseline, now) {
			delete(store.Sessions, key)
		}
	}
}

func outputStatePath(dataDir, name string) string {
	if dataDir == "" {
		dataDir = DefaultDataDir()
	}
	return filepath.Join(dataDir, name)
}

func withOutputStateLock(dataDir, name string, fn func() error) error {
	path := outputStatePath(dataDir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	lockPath := path + ".lock"
	deadline := time.Now().Add(5 * time.Second)
	for {
		lock, err := os.OpenFile(lockPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err == nil {
			_ = lock.Close()
			defer os.Remove(lockPath)
			return fn()
		}
		if !errors.Is(err, os.ErrExist) {
			return err
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("output state lock timeout: %s", lockPath)
		}
		time.Sleep(25 * time.Millisecond)
	}
}

func captureOutputFileSnapshots(ctx context.Context, root string) map[string]outputFileSnapshot {
	paths := gitSnapshotPaths(ctx, root)
	files := map[string]outputFileSnapshot{}
	for _, path := range paths {
		path = filepath.ToSlash(strings.TrimSpace(path))
		if path == "" || outputPathExcluded(path) {
			continue
		}
		kind := fileKind(path)
		if kind == "" {
			continue
		}
		if snapshot, ok := outputFileSnapshotFromPath(root, path, kind); ok {
			files[path] = snapshot
		}
	}
	return files
}

func gitSnapshotPaths(ctx context.Context, root string) []string {
	out, err := commandOutput(ctx, "git", "-C", root, "ls-files", "-z", "--cached", "--others", "--exclude-standard")
	if err != nil {
		return nil
	}
	seen := map[string]struct{}{}
	var paths []string
	for _, path := range strings.Split(out, "\x00") {
		path = strings.TrimSpace(path)
		if path == "" {
			continue
		}
		if _, ok := seen[path]; ok {
			continue
		}
		seen[path] = struct{}{}
		paths = append(paths, path)
	}
	sort.Strings(paths)
	return paths
}

func outputFileSnapshotFromPath(root, path, kind string) (outputFileSnapshot, bool) {
	fullPath := filepath.Join(root, filepath.FromSlash(path))
	info, err := os.Stat(fullPath)
	if err != nil || info.IsDir() || info.Size() > outputSnapshotMaxFileBytes {
		return outputFileSnapshot{}, false
	}
	file, err := os.Open(fullPath)
	if err != nil {
		return outputFileSnapshot{}, false
	}
	defer file.Close()

	snapshot := outputFileSnapshot{Kind: kind}
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 16*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.ContainsRune(line, '\x00') {
			return outputFileSnapshot{}, false
		}
		snapshot.Lines = append(snapshot.Lines, outputHashToken("line", line))
		if kind == "docs" {
			snapshot.Words = append(snapshot.Words, outputWordHashes(line)...)
		}
	}
	if scanner.Err() != nil {
		return outputFileSnapshot{}, false
	}
	return snapshot, true
}

func outputPathExcluded(path string) bool {
	lower := strings.ToLower(filepath.ToSlash(path))
	parts := strings.Split(lower, "/")
	for _, part := range parts {
		switch part {
		case "", ".", ".git", ".next", ".nuxt", ".svelte-kit", "node_modules", "vendor", "dist", "build", "coverage", ".cache", ".turbo", "target":
			return true
		}
	}
	base := filepath.Base(lower)
	switch base {
	case "package-lock.json", "pnpm-lock.yaml", "yarn.lock", "go.sum", "cargo.lock":
		return true
	default:
		return false
	}
}

func outputWordHashes(value string) []string {
	var words []string
	var current strings.Builder
	flush := func() {
		if current.Len() == 0 {
			return
		}
		words = append(words, outputHashToken("word", strings.ToLower(current.String())))
		current.Reset()
	}
	for _, r := range value {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			current.WriteRune(r)
			continue
		}
		flush()
	}
	flush()
	return words
}

func outputHashToken(kind, value string) string {
	return util.HashString(kind + "\x00" + value)
}

func outputSequenceDiff(before, after []string) (int64, int64) {
	if len(before) == 0 {
		return int64(len(after)), 0
	}
	if len(after) == 0 {
		return 0, int64(len(before))
	}
	if int64(len(before))*int64(len(after)) <= outputSnapshotExactDiffMaxCells {
		lcs := outputLCSLength(before, after)
		return int64(len(after) - lcs), int64(len(before) - lcs)
	}
	return outputMultisetDiff(before, after)
}

func outputLCSLength(before, after []string) int {
	prev := make([]int, len(after)+1)
	curr := make([]int, len(after)+1)
	for _, left := range before {
		curr[0] = 0
		for j, right := range after {
			if left == right {
				curr[j+1] = prev[j] + 1
			} else if prev[j+1] > curr[j] {
				curr[j+1] = prev[j+1]
			} else {
				curr[j+1] = curr[j]
			}
		}
		prev, curr = curr, prev
	}
	return prev[len(after)]
}

func outputMultisetDiff(before, after []string) (int64, int64) {
	counts := map[string]int{}
	for _, token := range before {
		counts[token]++
	}
	var added int64
	for _, token := range after {
		if counts[token] > 0 {
			counts[token]--
			continue
		}
		added++
	}
	var deleted int64
	for _, count := range counts {
		if count > 0 {
			deleted += int64(count)
		}
	}
	return added, deleted
}
