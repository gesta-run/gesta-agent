package statecleanup

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestCleanupDeprecatedStateRemovesObsoleteProtocolState(t *testing.T) {
	dataDir := t.TempDir()
	var wantBytes int64
	for _, name := range deprecatedStateFiles {
		content := []byte("obsolete")
		if err := os.WriteFile(filepath.Join(dataDir, name), content, 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
		wantBytes += int64(len(content))
	}
	retainedNames := []string{"queue-v3.db", "queue.jsonl", "state.json"}
	for _, name := range retainedNames {
		if err := os.WriteFile(filepath.Join(dataDir, name), []byte("keep"), 0o600); err != nil {
			t.Fatalf("write retained state %s: %v", name, err)
		}
	}

	removedBytes, err := CleanupDeprecatedState(dataDir)
	if err != nil {
		t.Fatalf("CleanupDeprecatedState: %v", err)
	}
	if removedBytes != wantBytes {
		t.Fatalf("removed bytes = %d, want %d", removedBytes, wantBytes)
	}
	for _, name := range deprecatedStateFiles {
		if _, err := os.Stat(filepath.Join(dataDir, name)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("%s still exists: %v", name, err)
		}
	}
	for _, name := range retainedNames {
		if _, err := os.Stat(filepath.Join(dataDir, name)); err != nil {
			t.Fatalf("retained state %s missing: %v", name, err)
		}
	}

	removedBytes, err = CleanupDeprecatedState(dataDir)
	if err != nil || removedBytes != 0 {
		t.Fatalf("idempotent cleanup = (%d, %v), want (0, nil)", removedBytes, err)
	}
}
