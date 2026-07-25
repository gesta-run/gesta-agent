package atomicfile

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

const (
	parentMode = 0o700
	fileMode   = 0o600
)

// Write replaces path atomically with data after syncing the temporary file.
// Use Replace for high-frequency, recoverable state that only requires readers
// to observe complete files.
func Write(path string, data []byte) error {
	return replace(path, data, true)
}

// Replace replaces path atomically with data without forcing a storage flush.
// Temporary files have unique names so concurrent writers cannot remove or
// rename each other's work.
func Replace(path string, data []byte) error {
	return replace(path, data, false)
}

func replace(path string, data []byte, sync bool) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, parentMode); err != nil {
		return fmt.Errorf("create parent directory for %s: %w", path, err)
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+"-*.tmp")
	if err != nil {
		return fmt.Errorf("create temporary file for %s: %w", path, err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err := tmp.Chmod(fileMode); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("set temporary file permissions for %s: %w", path, err)
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write temporary file for %s: %w", path, err)
	}
	if sync {
		if err := tmp.Sync(); err != nil {
			_ = tmp.Close()
			return fmt.Errorf("sync temporary file for %s: %w", path, err)
		}
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temporary file for %s: %w", path, err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("replace %s: %w", path, err)
	}
	return nil
}

// WriteJSON marshals value as indented JSON with a trailing newline and
// atomically replaces path.
func WriteJSON(path string, value interface{}) error {
	return writeJSON(path, value, Write)
}

// ReplaceJSON marshals value as indented JSON with a trailing newline and
// atomically replaces path without forcing a storage flush.
func ReplaceJSON(path string, value interface{}) error {
	return writeJSON(path, value, Replace)
}

func writeJSON(path string, value interface{}, writer func(string, []byte) error) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal JSON for %s: %w", path, err)
	}
	return writer(path, append(data, '\n'))
}
