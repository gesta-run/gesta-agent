package activitydetail

import (
	"strings"
	"testing"

	"github.com/gesta-run/gesta-agent/pkg/contextmatch"
)

func TestNormalizeContextMatchesPreservesExactBoundedContent(t *testing.T) {
	matches := normalizeContextMatches([]ContextRuleMatch{
		{
			RuleID:    "review",
			Name:      "Review",
			MatchType: "REGEX",
			Content:   "\nReview the complete diff.\nKeep this line.\n",
		},
		{
			RuleID:    "too-large",
			Name:      "Too large",
			MatchType: "keyword_any",
			Content:   strings.Repeat("界", contextmatch.MaxContextContent),
		},
		{
			RuleID:    "empty",
			Name:      "Empty",
			MatchType: "keyword_any",
			Content:   " \n\t ",
		},
	})

	if len(matches) != 1 {
		t.Fatalf("normalized matches = %#v", matches)
	}
	if matches[0].Content != "Review the complete diff.\nKeep this line." {
		t.Fatalf("normalized content = %q", matches[0].Content)
	}
}

func TestNormalizeContextMatchesAcceptsMatcherMaximum(t *testing.T) {
	content := strings.Repeat("界", contextmatch.MaxContextContent)
	matches := normalizeContextMatches([]ContextRuleMatch{{
		RuleID:    "maximum",
		Name:      "Maximum",
		MatchType: "keyword_any",
		Content:   content,
	}})
	if len(matches) != 1 || matches[0].Content != content {
		t.Fatalf("maximum content was not preserved: %d matches", len(matches))
	}
}
