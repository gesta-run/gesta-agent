package daemon

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
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
	if dataDir == "" {
		dataDir = DefaultDataDir()
	}
	return filepath.Join(dataDir, contextRuleCacheFile)
}

func SaveContextRuleCache(dataDir string, bundle model.ContextRuleBundle, syncedAt time.Time) error {
	if syncedAt.IsZero() {
		syncedAt = time.Now().UTC()
	}
	if bundle.Rules == nil {
		bundle.Rules = []model.ContextRule{}
	}
	if err := contextmatch.ValidateRules(bundle.Rules); err != nil {
		return err
	}
	path := ContextRuleCachePath(dataDir)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(ContextRuleCache{
		Version: bundle.Version, SyncedAt: syncedAt.UTC(), Rules: bundle.Rules,
	}, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".context-rules-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}

func LoadContextRuleCache(dataDir string) (ContextRuleCache, error) {
	data, err := os.ReadFile(ContextRuleCachePath(dataDir))
	if errors.Is(err, os.ErrNotExist) {
		return ContextRuleCache{Rules: []model.ContextRule{}}, err
	}
	if err != nil {
		return ContextRuleCache{}, err
	}
	var cache ContextRuleCache
	if err := json.Unmarshal(data, &cache); err != nil {
		return ContextRuleCache{}, err
	}
	if cache.Rules == nil {
		cache.Rules = []model.ContextRule{}
	}
	if err := contextmatch.ValidateRules(cache.Rules); err != nil {
		return ContextRuleCache{}, err
	}
	return cache, nil
}
