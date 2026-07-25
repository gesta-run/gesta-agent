package turnreceipt

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const cleanupCursorSchemaVersion = 1

type cleanupCursor struct {
	SchemaVersion int    `json:"schema_version"`
	After         string `json:"after"`
}

func (s Store) tryCleanupLock() (func(), bool, error) {
	unlock, err := acquireReceiptLock(s.cleanupLockPath(), false)
	if errors.Is(err, os.ErrExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return unlock, true, nil
}

func (s Store) readCleanupCursorBestEffort() string {
	var cursor cleanupCursor
	if err := readBoundedJSON(s.cleanupCursorPath(), &cursor); err != nil {
		return ""
	}
	if cursor.SchemaVersion != cleanupCursorSchemaVersion {
		return ""
	}
	return strings.TrimSpace(cursor.After)
}

func (s Store) writeCleanupCursor(after string) error {
	after = strings.TrimSpace(after)
	if after == "" {
		return s.clearCleanupCursor()
	}
	if err := atomicWriteJSON(s.cleanupCursorPath(), cleanupCursor{
		SchemaVersion: cleanupCursorSchemaVersion,
		After:         after,
	}); err != nil {
		return fmt.Errorf("write turn receipt cleanup cursor: %w", err)
	}
	return nil
}

func (s Store) clearCleanupCursor() error {
	if err := os.Remove(s.cleanupCursorPath()); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove turn receipt cleanup cursor: %w", err)
	}
	return nil
}

func (s Store) cleanupCursorPath() string {
	return filepath.Join(s.dataDir, "runtime", "turn-receipt-cleanup.json")
}

func (s Store) cleanupLockPath() string {
	return filepath.Join(s.lockRootPath(), "cleanup.lock")
}
