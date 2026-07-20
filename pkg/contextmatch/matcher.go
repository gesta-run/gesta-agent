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
	for _, rule := range candidates {
		if rule.Status != "active" || !supportsAgent(rule.AgentType, agentType) || !matchesPrompt(rule, prompt) {
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

func matchesPrompt(rule model.ContextRule, prompt string) bool {
	switch rule.MatchType {
	case "always":
		return true
	case "keyword_any":
		lowerPrompt := strings.ToLower(prompt)
		for _, keyword := range rule.Keywords {
			if strings.Contains(lowerPrompt, strings.ToLower(strings.TrimSpace(keyword))) {
				return true
			}
		}
	case "regex":
		re, err := regexp.Compile(rule.Pattern)
		return err == nil && re.MatchString(prompt)
	}
	return false
}
