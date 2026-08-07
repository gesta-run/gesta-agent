//go:build windows

package atomicfile

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReplaceOverwritesExistingFileOnWindows(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	if err := os.WriteFile(path, []byte("old"), 0o600); err != nil {
		t.Fatalf("write old file: %v", err)
	}
	if err := Replace(path, []byte("new")); err != nil {
		t.Fatalf("Replace: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read replaced file: %v", err)
	}
	if string(data) != "new" {
		t.Fatalf("replaced data = %q, want new", data)
	}
}
