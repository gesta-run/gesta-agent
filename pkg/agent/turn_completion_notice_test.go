package agent

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

func TestClaudeStopAttachesPreviousOutputWithoutUIPreflight(t *testing.T) {
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

	nextPrompt := runAgentHook(t, agentHookEvent{
		HookEventName: "UserPromptSubmit",
		Prompt:        "Continue with the next task",
		SessionID:     "claude-notice-session",
	}, "claude_code")
	gotContext := hookAdditionalContext(nextPrompt)
	if !strings.Contains(gotContext, "Follow organization defaults.") ||
		!strings.Contains(gotContext, "gesta_activity_notice") {
		t.Fatalf("next prompt context = %q", gotContext)
	}
	detail, err := activitydetail.NewStore(cfg.DataDir).Get(activityIDFromContext(t, gotContext))
	if err != nil || detail.Output.EquivalentLOC() != 2 {
		t.Fatalf("activity output = %#v, err = %v", detail.Output, err)
	}
	if _, found, err := turnreceipt.NewStore(cfg.DataDir).ConsumePending("claude_code", "claude-notice-session"); err != nil || found {
		t.Fatalf("pending output was not consumed: found = %v, err = %v", found, err)
	}
}

func TestInternalActivityNoticeCallRequiresExactAgentCommand(t *testing.T) {
	const activityID = "activity_0123456789abcdef0123456789abcdef"
	command := "curl -fsS --max-time 2 -X POST http://127.0.0.1:3333/api/v1/activity/notice" +
		" -H 'X-Gesta-Activity-ID: " + activityID + "' 2>/dev/null || true"
	if !isInternalActivityNoticeCall("functions", "exec", map[string]interface{}{"cmd": command}) {
		t.Fatal("exact internal activity notice command was not recognized")
	}
	wrapped := `const result = await tools.exec_command({cmd: "` + command + `"}); text(result.output);`
	if !isInternalActivityNoticeCall("functions", "exec", wrapped) {
		t.Fatal("free-form internal activity notice command was not recognized")
	}
	if isInternalActivityNoticeCall("functions", "exec", map[string]interface{}{
		"cmd": "apply patch documenting http://127.0.0.1:3333/api/v1/activity/notice",
	}) {
		t.Fatal("ordinary tool content containing the notice URL was excluded")
	}
	if isInternalActivityNoticeCall("functions", "exec", `text("`+command+`")`) {
		t.Fatal("non-command free-form content was excluded")
	}
	if isInternalActivityNoticeCall("github", "create_file", map[string]interface{}{"cmd": command}) {
		t.Fatal("non-agent tool call was excluded")
	}
}

func TestPendingOutputIsConsumedWhenActivityCreationFails(t *testing.T) {
	cfg := daemon.Config{DataDir: t.TempDir()}
	store := turnreceipt.NewStore(cfg.DataDir)
	if err := store.SavePending("codex", "failed-activity-session", turnreceipt.OutputSummary{CodeLines: 7}); err != nil {
		t.Fatal(err)
	}
	injectPendingTurnNoticeBestEffort(
		context.Background(),
		cfg,
		agentHookEvent{SessionID: "failed-activity-session"},
		"codex",
		nil,
	)
	if _, found, err := store.ConsumePending("codex", "failed-activity-session"); err != nil || found {
		t.Fatalf("stale pending output remains: found = %v, err = %v", found, err)
	}
}

func TestCodexKeywordContextIsNotDelayedToFollowingPrompt(t *testing.T) {
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
	if strings.Contains(hookAdditionalContext(secondPrompt), "Context append") {
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
	thirdDetail, err := activitydetail.NewStore(cfg.DataDir).Get(
		activityIDFromContext(t, hookAdditionalContext(thirdPrompt)),
	)
	if err != nil || !thirdDetail.Output.Empty() {
		t.Fatalf("third activity output = %#v, err = %v", thirdDetail.Output, err)
	}
}

func TestClaudeStopCreatesLinkedLocalActivityDetailWhenUIIsHealthy(t *testing.T) {
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
	firstPrompt := runAgentHook(t, agentHookEvent{
		HookEventName: "UserPromptSubmit",
		Prompt:        "review this",
		SessionID:     "claude-linked-session",
	}, "claude_code")
	activityID := activityIDFromContext(t, hookAdditionalContext(firstPrompt))
	detail, err := activitydetail.NewStore(cfg.DataDir).Get(activityID)
	if err != nil {
		t.Fatalf("Get current activity detail: %v", err)
	}
	if len(detail.ContextMatches) != 1 ||
		detail.ContextMatches[0].Name != "Linked Rule" ||
		detail.ContextMatches[0].Content != "Review carefully." {
		t.Fatalf("current activity detail = %#v", detail)
	}
	runAgentHook(t, agentHookEvent{
		HookEventName: "Stop",
		SessionID:     "claude-linked-session",
	}, "claude_code")
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
	nextDetail, err := activitydetail.NewStore(cfg.DataDir).Get(
		activityIDFromContext(t, hookAdditionalContext(nextPrompt)),
	)
	if err != nil {
		t.Fatalf("Get next activity detail: %v", err)
	}
	if len(nextDetail.ContextMatches) != 0 {
		t.Fatalf("next activity reused previous context: %#v", nextDetail.ContextMatches)
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
	activityIDFromContext(t, hookAdditionalContext(nextPrompt))
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
	activityIDFromContext(t, gotContext)
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
		turnreceipt.OutputSummary{CodeLines: 4},
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
		!strings.Contains(context, "/api/v1/activity/notice") {
		t.Fatalf("merged additional context = %q", context)
	}
	detail, err := activitydetail.NewStore(cfg.DataDir).Get(activityIDFromContext(t, context))
	if err != nil || detail.Output.EquivalentLOC() != 4 {
		t.Fatalf("activity detail = %#v, err = %v", detail, err)
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
		turnreceipt.OutputSummary{CodeLines: 3},
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
	if pending.Output.CodeLines != 3 {
		t.Fatalf("pending output = %#v", pending.Output)
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
	if agentType == "codex" && event.HookEventName == "UserPromptSubmit" {
		addCodexPromptProvenance(t, &event)
	}
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

func activityIDFromContext(t *testing.T, context string) string {
	t.Helper()
	const marker = "X-Gesta-Activity-ID: "
	index := strings.Index(context, marker)
	if index < 0 {
		t.Fatalf("activity ID missing from context: %q", context)
	}
	activityID := strings.SplitN(context[index+len(marker):], "'", 2)[0]
	if !strings.HasPrefix(activityID, "activity_") {
		t.Fatalf("invalid activity ID %q", activityID)
	}
	return activityID
}
