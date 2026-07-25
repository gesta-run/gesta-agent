package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gesta-run/gesta-agent/pkg/codexapp"
	"github.com/gesta-run/gesta-agent/pkg/daemon"
	"github.com/gesta-run/gesta-agent/pkg/model"
	"github.com/gesta-run/gesta-agent/pkg/toolinput"
)

func TestCodexHookMeasuresOnlyCompletedThreadItems(t *testing.T) {
	tmp := t.TempDir()
	home := filepath.Join(tmp, "home")
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatalf("mkdir home: %v", err)
	}
	t.Setenv("HOME", home)

	cfg := daemon.NewDirectRuntimeConfig("http://127.0.0.1:1", "dtok_codex_hook_meter")
	if err := daemon.SaveConfig("", cfg); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}

	mcpInput := map[string]interface{}{"title": "Release plan"}
	input, err := json.Marshal(agentHookEvent{
		HookEventName: "PreToolUse",
		ToolName:      "mcp__notion__create_page",
		ToolInput:     mcpInput,
		ToolUseID:     "call-mcp-1",
		SessionID:     "session-meter-1",
		TurnID:        "turn-meter-1",
	})
	if err != nil {
		t.Fatalf("marshal pre hook: %v", err)
	}
	response := processAgentHook(context.Background(), input, "codex", "codex")
	if len(response) != 0 {
		t.Fatalf("non-shell hook response = %#v, want empty allow response", response)
	}
	events, err := daemon.NewQueue(cfg.DataDir).ReadAll()
	if err != nil {
		t.Fatalf("read gross event: %v", err)
	}
	if len(events) != 0 {
		t.Fatalf("Codex PreToolUse attempt produced output metrics: %#v", events)
	}

	patch := "*** Begin Patch\n*** Update File: app.go\n@@\n-old\n+new content\n*** End Patch"
	input, err = json.Marshal(agentHookEvent{
		HookEventName: "PreToolUse",
		ToolName:      "apply_patch",
		ToolInput:     patch,
		ToolUseID:     "call-file-1",
		SessionID:     "session-meter-1",
		TurnID:        "turn-meter-1",
	})
	if err != nil {
		t.Fatalf("marshal file pre hook: %v", err)
	}
	processAgentHook(context.Background(), input, "codex", "codex")
	events, err = daemon.NewQueue(cfg.DataDir).ReadAll()
	if err != nil {
		t.Fatalf("read after file pre hook: %v", err)
	}
	if len(events) != 0 {
		t.Fatalf("Codex file PreToolUse produced an ink event: %#v", events)
	}

	originalReadCodexTurn := readCodexTurn
	readCodexTurn = func(context.Context, string, string) (codexapp.Turn, error) {
		completedAt := int64(1_700_000_000)
		return codexapp.Turn{
			ID:          "turn-meter-1",
			Status:      "completed",
			CompletedAt: &completedAt,
			Items: []codexapp.Item{
				{
					ID:     "call-file-1",
					Type:   "fileChange",
					Status: "completed",
					Changes: []codexapp.FileChange{{
						Path: "app.go",
						Kind: codexapp.ChangeKind{Type: "update"},
						Diff: "@@\n-old\n+new content\n",
					}},
				},
				{
					ID:        "call-mcp-1",
					Type:      "mcpToolCall",
					Status:    "completed",
					Server:    "notion",
					Tool:      "create_page",
					Arguments: mcpInput,
				},
				{
					ID:        "call-mcp-failed",
					Type:      "mcpToolCall",
					Status:    "failed",
					Server:    "notion",
					Tool:      "create_page",
					Arguments: map[string]interface{}{"title": "must not count"},
				},
				{
					ID:     "call-file-failed",
					Type:   "fileChange",
					Status: "failed",
					Changes: []codexapp.FileChange{{
						Path: "failed.go",
						Kind: codexapp.ChangeKind{Type: "add"},
						Diff: "must not be counted\n",
					}},
				},
			},
		}, nil
	}
	t.Cleanup(func() { readCodexTurn = originalReadCodexTurn })

	promptInput, err := json.Marshal(agentHookEvent{
		HookEventName: "UserPromptSubmit",
		Prompt:        "Update the release plan",
		SessionID:     "session-meter-1",
		TurnID:        "turn-meter-1",
	})
	if err != nil {
		t.Fatalf("marshal UserPromptSubmit hook: %v", err)
	}
	processAgentHook(context.Background(), promptInput, "codex", "codex")

	stopInput, err := json.Marshal(agentHookEvent{
		HookEventName: "Stop",
		SessionID:     "session-meter-1",
		TurnID:        "turn-meter-1",
	})
	if err != nil {
		t.Fatalf("marshal Stop hook: %v", err)
	}
	stopResponse := processAgentHook(context.Background(), stopInput, "codex", "codex")
	if len(stopResponse) != 0 {
		t.Fatalf("Stop response = %#v, want empty", stopResponse)
	}
	wantNotice := "Gesta active · Observed output: 1 code line, 2 doc words"
	nextPromptInput, err := json.Marshal(agentHookEvent{
		HookEventName: "UserPromptSubmit",
		Prompt:        "Continue",
		SessionID:     "session-meter-1",
		TurnID:        "turn-meter-2",
	})
	if err != nil {
		t.Fatalf("marshal next UserPromptSubmit hook: %v", err)
	}
	nextPromptResponse := processAgentHook(
		context.Background(),
		nextPromptInput,
		"codex",
		"codex",
	)
	if got := hookAdditionalContext(nextPromptResponse); got != pendingTurnNoticeContext(wantNotice) {
		t.Fatalf("next prompt context = %q, want %q", got, pendingTurnNoticeContext(wantNotice))
	}
	events, err = daemon.NewQueue(cfg.DataDir).ReadAll()
	if err != nil {
		t.Fatalf("read after Stop hook: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("events = %#v, want completed fileChange and MCP", events)
	}
	if events[0].Payload["category"] != "code" || events[0].Payload["characters"] != float64(11) {
		t.Fatalf("fileChange payload = %#v", events[0].Payload)
	}
	if events[0].Payload["schema_version"] != float64(3) || events[0].Payload["efficiency_eligible"] != true {
		t.Fatalf("fileChange eligibility payload = %#v", events[0].Payload)
	}
	all, _ := json.Marshal(events)
	for _, raw := range []string{"Release plan", "new content", "must not count", "must not be counted", "app.go", "failed.go", "session-meter-1", "turn-meter-1"} {
		if strings.Contains(string(all), raw) {
			t.Fatalf("Gross Ink events leaked raw value %q: %s", raw, all)
		}
	}
}

func TestGrossInkEventPreservesEfficiencyExclusionMetadata(t *testing.T) {
	cfg := daemon.Config{
		CustomerID:   "customer-1",
		DeploymentID: "deployment-1",
		DaemonID:     "daemon-1",
		DeviceID:     "device-1",
		DataDir:      t.TempDir(),
	}
	_, err := appendGrossMeasurementsWithSummary(cfg, grossObservation{
		CallID:     "call-1",
		SessionID:  "session-1",
		TurnID:     "turn-1",
		AgentType:  "codex",
		Source:     "codex",
		ObservedAt: time.Unix(1_700_000_000, 0),
		Measurements: []toolinput.Measurement{{
			ToolClass:                 "file_write",
			Category:                  toolinput.CategoryCode,
			Counts:                    toolinput.Counts{Characters: 200_000, Lines: 20_001, Words: 20_001},
			EfficiencyEligible:        false,
			EfficiencyExclusionReason: "observation_line_limit_exceeded",
		}},
	})
	if err != nil {
		t.Fatalf("appendGrossMeasurements: %v", err)
	}
	events, err := daemon.NewQueue(cfg.DataDir).ReadAll()
	if err != nil {
		t.Fatalf("read Gross Ink event: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("events = %#v, want one", events)
	}
	payload := events[0].Payload
	if payload["efficiency_eligible"] != false ||
		payload["efficiency_exclusion_reason"] != "observation_line_limit_exceeded" {
		t.Fatalf("eligibility metadata = %#v", payload)
	}
	if payload["lines"] != float64(20_001) {
		t.Fatalf("Gross Ink counts were dropped: %#v", payload)
	}
}

func TestCodexHookBlocksAnyBashCommandFromPolicy(t *testing.T) {
	tmp := t.TempDir()
	home := filepath.Join(tmp, "home")
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatalf("mkdir home: %v", err)
	}
	t.Setenv("HOME", home)

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
	response := processAgentHook(context.Background(), input, "codex", "codex")
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
	if err := daemon.SaveSensitiveRuleCache(cfg.DataDir, []model.SensitiveRule{openAIKeySensitiveRule()}, cfgTime()); err != nil {
		t.Fatalf("SaveSensitiveRuleCache: %v", err)
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
	response := processAgentHook(context.Background(), input, "codex", "codex")
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
	if got := atomic.LoadInt32(&eventRequests); got != 0 {
		t.Fatalf("event requests on prompt path = %d, want 0", got)
	}
	event := readSingleQueuedEvent(t, cfg)
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
	uploadedData, err := json.Marshal(event)
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
	if err := daemon.SaveSensitiveRuleCache(cfg.DataDir, []model.SensitiveRule{
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
	}, cfgTime()); err != nil {
		t.Fatalf("SaveSensitiveRuleCache: %v", err)
	}

	secret := "test_9xLmR7QpV2nB4sC8dF6gH1jK3zT5wY0aE9rU2iO4pS6dV8kN0m"
	input := []byte(`{
		"hook_event_name": "UserPromptSubmit",
		"prompt": "auth0 token is: ` + secret + `",
		"session_id": "built-in-sensitive-session"
	}`)
	response := processAgentHook(context.Background(), input, "codex", "codex")
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
	if got := atomic.LoadInt32(&eventRequests); got != 0 {
		t.Fatalf("event requests on prompt path = %d, want 0", got)
	}
	payload := readSingleQueuedEvent(t, cfg).Payload
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

func TestCodexHookRecordsNonBlockingSensitiveRule(t *testing.T) {
	tmp := t.TempDir()
	home := filepath.Join(tmp, "home")
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatalf("mkdir home: %v", err)
	}
	t.Setenv("HOME", home)

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
		case "/api/v1/context-rules":
			if err := json.NewEncoder(w).Encode(model.ContextRuleBundle{Version: "empty", Rules: []model.ContextRule{}}); err != nil {
				t.Fatalf("encode context rules: %v", err)
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
	if err := daemon.SaveSensitiveRuleCache(cfg.DataDir, []model.SensitiveRule{
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
	}, cfgTime()); err != nil {
		t.Fatalf("SaveSensitiveRuleCache: %v", err)
	}

	input := []byte(`{
		"hook_event_name": "UserPromptSubmit",
		"prompt": "customer_secret_123 should be observed",
		"session_id": "record-session"
	}`)
	response := processAgentHook(context.Background(), input, "codex", "codex")
	if len(response) != 0 {
		t.Fatalf("record-only finding should allow prompt, got %#v", response)
	}
	if got := atomic.LoadInt32(&eventRequests); got != 0 {
		t.Fatalf("event requests on prompt path = %d, want 0", got)
	}
	payload := readSingleQueuedEvent(t, cfg).Payload
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
	response := processAgentHook(context.Background(), input, "codex", "codex")
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
	response := processAgentHook(context.Background(), input, "codex", "codex")
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
	response := processAgentHook(context.Background(), input, "codex", "codex")
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
	response := processAgentHook(context.Background(), input, "codex", "codex")
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
