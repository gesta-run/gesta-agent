package cli

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

	"github.com/gesta-run/gesta-agent/pkg/daemon"
	"github.com/gesta-run/gesta-agent/pkg/eventqueue"
	"github.com/gesta-run/gesta-agent/pkg/model"
	"github.com/gesta-run/gesta-agent/pkg/rulecache"
)

func TestClaudeHookMeasuresWriteAndMCPAfterSuccessfulToolUse(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home")
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatalf("mkdir home: %v", err)
	}
	t.Setenv("HOME", home)

	cfg := daemon.NewDirectRuntimeConfig("http://127.0.0.1:1", "dtok_claude_ink")
	if err := daemon.SaveConfig("", cfg); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}
	for _, event := range []agentHookEvent{
		{
			HookEventName: "PostToolUse",
			ToolName:      "Write",
			ToolInput:     map[string]interface{}{"file_path": "docs/guide.md", "content": "hello docs"},
			ToolUseID:     "claude-write-1",
			SessionID:     "claude-session-1",
		},
		{
			HookEventName: "PostToolUse",
			ToolName:      "mcp__notion__create_page",
			ToolInput:     map[string]interface{}{"title": "Release plan"},
			ToolUseID:     "claude-mcp-1",
			SessionID:     "claude-session-1",
		},
	} {
		data, err := json.Marshal(event)
		if err != nil {
			t.Fatalf("marshal hook: %v", err)
		}
		processAgentHook(context.Background(), data, "claude_code", "claude_code")
	}
	events, err := eventqueue.NewQueue(cfg.DataDir).ReadAll()
	if err != nil {
		t.Fatalf("read queue: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("events = %#v, want write and MCP metrics", events)
	}
	if events[0].Payload["category"] != "docs" || events[1].Payload["category"] != "docs" {
		t.Fatalf("categories = %#v, %#v", events[0].Payload, events[1].Payload)
	}
}

func TestClaudeHookDoesNotMeasurePreToolUseAttempts(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home")
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatalf("mkdir home: %v", err)
	}
	t.Setenv("HOME", home)

	cfg := daemon.NewDirectRuntimeConfig("http://127.0.0.1:1", "dtok_claude_attempt")
	if err := daemon.SaveConfig("", cfg); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}
	data, err := json.Marshal(agentHookEvent{
		HookEventName: "PreToolUse",
		ToolName:      "Write",
		ToolInput:     map[string]interface{}{"file_path": "failed.go", "content": "must not count"},
		ToolUseID:     "claude-write-attempt",
		SessionID:     "claude-session-attempt",
	})
	if err != nil {
		t.Fatalf("marshal hook: %v", err)
	}
	processAgentHook(context.Background(), data, "claude_code", "claude_code")
	events, err := eventqueue.NewQueue(cfg.DataDir).ReadAll()
	if err != nil {
		t.Fatalf("read queue: %v", err)
	}
	if len(events) != 0 {
		t.Fatalf("PreToolUse attempt produced output metrics: %#v", events)
	}
}

func TestClaudeHookBlocksBashCommandFromPolicy(t *testing.T) {
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

	cfg := daemon.NewDirectRuntimeConfig(server.URL, "dtok_claude_hook")
	if err := daemon.SaveConfig("", cfg); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}
	if err := rulecache.SavePolicyCache(cfg.DataDir, []model.PolicyRule{
		{
			RuleID:      "rule_hook_block_ls",
			Name:        "Block ls",
			Description: "block any ls command",
			Status:      "active",
			AgentType:   "claude_code",
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
	response := processAgentHook(context.Background(), input, "claude_code", "claude_code")
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
	if len(uploaded.Events) != 1 {
		t.Fatalf("uploaded events = %d, want 1: %#v", len(uploaded.Events), uploaded.Events)
	}
	event := uploaded.Events[0]
	if event.AgentType != "claude_code" {
		t.Fatalf("policy.decision agent_type = %q, want claude_code", event.AgentType)
	}
}

func TestClaudeHookBlocksUserPromptSubmitSecret(t *testing.T) {
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

	cfg := daemon.NewDirectRuntimeConfig(server.URL, "dtok_claude_hook_sensitive")
	if err := daemon.SaveConfig("", cfg); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}
	if err := rulecache.SaveSensitiveRuleCache(cfg.DataDir, []model.SensitiveRule{openAIKeySensitiveRule()}, cfgTime()); err != nil {
		t.Fatalf("SaveSensitiveRuleCache: %v", err)
	}

	secret := "sk-" + strings.Repeat("a", 32)
	input := []byte(`{
		"hook_event_name": "UserPromptSubmit",
		"prompt": "please use ` + secret + ` for the test"
	}`)
	response := processAgentHook(context.Background(), input, "claude_code", "claude_code")
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
	if event.EventType != "sensitive.finding" || event.Source != "claude_code" || event.AgentType != "claude_code" {
		t.Fatalf("unexpected event envelope: %#v", event)
	}
	payload := event.Payload
	if payload["source"] != "user_prompt" || payload["action"] != "block" {
		t.Fatalf("unexpected finding payload source/action: %#v", payload)
	}
	if payload["category"] != "openai_api_key" {
		t.Fatalf("unexpected finding category: %#v", payload)
	}
}

func TestClaudeHookRecordsNonBlockingSensitiveRule(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home")
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatalf("mkdir home: %v", err)
	}
	t.Setenv("HOME", home)

	rule := customerSecretRecordRule()
	server := newHookRuleServer(t, []model.SensitiveRule{rule})
	cfg := daemon.NewDirectRuntimeConfig(server.Server.URL, "dtok_claude_hook_record")
	if err := daemon.SaveConfig("", cfg); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}
	if err := rulecache.SaveSensitiveRuleCache(cfg.DataDir, []model.SensitiveRule{rule}, cfgTime()); err != nil {
		t.Fatalf("SaveSensitiveRuleCache: %v", err)
	}

	input := []byte(`{
		"hook_event_name": "UserPromptSubmit",
		"prompt": "customer_secret_123 should be observed"
	}`)
	response := processAgentHook(context.Background(), input, "claude_code", "claude_code")
	if len(response) != 0 {
		t.Fatalf("record-only finding should allow prompt, got %#v", response)
	}
	if got := server.EventRequests.Load(); got != 0 {
		t.Fatalf("event requests on prompt path = %d, want 0", got)
	}
	event := readSingleQueuedEvent(t, cfg)
	if event.Source != "claude_code" || event.AgentType != "claude_code" {
		t.Fatalf("unexpected event envelope: %#v", event)
	}
	if event.Payload["action"] != "record" {
		t.Fatalf("unexpected record-only payload: %#v", event.Payload)
	}
}
