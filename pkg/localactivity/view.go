package localactivity

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/gesta-run/gesta-agent/pkg/activitydetail"
)

type activityView struct {
	AgentLabel    string
	CreatedAt     string
	ExpiresAt     string
	RuleCount     int
	Rules         []ruleView
	MemoryLabel   string
	MemoryEmpty   string
	Memories      []string
	Output        []metricView
	HasOutput     bool
	EquivalentLOC string
	ActivityID    string
}

type ruleView struct {
	Name       string
	MatchLabel string
	MatchClass string
	Priority   int
	Content    string
}

type metricView struct {
	Value string
	Label string
}

func newActivityView(detail activitydetail.Detail) activityView {
	rules := make([]ruleView, 0, len(detail.ContextMatches))
	for _, match := range detail.ContextMatches {
		rules = append(rules, ruleView{
			Name:       match.Name,
			MatchLabel: matchTypeLabel(match.MatchType),
			MatchClass: matchTypeClass(match.MatchType),
			Priority:   match.Priority,
			Content:    match.Content,
		})
	}
	memories := make([]string, 0, len(detail.Memories))
	for _, memory := range detail.Memories {
		memories = append(memories, memory.Content)
	}
	memoryPresentation := memoryRecallPresentation(
		detail.MemoryRecallStatus,
		detail.MemoryRecallFailure,
		detail.MemoryCount,
	)
	output := make([]metricView, 0, 5)
	appendMetric := func(value int64, label string) {
		if value > 0 {
			output = append(output, metricView{Value: formatNumber(value), Label: label})
		}
	}
	appendMetric(detail.Output.CodeLines, "code lines")
	appendMetric(detail.Output.TestLines, "test lines")
	appendMetric(detail.Output.DocWords, "doc words")
	appendMetric(detail.Output.ConfigLines, "config lines")
	appendMetric(detail.Output.OtherWords, "other words")
	return activityView{
		AgentLabel:    agentLabel(detail.AgentType),
		CreatedAt:     detail.CreatedAt.Local().Format("Jan 2, 2006 · 15:04:05 MST"),
		ExpiresAt:     detail.ExpiresAt.Local().Format("Jan 2, 2006 · 15:04 MST"),
		RuleCount:     len(rules),
		Rules:         rules,
		MemoryLabel:   memoryPresentation.label,
		MemoryEmpty:   memoryPresentation.empty,
		Memories:      memories,
		Output:        output,
		HasOutput:     len(output) > 0,
		EquivalentLOC: formatEquivalentLOC(detail.Output.EquivalentLOC()),
		ActivityID:    detail.ActivityID,
	}
}

type memoryRecallView struct {
	label  string
	empty  string
	notice string
}

func memoryRecallPresentation(
	status activitydetail.MemoryRecallStatus,
	failure activitydetail.MemoryRecallFailure,
	count int,
) memoryRecallView {
	switch status {
	case activitydetail.MemoryRecallTimeout:
		return memoryRecallView{
			label:  "Timed out",
			empty:  "Memory recall timed out before results were available.",
			notice: "timeout",
		}
	case activitydetail.MemoryRecallError:
		switch failure {
		case activitydetail.MemoryRecallFailureServiceUnavailable:
			return memoryRecallView{
				label:  "Unavailable",
				empty:  "The memory service was unavailable.",
				notice: "unavailable",
			}
		case activitydetail.MemoryRecallFailureInvalidResponse:
			return memoryRecallView{
				label:  "Invalid response",
				empty:  "The memory service returned an invalid response.",
				notice: "invalid response",
			}
		case activitydetail.MemoryRecallFailureSensitiveInput:
			return memoryRecallView{
				label:  "Blocked",
				empty:  "Memory recall was skipped because the request contained sensitive information.",
				notice: "blocked",
			}
		case activitydetail.MemoryRecallFailureRulesUnavailable:
			return memoryRecallView{
				label:  "Rules unavailable",
				empty:  "Memory recall was skipped because sensitive-data rules were unavailable.",
				notice: "rules unavailable",
			}
		default:
			return memoryRecallView{
				label:  "Error",
				empty:  "Memory recall failed before results were available.",
				notice: "error",
			}
		}
	case activitydetail.MemoryRecallDisabled:
		return memoryRecallView{
			label:  "Disabled",
			empty:  "Memory recall is disabled.",
			notice: "disabled",
		}
	default:
		value := strconv.Itoa(count)
		return memoryRecallView{label: value, empty: "No memory was recalled.", notice: value}
	}
}

func agentLabel(agentType string) string {
	if agentType == "claude_code" {
		return "Claude Code"
	}
	return "Codex"
}

func matchTypeLabel(matchType string) string {
	if matchType == "regex" {
		return "Regex match"
	}
	return "Keyword match"
}

func matchTypeClass(matchType string) string {
	if matchType == "regex" {
		return "regex"
	}
	return "keyword"
}

func formatNumber(value int64) string {
	raw := fmt.Sprintf("%d", value)
	if len(raw) <= 3 {
		return raw
	}
	first := len(raw) % 3
	if first == 0 {
		first = 3
	}
	var builder strings.Builder
	builder.WriteString(raw[:first])
	for index := first; index < len(raw); index += 3 {
		builder.WriteByte(',')
		builder.WriteString(raw[index : index+3])
	}
	return builder.String()
}
