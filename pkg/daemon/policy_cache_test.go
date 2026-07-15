package daemon

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/gesta-run/gesta-agent/pkg/model"
)

func TestPolicyCacheRoundTrip(t *testing.T) {
	dataDir := t.TempDir()
	syncedAt := time.Date(2026, 6, 23, 8, 0, 0, 0, time.UTC)
	rules := []model.PolicyRule{
		{
			RuleID:     "rule_cached",
			Name:       "Cached rule",
			Status:     "active",
			AgentType:  "codex",
			MatchType:  "command_regex",
			MatchValue: "cached-command",
			Action:     "block",
			Priority:   10,
			RiskLevel:  "high",
		},
	}

	if got, want := PolicyCachePath(dataDir), filepath.Join(dataDir, "policies.json"); got != want {
		t.Fatalf("policy cache path = %q, want %q", got, want)
	}
	if err := SavePolicyCache(dataDir, rules, syncedAt); err != nil {
		t.Fatalf("SavePolicyCache: %v", err)
	}
	cache, err := LoadPolicyCache(dataDir)
	if err != nil {
		t.Fatalf("LoadPolicyCache: %v", err)
	}
	if !cache.SyncedAt.Equal(syncedAt) {
		t.Fatalf("synced_at = %s, want %s", cache.SyncedAt, syncedAt)
	}
	if len(cache.Rules) != 1 || cache.Rules[0].RuleID != "rule_cached" {
		t.Fatalf("rules = %+v", cache.Rules)
	}
}
