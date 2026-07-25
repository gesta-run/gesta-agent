package daemon

import (
	"errors"
	"os"
	"time"

	"github.com/gesta-run/gesta-agent/pkg/contextmatch"
	"github.com/gesta-run/gesta-agent/pkg/model"
)

const contextRuleCacheFile = "context-rules.json"

type ContextRuleCache struct {
	Version  string              `json:"version"`
	SyncedAt time.Time           `json:"synced_at"`
	Rules    []model.ContextRule `json:"rules"`
}

func ContextRuleCachePath(dataDir string) string {
	return ruleCachePath(dataDir, contextRuleCacheFile)
}

func SaveContextRuleCache(dataDir string, bundle model.ContextRuleBundle, syncedAt time.Time) error {
	bundle.Rules = nonNilRules(bundle.Rules)
	if err := contextmatch.ValidateRules(bundle.Rules); err != nil {
		return err
	}
	return saveRuleCache(ContextRuleCachePath(dataDir), ContextRuleCache{
		Version: bundle.Version, SyncedAt: ruleCacheSyncedAt(syncedAt), Rules: bundle.Rules,
	})
}

func LoadContextRuleCache(dataDir string) (ContextRuleCache, error) {
	var cache ContextRuleCache
	err := loadRuleCache(ContextRuleCachePath(dataDir), &cache)
	if errors.Is(err, os.ErrNotExist) {
		return ContextRuleCache{Rules: []model.ContextRule{}}, err
	}
	if err != nil {
		return ContextRuleCache{}, err
	}
	cache.Rules = nonNilRules(cache.Rules)
	if err := contextmatch.ValidateRules(cache.Rules); err != nil {
		return ContextRuleCache{}, err
	}
	return cache, nil
}
