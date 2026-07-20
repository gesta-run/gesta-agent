package daemon

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/gesta-run/gesta-agent/pkg/model"
)

func TestContextRuleCacheRoundTrip(t *testing.T) {
	dataDir := t.TempDir()
	syncedAt := time.Date(2026, 7, 19, 8, 0, 0, 0, time.UTC)
	bundle := model.ContextRuleBundle{
		Version: "bundle-v1",
		Rules: []model.ContextRule{{
			RuleID: "crule_cached", Name: "Cached context", Status: "active",
			MatchType: "always", ContextContent: "Use the organization standard.", AgentType: "all",
		}},
	}
	if got, want := ContextRuleCachePath(dataDir), filepath.Join(dataDir, "context-rules.json"); got != want {
		t.Fatalf("cache path = %q, want %q", got, want)
	}
	if err := SaveContextRuleCache(dataDir, bundle, syncedAt); err != nil {
		t.Fatalf("SaveContextRuleCache: %v", err)
	}
	cache, err := LoadContextRuleCache(dataDir)
	if err != nil {
		t.Fatalf("LoadContextRuleCache: %v", err)
	}
	if cache.Version != bundle.Version || !cache.SyncedAt.Equal(syncedAt) || len(cache.Rules) != 1 {
		t.Fatalf("cache = %+v", cache)
	}
	info, err := os.Stat(ContextRuleCachePath(dataDir))
	if err != nil {
		t.Fatalf("stat cache: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("cache permissions = %o, want 600", info.Mode().Perm())
	}
}

func TestContextRuleCacheConcurrentWritesRemainValid(t *testing.T) {
	dataDir := t.TempDir()
	const writers = 8
	var wait sync.WaitGroup
	errors := make(chan error, writers)
	for index := 0; index < writers; index++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			errors <- SaveContextRuleCache(dataDir, model.ContextRuleBundle{
				Version: "concurrent",
				Rules: []model.ContextRule{{
					RuleID: "crule_concurrent", Status: "active", MatchType: "always",
					ContextContent: "Use the organization standard.", AgentType: "all",
				}},
			}, time.Now())
		}()
	}
	wait.Wait()
	close(errors)
	for err := range errors {
		if err != nil {
			t.Fatalf("SaveContextRuleCache: %v", err)
		}
	}
	cache, err := LoadContextRuleCache(dataDir)
	if err != nil {
		t.Fatalf("LoadContextRuleCache: %v", err)
	}
	if cache.Version != "concurrent" || len(cache.Rules) != 1 {
		t.Fatalf("cache = %+v", cache)
	}
}
