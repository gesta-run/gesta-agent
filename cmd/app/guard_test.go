package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gesta-run/gesta-agent/cmd/app/options"
	"github.com/gesta-run/gesta-agent/pkg/daemon"
	"github.com/gesta-run/gesta-agent/pkg/model"
	"github.com/gesta-run/gesta-agent/pkg/policy"
)

func TestGuardDoesNotExecuteBlockedCommand(t *testing.T) {
	tmp := t.TempDir()
	home := filepath.Join(tmp, "home")
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatalf("mkdir home: %v", err)
	}
	t.Setenv("HOME", home)

	binDir := filepath.Join(tmp, "bin")
	if err := os.MkdirAll(binDir, 0o700); err != nil {
		t.Fatalf("mkdir bin: %v", err)
	}
	marker := filepath.Join(tmp, "executed")
	fakeRM := filepath.Join(binDir, "rm")
	script := fmt.Sprintf("#!/bin/sh\ntouch %q\n", marker)
	if err := os.WriteFile(fakeRM, []byte(script), 0o700); err != nil {
		t.Fatalf("write fake rm: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	if err := daemon.SavePolicyCache(filepath.Join(home, ".gesta"), []model.PolicyRule{
		{
			RuleID:      "rule_test_root_delete",
			Name:        "Block root delete",
			Description: "test configured block rule",
			Status:      "active",
			AgentType:   "codex",
			MatchType:   "command_regex",
			MatchValue:  `(?i)^rm\s+-rf\s+/$`,
			Action:      "block",
			RiskLevel:   "critical",
		},
	}, cfgTime()); err != nil {
		t.Fatalf("SavePolicyCache: %v", err)
	}

	err := Run(context.Background(), []string{"guard", "--agent", "codex", "--", "rm", "-rf", "/"})
	var exitErr ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("expected ExitError, got %T %v", err, err)
	}
	if exitErr.Code != GuardBlockedExitCode {
		t.Fatalf("exit code = %d, want %d", exitErr.Code, GuardBlockedExitCode)
	}
	if exitErr.Message != "gesta-agent guard: "+gestaHighRiskCommandDeniedMessage {
		t.Fatalf("exit message = %q", exitErr.Message)
	}
	if _, statErr := os.Stat(marker); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("blocked command appears to have executed, marker stat err=%v", statErr)
	}

	queue := daemon.NewQueue(filepath.Join(home, ".gesta"))
	events, readErr := queue.ReadAll()
	if readErr != nil {
		t.Fatalf("read queue: %v", readErr)
	}
	if len(events) != 1 {
		t.Fatalf("queued events = %d, want 1", len(events))
	}
	event := events[0]
	if event.EventType != "policy.decision" || event.Source != "guard" || event.AgentType != "codex" {
		t.Fatalf("unexpected event envelope: %#v", event)
	}
	if got := event.Payload["decision"]; got != "block" {
		t.Fatalf("decision payload = %#v, want block", got)
	}
	if got := event.Payload["executed"]; got != false {
		t.Fatalf("executed payload = %#v, want false", got)
	}
	if got := event.Payload["command_hash"]; got == "" {
		t.Fatalf("expected command_hash in payload")
	}
	if _, ok := event.Payload["exit_code"]; !ok {
		t.Fatalf("expected exit_code in payload")
	}
}

func TestGuardExecutesAllowedUnmatchedCommandWithoutRecordingDecision(t *testing.T) {
	tmp := t.TempDir()
	home := filepath.Join(tmp, "home")
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatalf("mkdir home: %v", err)
	}
	t.Setenv("HOME", home)

	binDir := filepath.Join(tmp, "bin")
	if err := os.MkdirAll(binDir, 0o700); err != nil {
		t.Fatalf("mkdir bin: %v", err)
	}
	marker := filepath.Join(tmp, "executed")
	fakeCommand := filepath.Join(binDir, "gesta-allowed-test")
	script := fmt.Sprintf("#!/bin/sh\ntouch %q\n", marker)
	if err := os.WriteFile(fakeCommand, []byte(script), 0o700); err != nil {
		t.Fatalf("write fake command: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	if err := Run(context.Background(), []string{"guard", "--agent", "codex", "--", "gesta-allowed-test"}); err != nil {
		t.Fatalf("guard: %v", err)
	}
	if _, statErr := os.Stat(marker); statErr != nil {
		t.Fatalf("expected allowed command to execute, marker stat err=%v", statErr)
	}

	queue := daemon.NewQueue(filepath.Join(home, ".gesta"))
	events, readErr := queue.ReadAll()
	if readErr != nil {
		t.Fatalf("read queue: %v", readErr)
	}
	if len(events) != 0 {
		t.Fatalf("queued events = %d, want 0 for unmatched allow", len(events))
	}
}

func TestRecordGuardDecisionKeepsMatchedAllowDecision(t *testing.T) {
	dataDir := t.TempDir()
	cfg := daemon.Config{
		DataDir:      dataDir,
		CustomerID:   "default",
		DeploymentID: "local",
		DaemonID:     "daemon_test",
		DeviceID:     "device_test",
		UserID:       "user_test",
		UserName:     "user@example.com",
	}
	evaluation := policy.Evaluation{
		AgentType:      "codex",
		CommandHash:    "abc123",
		CommandPreview: "pwd",
		Decision:       policy.DecisionAllow,
		RuleIDs:        []string{"rule_allow_pwd"},
		Reason:         "matched low-risk allow rule",
	}
	if err := recordGuardDecisionWithConfig(cfg, false, evaluation, true, 0); err != nil {
		t.Fatalf("recordGuardDecisionWithConfig: %v", err)
	}
	events, err := daemon.NewQueue(dataDir).ReadAll()
	if err != nil {
		t.Fatalf("read queue: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("queued events = %d, want 1", len(events))
	}
	if got := events[0].Payload["decision"]; got != "allow" {
		t.Fatalf("decision payload = %#v, want allow", got)
	}
	if got := events[0].Payload["matched_rule"]; got != true {
		t.Fatalf("matched_rule payload = %#v, want true", got)
	}
}

func TestGuardDoesNotExecuteApprovalCommand(t *testing.T) {
	tmp := t.TempDir()
	home := filepath.Join(tmp, "home")
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatalf("mkdir home: %v", err)
	}
	t.Setenv("HOME", home)

	binDir := filepath.Join(tmp, "bin")
	if err := os.MkdirAll(binDir, 0o700); err != nil {
		t.Fatalf("mkdir bin: %v", err)
	}
	marker := filepath.Join(tmp, "executed")
	fakeTerraform := filepath.Join(binDir, "terraform")
	script := fmt.Sprintf("#!/bin/sh\ntouch %q\n", marker)
	if err := os.WriteFile(fakeTerraform, []byte(script), 0o700); err != nil {
		t.Fatalf("write fake terraform: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	if err := daemon.SavePolicyCache(filepath.Join(home, ".gesta"), []model.PolicyRule{
		{
			RuleID:      "rule_test_terraform_apply",
			Name:        "Approve terraform apply",
			Description: "test configured approval rule",
			Status:      "active",
			AgentType:   "codex",
			MatchType:   "command_regex",
			MatchValue:  `(?i)^terraform\s+apply$`,
			Action:      "approval",
			RiskLevel:   "high",
		},
	}, cfgTime()); err != nil {
		t.Fatalf("SavePolicyCache: %v", err)
	}

	err := Run(context.Background(), []string{"guard", "--agent", "codex", "--", "terraform", "apply"})
	var exitErr ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("expected ExitError, got %T %v", err, err)
	}
	if exitErr.Code != GuardApprovalExitCode {
		t.Fatalf("exit code = %d, want %d", exitErr.Code, GuardApprovalExitCode)
	}
	if _, statErr := os.Stat(marker); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("approval command appears to have executed, marker stat err=%v", statErr)
	}

	queue := daemon.NewQueue(filepath.Join(home, ".gesta"))
	events, readErr := queue.ReadAll()
	if readErr != nil {
		t.Fatalf("read queue: %v", readErr)
	}
	if len(events) != 1 {
		t.Fatalf("queued events = %d, want 1", len(events))
	}
	if got := events[0].Payload["decision"]; got != "approval" {
		t.Fatalf("decision payload = %#v, want approval", got)
	}
	if got := events[0].Payload["executed"]; got != false {
		t.Fatalf("executed payload = %#v, want false", got)
	}
}

func TestGuardUsesCachedControlPlanePolicyWhenSyncFails(t *testing.T) {
	tmp := t.TempDir()
	home := filepath.Join(tmp, "home")
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatalf("mkdir home: %v", err)
	}
	t.Setenv("HOME", home)
	t.Setenv("GESTA_USER_NAME", "cache-test@example.com")

	var eventRequests int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/policies":
			http.Error(w, "policy sync failed", http.StatusServiceUnavailable)
		case "/api/v1/events":
			atomic.AddInt32(&eventRequests, 1)
			_, _ = w.Write([]byte(`{"status":"ok"}`))
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	cfg := daemon.NewDirectRuntimeConfig(server.URL, "dtok_cached")
	if err := daemon.SaveConfig("", cfg); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}
	if err := daemon.SavePolicyCache(cfg.DataDir, []model.PolicyRule{
		{
			RuleID:      "rule_cached_block",
			Name:        "Cached block",
			Description: "cached control-plane rule",
			Status:      "active",
			AgentType:   "codex",
			MatchType:   "command_regex",
			MatchValue:  "cached-block-command",
			Action:      "block",
			RiskLevel:   "high",
		},
	}, cfgTime()); err != nil {
		t.Fatalf("SavePolicyCache: %v", err)
	}

	binDir := filepath.Join(tmp, "bin")
	if err := os.MkdirAll(binDir, 0o700); err != nil {
		t.Fatalf("mkdir bin: %v", err)
	}
	marker := filepath.Join(tmp, "executed")
	fakeCommand := filepath.Join(binDir, "cached-block-command")
	script := fmt.Sprintf("#!/bin/sh\ntouch %q\n", marker)
	if err := os.WriteFile(fakeCommand, []byte(script), 0o700); err != nil {
		t.Fatalf("write fake command: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	err := Run(context.Background(), []string{"guard", "--agent", "codex", "--", "cached-block-command"})
	var exitErr ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("expected ExitError, got %T %v", err, err)
	}
	if exitErr.Code != GuardBlockedExitCode {
		t.Fatalf("exit code = %d, want %d", exitErr.Code, GuardBlockedExitCode)
	}
	if _, statErr := os.Stat(marker); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("cached policy block appears to have executed command, marker stat err=%v", statErr)
	}

	if got := atomic.LoadInt32(&eventRequests); got != 1 {
		t.Fatalf("event flush requests = %d, want 1", got)
	}
}

func TestRunCanExecuteGuardedCommand(t *testing.T) {
	tmp := t.TempDir()
	home := filepath.Join(tmp, "home")
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatalf("mkdir home: %v", err)
	}
	t.Setenv("HOME", home)
	t.Setenv("GESTA_USER_NAME", "run-guard@example.com")

	var eventRequests int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/policies":
			_ = json.NewEncoder(w).Encode(model.PolicyRulesResponse{
				Rules: []model.PolicyRule{
					{
						RuleID:      "rule_run_block",
						Name:        "Run command block",
						Description: "run command matched control-plane policy",
						Status:      "active",
						AgentType:   "codex",
						MatchType:   "command_regex",
						MatchValue:  "run-block-command",
						Action:      "block",
						RiskLevel:   "high",
					},
				},
			})
		case "/api/v1/events":
			atomic.AddInt32(&eventRequests, 1)
			_, _ = w.Write([]byte(`{"status":"ok"}`))
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	binDir := filepath.Join(tmp, "bin")
	if err := os.MkdirAll(binDir, 0o700); err != nil {
		t.Fatalf("mkdir bin: %v", err)
	}
	marker := filepath.Join(tmp, "executed")
	fakeCommand := filepath.Join(binDir, "run-block-command")
	script := fmt.Sprintf("#!/bin/sh\ntouch %q\n", marker)
	if err := os.WriteFile(fakeCommand, []byte(script), 0o700); err != nil {
		t.Fatalf("write fake command: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	err := Run(context.Background(), []string{"run", "--control-url", server.URL, "--apikey", "dtok_run", "--", "run-block-command"})
	var exitErr ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("expected ExitError, got %T %v", err, err)
	}
	if exitErr.Code != GuardBlockedExitCode {
		t.Fatalf("exit code = %d, want %d", exitErr.Code, GuardBlockedExitCode)
	}
	if _, statErr := os.Stat(marker); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("run guarded command appears to have executed, marker stat err=%v", statErr)
	}
	if got := atomic.LoadInt32(&eventRequests); got != 1 {
		t.Fatalf("event flush requests = %d, want 1", got)
	}
}

func TestRunCommandLoadsSavedConfigWithoutAPIKey(t *testing.T) {
	tmp := t.TempDir()
	home := filepath.Join(tmp, "home")
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatalf("mkdir home: %v", err)
	}
	t.Setenv("HOME", home)
	t.Setenv("GESTA_USER_NAME", "saved-config@example.com")

	var eventRequests int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/policies":
			_ = json.NewEncoder(w).Encode(model.PolicyRulesResponse{})
		case "/api/v1/events":
			atomic.AddInt32(&eventRequests, 1)
			_, _ = w.Write([]byte(`{"status":"ok"}`))
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	cfg := daemon.NewDirectRuntimeConfig(server.URL, "dtok_saved_config")
	if err := daemon.SaveConfig("", cfg); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}

	binDir := filepath.Join(tmp, "bin")
	if err := os.MkdirAll(binDir, 0o700); err != nil {
		t.Fatalf("mkdir bin: %v", err)
	}
	marker := filepath.Join(tmp, "executed")
	fakeCommand := filepath.Join(binDir, "saved-config-command")
	script := fmt.Sprintf("#!/bin/sh\ntouch %q\n", marker)
	if err := os.WriteFile(fakeCommand, []byte(script), 0o700); err != nil {
		t.Fatalf("write fake command: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	if err := Run(context.Background(), []string{"run", "--", "saved-config-command"}); err != nil {
		t.Fatalf("run guarded command with saved config: %v", err)
	}
	if _, statErr := os.Stat(marker); statErr != nil {
		t.Fatalf("expected command to execute, marker stat err=%v", statErr)
	}
	if got := atomic.LoadInt32(&eventRequests); got != 0 {
		t.Fatalf("event flush requests = %d, want 0 for unmatched allow", got)
	}
}

func TestDaemonRunConfigLoadsSavedConfigWithoutAPIKey(t *testing.T) {
	tmp := t.TempDir()
	home := filepath.Join(tmp, "home")
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatalf("mkdir home: %v", err)
	}
	t.Setenv("HOME", home)
	cfg := daemon.NewDirectRuntimeConfig("https://control.example", "dtok_saved_daemon")
	if err := daemon.SaveConfig("", cfg); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}

	loaded, err := configForRun(options.RunOptions{ControlURL: "https://override.example", UsageWindow: time.Minute}, true)
	if err != nil {
		t.Fatalf("configForRun: %v", err)
	}
	if loaded.Token != "dtok_saved_daemon" {
		t.Fatalf("api key = %q", loaded.Token)
	}
	if loaded.EffectiveServerURL() != "https://override.example" {
		t.Fatalf("server URL = %q", loaded.EffectiveServerURL())
	}
	if loaded.UsageWindow != "1m0s" {
		t.Fatalf("usage window = %q", loaded.UsageWindow)
	}
}

func cfgTime() time.Time {
	return time.Date(2026, 6, 23, 8, 0, 0, 0, time.UTC)
}
