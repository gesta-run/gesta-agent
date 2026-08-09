package daemon

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	turnusage "github.com/gesta-run/gesta-agent/pkg/turn"
)

type codexRolloutCacheEntry struct {
	size         int64
	modifiedAt   int64
	titlePending bool
	retryPending bool
	session      turnusage.CodexSession
}

var codexRolloutCache = struct {
	sync.Mutex
	entries          map[string]codexRolloutCacheEntry
	directories      map[string]int64
	initializedRoots map[string]bool
}{
	entries:          map[string]codexRolloutCacheEntry{},
	directories:      map[string]int64{},
	initializedRoots: map[string]bool{},
}

func discoverCodexTurnSessions(root string) ([]turnusage.CodexSession, error) {
	return discoverCodexTurnSessionsWithTitles(root, codexSessionIndexTitles(codexSessionIndexPath()))
}

func discoverCodexTurnSessionsWithTitles(root string, titles map[string]string) ([]turnusage.CodexSession, error) {
	root = filepath.Clean(strings.TrimSpace(root))
	if root == "." || root == "" {
		return nil, nil
	}
	if _, err := os.Stat(root); errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	var discoveryErrors []error
	refreshPendingCodexRollouts(root, titles, &discoveryErrors)
	for _, scanRoot := range changedCodexDiscoveryDirectories(root) {
		discoveryErrors = append(discoveryErrors, scanCodexDiscoveryDirectory(root, scanRoot)...)
	}
	return cachedCodexTurnSessions(root, titles), errors.Join(discoveryErrors...)
}

func changedCodexDiscoveryDirectories(root string) []string {
	codexRolloutCache.Lock()
	initialized := codexRolloutCache.initializedRoots[root]
	directories := make(map[string]int64)
	for path, modifiedAt := range codexRolloutCache.directories {
		if pathWithin(root, path) {
			directories[path] = modifiedAt
		}
	}
	codexRolloutCache.Unlock()
	if !initialized || len(directories) == 0 {
		return []string{root}
	}
	var changed []string
	for path, modifiedAt := range directories {
		info, err := os.Stat(path)
		if err != nil || info.ModTime().UnixNano() != modifiedAt {
			changed = append(changed, path)
		}
	}
	sort.Slice(changed, func(i, j int) bool {
		return len(changed[i]) < len(changed[j])
	})
	selected := make([]string, 0, len(changed))
	for _, candidate := range changed {
		covered := false
		for _, parent := range selected {
			if pathWithin(parent, candidate) {
				covered = true
				break
			}
		}
		if !covered {
			selected = append(selected, candidate)
		}
	}
	return selected
}

func scanCodexDiscoveryDirectory(root, scanRoot string) []error {
	if _, err := os.Stat(scanRoot); errors.Is(err, os.ErrNotExist) {
		removeCodexDiscoverySubtree(scanRoot)
		return nil
	}
	seenFiles := map[string]struct{}{}
	seenDirectories := map[string]int64{}
	var discoveryErrors []error
	scanComplete := true
	walkErr := filepath.WalkDir(scanRoot, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			scanComplete = false
			discoveryErrors = append(discoveryErrors, fmt.Errorf("%s: %w", path, err))
			return nil
		}
		info, infoErr := entry.Info()
		if infoErr != nil {
			scanComplete = false
			discoveryErrors = append(discoveryErrors, fmt.Errorf("%s: %w", path, infoErr))
			return nil
		}
		if entry.IsDir() {
			seenDirectories[path] = info.ModTime().UnixNano()
			return nil
		}
		if !strings.EqualFold(filepath.Ext(path), ".jsonl") {
			return nil
		}
		seenFiles[path] = struct{}{}
		if _, _, parseErr := cachedCodexTurnSession(path, info); parseErr != nil {
			discoveryErrors = append(discoveryErrors, fmt.Errorf("%s: %w", path, parseErr))
		}
		return nil
	})
	if walkErr != nil {
		scanComplete = false
		discoveryErrors = append(discoveryErrors, walkErr)
	}
	updateCodexDiscoverySubtree(root, scanRoot, seenFiles, seenDirectories, scanComplete)
	return discoveryErrors
}

func cachedCodexTurnSession(path string, info fs.FileInfo) (turnusage.CodexSession, bool, error) {
	codexRolloutCache.Lock()
	entry, found := codexRolloutCache.entries[path]
	codexRolloutCache.Unlock()
	if found && entry.size == info.Size() && entry.modifiedAt == info.ModTime().UnixNano() && !entry.retryPending {
		return entry.session, true, nil
	}
	if found && info.Size() > entry.size && !entry.titlePending && !entry.retryPending {
		entry.size = info.Size()
		entry.modifiedAt = info.ModTime().UnixNano()
		codexRolloutCache.Lock()
		codexRolloutCache.entries[path] = entry
		codexRolloutCache.Unlock()
		return entry.session, true, nil
	}
	session, ok, titlePending, err := readCodexTurnSession(path)
	if err != nil || !ok {
		codexRolloutCache.Lock()
		codexRolloutCache.entries[path] = codexRolloutCacheEntry{
			size:         info.Size(),
			modifiedAt:   info.ModTime().UnixNano(),
			retryPending: true,
		}
		codexRolloutCache.Unlock()
		return turnusage.CodexSession{}, ok, err
	}
	codexRolloutCache.Lock()
	codexRolloutCache.entries[path] = codexRolloutCacheEntry{
		size:         info.Size(),
		modifiedAt:   info.ModTime().UnixNano(),
		titlePending: titlePending,
		session:      session,
	}
	codexRolloutCache.Unlock()
	return session, true, nil
}

func refreshPendingCodexRollouts(root string, titles map[string]string, discoveryErrors *[]error) {
	type pendingEntry struct {
		path  string
		entry codexRolloutCacheEntry
	}
	var pending []pendingEntry
	codexRolloutCache.Lock()
	for path, entry := range codexRolloutCache.entries {
		needsTitle := entry.titlePending && strings.TrimSpace(titles[entry.session.SessionID]) == ""
		if pathWithin(root, path) && (entry.retryPending || needsTitle) {
			pending = append(pending, pendingEntry{path: path, entry: entry})
		}
	}
	codexRolloutCache.Unlock()
	for _, candidate := range pending {
		info, err := os.Stat(candidate.path)
		if err != nil {
			if !errors.Is(err, os.ErrNotExist) {
				*discoveryErrors = append(*discoveryErrors, fmt.Errorf("%s: %w", candidate.path, err))
			}
			continue
		}
		if !candidate.entry.retryPending && info.Size() == candidate.entry.size && info.ModTime().UnixNano() == candidate.entry.modifiedAt {
			continue
		}
		if _, _, err := cachedCodexTurnSession(candidate.path, info); err != nil {
			*discoveryErrors = append(*discoveryErrors, fmt.Errorf("%s: %w", candidate.path, err))
		}
	}
}

func cachedCodexTurnSessions(root string, titles map[string]string) []turnusage.CodexSession {
	byID := map[string]codexRolloutCacheEntry{}
	codexRolloutCache.Lock()
	for path, entry := range codexRolloutCache.entries {
		if !pathWithin(root, path) || entry.retryPending || entry.session.SessionID == "" {
			continue
		}
		entry.session.RolloutPath = path
		if title := strings.TrimSpace(titles[entry.session.SessionID]); title != "" {
			entry.session.Title = title
		}
		existing, found := byID[entry.session.SessionID]
		if !found || entry.modifiedAt > existing.modifiedAt ||
			(entry.modifiedAt == existing.modifiedAt && entry.size > existing.size) {
			byID[entry.session.SessionID] = entry
		}
	}
	codexRolloutCache.Unlock()
	sessions := make([]turnusage.CodexSession, 0, len(byID))
	for _, entry := range byID {
		sessions = append(sessions, entry.session)
	}
	sort.Slice(sessions, func(i, j int) bool { return sessions[i].RolloutPath < sessions[j].RolloutPath })
	return sessions
}

func updateCodexDiscoverySubtree(root, scanRoot string, seenFiles map[string]struct{}, seenDirectories map[string]int64, prune bool) {
	codexRolloutCache.Lock()
	defer codexRolloutCache.Unlock()
	if !prune {
		return
	}
	for path := range codexRolloutCache.entries {
		if pathWithin(scanRoot, path) {
			if _, ok := seenFiles[path]; !ok {
				delete(codexRolloutCache.entries, path)
			}
		}
	}
	for path := range codexRolloutCache.directories {
		if pathWithin(scanRoot, path) {
			if _, ok := seenDirectories[path]; !ok {
				delete(codexRolloutCache.directories, path)
			}
		}
	}
	for path, modifiedAt := range seenDirectories {
		codexRolloutCache.directories[path] = modifiedAt
	}
	if scanRoot == root {
		codexRolloutCache.initializedRoots[root] = true
	}
}

func removeCodexDiscoverySubtree(root string) {
	codexRolloutCache.Lock()
	defer codexRolloutCache.Unlock()
	for path := range codexRolloutCache.entries {
		if pathWithin(root, path) {
			delete(codexRolloutCache.entries, path)
		}
	}
	for path := range codexRolloutCache.directories {
		if pathWithin(root, path) {
			delete(codexRolloutCache.directories, path)
		}
	}
}

func pathWithin(root, path string) bool {
	relative, err := filepath.Rel(root, path)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}
