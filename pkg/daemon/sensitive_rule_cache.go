package daemon

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/gesta-run/gesta-agent/pkg/model"
)

const sensitiveRuleCacheFile = "sensitive-rules.json"

type SensitiveRuleCache struct {
	SyncedAt time.Time             `json:"synced_at"`
	Rules    []model.SensitiveRule `json:"rules"`
}

func SensitiveRuleCachePath(dataDir string) string {
	if dataDir == "" {
		dataDir = DefaultDataDir()
	}
	return filepath.Join(dataDir, sensitiveRuleCacheFile)
}

func SaveSensitiveRuleCache(dataDir string, rules []model.SensitiveRule, syncedAt time.Time) error {
	if syncedAt.IsZero() {
		syncedAt = time.Now().UTC()
	}
	if rules == nil {
		rules = []model.SensitiveRule{}
	}
	path := SensitiveRuleCachePath(dataDir)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(SensitiveRuleCache{
		SyncedAt: syncedAt.UTC(),
		Rules:    rules,
	}, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func LoadSensitiveRuleCache(dataDir string) (SensitiveRuleCache, error) {
	data, err := os.ReadFile(SensitiveRuleCachePath(dataDir))
	if errors.Is(err, os.ErrNotExist) {
		return SensitiveRuleCache{Rules: []model.SensitiveRule{}}, err
	}
	if err != nil {
		return SensitiveRuleCache{}, err
	}
	var cache SensitiveRuleCache
	if err := json.Unmarshal(data, &cache); err != nil {
		return SensitiveRuleCache{}, err
	}
	if cache.Rules == nil {
		cache.Rules = []model.SensitiveRule{}
	}
	return cache, nil
}
