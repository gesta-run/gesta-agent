package app

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/gesta-run/gesta-agent/pkg/contextmatch"
	"github.com/gesta-run/gesta-agent/pkg/daemon"
	"github.com/gesta-run/gesta-agent/pkg/model"
	"github.com/gesta-run/gesta-agent/pkg/privacy"
	"github.com/gesta-run/gesta-agent/pkg/util"
)

func processOrganizationContext(cfg daemon.Config, event agentHookEvent, agentType, source string) map[string]interface{} {
	cache, ok := hookContextRules(cfg)
	if !ok {
		return map[string]interface{}{}
	}
	result := contextmatch.Match(event.Prompt, agentType, cache.Rules)
	if len(result.Rules) == 0 || strings.TrimSpace(result.AdditionalContext) == "" {
		return map[string]interface{}{}
	}
	recordContextRuleMatchBestEffort(cfg, agentType, source, cache.Version, result)
	recordTurnContextMatchesBestEffort(cfg, event, agentType, result)
	return map[string]interface{}{
		"hookSpecificOutput": map[string]interface{}{
			"hookEventName":     "UserPromptSubmit",
			"additionalContext": result.AdditionalContext,
		},
	}
}

func hookContextRules(cfg daemon.Config) (daemon.ContextRuleCache, bool) {
	cache, err := daemon.LoadContextRuleCache(cfg.DataDir)
	if err != nil {
		return daemon.ContextRuleCache{}, false
	}
	return cache, true
}

func recordContextRuleMatchBestEffort(
	cfg daemon.Config,
	agentType, source, bundleVersion string,
	result contextmatch.Result,
) {
	if err := recordContextRuleMatch(cfg, agentType, source, bundleVersion, result); err != nil {
		fmt.Fprintf(os.Stderr, "gesta-agent hook: context rule match was not recorded: %v\n", err)
	}
}

func recordContextRuleMatch(
	cfg daemon.Config,
	agentType, source, bundleVersion string,
	result contextmatch.Result,
) error {
	ruleMatches := make([]map[string]string, 0, len(result.Rules))
	for _, rule := range result.Rules {
		ruleMatches = append(ruleMatches, map[string]string{
			"rule_id":    rule.RuleID,
			"rule_name":  rule.Name,
			"match_type": rule.MatchType,
		})
	}
	payload := map[string]interface{}{
		"rule_matches":       ruleMatches,
		"bundle_version":     privacy.RedactAndTruncate(bundleVersion, 128),
		"truncated":          result.Truncated,
		"hook_event_name":    "UserPromptSubmit",
		"prompt_text_stored": false,
	}
	event := model.EventEnvelope{
		EventID: util.NewID("evt"), CustomerID: cfg.CustomerID, DeploymentID: cfg.DeploymentID,
		DaemonID: cfg.DaemonID, DeviceID: cfg.DeviceID, TeamID: cfg.TeamID,
		EventType: "context_rule.matched", Source: source, AgentType: agentType,
		CreatedAt: time.Now().UTC(), Payload: payload,
	}
	queue := daemon.NewQueue(cfg.DataDir)
	if err := queue.Append([]model.EventEnvelope{event}); err != nil {
		return fmt.Errorf("queue context rule match: %w", err)
	}
	return nil
}
