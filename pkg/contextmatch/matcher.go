package contextmatch

import (
	"errors"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/gesta-run/gesta-agent/pkg/model"
)

const (
	MaxMatchedRules   = 10
	MaxContextContent = 8000
)

type Result struct {
	Rules             []model.ContextRule
	AdditionalContext string
	Truncated         bool
}

func Match(prompt, agentType string, rules []model.ContextRule) Result {
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		return Result{Rules: []model.ContextRule{}}
	}
	agentType = strings.TrimSpace(strings.ToLower(agentType))
	candidates := append([]model.ContextRule(nil), rules...)
	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].Priority == candidates[j].Priority {
			return candidates[i].RuleID < candidates[j].RuleID
		}
		return candidates[i].Priority > candidates[j].Priority
	})

	result := Result{Rules: []model.ContextRule{}}
	contents := make([]string, 0, MaxMatchedRules)
	contentLength := 0
	lowerPrompt := ""
	for _, rule := range candidates {
		if rule.Status != "active" || !supportsAgent(rule.AgentType, agentType) {
			continue
		}
		if rule.MatchType == "keyword_any" && lowerPrompt == "" {
			lowerPrompt = strings.ToLower(prompt)
		}
		if !matchesPrompt(rule, prompt, lowerPrompt) {
			continue
		}
		content := strings.TrimSpace(rule.ContextContent)
		separatorLength := 0
		if len(contents) > 0 {
			separatorLength = 2
		}
		contentRunes := utf8.RuneCountInString(content)
		if len(result.Rules) >= MaxMatchedRules || contentLength+separatorLength+contentRunes > MaxContextContent {
			result.Truncated = true
			continue
		}
		result.Rules = append(result.Rules, rule)
		contents = append(contents, content)
		contentLength += separatorLength + contentRunes
	}
	result.AdditionalContext = strings.Join(contents, "\n\n")
	return result
}

func ValidateRules(rules []model.ContextRule) error {
	for _, rule := range rules {
		if strings.TrimSpace(rule.RuleID) == "" || rule.Status != "active" {
			return errors.New("context rule bundle contains an invalid rule identity or status")
		}
		if rule.AgentType != "all" && rule.AgentType != "codex" && rule.AgentType != "claude_code" {
			return errors.New("context rule bundle contains an invalid agent type")
		}
		if strings.TrimSpace(rule.ContextContent) == "" || utf8.RuneCountInString(rule.ContextContent) > MaxContextContent {
			return errors.New("context rule bundle contains invalid context content")
		}
		switch rule.MatchType {
		case "always":
		case "keyword_any":
			validKeyword := false
			for _, keyword := range rule.Keywords {
				if strings.TrimSpace(keyword) != "" {
					validKeyword = true
					break
				}
			}
			if !validKeyword {
				return errors.New("context rule bundle contains an empty keyword matcher")
			}
		case "regex":
			if _, err := regexp.Compile(rule.Pattern); err != nil {
				return errors.New("context rule bundle contains an invalid regular expression")
			}
		default:
			return errors.New("context rule bundle contains an invalid match type")
		}
	}
	return nil
}

func supportsAgent(ruleAgentType, agentType string) bool {
	ruleAgentType = strings.TrimSpace(strings.ToLower(ruleAgentType))
	return ruleAgentType == "all" || ruleAgentType == agentType
}

func matchesPrompt(rule model.ContextRule, prompt, lowerPrompt string) bool {
	switch rule.MatchType {
	case "always":
		return true
	case "keyword_any":
		for _, keyword := range rule.Keywords {
			if containsBoundedKeyword(lowerPrompt, keyword) {
				return true
			}
		}
	case "regex":
		re, err := regexp.Compile(rule.Pattern)
		return err == nil && re.MatchString(prompt)
	}
	return false
}

func containsBoundedKeyword(lowerPrompt, keyword string) bool {
	keyword = strings.ToLower(strings.TrimSpace(keyword))
	if keyword == "" {
		return false
	}
	for offset := 0; offset <= len(lowerPrompt)-len(keyword); {
		index := strings.Index(lowerPrompt[offset:], keyword)
		if index < 0 {
			return false
		}
		index += offset
		end := index + len(keyword)
		if keywordBoundaryMatches(lowerPrompt, keyword, index, end) {
			return true
		}
		offset = index + 1
	}
	return false
}

func keywordBoundaryMatches(prompt, keyword string, start, end int) bool {
	if isASCIIWordByte(keyword[0]) && start > 0 && isASCIIWordByte(prompt[start-1]) {
		return false
	}
	if isASCIIWordByte(keyword[len(keyword)-1]) &&
		end < len(prompt) &&
		isASCIIWordByte(prompt[end]) {
		return false
	}
	return true
}

func isASCIIWordByte(value byte) bool {
	return value >= 'a' && value <= 'z' ||
		value >= '0' && value <= '9' ||
		value == '_'
}
