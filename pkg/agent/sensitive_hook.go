package agent

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/gesta-run/gesta-agent/pkg/daemon"
	"github.com/gesta-run/gesta-agent/pkg/eventqueue"
	"github.com/gesta-run/gesta-agent/pkg/model"
	"github.com/gesta-run/gesta-agent/pkg/privacy"
	"github.com/gesta-run/gesta-agent/pkg/rulecache"
	"github.com/gesta-run/gesta-agent/pkg/util"
)

func detectSensitivePrompt(cfg daemon.Config, prompt string) []privacy.SensitiveFinding {
	rules, ok := hookSensitiveRules(cfg)
	if !ok {
		return nil
	}
	return privacy.DetectSensitiveTextWithRules(prompt, sensitiveFingerprintKey(cfg), rules)
}

func hookSensitiveRules(cfg daemon.Config) ([]model.SensitiveRule, bool) {
	cache, err := rulecache.LoadSensitiveRuleCache(cfg.DataDir)
	if err != nil {
		return nil, false
	}
	return cache.Rules, true
}

func sensitiveFindingsShouldBlock(findings []privacy.SensitiveFinding) bool {
	for _, finding := range findings {
		if strings.EqualFold(strings.TrimSpace(finding.Action), "block") {
			return true
		}
	}
	return false
}

func recordSensitiveFindingsBestEffortWithConfig(
	cfg daemon.Config,
	hookEvent agentHookEvent,
	findings []privacy.SensitiveFinding,
	source, agentType string,
) {
	if err := recordSensitiveFindingsWithConfig(cfg, hookEvent, findings, source, agentType); err != nil {
		fmt.Fprintf(os.Stderr, "gesta-agent hook: sensitive finding was not recorded: %v\n", err)
	}
}

func recordSensitiveFindingsWithConfig(
	cfg daemon.Config,
	hookEvent agentHookEvent,
	findings []privacy.SensitiveFinding,
	source, agentType string,
) error {
	if len(findings) == 0 {
		return nil
	}
	events := make([]model.EventEnvelope, 0, len(findings))
	for _, finding := range findings {
		events = append(events, sensitiveFindingEvent(cfg, hookEvent, finding, source, agentType))
	}
	queue := eventqueue.NewQueue(cfg.DataDir)
	if err := queue.Append(events); err != nil {
		return fmt.Errorf("queue sensitive finding: %w", err)
	}
	return nil
}

func sensitiveFindingEvent(
	cfg daemon.Config,
	hookEvent agentHookEvent,
	finding privacy.SensitiveFinding,
	source, agentType string,
) model.EventEnvelope {
	payload := map[string]interface{}{
		"source":             "user_prompt",
		"hook_event_name":    hookEvent.HookEventName,
		"category":           finding.Category,
		"severity":           finding.Severity,
		"confidence":         finding.Confidence,
		"fingerprint":        finding.Fingerprint,
		"sample":             finding.Sample,
		"sample_mode":        finding.SampleMode,
		"action":             firstNonEmpty(finding.Action, "block"),
		"metadata_only":      finding.Sample == "",
		"raw_content_stored": finding.SampleMode == "original" && finding.Sample != "",
	}
	if finding.RuleID != "" {
		payload["rule_id"] = finding.RuleID
	}
	if finding.RuleName != "" {
		payload["rule_name"] = finding.RuleName
	}
	if sessionID := firstNonEmpty(hookEvent.SessionID, hookEvent.ConversationID); sessionID != "" {
		payload["session_id_hash"] = util.ShortHash(sessionID)
		payload["session_id_is_hashed"] = true
	}
	if hookEvent.TurnID != "" {
		payload["turn_id_hash"] = util.ShortHash(hookEvent.TurnID)
	}
	if hookEvent.CWD != "" {
		payload["cwd_hash"] = util.ShortHash(hookEvent.CWD)
	}
	if hookEvent.Model != "" {
		payload["model"] = privacy.RedactAndTruncate(hookEvent.Model, 128)
	}
	return model.EventEnvelope{
		EventID: util.NewID("evt"), CustomerID: cfg.CustomerID, DeploymentID: cfg.DeploymentID,
		DaemonID: cfg.DaemonID, DeviceID: cfg.DeviceID, TeamID: cfg.TeamID,
		EventType: "sensitive.finding", Source: source, AgentType: agentType,
		CreatedAt: time.Now().UTC(), Payload: payload,
	}
}

func sensitiveFingerprintKey(cfg daemon.Config) string {
	for _, candidate := range []string{
		cfg.Token,
		cfg.APIKey,
		cfg.DaemonID,
		cfg.DeviceID,
		cfg.DataDir,
	} {
		if strings.TrimSpace(candidate) != "" {
			return candidate
		}
	}
	return "gesta-local-sensitive-finding"
}
