package policy

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/gesta-run/gesta-agent/pkg/model"
	"github.com/gesta-run/gesta-agent/pkg/privacy"
	"github.com/gesta-run/gesta-agent/pkg/util"
)

type Decision string

const (
	DecisionAllow    Decision = "allow"
	DecisionWarn     Decision = "warn"
	DecisionApproval Decision = "approval"
	DecisionBlock    Decision = "block"

	CommandPreviewLimit = 240
)

type Evaluation struct {
	AgentType      string
	CommandHash    string
	CommandPreview string
	Decision       Decision
	RiskScore      int
	RuleIDs        []string
	Reason         string
}

func (e Evaluation) MatchedRule() bool {
	return len(e.RuleIDs) > 0
}

type ruleMatch struct {
	id        string
	decision  Decision
	riskScore int
	reason    string
}

func EvaluateCommand(agentType string, args []string) Evaluation {
	return newEvaluation(agentType, args, "no local policy rules configured")
}

func EvaluateCommandWithRules(agentType string, args []string, rules []model.PolicyRule) Evaluation {
	evaluation := newEvaluation(agentType, args, "no control-plane policy rules matched")
	return applyRuleMatches(evaluation, policyRuleMatches(agentType, args, rules))
}

func newEvaluation(agentType string, args []string, reason string) Evaluation {
	if agentType == "" {
		agentType = "unknown"
	}
	return Evaluation{
		AgentType:      agentType,
		CommandHash:    util.HashString(agentType + "\x00" + strings.Join(args, "\x00")),
		CommandPreview: RedactedPreview(args),
		Decision:       DecisionAllow,
		Reason:         reason,
	}
}

func applyRuleMatches(evaluation Evaluation, matches []ruleMatch) Evaluation {
	if len(matches) == 0 {
		return evaluation
	}

	seenRules := map[string]bool{}
	var reasons []string
	for _, match := range matches {
		if match.decision == DecisionBlock {
			evaluation.Decision = DecisionBlock
		} else if evaluation.Decision != DecisionBlock && match.decision == DecisionApproval {
			evaluation.Decision = DecisionApproval
		} else if evaluation.Decision != DecisionBlock && match.decision == DecisionWarn {
			evaluation.Decision = DecisionWarn
		}
		if match.riskScore > evaluation.RiskScore {
			evaluation.RiskScore = match.riskScore
		}
		if !seenRules[match.id] {
			evaluation.RuleIDs = append(evaluation.RuleIDs, match.id)
			seenRules[match.id] = true
		}
		if match.reason != "" {
			reasons = append(reasons, match.reason)
		}
	}
	evaluation.Reason = strings.Join(reasons, "; ")
	return evaluation
}

func policyRuleMatches(agentType string, args []string, rules []model.PolicyRule) []ruleMatch {
	var matches []ruleMatch
	for _, rule := range rules {
		if !policyRuleAppliesToAgent(rule, agentType) || !policyRuleMatchesCommand(rule, args) {
			continue
		}
		matches = append(matches, ruleMatch{
			id:        rule.RuleID,
			decision:  decisionFromPolicyAction(rule.Action),
			riskScore: riskScoreFromPolicyRule(rule),
			reason:    firstNonEmpty(rule.Description, rule.Name, "matched control-plane policy rule"),
		})
	}
	return matches
}

func policyRuleAppliesToAgent(rule model.PolicyRule, agentType string) bool {
	if normalizeRuleToken(rule.Status) != "active" {
		return false
	}
	ruleAgent := normalizeRuleToken(rule.AgentType)
	if ruleAgent == "" || ruleAgent == "all" || ruleAgent == "all_agents" {
		return true
	}
	// Console v1 originally wrote "codex" for every command policy even after
	// Claude Code support landed. Treat that legacy value as an unscoped rule so
	// configured command regex policies protect every enrolled agent.
	if ruleAgent == "codex" {
		return true
	}
	return ruleAgent == normalizeRuleToken(agentType)
}

func policyRuleMatchesCommand(rule model.PolicyRule, args []string) bool {
	matchValue := strings.TrimSpace(rule.MatchValue)
	if matchValue == "" {
		return true
	}
	expr, err := regexp.Compile(matchValue)
	if err != nil {
		return false
	}
	for _, form := range guardedCommandForms(args) {
		if expr.MatchString(strings.Join(form, " ")) {
			return true
		}
	}
	return false
}

func decisionFromPolicyAction(value string) Decision {
	switch normalizeRuleToken(value) {
	case "block", "blocked", "deny", "denied":
		return DecisionBlock
	case "approval", "requires_approval", "require_approval", "approval_required", "review", "review_required":
		return DecisionApproval
	case "warn", "warning", "warned":
		return DecisionWarn
	default:
		return DecisionAllow
	}
}

func riskScoreFromPolicyRule(rule model.PolicyRule) int {
	switch normalizeRuleToken(rule.RiskLevel) {
	case "critical":
		return 95
	case "high":
		return 80
	case "medium":
		return 55
	case "low":
		return 25
	}
	switch decisionFromPolicyAction(rule.Action) {
	case DecisionBlock:
		return 85
	case DecisionApproval:
		return 70
	case DecisionWarn:
		return 50
	default:
		return 0
	}
}

func (e Evaluation) Payload(executed bool, exitCode int) map[string]interface{} {
	ruleIDs := append([]string{}, e.RuleIDs...)
	return map[string]interface{}{
		"command_hash":    e.CommandHash,
		"command_preview": e.CommandPreview,
		"decision":        string(e.Decision),
		"risk_score":      e.RiskScore,
		"rule_ids":        ruleIDs,
		"matched_rule":    e.MatchedRule(),
		"reason":          e.Reason,
		"mode":            "guard",
		"executed":        executed,
		"exit_code":       exitCode,
		"agent_type":      e.AgentType,
		"metadata_only":   true,
	}
}

func RedactedPreview(args []string) string {
	if len(args) == 0 {
		return ""
	}
	tokens := make([]string, 0, len(args))
	for _, arg := range args {
		tokens = append(tokens, shellPreviewToken(redactPreviewToken(arg)))
	}
	preview := strings.Join(tokens, " ")
	preview = privacy.RedactAndTruncate(preview, CommandPreviewLimit)
	return strings.ReplaceAll(preview, "\n", " ")
}

func guardedCommandForms(args []string) [][]string {
	forms := [][]string{args}
	unwrapped := unwrapCommandPrefix(args)
	if len(unwrapped) >= 3 {
		switch commandName(unwrapped[0]) {
		case "bash", "sh", "zsh":
			for i := 1; i < len(unwrapped)-1; i++ {
				if unwrapped[i] == "-c" {
					fields := strings.Fields(unwrapped[i+1])
					if len(fields) > 0 {
						forms = append(forms, fields)
						forms = append(forms, splitShellCommandFields(fields)...)
					}
					break
				}
			}
		}
	}
	return forms
}

func splitShellCommandFields(fields []string) [][]string {
	var forms [][]string
	var current []string
	flush := func() {
		if len(current) > 0 {
			forms = append(forms, current)
			current = nil
		}
	}
	for _, field := range fields {
		trimmed := strings.TrimSpace(field)
		if trimmed == "" {
			continue
		}
		if trimmed == "&&" || trimmed == "||" || trimmed == "|" || trimmed == ";" {
			flush()
			continue
		}
		for strings.HasSuffix(trimmed, ";") {
			withoutTerminator := strings.TrimSuffix(trimmed, ";")
			if withoutTerminator != "" {
				current = append(current, withoutTerminator)
			}
			flush()
			trimmed = ""
		}
		if trimmed != "" {
			current = append(current, trimmed)
		}
	}
	flush()
	return forms
}

func unwrapCommandPrefix(args []string) []string {
	for len(args) > 0 {
		switch commandName(args[0]) {
		case "sudo", "doas":
			args = args[1:]
			for len(args) > 0 && strings.HasPrefix(args[0], "-") {
				opt := args[0]
				args = args[1:]
				if (opt == "-u" || opt == "-g" || opt == "-h" || opt == "--user" || opt == "--group" || opt == "--host") && len(args) > 0 {
					args = args[1:]
				}
			}
		case "env":
			args = args[1:]
			for len(args) > 0 {
				if strings.Contains(args[0], "=") {
					args = args[1:]
					continue
				}
				if args[0] == "-u" || args[0] == "--unset" {
					args = args[1:]
					if len(args) > 0 {
						args = args[1:]
					}
					continue
				}
				if strings.HasPrefix(args[0], "-") {
					args = args[1:]
					continue
				}
				break
			}
		case "command", "builtin", "nohup", "time":
			args = args[1:]
		default:
			return args
		}
	}
	return args
}

func cleanTargetToken(target string) string {
	target = strings.TrimSpace(target)
	target = strings.Trim(target, `"'`)
	return target
}

func commandName(path string) string {
	return filepath.Base(cleanTargetToken(path))
}

func redactPreviewToken(token string) string {
	token = privacy.RedactString(token)
	lower := strings.ToLower(token)
	switch {
	case strings.Contains(lower, ".ssh"):
		return "[SSH_PATH]"
	case strings.Contains(lower, ".env"):
		return "[ENV_FILE]"
	case strings.Contains(lower, "id_rsa"):
		return "[SSH_KEY_PATH]"
	case strings.Contains(lower, "credentials"):
		return "[CREDENTIALS_PATH]"
	case strings.Contains(lower, "kubeconfig"):
		return "[KUBECONFIG_PATH]"
	}
	if len(token) > 96 {
		return token[:96] + "..."
	}
	return token
}

func normalizeRuleToken(value string) string {
	return strings.ToLower(strings.NewReplacer("-", "_", " ", "_").Replace(strings.TrimSpace(value)))
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			return value
		}
	}
	return ""
}

func shellPreviewToken(token string) string {
	if token == "" {
		return "''"
	}
	if strings.ContainsAny(token, " \t\n\"'\\$`") {
		return fmt.Sprintf("%q", token)
	}
	return token
}
