package cli

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gesta-run/gesta-agent/pkg/daemon"
	"github.com/gesta-run/gesta-agent/pkg/eventqueue"
	"github.com/gesta-run/gesta-agent/pkg/model"
	"github.com/gesta-run/gesta-agent/pkg/rulecache"
	"github.com/gesta-run/gesta-agent/pkg/turnreceipt"
)

func TestCodexHookInjectsOrganizationContextWithoutUploadingPrompt(t *testing.T) {
	cfg := setupContextHookTest(t, []model.ContextRule{
		{RuleID: "crule_always", Name: "Always", Status: "active", MatchType: "always", AgentType: "all", Priority: 10, ContextContent: "Use verified sources."},
		{RuleID: "crule_code", Name: "Code quality", Status: "active", MatchType: "keyword_any", Keywords: []string{"refactor"}, AgentType: "codex", Priority: 100, ContextContent: "Keep files focused and functions small."},
	})

	prompt := "Please refactor the billing package"
	input, err := json.Marshal(agentHookEvent{
		HookEventName: "UserPromptSubmit", Prompt: prompt, SessionID: "session-raw", TurnID: "turn-raw",
	})
	if err != nil {
		t.Fatalf("marshal hook input: %v", err)
	}
	response := processAgentHook(context.Background(), input, "codex", "codex")
	data, err := json.Marshal(response)
	if err != nil {
		t.Fatalf("marshal hook response: %v", err)
	}
	text := string(data)
	if !strings.Contains(text, `"hookEventName":"UserPromptSubmit"`) ||
		!strings.Contains(text, "Keep files focused and functions small.\\n\\nUse verified sources.") {
		t.Fatalf("unexpected hook response: %s", text)
	}
	queued, err := eventqueue.NewQueue(cfg.DataDir).ReadAll()
	if err != nil {
		t.Fatalf("read queued events: %v", err)
	}
	if len(queued) != 1 || queued[0].EventType != "context_rule.matched" {
		t.Fatalf("queued events = %#v", queued)
	}
	queuedData, err := json.Marshal(queued)
	if err != nil {
		t.Fatalf("marshal uploaded events: %v", err)
	}
	for _, raw := range []string{
		prompt,
		"session-raw",
		"turn-raw",
		"Keep files focused and functions small.",
		"Use verified sources.",
	} {
		if strings.Contains(string(queuedData), raw) {
			t.Fatalf("queued event leaked %q: %s", raw, queuedData)
		}
	}
	payload := queued[0].Payload
	if payload["prompt_text_stored"] != false {
		t.Fatalf("unexpected context event payload: %#v", payload)
	}
	for _, key := range []string{"rule_ids", "matched_count"} {
		if _, exists := payload[key]; exists {
			t.Fatalf("context event contains redundant %q: %#v", key, payload)
		}
	}
	matches, ok := payload["rule_matches"].([]interface{})
	if !ok || len(matches) != 2 {
		t.Fatalf("rule match snapshots = %#v, want 2 entries", payload["rule_matches"])
	}
	firstMatch, ok := matches[0].(map[string]interface{})
	if !ok ||
		firstMatch["rule_id"] != "crule_code" ||
		firstMatch["rule_name"] != "Code quality" ||
		firstMatch["match_type"] != "keyword_any" {
		t.Fatalf("first rule match snapshot = %#v", matches[0])
	}
	for _, key := range []string{"prompt_hash", "session_id_hash", "turn_id_hash"} {
		if _, exists := payload[key]; exists {
			t.Fatalf("context event contains unnecessary identifier %q: %#v", key, payload)
		}
	}
}

func TestCodexHookMatchesOnlyRequestBodyInFileEnvelope(t *testing.T) {
	cfg := setupContextHookTest(t, []model.ContextRule{
		{RuleID: "crule_path", Name: "Path only", Status: "active", MatchType: "keyword_any", Keywords: []string{"refactor"}, AgentType: "codex", Priority: 100, ContextContent: "PATH CONTEXT MUST NOT APPEAR"},
		{RuleID: "crule_body", Name: "Layout", Status: "active", MatchType: "keyword_any", Keywords: []string{"layout"}, AgentType: "codex", Priority: 90, ContextContent: "Apply the layout standard."},
		{RuleID: "crule_always", Name: "Always", Status: "active", MatchType: "always", AgentType: "all", Priority: 10, ContextContent: "Use verified sources."},
	})
	const prompt = `# Files mentioned by the user:

## refactor-notes.png: /tmp/refactor-notes.png

## My request for Codex:

Please review this layout.

<image name=[Image #1] path="/tmp/refactor-notes.png">
</image>`
	input, err := json.Marshal(agentHookEvent{
		HookEventName: "UserPromptSubmit",
		Prompt:        prompt,
		SessionID:     "session-envelope",
		TurnID:        "turn-envelope",
	})
	if err != nil {
		t.Fatalf("marshal hook input: %v", err)
	}
	response := processAgentHook(context.Background(), input, "codex", "codex")
	data, err := json.Marshal(response)
	if err != nil {
		t.Fatalf("marshal hook response: %v", err)
	}
	text := string(data)
	if !strings.Contains(text, "Apply the layout standard.\\n\\nUse verified sources.") {
		t.Fatalf("request context missing from response: %s", text)
	}
	if strings.Contains(text, "PATH CONTEXT MUST NOT APPEAR") {
		t.Fatalf("attachment metadata matched context rule: %s", text)
	}

	queued, err := eventqueue.NewQueue(cfg.DataDir).ReadAll()
	if err != nil {
		t.Fatalf("read queued events: %v", err)
	}
	if len(queued) != 1 {
		t.Fatalf("queued events = %#v, want one context match", queued)
	}
	matches, ok := queued[0].Payload["rule_matches"].([]interface{})
	if !ok || len(matches) != 2 {
		t.Fatalf("rule matches = %#v, want body and always rules", queued[0].Payload["rule_matches"])
	}
	for _, rawMatch := range matches {
		match, ok := rawMatch.(map[string]interface{})
		if ok && match["rule_id"] == "crule_path" {
			t.Fatalf("path-only rule was recorded: %#v", matches)
		}
	}
}

func TestCodexHookMatchesRequestWithoutFileEnvelope(t *testing.T) {
	cfg := setupContextHookTest(t, []model.ContextRule{
		{RuleID: "crule_ambient", Name: "Ambient only", Status: "active", MatchType: "keyword_any", Keywords: []string{"activity"}, AgentType: "codex", Priority: 100, ContextContent: "AMBIENT CONTEXT MUST NOT APPEAR"},
		{RuleID: "crule_pr", Name: "PR Standards", Status: "active", MatchType: "keyword_any", Keywords: []string{"PR"}, AgentType: "codex", Priority: 90, ContextContent: "Apply the PR standard."},
	})
	const prompt = `<in-app-browser-context source="ambient-ui-state">
Current URL: http://127.0.0.1:3333/activity/example
</in-app-browser-context>

## My request for Codex:

提PR`
	const sessionID = "session-request-only"
	const turnID = "turn-request-only"
	input, err := json.Marshal(agentHookEvent{
		HookEventName: "UserPromptSubmit",
		Prompt:        prompt,
		SessionID:     sessionID,
		TurnID:        turnID,
	})
	if err != nil {
		t.Fatalf("marshal hook input: %v", err)
	}
	response := processAgentHook(context.Background(), input, "codex", "codex")
	data, err := json.Marshal(response)
	if err != nil {
		t.Fatalf("marshal hook response: %v", err)
	}
	if !strings.Contains(string(data), "Apply the PR standard.") {
		t.Fatalf("PR context missing from response: %s", data)
	}
	if strings.Contains(string(data), "AMBIENT CONTEXT MUST NOT APPEAR") {
		t.Fatalf("ambient UI context matched a rule: %s", data)
	}

	receipt, found, err := turnreceipt.NewStore(cfg.DataDir).Consume(
		"codex",
		sessionID,
		turnID,
	)
	if err != nil || !found {
		t.Fatalf("turn receipt found = %v, err = %v", found, err)
	}
	if len(receipt.ContextMatches) != 1 ||
		receipt.ContextMatches[0].RuleID != "crule_pr" {
		t.Fatalf("turn receipt context matches = %#v", receipt.ContextMatches)
	}
}

func TestCodexHookSkipsContextForMalformedFileEnvelope(t *testing.T) {
	cfg := setupContextHookTest(t, []model.ContextRule{
		{RuleID: "crule_path", Name: "Path only", Status: "active", MatchType: "keyword_any", Keywords: []string{"refactor"}, AgentType: "codex", Priority: 100, ContextContent: "must not appear"},
		{RuleID: "crule_always", Name: "Always", Status: "active", MatchType: "always", AgentType: "all", Priority: 10, ContextContent: "must not appear"},
	})
	const prompt = `# Files mentioned by the user:

## refactor-notes.png: /tmp/refactor-notes.png`
	input, err := json.Marshal(agentHookEvent{
		HookEventName: "UserPromptSubmit",
		Prompt:        prompt,
		SessionID:     "session-malformed",
		TurnID:        "turn-malformed",
	})
	if err != nil {
		t.Fatalf("marshal hook input: %v", err)
	}
	response := processAgentHook(context.Background(), input, "codex", "codex")
	if len(response) != 0 {
		t.Fatalf("malformed envelope response = %#v, want no context", response)
	}
	queued, err := eventqueue.NewQueue(cfg.DataDir).ReadAll()
	if err != nil {
		t.Fatalf("read queued events: %v", err)
	}
	if len(queued) != 0 {
		t.Fatalf("malformed envelope queued context events: %#v", queued)
	}
}

func TestCodexHookScansRawEnvelopeForSensitiveData(t *testing.T) {
	cfg := setupContextHookTest(t, []model.ContextRule{{
		RuleID: "crule_always", Name: "Always", Status: "active", MatchType: "always",
		AgentType: "all", Priority: 10, ContextContent: "must not appear",
	}})
	if err := rulecache.SaveSensitiveRuleCache(cfg.DataDir, []model.SensitiveRule{openAIKeySensitiveRule()}, time.Now()); err != nil {
		t.Fatalf("SaveSensitiveRuleCache: %v", err)
	}
	secret := "sk-" + strings.Repeat("a", 32)
	prompt := `# Files mentioned by the user:

## screenshot.png: /tmp/` + secret + `/screenshot.png

## My request for Codex:

Review this image.`
	input, err := json.Marshal(agentHookEvent{
		HookEventName: "UserPromptSubmit",
		Prompt:        prompt,
		SessionID:     "session-sensitive-envelope",
		TurnID:        "turn-sensitive-envelope",
	})
	if err != nil {
		t.Fatalf("marshal hook input: %v", err)
	}
	response := processAgentHook(context.Background(), input, "codex", "codex")
	data, err := json.Marshal(response)
	if err != nil {
		t.Fatalf("marshal response: %v", err)
	}
	if !strings.Contains(string(data), `"decision":"block"`) || strings.Contains(string(data), "must not appear") {
		t.Fatalf("unexpected sensitive envelope response: %s", data)
	}
}

func TestHookContextRulesUsesOnlyDaemonCache(t *testing.T) {
	dataDir := t.TempDir()
	cfg := daemon.NewDirectRuntimeConfig("http://127.0.0.1:1", "dtok_install_only")
	cfg.DataDir = dataDir
	if _, ok := hookContextRules(cfg); ok {
		t.Fatal("hook unexpectedly fetched rules without a daemon cache")
	}
	if err := rulecache.SaveContextRuleCache(cfg.DataDir, model.ContextRuleBundle{
		Version: "bundle-daemon",
		Rules: []model.ContextRule{{
			RuleID: "crule_daemon", Status: "active", MatchType: "always",
			AgentType: "all", ContextContent: "Use the organization standard.",
		}},
	}, time.Now()); err != nil {
		t.Fatalf("SaveContextRuleCache: %v", err)
	}
	cache, ok := hookContextRules(cfg)
	if !ok || cache.Version != "bundle-daemon" || len(cache.Rules) != 1 {
		t.Fatalf("daemon cache = %+v, ok = %v", cache, ok)
	}
}

func setupContextHookTest(t *testing.T, rules []model.ContextRule) daemon.Config {
	t.Helper()
	home := filepath.Join(t.TempDir(), "home")
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatalf("mkdir home: %v", err)
	}
	t.Setenv("HOME", home)

	cfg := daemon.NewDirectRuntimeConfig("http://127.0.0.1:1", "dtok_codex_context")
	if err := daemon.SaveConfig("", cfg); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}
	if err := rulecache.SaveSensitiveRuleCache(cfg.DataDir, []model.SensitiveRule{}, time.Now()); err != nil {
		t.Fatalf("SaveSensitiveRuleCache: %v", err)
	}
	if err := rulecache.SaveContextRuleCache(cfg.DataDir, model.ContextRuleBundle{
		Version: "bundle-v1",
		Rules:   rules,
	}, time.Now()); err != nil {
		t.Fatalf("SaveContextRuleCache: %v", err)
	}
	return cfg
}

func TestCodexHookDoesNotInjectContextWhenSensitivePromptIsBlocked(t *testing.T) {
	tmp := t.TempDir()
	home := filepath.Join(tmp, "home")
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatalf("mkdir home: %v", err)
	}
	t.Setenv("HOME", home)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/events" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	}))
	defer server.Close()
	cfg := daemon.NewDirectRuntimeConfig(server.URL, "dtok_codex_context_block")
	if err := daemon.SaveConfig("", cfg); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}
	if err := rulecache.SaveSensitiveRuleCache(cfg.DataDir, []model.SensitiveRule{openAIKeySensitiveRule()}, time.Now()); err != nil {
		t.Fatalf("SaveSensitiveRuleCache: %v", err)
	}
	if err := rulecache.SaveContextRuleCache(cfg.DataDir, model.ContextRuleBundle{
		Version: "bundle-v1",
		Rules:   []model.ContextRule{{RuleID: "crule", Status: "active", MatchType: "always", AgentType: "all", ContextContent: "must not appear"}},
	}, time.Now()); err != nil {
		t.Fatalf("SaveContextRuleCache: %v", err)
	}

	secret := "sk-" + strings.Repeat("a", 32)
	input := []byte(`{"hook_event_name":"UserPromptSubmit","prompt":"use ` + secret + `"}`)
	response := processAgentHook(context.Background(), input, "codex", "codex")
	data, err := json.Marshal(response)
	if err != nil {
		t.Fatalf("marshal response: %v", err)
	}
	if !strings.Contains(string(data), `"decision":"block"`) || strings.Contains(string(data), "must not appear") {
		t.Fatalf("unexpected blocked response: %s", data)
	}
}
