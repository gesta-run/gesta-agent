package localactivity

import (
	"testing"
	"time"

	"github.com/gesta-run/gesta-agent/pkg/activitydetail"
	"github.com/gesta-run/gesta-agent/pkg/turnreceipt"
)

func TestNewActivityViewBuildsConsoleSummaryAndRuleKinds(t *testing.T) {
	createdAt := time.Date(2026, time.July, 26, 14, 30, 0, 0, time.UTC)
	view := newActivityView(activitydetail.Detail{
		ActivityID: "activity_test",
		CreatedAt:  createdAt,
		ExpiresAt:  createdAt.Add(24 * time.Hour),
		AgentType:  "claude_code",
		ContextMatches: []activitydetail.ContextRuleMatch{
			{Name: "Review standards", MatchType: "regex", Priority: 90, Content: "Review the diff."},
			{Name: "Repository guidance", MatchType: "keyword_any", Priority: 70, Content: "Follow repository guidance."},
		},
		Output: turnreceipt.OutputSummary{
			CodeLines: 12,
			DocWords:  450,
		},
	})

	if view.RuleCount != 2 {
		t.Fatalf("rule count = %d", view.RuleCount)
	}
	if view.AgentLabel != "Claude Code" {
		t.Fatalf("agent label = %q", view.AgentLabel)
	}
	if view.Rules[0].MatchClass != "regex" || view.Rules[1].MatchClass != "keyword" {
		t.Fatalf("rule classes = %#v", view.Rules)
	}
	if view.Rules[0].Content != "Review the diff." {
		t.Fatalf("rule content = %q", view.Rules[0].Content)
	}
	if view.Output[1].Value != "450" || view.Output[1].Label != "doc words" {
		t.Fatalf("output metrics = %#v", view.Output)
	}
}

func TestNewActivityViewKeepsOneMeasuredOutput(t *testing.T) {
	view := newActivityView(activitydetail.Detail{
		ContextMatches: []activitydetail.ContextRuleMatch{
			{Name: "Review standards", MatchType: "regex", Content: "Review the diff."},
		},
		Output: turnreceipt.OutputSummary{TestLines: 1},
	})

	if len(view.Output) != 1 || view.Output[0].Value != "1" || view.Output[0].Label != "test lines" {
		t.Fatalf("output metrics = %#v", view.Output)
	}
}

func TestNewActivityViewExplainsMemoryRecallTimeout(t *testing.T) {
	view := newActivityView(activitydetail.Detail{MemoryRecallStatus: activitydetail.MemoryRecallTimeout})
	if view.MemoryLabel != "Timed out" || view.MemoryEmpty != "Memory recall timed out before results were available." {
		t.Fatalf("memory presentation = %q, %q", view.MemoryLabel, view.MemoryEmpty)
	}
}

func TestNewActivityViewExplainsMemoryRecallFailures(t *testing.T) {
	tests := []struct {
		name    string
		failure activitydetail.MemoryRecallFailure
		label   string
		empty   string
	}{
		{
			name:    "service unavailable",
			failure: activitydetail.MemoryRecallFailureServiceUnavailable,
			label:   "Unavailable",
			empty:   "The memory service was unavailable.",
		},
		{
			name:    "invalid response",
			failure: activitydetail.MemoryRecallFailureInvalidResponse,
			label:   "Invalid response",
			empty:   "The memory service returned an invalid response.",
		},
		{
			name:    "sensitive input",
			failure: activitydetail.MemoryRecallFailureSensitiveInput,
			label:   "Blocked",
			empty:   "Memory recall was skipped because the request contained sensitive information.",
		},
		{
			name:    "rules unavailable",
			failure: activitydetail.MemoryRecallFailureRulesUnavailable,
			label:   "Rules unavailable",
			empty:   "Memory recall was skipped because sensitive-data rules were unavailable.",
		},
		{
			name:    "unknown",
			failure: activitydetail.MemoryRecallFailureUnknown,
			label:   "Error",
			empty:   "Memory recall failed before results were available.",
		},
		{
			name:  "legacy record without failure",
			label: "Error",
			empty: "Memory recall failed before results were available.",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			view := newActivityView(activitydetail.Detail{
				MemoryRecallStatus:  activitydetail.MemoryRecallError,
				MemoryRecallFailure: test.failure,
			})
			if view.MemoryLabel != test.label || view.MemoryEmpty != test.empty {
				t.Fatalf("memory presentation = %q, %q", view.MemoryLabel, view.MemoryEmpty)
			}
		})
	}
}
