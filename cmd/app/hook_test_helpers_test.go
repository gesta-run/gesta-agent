package app

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gesta-run/gesta-agent/pkg/daemon"
	"github.com/gesta-run/gesta-agent/pkg/model"
)

type hookRuleServer struct {
	Server        *httptest.Server
	EventRequests atomic.Int32
}

func saveTestOutputClassification(t *testing.T, cfg daemon.Config) {
	t.Helper()
	if err := daemon.SaveOutputClassificationCache(cfg.DataDir, model.OutputClassificationSettings{
		Revision:      1,
		CodeSuffixes:  []string{".go", ".html", ".ts", ".tsx"},
		CodeFilenames: []string{"Dockerfile", "Makefile"},
	}, time.Now().UTC()); err != nil {
		t.Fatalf("SaveOutputClassificationCache: %v", err)
	}
}

func readSingleQueuedEvent(t *testing.T, cfg daemon.Config) model.EventEnvelope {
	t.Helper()
	events, err := daemon.NewQueue(cfg.DataDir).ReadAll()
	if err != nil {
		t.Fatalf("read queued events: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("queued events = %d, want 1: %#v", len(events), events)
	}
	return events[0]
}

func newHookRuleServer(t *testing.T, sensitiveRules []model.SensitiveRule) *hookRuleServer {
	t.Helper()
	result := &hookRuleServer{}
	result.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/sensitive-rules":
			if err := json.NewEncoder(w).Encode(model.SensitiveRulesResponse{Rules: sensitiveRules}); err != nil {
				t.Errorf("encode sensitive rules: %v", err)
			}
		case "/api/v1/context-rules":
			if err := json.NewEncoder(w).Encode(model.ContextRuleBundle{Version: "empty", Rules: []model.ContextRule{}}); err != nil {
				t.Errorf("encode context rules: %v", err)
			}
		case "/api/v1/events":
			result.EventRequests.Add(1)
			var uploaded model.EventBatch
			if err := json.NewDecoder(r.Body).Decode(&uploaded); err != nil {
				t.Errorf("decode events: %v", err)
			}
			_, _ = w.Write([]byte(`{"status":"ok"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(result.Server.Close)
	return result
}

func customerSecretRecordRule() model.SensitiveRule {
	return model.SensitiveRule{
		RuleID:       "srule_record_customer_secret",
		Name:         "Customer secrets",
		Status:       "active",
		Source:       "user_prompt",
		DetectorType: "regex",
		Pattern:      `customer_secret_[0-9]+`,
		Category:     "customer_secret",
		Severity:     "medium",
		Action:       "record",
		SampleMode:   "fingerprint_only",
		Confidence:   0.77,
		Priority:     1,
	}
}

func configureCodexHookPolicy(t *testing.T, token string, rule model.PolicyRule) daemon.Config {
	t.Helper()
	home := filepath.Join(t.TempDir(), "home")
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatalf("mkdir home: %v", err)
	}
	t.Setenv("HOME", home)
	cfg := daemon.NewDirectRuntimeConfig("http://127.0.0.1:1", token)
	if err := daemon.SaveConfig("", cfg); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}
	if err := daemon.SavePolicyCache(cfg.DataDir, []model.PolicyRule{rule}, cfgTime()); err != nil {
		t.Fatalf("SavePolicyCache: %v", err)
	}
	return cfg
}
