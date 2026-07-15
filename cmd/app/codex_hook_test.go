package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gesta-run/gesta-agent/pkg/daemon"
	"github.com/gesta-run/gesta-agent/pkg/model"
)

func TestCodexHookCapturesOutputBaselineForNonShellTool(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	tmp := t.TempDir()
	home := filepath.Join(tmp, "home")
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatalf("mkdir home: %v", err)
	}
	t.Setenv("HOME", home)
	t.Setenv("GESTA_USER_NAME", "codex-hook-baseline@example.com")

	repo := filepath.Join(tmp, "repo")
	if err := os.MkdirAll(repo, 0o700); err != nil {
		t.Fatalf("mkdir repo: %v", err)
	}
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, string(out))
		}
	}
	git("init")
	git("config", "user.email", "test@example.com")
	git("config", "user.name", "Test User")
	if err := os.WriteFile(filepath.Join(repo, "main.go"), []byte("package main\n"), 0o600); err != nil {
		t.Fatalf("write main.go: %v", err)
	}
	git("add", ".")
	git("commit", "-m", "initial")

	cfg := daemon.NewDirectRuntimeConfig("http://127.0.0.1:1", "dtok_codex_hook_baseline")
	if err := daemon.SaveConfig("", cfg); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}

	input := []byte(`{
		"hook_event_name": "PreToolUse",
		"tool_name": "apply_patch",
		"session_id": "baseline-session",
		"cwd": "` + filepath.ToSlash(repo) + `"
	}`)
	response := processCodexHook(context.Background(), input)
	if len(response) != 0 {
		t.Fatalf("non-shell hook response = %#v, want empty allow response", response)
	}

	data, err := os.ReadFile(filepath.Join(home, ".gesta", "output-baselines.json"))
	if err != nil {
		t.Fatalf("read output baseline: %v", err)
	}
	if !strings.Contains(string(data), `"sessions"`) || !strings.Contains(string(data), `"git_sha_before"`) {
		t.Fatalf("baseline file did not capture session: %s", data)
	}
}

func TestCodexHookBlocksAnyBashCommandFromPolicy(t *testing.T) {
	tmp := t.TempDir()
	home := filepath.Join(tmp, "home")
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatalf("mkdir home: %v", err)
	}
	t.Setenv("HOME", home)
	t.Setenv("GESTA_USER_NAME", "codex-hook@example.com")

	var eventRequests int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/events":
			atomic.AddInt32(&eventRequests, 1)
			_, _ = w.Write([]byte(`{"status":"ok"}`))
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	cfg := daemon.NewDirectRuntimeConfig(server.URL, "dtok_codex_hook")
	if err := daemon.SaveConfig("", cfg); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}
	if err := daemon.SavePolicyCache(cfg.DataDir, []model.PolicyRule{
		{
			RuleID:      "rule_hook_block_ls",
			Name:        "Block ls",
			Description: "block any ls command",
			Status:      "active",
			AgentType:   "codex",
			MatchType:   "command_regex",
			MatchValue:  ".*ls.*",
			Action:      "block",
			RiskLevel:   "medium",
		},
	}, cfgTime()); err != nil {
		t.Fatalf("SavePolicyCache: %v", err)
	}

	input := []byte(`{
		"hook_event_name": "PreToolUse",
		"tool_name": "Bash",
		"tool_input": {"command": "ls -al"}
	}`)
	response := processCodexHook(context.Background(), input)
	data, err := json.Marshal(response)
	if err != nil {
		t.Fatalf("marshal response: %v", err)
	}
	text := string(data)
	if !strings.Contains(text, `"permissionDecision":"deny"`) {
		t.Fatalf("expected deny hook response, got %s", text)
	}
	if !strings.Contains(text, gestaHighRiskCommandDeniedMessage) {
		t.Fatalf("expected fixed user-facing reason in response, got %s", text)
	}
	if strings.Contains(text, "block any ls command") {
		t.Fatalf("response leaked policy description: %s", text)
	}
	if got := atomic.LoadInt32(&eventRequests); got != 1 {
		t.Fatalf("event flush requests = %d, want 1", got)
	}
}

func TestCodexHookBlocksUserPromptSubmitSecret(t *testing.T) {
	tmp := t.TempDir()
	home := filepath.Join(tmp, "home")
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatalf("mkdir home: %v", err)
	}
	t.Setenv("HOME", home)
	t.Setenv("GESTA_USER_NAME", "codex-hook-sensitive@example.com")

	var eventRequests int32
	var uploaded model.EventBatch
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/sensitive-rules":
			if err := json.NewEncoder(w).Encode(model.SensitiveRulesResponse{Rules: []model.SensitiveRule{openAIKeySensitiveRule()}}); err != nil {
				t.Fatalf("encode sensitive rules: %v", err)
			}
		case "/api/v1/events":
			atomic.AddInt32(&eventRequests, 1)
			if err := json.NewDecoder(r.Body).Decode(&uploaded); err != nil {
				t.Fatalf("decode events: %v", err)
			}
			_, _ = w.Write([]byte(`{"status":"ok"}`))
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	cfg := daemon.NewDirectRuntimeConfig(server.URL, "dtok_codex_hook_sensitive")
	if err := daemon.SaveConfig("", cfg); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}

	secret := "sk-" + strings.Repeat("a", 32)
	input := []byte(`{
		"hook_event_name": "UserPromptSubmit",
		"prompt": "please use ` + secret + ` for the test",
		"session_id": "raw-session-id",
		"turn_id": "raw-turn-id",
		"cwd": "/Users/alice/private/repo",
		"model": "gpt-5"
	}`)
	response := processCodexHook(context.Background(), input)
	data, err := json.Marshal(response)
	if err != nil {
		t.Fatalf("marshal response: %v", err)
	}
	text := string(data)
	if !strings.Contains(text, `"decision":"block"`) {
		t.Fatalf("expected block response, got %s", text)
	}
	if !strings.Contains(text, gestaSensitivePromptDeniedMessage) {
		t.Fatalf("expected sensitive prompt message, got %s", text)
	}
	if strings.Contains(text, secret) {
		t.Fatalf("response leaked secret: %s", text)
	}
	if got := atomic.LoadInt32(&eventRequests); got != 1 {
		t.Fatalf("event flush requests = %d, want 1", got)
	}
	if len(uploaded.Events) != 1 {
		t.Fatalf("uploaded events = %d, want 1: %#v", len(uploaded.Events), uploaded.Events)
	}
	event := uploaded.Events[0]
	if event.EventType != "sensitive.finding" || event.Source != "codex" || event.AgentType != "codex" {
		t.Fatalf("unexpected event envelope: %#v", event)
	}
	payload := event.Payload
	if payload["source"] != "user_prompt" || payload["action"] != "block" {
		t.Fatalf("unexpected finding payload source/action: %#v", payload)
	}
	if payload["category"] != "openai_api_key" || payload["severity"] != "critical" {
		t.Fatalf("unexpected finding category/severity: %#v", payload)
	}
	if payload["rule_id"] != "srule_openai_api_key" || payload["rule_name"] == "" {
		t.Fatalf("missing matched rule metadata: %#v", payload)
	}
	if payload["sample_mode"] != "original" {
		t.Fatalf("sample_mode = %#v, want original", payload["sample_mode"])
	}
	if payload["fingerprint"] == "" || !strings.HasPrefix(payload["fingerprint"].(string), "hmac-sha256:") {
		t.Fatalf("missing HMAC fingerprint: %#v", payload)
	}
	if payload["raw_content_stored"] != true || payload["metadata_only"] != false {
		t.Fatalf("unexpected storage markers: %#v", payload)
	}
	if sample, ok := payload["sample"].(string); !ok || !strings.Contains(sample, secret) {
		t.Fatalf("uploaded finding should keep the original sample: %#v", payload)
	}
	uploadedData, err := json.Marshal(uploaded)
	if err != nil {
		t.Fatalf("marshal uploaded: %v", err)
	}
	uploadedText := string(uploadedData)
	for _, raw := range []string{"raw-session-id", "raw-turn-id", "/Users/alice/private/repo"} {
		if strings.Contains(uploadedText, raw) {
			t.Fatalf("uploaded event leaked raw value %q: %s", raw, uploadedText)
		}
	}
	if !strings.Contains(uploadedText, secret) {
		t.Fatalf("uploaded finding should contain original sensitive text: %s", uploadedText)
	}
	if strings.Contains(uploadedText, "[REDACTED]") {
		t.Fatalf("uploaded finding should not redact sensitive text: %s", uploadedText)
	}
}

func TestCodexHookBlocksBuiltInSmartSecretRule(t *testing.T) {
	tmp := t.TempDir()
	home := filepath.Join(tmp, "home")
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatalf("mkdir home: %v", err)
	}
	t.Setenv("HOME", home)
	t.Setenv("GESTA_USER_NAME", "codex-hook-built-in-sensitive@example.com")

	var eventRequests int32
	var uploaded model.EventBatch
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/sensitive-rules":
			if err := json.NewEncoder(w).Encode(model.SensitiveRulesResponse{Rules: []model.SensitiveRule{
				{
					RuleID:       "srule_builtin_smart_secret_detector",
					Name:         "Smart secret detector",
					Status:       "active",
					Source:       "user_prompt",
					DetectorType: "secret",
					Category:     "secret",
					Severity:     "critical",
					Action:       "block",
					SampleMode:   "redacted",
					Confidence:   0.9,
					Priority:     0,
					Owner:        "system",
				},
			}}); err != nil {
				t.Fatalf("encode sensitive rules: %v", err)
			}
		case "/api/v1/events":
			atomic.AddInt32(&eventRequests, 1)
			if err := json.NewDecoder(r.Body).Decode(&uploaded); err != nil {
				t.Fatalf("decode events: %v", err)
			}
			_, _ = w.Write([]byte(`{"status":"ok"}`))
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	cfg := daemon.NewDirectRuntimeConfig(server.URL, "dtok_codex_hook_builtin_sensitive")
	if err := daemon.SaveConfig("", cfg); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}

	secret := "test_9xLmR7QpV2nB4sC8dF6gH1jK3zT5wY0aE9rU2iO4pS6dV8kN0m"
	input := []byte(`{
		"hook_event_name": "UserPromptSubmit",
		"prompt": "auth0 token is: ` + secret + `",
		"session_id": "built-in-sensitive-session"
	}`)
	response := processCodexHook(context.Background(), input)
	data, err := json.Marshal(response)
	if err != nil {
		t.Fatalf("marshal response: %v", err)
	}
	text := string(data)
	if !strings.Contains(text, `"decision":"block"`) {
		t.Fatalf("expected block response, got %s", text)
	}
	if strings.Contains(text, secret) {
		t.Fatalf("response leaked secret: %s", text)
	}
	if got := atomic.LoadInt32(&eventRequests); got != 1 {
		t.Fatalf("event flush requests = %d, want 1", got)
	}
	if len(uploaded.Events) != 1 {
		t.Fatalf("uploaded events = %d, want 1: %#v", len(uploaded.Events), uploaded.Events)
	}
	payload := uploaded.Events[0].Payload
	if payload["rule_id"] != "srule_builtin_smart_secret_detector" ||
		payload["category"] != "secret" ||
		payload["sample_mode"] != "redacted" ||
		payload["action"] != "block" ||
		payload["raw_content_stored"] != false {
		t.Fatalf("unexpected built-in smart secret payload: %#v", payload)
	}
	sample, ok := payload["sample"].(string)
	if !ok || !strings.Contains(sample, "[REDACTED]") || strings.Contains(sample, secret) {
		t.Fatalf("sample should be redacted, got %#v", payload["sample"])
	}
}

func TestCodexHookRefreshesStaleEmptySensitiveRuleCache(t *testing.T) {
	tmp := t.TempDir()
	home := filepath.Join(tmp, "home")
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatalf("mkdir home: %v", err)
	}
	t.Setenv("HOME", home)
	t.Setenv("GESTA_USER_NAME", "codex-hook-stale-sensitive-cache@example.com")

	var sensitiveRuleRequests int32
	var eventRequests int32
	var uploaded model.EventBatch
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/sensitive-rules":
			atomic.AddInt32(&sensitiveRuleRequests, 1)
			if err := json.NewEncoder(w).Encode(model.SensitiveRulesResponse{Rules: []model.SensitiveRule{
				{
					RuleID:       "srule_builtin_smart_secret_detector",
					Name:         "Smart secret detector",
					Status:       "active",
					Source:       "user_prompt",
					DetectorType: "secret",
					Category:     "secret",
					Severity:     "critical",
					Action:       "block",
					SampleMode:   "redacted",
					Confidence:   0.9,
					Priority:     0,
					Owner:        "system",
				},
			}}); err != nil {
				t.Fatalf("encode sensitive rules: %v", err)
			}
		case "/api/v1/events":
			atomic.AddInt32(&eventRequests, 1)
			if err := json.NewDecoder(r.Body).Decode(&uploaded); err != nil {
				t.Fatalf("decode events: %v", err)
			}
			_, _ = w.Write([]byte(`{"status":"ok"}`))
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	cfg := daemon.NewDirectRuntimeConfig(server.URL, "dtok_codex_hook_stale_sensitive_cache")
	if err := daemon.SaveConfig("", cfg); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}
	if err := daemon.SaveSensitiveRuleCache(cfg.DataDir, []model.SensitiveRule{}, time.Now().Add(-2*hookSensitiveRulesCacheMaxAge)); err != nil {
		t.Fatalf("SaveSensitiveRuleCache: %v", err)
	}

	secret := "test_9xLmR7QpV2nB4sC8dF6gH1jK3zT5wY0aE9rU2iO4pS6dV8kN0m"
	input := []byte(`{
		"hook_event_name": "UserPromptSubmit",
		"prompt": "auth0 token is: ` + secret + `",
		"session_id": "stale-sensitive-cache-session"
	}`)
	response := processCodexHook(context.Background(), input)
	data, err := json.Marshal(response)
	if err != nil {
		t.Fatalf("marshal response: %v", err)
	}
	if !strings.Contains(string(data), `"decision":"block"`) {
		t.Fatalf("expected block response after refreshing stale empty cache, got %s", data)
	}
	if got := atomic.LoadInt32(&sensitiveRuleRequests); got != 1 {
		t.Fatalf("sensitive rule fetches = %d, want 1", got)
	}
	if got := atomic.LoadInt32(&eventRequests); got != 1 {
		t.Fatalf("event flush requests = %d, want 1", got)
	}
	if len(uploaded.Events) != 1 {
		t.Fatalf("uploaded events = %d, want 1: %#v", len(uploaded.Events), uploaded.Events)
	}
	cache, err := daemon.LoadSensitiveRuleCache(cfg.DataDir)
	if err != nil {
		t.Fatalf("LoadSensitiveRuleCache: %v", err)
	}
	if len(cache.Rules) != 1 || cache.Rules[0].RuleID != "srule_builtin_smart_secret_detector" {
		t.Fatalf("cache rules = %#v, want refreshed built-in rule", cache.Rules)
	}
}

func TestCodexHookRecordsNonBlockingSensitiveRule(t *testing.T) {
	tmp := t.TempDir()
	home := filepath.Join(tmp, "home")
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatalf("mkdir home: %v", err)
	}
	t.Setenv("HOME", home)
	t.Setenv("GESTA_USER_NAME", "codex-hook-record@example.com")

	var eventRequests int32
	var uploaded model.EventBatch
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/sensitive-rules":
			if err := json.NewEncoder(w).Encode(model.SensitiveRulesResponse{Rules: []model.SensitiveRule{
				{
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
				},
			}}); err != nil {
				t.Fatalf("encode sensitive rules: %v", err)
			}
		case "/api/v1/events":
			atomic.AddInt32(&eventRequests, 1)
			if err := json.NewDecoder(r.Body).Decode(&uploaded); err != nil {
				t.Fatalf("decode events: %v", err)
			}
			_, _ = w.Write([]byte(`{"status":"ok"}`))
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	cfg := daemon.NewDirectRuntimeConfig(server.URL, "dtok_codex_hook_sensitive_record")
	if err := daemon.SaveConfig("", cfg); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}

	input := []byte(`{
		"hook_event_name": "UserPromptSubmit",
		"prompt": "customer_secret_123 should be observed",
		"session_id": "record-session"
	}`)
	response := processCodexHook(context.Background(), input)
	if len(response) != 0 {
		t.Fatalf("record-only finding should allow prompt, got %#v", response)
	}
	if got := atomic.LoadInt32(&eventRequests); got != 1 {
		t.Fatalf("event flush requests = %d, want 1", got)
	}
	if len(uploaded.Events) != 1 {
		t.Fatalf("uploaded events = %d, want 1: %#v", len(uploaded.Events), uploaded.Events)
	}
	payload := uploaded.Events[0].Payload
	if payload["action"] != "record" ||
		payload["rule_id"] != "srule_record_customer_secret" ||
		payload["category"] != "customer_secret" ||
		payload["sample_mode"] != "fingerprint_only" ||
		payload["metadata_only"] != true ||
		payload["raw_content_stored"] != false {
		t.Fatalf("unexpected record-only payload: %#v", payload)
	}
	if sample, ok := payload["sample"].(string); ok && sample != "" {
		t.Fatalf("fingerprint_only finding should not upload sample, got %q", sample)
	}
}

func TestCodexHookAllowsUserPromptSubmitWithoutSensitiveData(t *testing.T) {
	tmp := t.TempDir()
	home := filepath.Join(tmp, "home")
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatalf("mkdir home: %v", err)
	}
	t.Setenv("HOME", home)

	cfg := daemon.NewDirectRuntimeConfig("http://127.0.0.1:1", "dtok_codex_hook_no_sensitive")
	if err := daemon.SaveConfig("", cfg); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}

	input := []byte(`{
		"hook_event_name": "UserPromptSubmit",
		"prompt": "please summarize the dashboard and suggest better labels"
	}`)
	response := processCodexHook(context.Background(), input)
	if len(response) != 0 {
		t.Fatalf("plain prompt should be allowed, got %#v", response)
	}
}

func TestCodexHookAllowsWarnPolicy(t *testing.T) {
	tmp := t.TempDir()
	home := filepath.Join(tmp, "home")
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatalf("mkdir home: %v", err)
	}
	t.Setenv("HOME", home)
	t.Setenv("GESTA_USER_NAME", "codex-hook-warn@example.com")

	cfg := daemon.NewDirectRuntimeConfig("http://127.0.0.1:1", "dtok_codex_hook_warn")
	if err := daemon.SaveConfig("", cfg); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}
	if err := daemon.SavePolicyCache(cfg.DataDir, []model.PolicyRule{
		{
			RuleID:      "rule_hook_warn",
			Name:        "Warn echo",
			Description: "warn on echo",
			Status:      "active",
			AgentType:   "codex",
			MatchType:   "command_regex",
			MatchValue:  ".*echo.*",
			Action:      "warn",
			RiskLevel:   "low",
		},
	}, cfgTime()); err != nil {
		t.Fatalf("SavePolicyCache: %v", err)
	}

	input := []byte(`{
		"hook_event_name": "PreToolUse",
		"tool_name": "Bash",
		"tool_input": {"command": "echo ok"}
	}`)
	response := processCodexHook(context.Background(), input)
	if len(response) != 0 {
		t.Fatalf("warn should allow Codex tool call, got %#v", response)
	}
}

func TestCodexHookTreatsExecCommandAsShellCommand(t *testing.T) {
	tmp := t.TempDir()
	home := filepath.Join(tmp, "home")
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatalf("mkdir home: %v", err)
	}
	t.Setenv("HOME", home)
	t.Setenv("GESTA_USER_NAME", "codex-hook-exec@example.com")

	cfg := daemon.NewDirectRuntimeConfig("http://127.0.0.1:1", "dtok_codex_hook_exec")
	if err := daemon.SaveConfig("", cfg); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}
	if err := daemon.SavePolicyCache(cfg.DataDir, []model.PolicyRule{
		{
			RuleID:     "rule_hook_block_ls",
			Name:       "Block ls",
			Status:     "active",
			AgentType:  "codex",
			MatchType:  "command_regex",
			MatchValue: ".*ls.*",
			Action:     "block",
			RiskLevel:  "medium",
		},
	}, cfgTime()); err != nil {
		t.Fatalf("SavePolicyCache: %v", err)
	}

	input := []byte(`{
		"hook_event_name": "PreToolUse",
		"tool_name": "functions.exec_command",
		"tool_input": {"cmd": "ls -alh"}
	}`)
	response := processCodexHook(context.Background(), input)
	data, err := json.Marshal(response)
	if err != nil {
		t.Fatalf("marshal response: %v", err)
	}
	text := string(data)
	if !strings.Contains(text, `"permissionDecision":"deny"`) {
		t.Fatalf("expected deny hook response, got %s", text)
	}
	if !strings.Contains(text, gestaHighRiskCommandDeniedMessage) {
		t.Fatalf("expected fixed user-facing reason in response, got %s", text)
	}
}

func TestCodexHookDoesNotApplyCommandRegexToNonBashTools(t *testing.T) {
	tmp := t.TempDir()
	home := filepath.Join(tmp, "home")
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatalf("mkdir home: %v", err)
	}
	t.Setenv("HOME", home)

	cfg := daemon.NewDirectRuntimeConfig("http://127.0.0.1:1", "dtok_codex_hook_patch")
	if err := daemon.SaveConfig("", cfg); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}
	if err := daemon.SavePolicyCache(cfg.DataDir, []model.PolicyRule{
		{
			RuleID:      "rule_hook_block_ls",
			Name:        "Block ls",
			Description: "block any ls command",
			Status:      "active",
			AgentType:   "codex",
			MatchType:   "command_regex",
			MatchValue:  ".*ls.*",
			Action:      "block",
			RiskLevel:   "medium",
		},
	}, cfgTime()); err != nil {
		t.Fatalf("SavePolicyCache: %v", err)
	}

	input := []byte(`{
		"hook_event_name": "PreToolUse",
		"tool_name": "apply_patch",
		"tool_input": {"command": "*** Begin Patch\n+listing text with ls\n*** End Patch"}
	}`)
	response := processCodexHook(context.Background(), input)
	if len(response) != 0 {
		t.Fatalf("non-Bash tools should not be evaluated as shell commands, got %#v", response)
	}
}

func openAIKeySensitiveRule() model.SensitiveRule {
	return model.SensitiveRule{
		RuleID:       "srule_openai_api_key",
		Name:         "OpenAI API keys",
		Status:       "active",
		Source:       "user_prompt",
		DetectorType: "regex",
		Pattern:      `\bsk-[A-Za-z0-9_-]{20,}\b`,
		Category:     "openai_api_key",
		Severity:     "critical",
		Action:       "block",
		SampleMode:   "original",
		Confidence:   0.99,
		Priority:     1,
	}
}
