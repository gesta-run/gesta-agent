package rulecache

import (
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/gesta-run/gesta-agent/pkg/model"
)

const outputClassificationCacheFile = "output-classification.json"

type OutputClassificationCache struct {
	SyncedAt      time.Time `json:"synced_at"`
	Revision      int64     `json:"revision"`
	CodeSuffixes  []string  `json:"code_suffixes"`
	CodeFilenames []string  `json:"code_filenames"`
}

func OutputClassificationCachePath(dataDir string) string {
	return ruleCachePath(dataDir, outputClassificationCacheFile)
}

func SaveOutputClassificationCache(dataDir string, settings model.OutputClassificationSettings, syncedAt time.Time) error {
	if settings.Revision < 0 {
		return fmt.Errorf("output classification revision cannot be negative")
	}
	return saveRuleCache(OutputClassificationCachePath(dataDir), OutputClassificationCache{
		SyncedAt:      ruleCacheSyncedAt(syncedAt),
		Revision:      settings.Revision,
		CodeSuffixes:  nonNilRules(settings.CodeSuffixes),
		CodeFilenames: nonNilRules(settings.CodeFilenames),
	})
}

func LoadOutputClassificationCache(dataDir string) (OutputClassificationCache, error) {
	var cache OutputClassificationCache
	err := loadRuleCache(OutputClassificationCachePath(dataDir), &cache)
	if errors.Is(err, os.ErrNotExist) {
		return OutputClassificationCache{CodeSuffixes: []string{}, CodeFilenames: []string{}}, err
	}
	if err != nil {
		return OutputClassificationCache{}, err
	}
	if cache.Revision < 0 {
		return OutputClassificationCache{}, fmt.Errorf("output classification revision cannot be negative")
	}
	cache.CodeSuffixes = nonNilRules(cache.CodeSuffixes)
	cache.CodeFilenames = nonNilRules(cache.CodeFilenames)
	return cache, nil
}
