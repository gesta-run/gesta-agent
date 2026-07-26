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
		ContextMatches: []turnreceipt.ContextRuleMatch{
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
	if !view.Rules[0].Open || view.Rules[1].Open {
		t.Fatalf("rule open states = %#v", view.Rules)
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
		ContextMatches: []turnreceipt.ContextRuleMatch{
			{Name: "Review standards", MatchType: "regex", Content: "Review the diff."},
		},
		Output: turnreceipt.OutputSummary{TestLines: 1},
	})

	if len(view.Output) != 1 || view.Output[0].Value != "1" || view.Output[0].Label != "test lines" {
		t.Fatalf("output metrics = %#v", view.Output)
	}
}
