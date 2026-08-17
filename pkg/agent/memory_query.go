package agent

import (
	"strings"
	"unicode/utf8"

	"github.com/gesta-run/gesta-agent/pkg/contextmatch"
)

const (
	maxMemoryRecallQueryBytes = 2048
	maxMemoryContextBytes     = 4096
	recentMemoryPromptCount   = 2
)

func memoryRecallQuery(currentPrompt string, history []string) string {
	currentPrompt = strings.TrimSpace(currentPrompt)
	if len(currentPrompt)+len("Current request:\n") > maxMemoryRecallQueryBytes {
		return boundedUTF8(currentPrompt, maxMemoryRecallQueryBytes)
	}
	if len(history) == 0 {
		return boundedUTF8(currentPrompt, maxMemoryRecallQueryBytes)
	}

	var query strings.Builder
	query.WriteString("Current request:\n")
	query.WriteString(currentPrompt)
	for index := len(history) - 1; index >= 0; index-- {
		addition := "\n\nRecent user request:\n" + history[index]
		if query.Len()+len(addition) > maxMemoryRecallQueryBytes {
			continue
		}
		query.WriteString(addition)
	}
	return boundedUTF8(query.String(), maxMemoryRecallQueryBytes)
}

func memorySuppliedContext(result contextmatch.Result) string {
	contents := make([]string, 0, len(result.Rules))
	used := 0
	for _, rule := range result.Rules {
		if rule.MatchType == "always" {
			continue
		}
		content := strings.TrimSpace(rule.ContextContent)
		if content == "" {
			continue
		}
		separatorBytes := 0
		if len(contents) > 0 {
			separatorBytes = 2
		}
		remaining := maxMemoryContextBytes - used - separatorBytes
		if remaining <= 0 {
			break
		}
		content = boundedUTF8(content, remaining)
		contents = append(contents, content)
		used += separatorBytes + len(content)
	}
	return strings.Join(contents, "\n\n")
}

func boundedUTF8(value string, maxBytes int) string {
	if maxBytes <= 0 {
		return ""
	}
	if len(value) <= maxBytes {
		return value
	}
	value = value[:maxBytes]
	for value != "" && !utf8.ValidString(value) {
		value = value[:len(value)-1]
	}
	return value
}
