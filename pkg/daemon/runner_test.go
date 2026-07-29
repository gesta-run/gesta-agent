package daemon

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gesta-run/gesta-agent/pkg/eventqueue"
	"github.com/gesta-run/gesta-agent/pkg/model"
	"github.com/gesta-run/gesta-agent/pkg/rulecache"
)

func TestFlushWithHeartbeatAppliesUpgradeBeforeFlush(t *testing.T) {
	previousVersion := model.DaemonVersion
	model.DaemonVersion = "0.0.1-rc1"
	t.Cleanup(func() { model.DaemonVersion = previousVersion })

	dir := t.TempDir()
	q := eventqueue.NewQueue(dir)
	if err := q.Append([]model.EventEnvelope{{EventID: "evt_1", EventType: "test", CreatedAt: time.Now()}}); err != nil {
		t.Fatalf("append: %v", err)
	}

	var eventFlushes int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/heartbeat":
			_ = json.NewEncoder(w).Encode(model.HeartbeatResponse{
				Upgrade: &model.AgentUpgradePolicy{
					Mode:          "auto",
					TargetVersion: "0.0.1-rc99",
					URL:           "https://updates.example/gesta-agent-darwin-arm64",
				},
				OutputClassification: &model.OutputClassificationSettings{
					Revision:      2,
					CodeSuffixes:  []string{".html"},
					CodeFilenames: []string{"Dockerfile"},
				},
			})
		case "/api/v1/events":
			eventFlushes++
			http.Error(w, "events should not flush before upgrade", http.StatusInternalServerError)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	var applied bool
	runner := &Runner{
		cfg: Config{
			DaemonID:      "daemon_test",
			DeviceID:      "dev_test",
			PolicyVersion: model.DefaultPolicyVersion,
			DataDir:       dir,
		},
		client: NewClient(server.URL, "dtok_test"),
		queue:  q,
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		applyUpgrade: func(resp model.HeartbeatResponse) error {
			if resp.Upgrade == nil || resp.Upgrade.TargetVersion != "0.0.1-rc99" {
				t.Fatalf("upgrade response = %+v", resp.Upgrade)
			}
			applied = true
			return ErrUpgradeApplied
		},
	}

	err := runner.flushWithHeartbeat(nil)
	if !errors.Is(err, ErrUpgradeApplied) {
		t.Fatalf("flushWithHeartbeat error = %v, want %v", err, ErrUpgradeApplied)
	}
	if !applied {
		t.Fatal("expected upgrade to be applied")
	}
	if eventFlushes != 0 {
		t.Fatalf("event flushes = %d, want 0", eventFlushes)
	}
	cache, cacheErr := rulecache.LoadOutputClassificationCache(dir)
	if cacheErr != nil || cache.Revision != 2 {
		t.Fatalf("output classification cache = (%+v, %v)", cache, cacheErr)
	}
}

func TestEnsureRuntimeSettingsSyncsClassificationOnce(t *testing.T) {
	var heartbeats int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/heartbeat" {
			http.NotFound(w, r)
			return
		}
		heartbeats++
		_ = json.NewEncoder(w).Encode(model.HeartbeatResponse{
			DailyWorkTimezone: "Asia/Shanghai",
			OutputClassification: &model.OutputClassificationSettings{
				Revision:      5,
				CodeSuffixes:  []string{".html"},
				CodeFilenames: []string{"Dockerfile"},
			},
		})
	}))
	defer server.Close()

	dataDir := t.TempDir()
	runner := &Runner{
		cfg: Config{
			DaemonID:      "daemon_settings",
			DeviceID:      "device_settings",
			PolicyVersion: model.DefaultPolicyVersion,
			DataDir:       dataDir,
		},
		client: NewClient(server.URL, "dtok_settings"),
		queue:  eventqueue.NewQueue(dataDir),
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	if err := runner.ensureRuntimeSettings(); err != nil {
		t.Fatalf("ensureRuntimeSettings: %v", err)
	}
	if err := runner.ensureRuntimeSettings(); err != nil {
		t.Fatalf("ensureRuntimeSettings repeat: %v", err)
	}
	if heartbeats != 1 {
		t.Fatalf("heartbeats = %d, want 1", heartbeats)
	}
	if runner.cfg.DailyWorkTimezone != "Asia/Shanghai" {
		t.Fatalf("daily work timezone = %q", runner.cfg.DailyWorkTimezone)
	}
	cache, err := rulecache.LoadOutputClassificationCache(dataDir)
	if err != nil || cache.Revision != 5 {
		t.Fatalf("output classification cache = (%+v, %v)", cache, err)
	}
}

func TestHeartbeatWithoutClassificationPreservesLastValidCache(t *testing.T) {
	dataDir := t.TempDir()
	if err := rulecache.SaveOutputClassificationCache(dataDir, model.OutputClassificationSettings{
		Revision:      7,
		CodeSuffixes:  []string{".html"},
		CodeFilenames: []string{"Dockerfile"},
	}, time.Now().UTC()); err != nil {
		t.Fatalf("save initial output classification cache: %v", err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/heartbeat" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(model.HeartbeatResponse{})
	}))
	defer server.Close()

	runner := &Runner{
		cfg: Config{
			DaemonID:      "daemon_settings",
			DeviceID:      "device_settings",
			PolicyVersion: model.DefaultPolicyVersion,
			DataDir:       dataDir,
		},
		client: NewClient(server.URL, "dtok_settings"),
		queue:  eventqueue.NewQueue(dataDir),
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	if _, err := runner.sendHeartbeat("ok", nil); err != nil {
		t.Fatalf("sendHeartbeat: %v", err)
	}
	cache, err := rulecache.LoadOutputClassificationCache(dataDir)
	if err != nil {
		t.Fatalf("load output classification cache: %v", err)
	}
	if cache.Revision != 7 || len(cache.CodeSuffixes) != 1 || cache.CodeSuffixes[0] != ".html" {
		t.Fatalf("output classification cache = %+v", cache)
	}
	if runner.runtimeSettingsSynced {
		t.Fatal("runtime settings should remain eligible for retry")
	}
}
