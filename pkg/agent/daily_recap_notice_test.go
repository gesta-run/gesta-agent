package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gesta-run/gesta-agent/pkg/daemon"
)

func TestMain(m *testing.M) {
	dailyRecapNoticeNow = func() time.Time {
		return time.Date(2026, time.August, 1, 16, 59, 0, 0, time.Local)
	}
	os.Exit(m.Run())
}

func TestConsoleRecapURLUsesProductionOnlyForProductionAPI(t *testing.T) {
	tests := []struct {
		controlURL      string
		wantEnvironment string
		wantURL         string
	}{
		{
			controlURL: "https://api.gesta.run",
			wantURL:    productionRecapURL,
		},
		{
			controlURL: "https://API.GESTA.RUN:443/api/v1",
			wantURL:    productionRecapURL,
		},
		{
			controlURL:      "https://pre-api.gesta.run",
			wantEnvironment: " (Pre)",
			wantURL:         preproductionRecapURL,
		},
		{
			controlURL:      "http://localhost:8080",
			wantEnvironment: " (Pre)",
			wantURL:         preproductionRecapURL,
		},
		{
			controlURL:      "://invalid",
			wantEnvironment: " (Pre)",
			wantURL:         preproductionRecapURL,
		},
	}

	for _, test := range tests {
		t.Run(test.controlURL, func(t *testing.T) {
			gotURL, gotEnvironment := consoleRecapURL(test.controlURL)
			if gotURL != test.wantURL || gotEnvironment != test.wantEnvironment {
				t.Fatalf(
					"consoleRecapURL(%q) = (%q, %q), want (%q, %q)",
					test.controlURL,
					gotURL,
					gotEnvironment,
					test.wantURL,
					test.wantEnvironment,
				)
			}
		})
	}
}

func TestDailyRecapNoticeStartsAtFiveAndAppearsOncePerDay(t *testing.T) {
	now := time.Date(2026, time.August, 1, 16, 59, 0, 0, time.Local)
	stubDailyRecapNoticeNow(t, &now)
	cfg := daemon.NewDirectRuntimeConfig("https://pre-api.gesta.run", "token")
	cfg.DataDir = t.TempDir()

	if got := dailyRecapNoticeBestEffort(cfg); got != "" {
		t.Fatalf("notice before 17:00 = %q, want empty", got)
	}

	now = time.Date(2026, time.August, 1, 17, 0, 0, 0, time.Local)
	want := "Gesta recap (Pre) · Your work recap is ready · " +
		"[Review your day →](https://pre-console.gesta.run/#my-recap)"
	if got := dailyRecapNoticeBestEffort(cfg); got != want {
		t.Fatalf("notice at 17:00 = %q, want %q", got, want)
	}
	if got := dailyRecapNoticeBestEffort(cfg); got != "" {
		t.Fatalf("second notice on same day = %q, want empty", got)
	}

	now = time.Date(2026, time.August, 2, 17, 1, 0, 0, time.Local)
	if got := dailyRecapNoticeBestEffort(cfg); got != want {
		t.Fatalf("notice on next day = %q, want %q", got, want)
	}
	statePath := filepath.Join(cfg.DataDir, "runtime", "daily-recap-notice.json")
	data, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatalf("read daily recap notice state: %v", err)
	}
	var state dailyRecapNoticeState
	if err := json.Unmarshal(data, &state); err != nil {
		t.Fatalf("decode daily recap notice state: %v", err)
	}
	if state.LastShownDate != "2026-08-02" {
		t.Fatalf("last shown date = %q, want 2026-08-02", state.LastShownDate)
	}
	if _, err := os.Stat(filepath.Join(cfg.DataDir, "runtime", "daily-recap-notices")); !os.IsNotExist(err) {
		t.Fatalf("legacy per-day marker directory exists: %v", err)
	}
}

func TestClaimDailyRecapNoticeSerializesConcurrentHooks(t *testing.T) {
	dataDir := t.TempDir()
	now := time.Date(2026, time.August, 1, 18, 0, 0, 0, time.Local)
	const callers = 32
	var wait sync.WaitGroup
	var claims atomic.Int32
	errorsFound := make(chan error, callers)

	for range callers {
		wait.Add(1)
		go func() {
			defer wait.Done()
			claimed, err := claimDailyRecapNotice(dataDir, now)
			if claimed {
				claims.Add(1)
			}
			errorsFound <- err
		}()
	}
	wait.Wait()
	close(errorsFound)
	for err := range errorsFound {
		if err != nil {
			t.Fatalf("claim daily recap notice: %v", err)
		}
	}
	if got := claims.Load(); got != 1 {
		t.Fatalf("successful claims = %d, want 1", got)
	}
}

func TestDailyRecapNoticeUsesProductionLink(t *testing.T) {
	now := time.Date(2026, time.August, 1, 18, 0, 0, 0, time.Local)
	stubDailyRecapNoticeNow(t, &now)
	cfg := daemon.NewDirectRuntimeConfig("https://api.gesta.run", "token")
	cfg.DataDir = t.TempDir()

	got := dailyRecapNoticeBestEffort(cfg)
	if strings.Contains(got, "(Pre)") || !strings.Contains(got, productionRecapURL) {
		t.Fatalf("production notice = %q", got)
	}
}

func TestDailyRecapNoticeInjectsWithoutPendingTurnNotice(t *testing.T) {
	now := time.Date(2026, time.August, 1, 18, 0, 0, 0, time.Local)
	stubDailyRecapNoticeNow(t, &now)
	cfg := daemon.NewDirectRuntimeConfig("https://custom-api.example.com", "token")
	cfg.DataDir = t.TempDir()
	want := "Gesta recap (Pre) · Your work recap is ready · " +
		"[Review your day →](https://pre-console.gesta.run/#my-recap)"

	response := injectPendingTurnNoticeBestEffort(
		context.Background(),
		cfg,
		agentHookEvent{SessionID: "daily-recap-session"},
		"codex",
		nil,
	)
	if got := hookAdditionalContext(response); got != pendingTurnNoticeContext(want) {
		t.Fatalf("daily recap context = %q, want %q", got, pendingTurnNoticeContext(want))
	}

	second := injectPendingTurnNoticeBestEffort(
		context.Background(),
		cfg,
		agentHookEvent{SessionID: "another-session"},
		"claude_code",
		nil,
	)
	if len(second) != 0 {
		t.Fatalf("second daily recap response = %#v, want empty", second)
	}
}

func stubDailyRecapNoticeNow(t *testing.T, now *time.Time) {
	t.Helper()
	original := dailyRecapNoticeNow
	dailyRecapNoticeNow = func() time.Time { return *now }
	t.Cleanup(func() { dailyRecapNoticeNow = original })
}
