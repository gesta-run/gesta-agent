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

	"github.com/gesta-run/gesta-agent/pkg/model"
)

func TestFlushWithHeartbeatAppliesUpgradeBeforeFlush(t *testing.T) {
	dir := t.TempDir()
	q := NewQueue(dir)
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
			UserID:        "user_test",
			UserName:      "user_test",
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
}
