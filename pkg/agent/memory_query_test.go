package agent

import (
	"strings"
	"testing"

	"github.com/gesta-run/gesta-agent/pkg/contextmatch"
	"github.com/gesta-run/gesta-agent/pkg/model"
)

func TestMemoryRecallQueryIncludesOnlyTwoMostRecentUserRequests(t *testing.T) {
	query := memoryRecallQuery(
		"how do I do that?",
		[]string{"release Gesta", "use preproduction"},
	)

	for _, expected := range []string{"how do I do that?", "release Gesta", "use preproduction"} {
		if !strings.Contains(query, expected) {
			t.Fatalf("recall query missing %q: %q", expected, query)
		}
	}
}

func TestMemorySuppliedContextExcludesAlwaysRules(t *testing.T) {
	context := memorySuppliedContext(contextmatch.Result{Rules: []model.ContextRule{
		{MatchType: "keyword_any", ContextContent: "Release-specific procedure"},
		{MatchType: "always", ContextContent: "General coding guidance"},
	}})
	if context != "Release-specific procedure" {
		t.Fatalf("supplied context = %q", context)
	}
}
