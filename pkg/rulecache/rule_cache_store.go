package rulecache

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"github.com/gesta-run/gesta-agent/pkg/atomicfile"
)

func ruleCachePath(dataDir, fileName string) string {
	if dataDir == "" {
		if home, err := os.UserHomeDir(); err == nil {
			dataDir = filepath.Join(home, ".gesta")
		} else {
			dataDir = ".gesta"
		}
	}
	return filepath.Join(dataDir, fileName)
}

func ruleCacheSyncedAt(value time.Time) time.Time {
	if value.IsZero() {
		value = time.Now().UTC()
	}
	return value.UTC()
}

func nonNilRules[T any](rules []T) []T {
	if rules == nil {
		return []T{}
	}
	return rules
}

func saveRuleCache(path string, cache interface{}) error {
	return atomicfile.WriteJSON(path, cache)
}

func loadRuleCache(path string, cache interface{}) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, cache)
}
