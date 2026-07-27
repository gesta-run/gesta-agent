package daemon

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

var obsoleteOutputBaselineFiles = []string{
	"output-baselines.json",
	"output-baselines.json.tmp",
	"output-baselines.json.lock",
}

func cleanupDeprecatedState(dataDir string) (int64, error) {
	if dataDir == "" {
		dataDir = DefaultDataDir()
	}
	var removedBytes int64
	for _, name := range obsoleteOutputBaselineFiles {
		target := filepath.Join(dataDir, name)
		info, err := os.Lstat(target)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return removedBytes, fmt.Errorf("inspect deprecated state %s: %w", name, err)
		}
		if info.IsDir() {
			return removedBytes, fmt.Errorf("deprecated state %s is a directory", name)
		}
		if err := os.Remove(target); err != nil {
			return removedBytes, fmt.Errorf("remove deprecated state %s: %w", name, err)
		}
		removedBytes += info.Size()
	}
	return removedBytes, nil
}
