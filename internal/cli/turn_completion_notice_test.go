package cli

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gesta-run/gesta-agent/pkg/activitydetail"
	"github.com/gesta-run/gesta-agent/pkg/codexapp"
	"github.com/gesta-run/gesta-agent/pkg/daemon"
	"github.com/gesta-run/gesta-agent/pkg/eventqueue"
	"github.com/gesta-run/gesta-agent/pkg/model"
	"github.com/gesta-run/gesta-agent/pkg/rulecache"
	"github.com/gesta-run/gesta-agent/pkg/turnreceipt"
)

func TestFormatTurnCompletionNoticeCombinesContextAppendAndOutput(t *testing.T) {
	receipt := turnreceipt.Receipt{
		ContextMatches: testReceiptContextMatches(3),
		Output: turnreceipt.OutputSummary{
			CodeLines:   1280,
			TestLines:   42,
			DocWords:    310,
			ConfigLines: 8,
			OtherLines:  2,
		},
	}
	got := formatTurnCompletionNoticeWithDetails(receipt, "")
	want := "Gesta governance · Context append: 3 · " +
		"Observed output: 1,280 code lines, 42 test lines, 310 doc words, +2 categories"
	if got != want {
		t.Fatalf("notice = %q, want %q", got, want)
	}
}

func TestFormatTurnCompletionNoticeReportsContextAppendOnly(t *testing.T) {
	receipt := turnreceipt.Receipt{
		ContextMatches: testReceiptContextMatches(2),
	}
	got := formatTurnCompletionNoticeWithDetails(receipt, "")
	want := "Gesta governance · Context append: 2"
	if got != want {
		t.Fatalf("notice = %q, want %q", got, want)
	}
	if len([]rune(got)) > maxTurnCompletionNoticeRunes {
		t.Fatalf("notice exceeds %d runes: %q", maxTurnCompletionNoticeRunes, got)
	}
}

func TestFormatTurnCompletionNoticeAddsDetailsOnlyForContextMatches(t *testing.T) {
	detailURL := "http://127.0.0.1:3333/activity/activity_0123456789abcdef0123456789abcdef"
	withContext := formatTurnCompletionNoticeWithDetails(turnreceipt.Receipt{
		ContextMatches: testReceiptContextMatches(1),
		Output:         turnreceipt.OutputSummary{CodeLines: 2},
	}, detailURL)
	want := "Gesta governance · Context append: 1 · Observed output: 2 code lines · " +
		"[Details](" + detailURL + ")"
	if withContext != want {
		t.Fatalf("notice = %q, want %q", withContext, want)
	}
	outputOnly := formatTurnCompletionNoticeWithDetails(turnreceipt.Receipt{
		Output: turnreceipt.OutputSummary{CodeLines: 2},
	}, detailURL)
	if strings.Contains(outputOnly, "Details") {
		t.Fatalf("output-only notice contains Details: %q", outputOnly)
	}
}

func testReceiptContextMatches(count int) []turnreceipt.ContextRuleMatch {
	matches := make([]turnreceipt.ContextRuleMatch, 0, count)
	for index := 0; index < count; index++ {
		matches = append(matches, turnreceipt.ContextRuleMatch{
			RuleID:    "rule-" + string(rune('a'+index)),
			Name:      "Rule",
			MatchType: "keyword_any",
			Priority:  100,
			Content:   "Follow the organization rule.",
		})
	}
	return matches
}

func TestFormatTurnCompletionNoticeIsSilentWithoutMaterialAction(t *testing.T) {
	if got := formatTurnCompletionNoticeWithDetails(turnreceipt.Receipt{}, ""); got != "" {
		t.Fatalf("notice = %q, want empty", got)
	}
}

func TestFormatTurnCompletionNoticeReportsOutputWithoutContextAppend(t *testing.T) {
	receipt := turnreceipt.Receipt{
		Output: turnreceipt.OutputSummary{DocWords: 23},
	}
	got := formatTurnCompletionNoticeWithDetails(receipt, "")
	want := "Gesta governance · Observed output: 23 doc words"
	if got != want {
		t.Fatalf("notice = %q, want %q", got, want)
	}
}

func TestClaudeStopQueuesOneShotNoticeForNextPrompt(t *testing.T) {
	stubLocalActivityHealth(t, false)
	home := filepath.Join(t.TempDir(), "home")
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatalf("mkdir home: %v", err)
	}
	t.Setenv("HOME", home)

	cfg := daemon.NewDirectRuntimeConfig("http://127.0.0.1:1", "dtok_claude_notice")
	if err := daemon.SaveConfig("", cfg); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}
	saveTestOutputClassification(t, cfg)
	if err := rulecache.SaveSensitiveRuleCache(cfg.DataDir, []model.SensitiveRule{}, time.Now()); err != nil {
		t.Fatalf("SaveSensitiveRuleCache: %v", err)
	}
	if err := rulecache.SaveContextRuleCache(cfg.DataDir, model.ContextRuleBundle{
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
	if _, err := os.Stat(filepath.Join(cfg.DataDir, "activity-details")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unhealthy local UI created activity details: %v", err)
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

func TestCodexKeywordNoticeAppearsOnlyOnImmediatelyFollowingPrompt(t *testing.T) {
	stubLocalActivityHealth(t, false)
	home := filepath.Join(t.TempDir(), "home")
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatalf("mkdir home: %v", err)
	}
	t.Setenv("HOME", home)

	cfg := daemon.NewDirectRuntimeConfig("http://127.0.0.1:1", "dtok_codex_keyword_notice")
	if err := daemon.SaveConfig("", cfg); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}
	if err := rulecache.SaveSensitiveRuleCache(cfg.DataDir, []model.SensitiveRule{}, time.Now()); err != nil {
		t.Fatalf("SaveSensitiveRuleCache: %v", err)
	}
	if err := rulecache.SaveContextRuleCache(cfg.DataDir, model.ContextRuleBundle{
		Version: "bundle-codex-keyword-notice",
		Rules: []model.ContextRule{{
			RuleID: "rule-pr", Name: "PR Standards",
			Status: "active", MatchType: "keyword_any", Keywords: []string{"PR"},
			AgentType: "codex", ContextContent: "Apply PR standards.",
		}},
	}, time.Now()); err != nil {
		t.Fatalf("SaveContextRuleCache: %v", err)
	}
	originalReadCodexTurn := readCodexTurn
	readCodexTurn = func(_ context.Context, _ string, turnID string) (codexapp.Turn, error) {
		return codexapp.Turn{ID: turnID, Status: "completed"}, nil
	}
	t.Cleanup(func() { readCodexTurn = originalReadCodexTurn })

	firstPrompt := runAgentHook(t, agentHookEvent{
		HookEventName: "UserPromptSubmit",
		Prompt:        "提PR",
		SessionID:     "codex-keyword-notice-session",
		TurnID:        "turn-1",
	}, "codex")
	if !strings.Contains(hookAdditionalContext(firstPrompt), "Apply PR standards.") {
		t.Fatalf("first prompt context = %q", hookAdditionalContext(firstPrompt))
	}
	runAgentHook(t, agentHookEvent{
		HookEventName: "Stop",
		SessionID:     "codex-keyword-notice-session",
		TurnID:        "turn-1",
	}, "codex")

	secondPrompt := runAgentHook(t, agentHookEvent{
		HookEventName: "UserPromptSubmit",
		Prompt:        "continue",
		SessionID:     "codex-keyword-notice-session",
		TurnID:        "turn-2",
	}, "codex")
	const wantNotice = "Gesta governance · Context append: 1"
	if !strings.Contains(hookAdditionalContext(secondPrompt), pendingTurnNoticeContext(wantNotice)) {
		t.Fatalf("second prompt context = %q", hookAdditionalContext(secondPrompt))
	}
	runAgentHook(t, agentHookEvent{
		HookEventName: "Stop",
		SessionID:     "codex-keyword-notice-session",
		TurnID:        "turn-2",
	}, "codex")

	thirdPrompt := runAgentHook(t, agentHookEvent{
		HookEventName: "UserPromptSubmit",
		Prompt:        "continue again",
		SessionID:     "codex-keyword-notice-session",
		TurnID:        "turn-3",
	}, "codex")
	if strings.Contains(hookAdditionalContext(thirdPrompt), "gesta_activity_notice") {
		t.Fatalf("third prompt repeated pending notice: %#v", thirdPrompt)
	}
}

func TestClaudeStopCreatesLinkedLocalActivityDetailWhenUIIsHealthy(t *testing.T) {
	stubLocalActivityHealth(t, true)
	home := filepath.Join(t.TempDir(), "home")
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatalf("mkdir home: %v", err)
	}
	t.Setenv("HOME", home)
	cfg := daemon.NewDirectRuntimeConfig("http://127.0.0.1:1", "dtok_claude_linked_notice")
	if err := daemon.SaveConfig("", cfg); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}
	saveTestOutputClassification(t, cfg)
	if err := rulecache.SaveSensitiveRuleCache(cfg.DataDir, []model.SensitiveRule{}, time.Now()); err != nil {
		t.Fatalf("SaveSensitiveRuleCache: %v", err)
	}
	if err := rulecache.SaveContextRuleCache(cfg.DataDir, model.ContextRuleBundle{
		Version: "bundle-linked-notice",
		Rules: []model.ContextRule{{
			RuleID: "rule-linked", Name: "Linked Rule",
			Status: "active", MatchType: "regex", Pattern: "review",
			AgentType: "claude_code", Priority: 80, ContextContent: "Review carefully.",
		}},
	}, time.Now()); err != nil {
		t.Fatalf("SaveContextRuleCache: %v", err)
	}
	runAgentHook(t, agentHookEvent{
		HookEventName: "UserPromptSubmit",
		Prompt:        "review this",
		SessionID:     "claude-linked-session",
	}, "claude_code")
	runAgentHook(t, agentHookEvent{
		HookEventName: "Stop",
		SessionID:     "claude-linked-session",
	}, "claude_code")
	if _, err := os.Stat(filepath.Join(cfg.DataDir, "activity-details")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Stop created activity detail before notice consumption: %v", err)
	}
	if err := rulecache.SaveContextRuleCache(cfg.DataDir, model.ContextRuleBundle{
		Version: "bundle-linked-notice-updated",
		Rules: []model.ContextRule{{
			RuleID: "rule-linked", Name: "Linked Rule",
			Status: "active", MatchType: "regex", Pattern: "never-match",
			AgentType: "claude_code", Priority: 80, ContextContent: "Updated guidance.",
		}},
	}, time.Now()); err != nil {
		t.Fatalf("Save updated context cache: %v", err)
	}

	nextPrompt := runAgentHook(t, agentHookEvent{
		HookEventName: "UserPromptSubmit",
		Prompt:        "continue",
		SessionID:     "claude-linked-session",
	}, "claude_code")
	noticeContext := hookAdditionalContext(nextPrompt)
	const prefix = "[Details](http://127.0.0.1:3333/activity/"
	index := strings.Index(noticeContext, prefix)
	if index < 0 {
		t.Fatalf("linked notice context = %q", noticeContext)
	}
	activityID := strings.SplitN(noticeContext[index+len(prefix):], ")", 2)[0]
	detail, err := activitydetail.NewStore(cfg.DataDir).Get(activityID)
	if err != nil {
		t.Fatalf("Get activity detail: %v", err)
	}
	if len(detail.ContextMatches) != 1 ||
		detail.ContextMatches[0].Name != "Linked Rule" ||
		detail.ContextMatches[0].MatchType != "regex" ||
		detail.ContextMatches[0].Content != "Review carefully." {
		t.Fatalf("activity detail = %#v", detail)
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
	if err := rulecache.SaveSensitiveRuleCache(cfg.DataDir, []model.SensitiveRule{}, time.Now()); err != nil {
		t.Fatalf("SaveSensitiveRuleCache: %v", err)
	}
	if err := rulecache.SaveContextRuleCache(cfg.DataDir, model.ContextRuleBundle{
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
	if err := rulecache.SaveSensitiveRuleCache(cfg.DataDir, []model.SensitiveRule{}, time.Now()); err != nil {
		t.Fatalf("SaveSensitiveRuleCache: %v", err)
	}
	if err := rulecache.SaveContextRuleCache(cfg.DataDir, model.ContextRuleBundle{
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
	if err := rulecache.SaveSensitiveRuleCache(cfg.DataDir, []model.SensitiveRule{}, time.Now()); err != nil {
		t.Fatalf("SaveSensitiveRuleCache: %v", err)
	}
	if err := rulecache.SaveContextRuleCache(cfg.DataDir, model.ContextRuleBundle{
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
		turnreceipt.Receipt{Output: turnreceipt.OutputSummary{CodeLines: 4}},
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
	if err := rulecache.SaveSensitiveRuleCache(
		cfg.DataDir,
		[]model.SensitiveRule{openAIKeySensitiveRule()},
		time.Now(),
	); err != nil {
		t.Fatalf("SaveSensitiveRuleCache: %v", err)
	}
	if err := turnreceipt.NewStore(cfg.DataDir).SavePending(
		"codex",
		"blocked-notice-session",
		turnreceipt.Receipt{ContextMatches: testReceiptContextMatches(1)},
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
	if len(pending.ContextMatches) != 1 {
		t.Fatalf("pending context matches = %#v", pending.ContextMatches)
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
	saveTestOutputClassification(t, cfg)

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
	events, err := consumeQueuedEvents(eventqueue.NewQueue(cfg.DataDir))
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

func stubLocalActivityHealth(t *testing.T, healthy bool) {
	t.Helper()
	original := localActivityUIHealthy
	localActivityUIHealthy = func(context.Context) bool { return healthy }
	t.Cleanup(func() { localActivityUIHealthy = original })
}
