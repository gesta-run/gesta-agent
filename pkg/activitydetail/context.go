package activitydetail

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/gesta-run/gesta-agent/pkg/contextmatch"
)

const (
	maxContextMatches       = 10
	maxContextRuleIDBytes   = 128
	maxContextRuleNameBytes = 160
)

type ContextRuleMatch struct {
	RuleID    string `json:"rule_id"`
	Name      string `json:"name"`
	MatchType string `json:"match_type"`
	Priority  int    `json:"priority"`
	Content   string `json:"content"`
}

func normalizeContextMatches(matches []ContextRuleMatch) []ContextRuleMatch {
	normalized := make([]ContextRuleMatch, 0, min(len(matches), maxContextMatches))
	seen := make(map[string]struct{}, min(len(matches), maxContextMatches))
	contentRunes := 0
	for _, match := range matches {
		if len(normalized) >= maxContextMatches {
			break
		}
		match.RuleID = truncateUTF8Bytes(strings.TrimSpace(match.RuleID), maxContextRuleIDBytes)
		match.Name = truncateUTF8Bytes(strings.TrimSpace(match.Name), maxContextRuleNameBytes)
		match.MatchType = strings.TrimSpace(strings.ToLower(match.MatchType))
		match.Content = strings.TrimSpace(strings.ToValidUTF8(match.Content, ""))
		if match.RuleID == "" || (match.MatchType != "keyword_any" && match.MatchType != "regex") || match.Content == "" {
			continue
		}
		if _, exists := seen[match.RuleID]; exists {
			continue
		}
		separatorRunes := 0
		if len(normalized) > 0 {
			separatorRunes = 2
		}
		matchContentRunes := utf8.RuneCountInString(match.Content)
		if contentRunes+separatorRunes+matchContentRunes > contextmatch.MaxContextContent {
			continue
		}
		if match.Name == "" {
			match.Name = "Unnamed context rule"
		}
		seen[match.RuleID] = struct{}{}
		normalized = append(normalized, match)
		contentRunes += separatorRunes + matchContentRunes
	}
	return normalized
}

func truncateUTF8Bytes(value string, maxBytes int) string {
	value = strings.ToValidUTF8(value, "")
	value = strings.Map(func(character rune) rune {
		if unicode.IsControl(character) {
			return -1
		}
		return character
	}, value)
	if len(value) <= maxBytes {
		return value
	}
	for maxBytes > 0 && (value[maxBytes]&0xc0) == 0x80 {
		maxBytes--
	}
	return value[:maxBytes]
}
