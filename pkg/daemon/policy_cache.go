package daemon

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/gesta-run/gesta-agent/pkg/model"
)

const policyCacheFile = "policies.json"

type PolicyCache struct {
	SyncedAt time.Time          `json:"synced_at"`
	Rules    []model.PolicyRule `json:"rules"`
}

func PolicyCachePath(dataDir string) string {
	if dataDir == "" {
		dataDir = DefaultDataDir()
	}
	return filepath.Join(dataDir, policyCacheFile)
}

func SavePolicyCache(dataDir string, rules []model.PolicyRule, syncedAt time.Time) error {
	if syncedAt.IsZero() {
		syncedAt = time.Now().UTC()
	}
	if rules == nil {
		rules = []model.PolicyRule{}
	}
	path := PolicyCachePath(dataDir)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(PolicyCache{
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

func LoadPolicyCache(dataDir string) (PolicyCache, error) {
	data, err := os.ReadFile(PolicyCachePath(dataDir))
	if errors.Is(err, os.ErrNotExist) {
		return PolicyCache{Rules: []model.PolicyRule{}}, err
	}
	if err != nil {
		return PolicyCache{}, err
	}
	var cache PolicyCache
	if err := json.Unmarshal(data, &cache); err != nil {
		return PolicyCache{}, err
	}
	if cache.Rules == nil {
		cache.Rules = []model.PolicyRule{}
	}
	return cache, nil
}
