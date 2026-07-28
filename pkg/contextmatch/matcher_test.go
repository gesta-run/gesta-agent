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

func TestMatchASCIIKeywordUsesWordBoundaries(t *testing.T) {
	tests := []struct {
		name    string
		keyword string
		prompt  string
		want    bool
	}{
		{name: "Chinese prefix", keyword: "PR", prompt: "提PR", want: true},
		{name: "Delimited acronym", keyword: "PR", prompt: "review PR #42", want: true},
		{name: "Chinese suffix", keyword: "PR", prompt: "PR已创建", want: true},
		{name: "Prompt false positive", keyword: "PR", prompt: "prepare the prompt", want: false},
		{name: "Promote false positive", keyword: "PR", prompt: "promote this release", want: false},
		{name: "Alphanumeric suffix", keyword: "PR", prompt: "PR2 release", want: false},
		{name: "Whole word", keyword: "deploy", prompt: "deploy the service", want: true},
		{name: "Long word prefix", keyword: "deploy", prompt: "deployment status", want: false},
		{name: "Whole phrase", keyword: "merge request", prompt: "open a merge request", want: true},
		{name: "Phrase embedded in words", keyword: "merge request", prompt: "premerge requester", want: false},
		{name: "Chinese substring", keyword: "发布", prompt: "请发布服务", want: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			rules := []model.ContextRule{{
				RuleID: "keyword", Name: "Keyword rule", Status: "active",
				MatchType: "keyword_any", Keywords: []string{test.keyword},
				AgentType: "all", ContextContent: "Apply keyword guidance.",
			}}
			result := Match(test.prompt, "codex", rules)
			if got := len(result.Rules) == 1; got != test.want {
				t.Fatalf(
					"Match(%q, keyword %q) matched = %v, want %v",
					test.prompt,
					test.keyword,
					got,
					test.want,
				)
			}
		})
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
