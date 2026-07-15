package policy

import (
	"strings"
	"testing"

	"github.com/gesta-run/gesta-agent/pkg/model"
)

func TestEvaluateCommandWithoutRulesAllowsDangerousDelete(t *testing.T) {
	evaluation := EvaluateCommand("codex", []string{"rm", "-rf", "/"})
	if evaluation.Decision != DecisionAllow {
		t.Fatalf("decision = %s, want %s", evaluation.Decision, DecisionAllow)
	}
	if evaluation.MatchedRule() {
		t.Fatalf("expected no matched rules, got %#v", evaluation.RuleIDs)
	}
	if evaluation.CommandHash == "" || evaluation.CommandPreview == "" {
		t.Fatalf("expected command metadata, got hash=%q preview=%q", evaluation.CommandHash, evaluation.CommandPreview)
	}
}

func TestEvaluateCommandWarnsRiskyConfiguredCommands(t *testing.T) {
	tests := []struct {
		name     string
		args     []string
		decision Decision
		rule     string
		action   string
		pattern  string
	}{
		{name: "kubectl delete", args: []string{"kubectl", "-n", "prod", "delete", "pod", "api"}, decision: DecisionBlock, rule: "rule_kubectl_delete", action: "block", pattern: `(?i)kubectl\s+.*delete`},
		{name: "terraform apply", args: []string{"terraform", "-chdir=infra", "apply"}, decision: DecisionApproval, rule: "rule_terraform_apply", action: "approval", pattern: `(?i)terraform\s+.*apply`},
		{name: "git force push", args: []string{"git", "push", "--force-with-lease"}, decision: DecisionApproval, rule: "rule_git_force_push", action: "approval", pattern: `(?i)git\s+push\s+--force`},
		{name: "gh pr merge", args: []string{"gh", "pr", "merge", "123"}, decision: DecisionApproval, rule: "rule_gh_pr_merge", action: "approval", pattern: `(?i)gh\s+pr\s+merge`},
		{name: "aws s3 rm", args: []string{"aws", "--profile", "prod", "s3", "rm", "s3://bucket/key"}, decision: DecisionBlock, rule: "rule_aws_s3_rm", action: "block", pattern: `(?i)aws\s+.*s3\s+rm`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			evaluation := EvaluateCommandWithRules("codex", tt.args, []model.PolicyRule{
				{
					RuleID:     tt.rule,
					Status:     "active",
					AgentType:  "codex",
					MatchValue: tt.pattern,
					Action:     tt.action,
				},
			})
			if evaluation.Decision != tt.decision {
				t.Fatalf("decision = %s, want %s", evaluation.Decision, tt.decision)
			}
			if !hasRule(evaluation.RuleIDs, tt.rule) {
				t.Fatalf("rule ids = %#v, want %s", evaluation.RuleIDs, tt.rule)
			}
		})
	}
}

func TestEvaluateCommandWarnsSensitivePathAndRedactsPreview(t *testing.T) {
	rules := []model.PolicyRule{
		{
			RuleID:     "rule_sensitive_path",
			Status:     "active",
			AgentType:  "codex",
			MatchValue: `(?i)(\.ssh|kubeconfig)`,
			Action:     "warn",
		},
	}
	evaluation := EvaluateCommandWithRules("codex", []string{"cat", "api_key=sk-test-secret-value-1234567890", "~/.ssh/id_rsa"}, rules)
	if evaluation.Decision != DecisionWarn {
		t.Fatalf("decision = %s, want %s", evaluation.Decision, DecisionWarn)
	}
	if !hasRule(evaluation.RuleIDs, "rule_sensitive_path") {
		t.Fatalf("rule ids = %#v, want sensitive file read rule", evaluation.RuleIDs)
	}
	if strings.Contains(evaluation.CommandPreview, "sk-test-secret-value") {
		t.Fatalf("preview was not redacted: %q", evaluation.CommandPreview)
	}
	if strings.Contains(evaluation.CommandPreview, ".ssh") {
		t.Fatalf("sensitive path was not masked: %q", evaluation.CommandPreview)
	}

	kubeconfig := EvaluateCommandWithRules("codex", []string{"cat", "~/.kube/kubeconfig"}, rules)
	if kubeconfig.Decision != DecisionWarn {
		t.Fatalf("kubeconfig decision = %s, want %s", kubeconfig.Decision, DecisionWarn)
	}
	if strings.Contains(kubeconfig.CommandPreview, "kubeconfig") {
		t.Fatalf("kubeconfig path was not masked: %q", kubeconfig.CommandPreview)
	}
}

func TestEvaluateCommandShellWrapperUsesSameRules(t *testing.T) {
	rules := []model.PolicyRule{
		{
			RuleID:     "rule_root_delete",
			Status:     "active",
			AgentType:  "codex",
			MatchValue: `(?i)^rm\s+-rf\s+/$`,
			Action:     "block",
		},
	}
	evaluation := EvaluateCommandWithRules("codex", []string{"sh", "-c", "rm -rf /"}, rules)
	if evaluation.Decision != DecisionBlock {
		t.Fatalf("decision = %s, want %s", evaluation.Decision, DecisionBlock)
	}

	compound := EvaluateCommandWithRules("codex", []string{"sh", "-c", "echo ok && rm -rf /"}, rules)
	if compound.Decision != DecisionBlock {
		t.Fatalf("compound decision = %s, want %s", compound.Decision, DecisionBlock)
	}

	allowed := EvaluateCommandWithRules("codex", []string{"sh", "-c", "rm -rf /tmp/gesta-cache"}, rules)
	if allowed.Decision == DecisionBlock {
		t.Fatalf("expected scoped tmp deletion not to be blocked, got %#v", allowed)
	}
}

func TestEvaluateCommandWithControlPlaneRules(t *testing.T) {
	rules := []model.PolicyRule{
		{
			RuleID:     "rule_disabled_delete",
			Status:     "disabled",
			AgentType:  "codex",
			MatchValue: `(?i)kubectl\s+delete`,
			Action:     "block",
			RiskLevel:  "critical",
		},
		{
			RuleID:     "rule_remote_apply",
			Name:       "Require approval for k8s mutations",
			Status:     "active",
			AgentType:  "codex",
			MatchValue: `(?i)kubectl\s+apply`,
			Action:     "approval",
			RiskLevel:  "high",
		},
	}

	disabled := EvaluateCommandWithRules("codex", []string{"kubectl", "delete", "pod", "api"}, rules)
	if disabled.Decision != DecisionAllow {
		t.Fatalf("disabled remote rule decision = %s, want %s", disabled.Decision, DecisionAllow)
	}
	if disabled.MatchedRule() {
		t.Fatalf("disabled remote rule should not count as matched")
	}

	approval := EvaluateCommandWithRules("codex", []string{"kubectl", "apply", "-f", "deploy.yaml"}, rules)
	if approval.Decision != DecisionApproval {
		t.Fatalf("remote approval decision = %s, want %s", approval.Decision, DecisionApproval)
	}
	if !approval.MatchedRule() {
		t.Fatalf("remote approval should count as matched")
	}
	if !hasRule(approval.RuleIDs, "rule_remote_apply") {
		t.Fatalf("rule ids = %#v, want remote apply rule", approval.RuleIDs)
	}
}

func TestEvaluateCommandTreatsLegacyCodexRuleAsUnscoped(t *testing.T) {
	rules := []model.PolicyRule{
		{
			RuleID:     "rule_echo_test",
			Status:     "active",
			AgentType:  "codex",
			MatchValue: `echo test1`,
			Action:     "block",
		},
	}
	evaluation := EvaluateCommandWithRules("claude_code", []string{"sh", "-c", "echo test1"}, rules)
	if evaluation.Decision != DecisionBlock {
		t.Fatalf("legacy codex rule decision = %s, want %s", evaluation.Decision, DecisionBlock)
	}
	if !hasRule(evaluation.RuleIDs, "rule_echo_test") {
		t.Fatalf("rule ids = %#v, want legacy codex rule to apply to claude_code", evaluation.RuleIDs)
	}
}

func hasRule(rules []string, want string) bool {
	for _, rule := range rules {
		if rule == want {
			return true
		}
	}
	return false
}
