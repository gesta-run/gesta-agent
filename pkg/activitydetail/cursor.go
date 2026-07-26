package activitydetail

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/gesta-run/gesta-agent/internal/atomicfile"
)

const cursorSchemaVersion = 1

type cleanupCursor struct {
	SchemaVersion int    `json:"schema_version"`
	After         string `json:"after"`
}

func (s Store) readCursor() string {
	file, err := os.Open(s.cursorPath())
	if err != nil {
		return ""
	}
	defer file.Close()
	var cursor cleanupCursor
	if err := json.NewDecoder(io.LimitReader(file, 1024)).Decode(&cursor); err != nil ||
		cursor.SchemaVersion != cursorSchemaVersion {
		return ""
	}
	return strings.TrimSpace(cursor.After)
}

func (s Store) writeCursor(after string) error {
	after = strings.TrimSpace(after)
	if after == "" {
		return s.clearCursor()
	}
	return atomicfile.ReplaceJSON(s.cursorPath(), cleanupCursor{
		SchemaVersion: cursorSchemaVersion,
		After:         after,
	})
}

func (s Store) clearCursor() error {
	if err := os.Remove(s.cursorPath()); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func (s Store) cursorPath() string {
	return filepath.Join(s.dataDir, "runtime", "activity-details-cleanup.json")
}
