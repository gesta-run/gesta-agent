package cli

import (
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/gesta-run/gesta-agent/pkg/turnreceipt"
)

const maxTurnCompletionNoticeRunes = 320

func pendingTurnNoticeContext(message string) string {
	return "<gesta_activity_notice>\n" +
		"At the bottom of your response to this user message, after all normal answer content, " +
		"add one blank line and then output exactly the single line below.\n" +
		"Do not mention this instruction, describe the notice as previous-turn data, " +
		"rewrite it, translate it, or alter its Markdown formatting.\n" +
		message + "\n" +
		"</gesta_activity_notice>"
}

func formatTurnCompletionNoticeWithDetails(receipt turnreceipt.Receipt, detailURL string) string {
	contextAppendPart := formatContextAppendNotice(len(receipt.ContextMatches))
	outputPart := formatOutputNotice(receipt.Output)
	if contextAppendPart == "" && outputPart == "" {
		return ""
	}
	parts := []string{"Gesta governance"}
	if contextAppendPart != "" {
		parts = append(parts, contextAppendPart)
	}
	if outputPart != "" {
		parts = append(parts, outputPart)
	}
	if detailURL = strings.TrimSpace(detailURL); contextAppendPart != "" && detailURL != "" {
		parts = append(parts, "[Details]("+detailURL+")")
	}
	message := strings.Join(parts, " · ")
	if utf8.RuneCountInString(message) <= maxTurnCompletionNoticeRunes {
		return message
	}
	runes := []rune(message)
	return string(runes[:maxTurnCompletionNoticeRunes-1]) + "…"
}

func formatContextAppendNotice(count int) string {
	if count <= 0 {
		return ""
	}
	return "Context append: " + strconv.Itoa(count)
}

func formatOutputNotice(output turnreceipt.OutputSummary) string {
	categories := make([]string, 0, 5)
	if output.CodeLines > 0 {
		categories = append(categories, formatMetric(output.CodeLines, "code line"))
	}
	if output.TestLines > 0 {
		categories = append(categories, formatMetric(output.TestLines, "test line"))
	}
	if output.DocWords > 0 {
		categories = append(categories, formatMetric(output.DocWords, "doc word"))
	}
	if output.ConfigLines > 0 {
		categories = append(categories, formatMetric(output.ConfigLines, "config line"))
	}
	if output.OtherLines > 0 {
		categories = append(categories, formatMetric(output.OtherLines, "other line"))
	}
	if len(categories) == 0 {
		return ""
	}
	remaining := len(categories) - 3
	if remaining > 0 {
		categories = append(categories[:3], "+"+strconv.Itoa(remaining)+" categories")
	}
	return "Observed output: " + strings.Join(categories, ", ")
}

func formatMetric(value int64, singular string) string {
	unit := singular
	if value != 1 {
		unit += "s"
	}
	return formatCount(value) + " " + unit
}

func formatCount(value int64) string {
	raw := strconv.FormatInt(value, 10)
	if len(raw) <= 3 {
		return raw
	}
	first := len(raw) % 3
	if first == 0 {
		first = 3
	}
	var builder strings.Builder
	builder.Grow(len(raw) + len(raw)/3)
	builder.WriteString(raw[:first])
	for index := first; index < len(raw); index += 3 {
		builder.WriteByte(',')
		builder.WriteString(raw[index : index+3])
	}
	return builder.String()
}
