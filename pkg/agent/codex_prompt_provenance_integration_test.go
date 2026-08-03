package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/gesta-run/gesta-agent/pkg/daemon"
	"github.com/gesta-run/gesta-agent/pkg/eventqueue"
	"github.com/gesta-run/gesta-agent/pkg/model"
	"github.com/gesta-run/gesta-agent/pkg/rulecache"
)

func TestCodexHookScansOnlyUserAuthoredPromptScope(t *testing.T) {
	cfg := setupSensitivePromptProvenanceTest(t, "dtok_codex_hook_prompt_scope")
	prompt := `# Files mentioned by the user:

## screenshot.png: /tmp/screenshot.png

Hidden task metadata: customer_secret_123

## My request for Codex:

Please review the screenshot.

<image name=[Image #1] path="/tmp/screenshot.png">
</image>`
	input := marshalVerifiedCodexPrompt(t, agentHookEvent{
		HookEventName: "UserPromptSubmit",
		Prompt:        prompt,
	})

	response := processAgentHook(context.Background(), input, "codex", "codex")
	if len(response) != 0 {
		t.Fatalf("generated envelope content should not block the user prompt: %#v", response)
	}
	assertQueuedEventCount(t, cfg, 0)
}

func TestCodexHookIgnoresSyntheticTurnPrompt(t *testing.T) {
	cfg := setupSensitivePromptProvenanceTest(t, "dtok_codex_hook_synthetic_turn")
	transcriptPath := writeCodexTranscript(t,
		`{"type":"event_msg","payload":{"type":"task_started","turn_id":"turn-visible"}}`,
	)
	input, err := json.Marshal(agentHookEvent{
		HookEventName:  "UserPromptSubmit",
		Prompt:         `[{"title":"customer_secret_123","updatedAt":"2026-07-25T16:03:26.000Z"}]`,
		TurnID:         "turn-synthetic",
		TranscriptPath: transcriptPath,
	})
	if err != nil {
		t.Fatalf("marshal synthetic hook input: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()

	response := processAgentHook(ctx, input, "codex", "codex")
	if len(response) != 0 {
		t.Fatalf("synthetic turn should be ignored: %#v", response)
	}
	assertQueuedEventCount(t, cfg, 0)
}

func TestCodexHookProcessesVerifiedTurnPrompt(t *testing.T) {
	cfg := setupSensitivePromptProvenanceTest(t, "dtok_codex_hook_verified_turn")
	input := marshalVerifiedCodexPrompt(t, agentHookEvent{
		HookEventName: "UserPromptSubmit",
		Prompt:        "observe customer_secret_123",
		TurnID:        "turn-visible",
	})

	response := processAgentHook(context.Background(), input, "codex", "codex")
	if len(response) != 0 {
		t.Fatalf("record-only verified turn should be allowed: %#v", response)
	}
	assertQueuedEventCount(t, cfg, 1)
}

func setupSensitivePromptProvenanceTest(t *testing.T, token string) daemon.Config {
	t.Helper()
	home := filepath.Join(t.TempDir(), "home")
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatalf("mkdir home: %v", err)
	}
	t.Setenv("HOME", home)

	rule := customerSecretRecordRule()
	server := newHookRuleServer(t, []model.SensitiveRule{rule})
	cfg := daemon.NewDirectRuntimeConfig(server.Server.URL, token)
	if err := daemon.SaveConfig("", cfg); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}
	if err := rulecache.SaveSensitiveRuleCache(cfg.DataDir, []model.SensitiveRule{rule}, cfgTime()); err != nil {
		t.Fatalf("SaveSensitiveRuleCache: %v", err)
	}
	return cfg
}

func assertQueuedEventCount(t *testing.T, cfg daemon.Config, want int) {
	t.Helper()
	stats, err := eventqueue.NewQueue(cfg.DataDir).Stats()
	if err != nil {
		t.Fatalf("queue stats: %v", err)
	}
	if stats.QueuedEvents != want {
		t.Fatalf("queued events = %d, want %d", stats.QueuedEvents, want)
	}
}
