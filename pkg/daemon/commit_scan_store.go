package daemon

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/gesta-run/gesta-agent/pkg/util"
)

type commitRepoCursor struct {
	LastSHA   string `json:"last_sha"`
	ScannedAt string `json:"scanned_at"`
}

type commitCursorStore struct {
	Repos map[string]commitRepoCursor `json:"repos"`
}

// registerCommitScanRepo records a repo root for commit scanning. One file per
// repo, write-once: the hook process and the daemon can both register without a
// shared lock.
func registerCommitScanRepo(dataDir, root string) {
	root = strings.TrimSpace(root)
	if root == "" {
		return
	}
	if dataDir == "" {
		dataDir = DefaultDataDir()
	}
	dir := filepath.Join(dataDir, commitScanRepoDir)
	path := filepath.Join(dir, util.ShortHash(root)+".json")
	if _, err := os.Stat(path); err == nil {
		return
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return
	}
	data, err := json.Marshal(map[string]string{"root": root})
	if err != nil {
		return
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return
	}
	_ = os.Rename(tmp, path)
}

func registeredCommitScanRepos(dataDir string) []string {
	if dataDir == "" {
		dataDir = DefaultDataDir()
	}
	entries, err := os.ReadDir(filepath.Join(dataDir, commitScanRepoDir))
	if err != nil {
		return nil
	}
	var roots []string
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dataDir, commitScanRepoDir, entry.Name()))
		if err != nil {
			continue
		}
		var record struct {
			Root string `json:"root"`
		}
		if err := json.Unmarshal(data, &record); err != nil {
			continue
		}
		if root := strings.TrimSpace(record.Root); root != "" {
			roots = append(roots, root)
		}
	}
	sort.Strings(roots)
	return roots
}

func loadCommitCursorStore(dataDir string) (commitCursorStore, error) {
	if dataDir == "" {
		dataDir = DefaultDataDir()
	}
	path := filepath.Join(dataDir, commitCursorFile)
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return commitCursorStore{Repos: map[string]commitRepoCursor{}}, nil
	}
	if err != nil {
		return commitCursorStore{}, err
	}
	var store commitCursorStore
	if err := json.Unmarshal(data, &store); err != nil {
		return commitCursorStore{}, err
	}
	if store.Repos == nil {
		store.Repos = map[string]commitRepoCursor{}
	}
	return store, nil
}

func saveCommitCursorStore(dataDir string, store commitCursorStore) error {
	if dataDir == "" {
		dataDir = DefaultDataDir()
	}
	if store.Repos == nil {
		store.Repos = map[string]commitRepoCursor{}
	}
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(store, "", "  ")
	if err != nil {
		return err
	}
	path := filepath.Join(dataDir, commitCursorFile)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
