package rulecache

import (
	"errors"
	"os"
	"time"

	"github.com/gesta-run/gesta-agent/pkg/model"
)

const policyCacheFile = "policies.json"

type PolicyCache struct {
	SyncedAt time.Time          `json:"synced_at"`
	Rules    []model.PolicyRule `json:"rules"`
}

func PolicyCachePath(dataDir string) string {
	return ruleCachePath(dataDir, policyCacheFile)
}

func SavePolicyCache(dataDir string, rules []model.PolicyRule, syncedAt time.Time) error {
	return saveRuleCache(PolicyCachePath(dataDir), PolicyCache{
		SyncedAt: ruleCacheSyncedAt(syncedAt),
		Rules:    nonNilRules(rules),
	})
}

func LoadPolicyCache(dataDir string) (PolicyCache, error) {
	var cache PolicyCache
	err := loadRuleCache(PolicyCachePath(dataDir), &cache)
	if errors.Is(err, os.ErrNotExist) {
		return PolicyCache{Rules: []model.PolicyRule{}}, err
	}
	if err != nil {
		return PolicyCache{}, err
	}
	cache.Rules = nonNilRules(cache.Rules)
	return cache, nil
}
