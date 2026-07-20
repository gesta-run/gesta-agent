package contextmatch

import (
	"strings"
	"testing"

	"github.com/gesta-run/gesta-agent/pkg/model"
)

func TestMatchCombinesRulesInPriorityOrder(t *testing.T) {
	rules := []model.ContextRule{
		{RuleID: "always", Status: "active", MatchType: "always", AgentType: "all", Priority: 10, ContextContent: "always"},
		{RuleID: "keyword", Status: "active", MatchType: "keyword_any", Keywords: []string{"Deploy"}, AgentType: "codex", Priority: 100, ContextContent: "keyword"},
		{RuleID: "other-agent", Status: "active", MatchType: "always", AgentType: "claude_code", Priority: 1000, ContextContent: "other"},
	}
	result := Match("deploy the service", "codex", rules)
	if result.AdditionalContext != "keyword\n\nalways" {
		t.Fatalf("additional context = %q", result.AdditionalContext)
	}
	if len(result.Rules) != 2 || result.Rules[0].RuleID != "keyword" {
		t.Fatalf("rules = %#v", result.Rules)
	}
}

func TestMatchDoesNotPartiallyAppendContent(t *testing.T) {
	rules := []model.ContextRule{
		{RuleID: "high", Status: "active", MatchType: "always", AgentType: "all", Priority: 100, ContextContent: strings.Repeat("a", MaxContextContent)},
		{RuleID: "low", Status: "active", MatchType: "always", AgentType: "all", Priority: 1, ContextContent: "low"},
	}
	result := Match("prompt", "codex", rules)
	if len(result.Rules) != 1 || result.Rules[0].RuleID != "high" || !result.Truncated {
		t.Fatalf("result = %#v", result)
	}
}

func TestValidateRulesRejectsInvalidBundle(t *testing.T) {
	if err := ValidateRules([]model.ContextRule{{RuleID: "rule", Status: "active", MatchType: "regex", Pattern: "[", AgentType: "all", ContextContent: "context"}}); err == nil {
		t.Fatal("expected invalid regular expression to be rejected")
	}
}
