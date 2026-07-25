package turnreceipt

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func (s Store) CleanupExpired() error {
	root := s.rootPath()
	rootExists := true
	if _, err := os.Stat(root); errors.Is(err, os.ErrNotExist) {
		rootExists = false
	} else if err != nil {
		return fmt.Errorf("stat turn receipt root: %w", err)
	}

	unlock, acquired, err := s.tryCleanupLock()
	if err != nil {
		return err
	}
	if !acquired {
		return nil
	}
	defer unlock()

	now := s.now()
	if err := s.cleanupStaleLocks(now); err != nil {
		return err
	}
	if !rootExists {
		return s.clearCleanupCursor()
	}
	cursor := s.readCleanupCursorBestEffort()
	scan, err := scanCleanupCandidates(root, cursor, now)
	if err != nil {
		return err
	}
	for _, path := range scan.candidates {
		if err := s.removeExpiredDirectory(path, now); err != nil {
			return err
		}
	}
	if scan.complete {
		return s.clearCleanupCursor()
	}
	return s.writeCleanupCursor(scan.after)
}

func (s Store) cleanupStaleLocks(now time.Time) error {
	entries, err := os.ReadDir(s.lockRootPath())
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read turn receipt lock directory: %w", err)
	}
	visits := 0
	removals := 0
	for _, entry := range entries {
		if entry.IsDir() || entry.Name() == filepath.Base(s.cleanupLockPath()) {
			continue
		}
		visits++
		removed, err := removeStaleReceiptLock(
			filepath.Join(s.lockRootPath(), entry.Name()),
			now,
		)
		if err != nil {
			return err
		}
		if removed {
			removals++
		}
		if visits >= maxCleanupVisits || removals >= maxCleanupRemovals {
			break
		}
	}
	return nil
}

type cleanupScan struct {
	candidates []string
	after      string
	complete   bool
}

func scanCleanupCandidates(root, after string, now time.Time) (cleanupScan, error) {
	scan := cleanupScan{
		candidates: make([]string, 0, maxCleanupRemovals),
		complete:   true,
	}
	visits := 0
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil || !entry.IsDir() || path == root || !isReceiptDirectory(entry.Name()) {
			return nil
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return nil
		}
		if after != "" && relative <= after {
			return fs.SkipDir
		}
		expired := receiptDirectoryExpired(path, entry, now)
		if expired && len(scan.candidates) >= maxCleanupRemovals {
			scan.complete = false
			return fs.SkipAll
		}
		if expired {
			scan.candidates = append(scan.candidates, path)
		}
		scan.after = relative
		visits++
		if visits >= maxCleanupVisits {
			scan.complete = false
			return fs.SkipAll
		}
		return fs.SkipDir
	})
	if err != nil {
		return cleanupScan{}, fmt.Errorf("walk turn receipts: %w", err)
	}
	return scan, nil
}

func (s Store) removeExpiredDirectory(path string, now time.Time) error {
	if strings.Contains(filepath.Base(path), ".consuming-") {
		if receiptDirectoryExpired(path, nil, now) {
			if err := os.RemoveAll(path); err != nil {
				return fmt.Errorf("remove expired claimed turn receipt: %w", err)
			}
			pruneEmptyParents(filepath.Dir(path), s.rootPath())
		}
		return nil
	}
	unlock, acquired, err := s.tryReceiptLock(path)
	if err != nil || !acquired {
		return err
	}
	if !receiptDirectoryExpired(path, nil, now) {
		unlock()
		return nil
	}
	if err := os.RemoveAll(path); err != nil {
		unlock()
		return fmt.Errorf("remove expired turn receipt: %w", err)
	}
	unlock()
	pruneEmptyParents(filepath.Dir(path), s.rootPath())
	return nil
}

func receiptDirectoryExpired(path string, entry fs.DirEntry, now time.Time) bool {
	var receipt Receipt
	if err := readBoundedJSON(filepath.Join(path, "receipt.json"), &receipt); err == nil &&
		!receipt.ExpiresAt.IsZero() {
		return !receipt.ExpiresAt.After(now)
	}
	var (
		info fs.FileInfo
		err  error
	)
	if entry == nil {
		info, err = os.Stat(path)
	} else {
		info, err = entry.Info()
	}
	return err == nil && info.ModTime().Before(now.Add(-receiptTTL))
}

func isReceiptDirectory(name string) bool {
	return name == "active" ||
		name == "pending" ||
		strings.HasPrefix(name, "turn-") ||
		strings.HasPrefix(name, "active.consuming-") ||
		strings.HasPrefix(name, "pending.consuming-")
}
