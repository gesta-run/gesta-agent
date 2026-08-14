package rulecache

import (
	"errors"
	"os"
	"time"

	"github.com/gesta-run/gesta-agent/pkg/model"
)

const memorySettingsCacheFile = "memory-settings.json"

type MemorySettingsCache struct {
	SyncedAt time.Time `json:"synced_at"`
	model.MemorySettings
}

func MemorySettingsCachePath(dataDir string) string {
	return ruleCachePath(dataDir, memorySettingsCacheFile)
}

func SaveMemorySettingsCache(dataDir string, settings model.MemorySettings, syncedAt time.Time) error {
	return saveRuleCache(MemorySettingsCachePath(dataDir), MemorySettingsCache{
		SyncedAt: ruleCacheSyncedAt(syncedAt), MemorySettings: settings,
	})
}

func LoadMemorySettingsCache(dataDir string) (MemorySettingsCache, error) {
	var cache MemorySettingsCache
	err := loadRuleCache(MemorySettingsCachePath(dataDir), &cache)
	if errors.Is(err, os.ErrNotExist) {
		return MemorySettingsCache{}, err
	}
	if err != nil {
		return MemorySettingsCache{}, err
	}
	return cache, nil
}
