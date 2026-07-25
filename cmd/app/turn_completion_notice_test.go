package app

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gesta-run/gesta-agent/pkg/codexapp"
	"github.com/gesta-run/gesta-agent/pkg/daemon"
	"github.com/gesta-run/gesta-agent/pkg/model"
	"github.com/gesta-run/gesta-agent/pkg/turnreceipt"
)

func TestFormatTurnCompletionNoticeCombinesContextAppendAndOutput(t *testing.T) {
	receipt := turnreceipt.Receipt{
		PolicyMatchCount: 3,
		Output: turnreceipt.OutputSummary{
			CodeLines:   1280,
			TestLines:   42,
			DocWords:    310,
			ConfigLines: 8,
			OtherLines:  2,
		},
	}
	got := formatTurnCompletionNotice(receipt)
	want := "Gesta governance · Context append: 3 · " +
		"Observed output: 1,280 code lines, 42 test lines, 310 doc words, +2 categories"
	if got != want {
		t.Fatalf("notice = %q, want %q", got, want)
	}
}

func TestFormatTurnCompletionNoticeReportsContextAppendOnly(t *testing.T) {
	receipt := turnreceipt.Receipt{
		PolicyMatchCount: 2,
	}
	got := formatTurnCompletionNotice(receipt)
	want := "Gesta governance · Context append: 2"
	if got != want {
		t.Fatalf("notice = %q, want %q", got, want)
	}
	if len([]rune(got)) > maxTurnCompletionNoticeRunes {
		t.Fatalf("notice exceeds %d runes: %q", maxTurnCompletionNoticeRunes, got)
	}
}

func TestFormatTurnCompletionNoticeIsSilentWithoutMaterialAction(t *testing.T) {
	if got := formatTurnCompletionNotice(turnreceipt.Receipt{}); got != "" {
		t.Fatalf("notice = %q, want empty", got)
	}
}

func TestFormatTurnCompletionNoticeReportsOutputWithoutContextAppend(t *testing.T) {
	receipt := turnreceipt.Receipt{
		Output: turnreceipt.OutputSummary{DocWords: 23},
	}
	got := formatTurnCompletionNotice(receipt)
	want := "Gesta governance · Observed output: 23 doc words"
	if got != want {
		t.Fatalf("notice = %q, want %q", got, want)
	}
}

func TestClaudeStopQueuesOneShotNoticeForNextPrompt(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home")
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatalf("mkdir home: %v", err)
	}
	t.Setenv("HOME", home)

	cfg := daemon.NewDirectRuntimeConfig("http://127.0.0.1:1", "dtok_claude_notice")
	if err := daemon.SaveConfig("", cfg); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}
	if err := daemon.SaveSensitiveRuleCache(cfg.DataDir, []model.SensitiveRule{}, time.Now()); err != nil {
		t.Fatalf("SaveSensitiveRuleCache: %v", err)
	}
	if err := daemon.SaveContextRuleCache(cfg.DataDir, model.ContextRuleBundle{
		Version: "bundle-notice",
		Rules: []model.ContextRule{
			{
				RuleID: "rule-default",
				Name:   "Default Guidance",
				Status: "active", MatchType: "always",
				AgentType: "claude_code", ContextContent: "Follow organization defaults.",
			},
			{
				RuleID: "rule-pr",
				Name:   "PR Standards",
				Status: "active", MatchType: "keyword_any", Keywords: []string{"review"},
				AgentType: "claude_code", ContextContent: "Review the complete diff.",
			},
		},
	}, time.Now()); err != nil {
		t.Fatalf("SaveContextRuleCache: %v", err)
	}

	promptResponse := runAgentHook(t, agentHookEvent{
		HookEventName: "UserPromptSubmit",
		Prompt:        "Review this change",
		SessionID:     "claude-notice-session",
	}, "claude_code")
	if !strings.Contains(marshalHookResponse(t, promptResponse), "Review the complete diff.") {
		t.Fatalf("context response = %#v", promptResponse)
	}
	runAgentHook(t, agentHookEvent{
		HookEventName: "PostToolUse",
		ToolName:      "Write",
		ToolInput: map[string]interface{}{
			"file_path": "main.go",
			"content":   "package main\nfunc main() {}",
		},
		ToolUseID: "claude-write-notice",
		SessionID: "claude-notice-session",
	}, "claude_code")

	stop := runAgentHook(t, agentHookEvent{
		HookEventName: "Stop",
		SessionID:     "claude-notice-session",
	}, "claude_code")
	if len(stop) != 0 {
		t.Fatalf("Stop response = %#v, want empty", stop)
	}

	wantNotice := "Gesta governance · Context append: 1 · Observed output: 2 code lines"
	nextPrompt := runAgentHook(t, agentHookEvent{
		HookEventName: "UserPromptSubmit",
		Prompt:        "Continue with the next task",
		SessionID:     "claude-notice-session",
	}, "claude_code")
	gotContext := hookAdditionalContext(nextPrompt)
	if !strings.Contains(gotContext, "Follow organization defaults.") ||
		!strings.Contains(gotContext, pendingTurnNoticeContext(wantNotice)) {
		t.Fatalf("next prompt context = %q", gotContext)
	}

	thirdPrompt := runAgentHook(t, agentHookEvent{
		HookEventName: "UserPromptSubmit",
		Prompt:        "Continue again",
		SessionID:     "claude-notice-session",
	}, "claude_code")
	if strings.Contains(hookAdditionalContext(thirdPrompt), "gesta_activity_notice") {
		t.Fatalf("third prompt repeated pending notice: %#v", thirdPrompt)
	}
}

func TestClaudeStopIsSilentWithoutContextOrOutput(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home")
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatalf("mkdir home: %v", err)
	}
	t.Setenv("HOME", home)
	cfg := daemon.NewDirectRuntimeConfig("http://127.0.0.1:1", "dtok_claude_silent_notice")
	if err := daemon.SaveConfig("", cfg); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}
	if err := daemon.SaveSensitiveRuleCache(cfg.DataDir, []model.SensitiveRule{}, time.Now()); err != nil {
		t.Fatalf("SaveSensitiveRuleCache: %v", err)
	}
	if err := daemon.SaveContextRuleCache(cfg.DataDir, model.ContextRuleBundle{
		Version: "empty",
		Rules:   []model.ContextRule{},
	}, time.Now()); err != nil {
		t.Fatalf("SaveContextRuleCache: %v", err)
	}
	runAgentHook(t, agentHookEvent{
		HookEventName: "UserPromptSubmit",
		Prompt:        "Explain this function",
		SessionID:     "claude-silent-session",
	}, "claude_code")
	stop := runAgentHook(t, agentHookEvent{
		HookEventName: "Stop",
		SessionID:     "claude-silent-session",
	}, "claude_code")
	if len(stop) != 0 {
		t.Fatalf("Stop response = %#v, want empty", stop)
	}
	nextPrompt := runAgentHook(t, agentHookEvent{
		HookEventName: "UserPromptSubmit",
		Prompt:        "Continue",
		SessionID:     "claude-silent-session",
	}, "claude_code")
	if strings.Contains(hookAdditionalContext(nextPrompt), "gesta_activity_notice") {
		t.Fatalf("next prompt unexpectedly received notice: %#v", nextPrompt)
	}
}

func TestEveryPromptContextDoesNotCreateNoticeWhenTurnReadFails(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home")
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatalf("mkdir home: %v", err)
	}
	t.Setenv("HOME", home)
	cfg := daemon.NewDirectRuntimeConfig("http://127.0.0.1:1", "dtok_codex_context_notice")
	if err := daemon.SaveConfig("", cfg); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}
	if err := daemon.SaveSensitiveRuleCache(cfg.DataDir, []model.SensitiveRule{}, time.Now()); err != nil {
		t.Fatalf("SaveSensitiveRuleCache: %v", err)
	}
	if err := daemon.SaveContextRuleCache(cfg.DataDir, model.ContextRuleBundle{
		Version: "bundle-codex-notice",
		Rules: []model.ContextRule{{
			RuleID: "rule-operations",
			Name:   "Operations Safety",
			Status: "active", MatchType: "always", AgentType: "codex",
			ContextContent: "Verify the target environment.",
		}},
	}, time.Now()); err != nil {
		t.Fatalf("SaveContextRuleCache: %v", err)
	}
	originalReadCodexTurn := readCodexTurn
	readCodexTurn = func(context.Context, string, string) (codexapp.Turn, error) {
		return codexapp.Turn{}, errors.New("app server unavailable")
	}
	t.Cleanup(func() { readCodexTurn = originalReadCodexTurn })

	runAgentHook(t, agentHookEvent{
		HookEventName: "UserPromptSubmit",
		Prompt:        "Deploy the service",
		SessionID:     "codex-context-session",
		TurnID:        "codex-context-turn",
	}, "codex")
	stop := runAgentHook(t, agentHookEvent{
		HookEventName: "Stop",
		SessionID:     "codex-context-session",
		TurnID:        "codex-context-turn",
	}, "codex")
	if len(stop) != 0 {
		t.Fatalf("Stop response = %#v, want empty", stop)
	}
	nextPrompt := runAgentHook(t, agentHookEvent{
		HookEventName: "UserPromptSubmit",
		Prompt:        "Continue",
		SessionID:     "codex-context-session",
		TurnID:        "codex-next-turn",
	}, "codex")
	gotContext := hookAdditionalContext(nextPrompt)
	if !strings.Contains(gotContext, "Verify the target environment.") {
		t.Fatalf("next prompt context = %q", gotContext)
	}
	if strings.Contains(gotContext, "gesta_activity_notice") {
		t.Fatalf("every-prompt context created a pending notice: %q", gotContext)
	}
}

func TestPendingNoticeMergesWithCurrentOrganizationContext(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home")
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatalf("mkdir home: %v", err)
	}
	t.Setenv("HOME", home)
	cfg := daemon.NewDirectRuntimeConfig("http://127.0.0.1:1", "dtok_merge_notice")
	if err := daemon.SaveConfig("", cfg); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}
	if err := daemon.SaveSensitiveRuleCache(cfg.DataDir, []model.SensitiveRule{}, time.Now()); err != nil {
		t.Fatalf("SaveSensitiveRuleCache: %v", err)
	}
	if err := daemon.SaveContextRuleCache(cfg.DataDir, model.ContextRuleBundle{
		Version: "merge-context",
		Rules: []model.ContextRule{{
			RuleID: "rule-current",
			Name:   "Current Rule",
			Status: "active", MatchType: "always", AgentType: "claude_code",
			ContextContent: "Apply the current organization guidance.",
		}},
	}, time.Now()); err != nil {
		t.Fatalf("SaveContextRuleCache: %v", err)
	}
	store := turnreceipt.NewStore(cfg.DataDir)
	if err := store.SavePending(
		"claude_code",
		"merge-session",
		"Gesta governance · Observed output: 4 code lines",
	); err != nil {
		t.Fatalf("SavePending: %v", err)
	}

	response := runAgentHook(t, agentHookEvent{
		HookEventName: "UserPromptSubmit",
		Prompt:        "Implement the feature",
		SessionID:     "merge-session",
	}, "claude_code")
	context := hookAdditionalContext(response)
	if !strings.Contains(context, "Apply the current organization guidance.") ||
		!strings.Contains(context, "Gesta governance · Observed output: 4 code lines") ||
		!strings.Contains(context, "At the bottom of your response") {
		t.Fatalf("merged additional context = %q", context)
	}
}

func TestBlockedPromptDoesNotConsumePendingNotice(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home")
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatalf("mkdir home: %v", err)
	}
	t.Setenv("HOME", home)
	cfg := daemon.NewDirectRuntimeConfig("http://127.0.0.1:1", "dtok_blocked_notice")
	if err := daemon.SaveConfig("", cfg); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}
	if err := daemon.SaveSensitiveRuleCache(
		cfg.DataDir,
		[]model.SensitiveRule{openAIKeySensitiveRule()},
		time.Now(),
	); err != nil {
		t.Fatalf("SaveSensitiveRuleCache: %v", err)
	}
	if err := turnreceipt.NewStore(cfg.DataDir).SavePending(
		"codex",
		"blocked-notice-session",
		"Gesta governance · Context append: 1",
	); err != nil {
		t.Fatalf("SavePending: %v", err)
	}

	blocked := runAgentHook(t, agentHookEvent{
		HookEventName: "UserPromptSubmit",
		Prompt:        "Use sk-" + strings.Repeat("a", 48),
		SessionID:     "blocked-notice-session",
	}, "codex")
	if blocked["decision"] != "block" {
		t.Fatalf("blocked response = %#v", blocked)
	}
	pending, found, err := turnreceipt.NewStore(cfg.DataDir).ConsumePending(
		"codex",
		"blocked-notice-session",
	)
	if err != nil || !found {
		t.Fatalf("pending after blocked prompt found = %v, err = %v", found, err)
	}
	if pending.Notice != "Gesta governance · Context append: 1" {
		t.Fatalf("pending notice = %q", pending.Notice)
	}
}

func TestCodexStopRecordsOutputWithoutNoticeReceipt(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home")
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatalf("mkdir home: %v", err)
	}
	t.Setenv("HOME", home)
	cfg := daemon.NewDirectRuntimeConfig("http://127.0.0.1:1", "dtok_codex_no_receipt")
	if err := daemon.SaveConfig("", cfg); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}

	originalReadCodexTurn := readCodexTurn
	readCodexTurn = func(context.Context, string, string) (codexapp.Turn, error) {
		return codexapp.Turn{
			ID:     "turn-without-receipt",
			Status: "completed",
			Items: []codexapp.Item{{
				ID:     "file-without-receipt",
				Type:   "fileChange",
				Status: "completed",
				Changes: []codexapp.FileChange{{
					Path: "main.go",
					Kind: codexapp.ChangeKind{Type: "add"},
					Diff: "@@\n+package main\n",
				}},
			}},
		}, nil
	}
	t.Cleanup(func() { readCodexTurn = originalReadCodexTurn })

	stop := runAgentHook(t, agentHookEvent{
		HookEventName: "Stop",
		SessionID:     "session-without-receipt",
		TurnID:        "turn-without-receipt",
	}, "codex")
	if len(stop) != 0 {
		t.Fatalf("Stop response = %#v, want no notice without a receipt", stop)
	}
	events, err := daemon.NewQueue(cfg.DataDir).ReadAll()
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if len(events) != 1 || events[0].Payload["category"] != "code" {
		t.Fatalf("telemetry events = %#v, want one code event", events)
	}
}

func runAgentHook(
	t *testing.T,
	event agentHookEvent,
	agentType string,
) map[string]interface{} {
	t.Helper()
	input, err := json.Marshal(event)
	if err != nil {
		t.Fatalf("marshal hook event: %v", err)
	}
	return processAgentHook(context.Background(), input, agentType, agentType)
}

func marshalHookResponse(t *testing.T, response map[string]interface{}) string {
	t.Helper()
	data, err := json.Marshal(response)
	if err != nil {
		t.Fatalf("marshal hook response: %v", err)
	}
	return string(data)
}

func hookAdditionalContext(response map[string]interface{}) string {
	output, _ := response["hookSpecificOutput"].(map[string]interface{})
	context, _ := output["additionalContext"].(string)
	return context
}
