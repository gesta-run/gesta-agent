package rulecache

import (
	"errors"
	"os"
	"time"

	"github.com/gesta-run/gesta-agent/pkg/model"
)

const sensitiveRuleCacheFile = "sensitive-rules.json"

type SensitiveRuleCache struct {
	SyncedAt time.Time             `json:"synced_at"`
	Rules    []model.SensitiveRule `json:"rules"`
}

func SensitiveRuleCachePath(dataDir string) string {
	return ruleCachePath(dataDir, sensitiveRuleCacheFile)
}

func SaveSensitiveRuleCache(dataDir string, rules []model.SensitiveRule, syncedAt time.Time) error {
	return saveRuleCache(SensitiveRuleCachePath(dataDir), SensitiveRuleCache{
		SyncedAt: ruleCacheSyncedAt(syncedAt),
		Rules:    nonNilRules(rules),
	})
}

func LoadSensitiveRuleCache(dataDir string) (SensitiveRuleCache, error) {
	var cache SensitiveRuleCache
	err := loadRuleCache(SensitiveRuleCachePath(dataDir), &cache)
	if errors.Is(err, os.ErrNotExist) {
		return SensitiveRuleCache{Rules: []model.SensitiveRule{}}, err
	}
	if err != nil {
		return SensitiveRuleCache{}, err
	}
	cache.Rules = nonNilRules(cache.Rules)
	return cache, nil
}
