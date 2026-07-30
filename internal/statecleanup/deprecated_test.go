package statecleanup

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestCleanupDeprecatedStateRemovesOnlyOutputBaselineFiles(t *testing.T) {
	dataDir := t.TempDir()
	var wantBytes int64
	for _, name := range obsoleteOutputBaselineFiles {
		content := []byte("obsolete")
		if err := os.WriteFile(filepath.Join(dataDir, name), content, 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
		wantBytes += int64(len(content))
	}
	keepPath := filepath.Join(dataDir, "state.json")
	if err := os.WriteFile(keepPath, []byte("keep"), 0o600); err != nil {
		t.Fatalf("write retained state: %v", err)
	}

	removedBytes, err := CleanupDeprecatedState(dataDir)
	if err != nil {
		t.Fatalf("CleanupDeprecatedState: %v", err)
	}
	if removedBytes != wantBytes {
		t.Fatalf("removed bytes = %d, want %d", removedBytes, wantBytes)
	}
	for _, name := range obsoleteOutputBaselineFiles {
		if _, err := os.Stat(filepath.Join(dataDir, name)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("%s still exists: %v", name, err)
		}
	}
	if _, err := os.Stat(keepPath); err != nil {
		t.Fatalf("retained state missing: %v", err)
	}

	removedBytes, err = CleanupDeprecatedState(dataDir)
	if err != nil || removedBytes != 0 {
		t.Fatalf("idempotent cleanup = (%d, %v), want (0, nil)", removedBytes, err)
	}
}
