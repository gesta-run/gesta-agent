package daemon

import (
	"strings"
	"time"

	"github.com/gesta-run/gesta-agent/pkg/model"
	"github.com/gesta-run/gesta-agent/pkg/privacy"
	"github.com/gesta-run/gesta-agent/pkg/util"
)

func codexSensitiveRulesForCollection(cfg Config) []model.SensitiveRule {
	if cached, err := LoadSensitiveRuleCache(cfg.DataDir); err == nil {
		return cached.Rules
	}
	return []model.SensitiveRule{}
}

func codexSensitiveFindingEventsFromTranscripts(cfg Config, transcripts []map[string]interface{}, rules []model.SensitiveRule) []model.EventEnvelope {
	if len(transcripts) == 0 || len(rules) == 0 {
		return nil
	}
	if codexUserPromptSubmitHookActive() {
		return nil
	}
	var events []model.EventEnvelope
	observedAt := time.Now().UTC()
	for _, transcript := range transcripts {
		events = append(events, codexSensitiveFindingEventsAt(cfg, transcript, rules, observedAt)...)
	}
	return events
}

func codexSensitiveFindingEvents(cfg Config, transcript map[string]interface{}, rules []model.SensitiveRule) []model.EventEnvelope {
	return codexSensitiveFindingEventsAt(cfg, transcript, rules, time.Now().UTC())
}

func codexSensitiveFindingEventsAt(cfg Config, transcript map[string]interface{}, rules []model.SensitiveRule, observedAt time.Time) []model.EventEnvelope {
	if len(rules) == 0 {
		return nil
	}
	if observedAt.IsZero() {
		observedAt = time.Now().UTC()
	}
	rolloutPathHash := firstString(transcript, "transcript_path_hash")
	rolloutPath := firstString(transcript, "rollout_path")
	if rolloutPath == "" {
		rolloutPath = firstString(transcript, "_rollout_path")
	}
	if rolloutPath == "" {
		return nil
	}
	sessionID := firstString(transcript, "session_id", "session_id_hash")
	if sessionID == "" {
		return nil
	}
	messages, err := readCodexSensitiveTranscriptMessages(rolloutPath)
	if err != nil || len(messages) == 0 {
		return nil
	}
	var events []model.EventEnvelope
	for _, message := range messages {
		messageObservedAt, hasMessageObservedAt := parseCodexTimestamp(message.Timestamp)
		if hasMessageObservedAt && codexSensitiveTranscriptMessageTooOld(messageObservedAt, observedAt) {
			continue
		}
		findings := privacy.DetectSensitiveTextWithRules(message.Text, codexSensitiveFingerprintKey(cfg), rules)
		for _, finding := range findings {
			payload := map[string]interface{}{
				"source":               "user_prompt",
				"detection_source":     "session_transcript",
				"hook_event_name":      "SessionTranscript",
				"category":             finding.Category,
				"severity":             finding.Severity,
				"confidence":           finding.Confidence,
				"fingerprint":          finding.Fingerprint,
				"sample":               finding.Sample,
				"sample_mode":          finding.SampleMode,
				"action":               firstNonEmptyString(finding.Action, "block"),
				"metadata_only":        finding.Sample == "",
				"raw_content_stored":   finding.SampleMode == "original" && finding.Sample != "",
				"session_id_hash":      sessionID,
				"session_id_is_hashed": true,
				"message_hash":         util.ShortHash(message.Text),
			}
			if rolloutPathHash != "" {
				payload["transcript_path_hash"] = rolloutPathHash
			}
			if message.Timestamp != "" {
				payload["message_timestamp"] = message.Timestamp
			}
			if finding.RuleID != "" {
				payload["rule_id"] = finding.RuleID
			}
			if finding.RuleName != "" {
				payload["rule_name"] = finding.RuleName
			}
			if modelName := firstString(transcript, "model"); modelName != "" {
				payload["model"] = privacy.RedactAndTruncate(modelName, 128)
			}
			event := baseEvent(cfg, "sensitive.finding", "codex", "codex", payload)
			if hasMessageObservedAt {
				event.CreatedAt = messageObservedAt
			}
			event.EventID = codexSensitiveFindingEventID(sessionID, message, finding)
			events = append(events, event)
		}
	}
	return events
}

func codexSensitiveFingerprintKey(cfg Config) string {
	for _, candidate := range []string{
		cfg.Token,
		cfg.APIKey,
		cfg.DaemonID,
		cfg.DeviceID,
		cfg.UserID,
		cfg.DataDir,
	} {
		if strings.TrimSpace(candidate) != "" {
			return candidate
		}
	}
	return "gesta-local-sensitive-finding"
}

func codexSensitiveFindingEventID(sessionID string, message codexSensitiveTranscriptMessage, finding privacy.SensitiveFinding) string {
	parts := []string{
		"codex.sensitive.finding",
		sessionID,
		message.Timestamp,
		util.ShortHash(message.Text),
		finding.RuleID,
		finding.Fingerprint,
	}
	return "evt_" + util.ShortHash(strings.Join(parts, "\x00"))
}

func codexSensitiveTranscriptMessageTooOld(messageTime, observedAt time.Time) bool {
	if messageTime.IsZero() || observedAt.IsZero() {
		return false
	}
	return messageTime.Before(observedAt.Add(-codexSensitiveTranscriptWindow)) || messageTime.After(observedAt.Add(5*time.Minute))
}
