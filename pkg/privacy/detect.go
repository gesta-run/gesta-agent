package privacy

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"sort"
	"strings"

	"github.com/gesta-run/gesta-agent/pkg/model"
)

const sensitiveFindingLimit = 20

type SensitiveFinding struct {
	Category    string  `json:"category"`
	Severity    string  `json:"severity"`
	Action      string  `json:"action"`
	Confidence  float64 `json:"confidence"`
	Fingerprint string  `json:"fingerprint"`
	Sample      string  `json:"sample"`
	SampleMode  string  `json:"sample_mode"`
	RuleID      string  `json:"rule_id"`
	RuleName    string  `json:"rule_name"`
}

func DetectSensitiveTextWithRules(input, fingerprintKey string, rules []model.SensitiveRule) []SensitiveFinding {
	input = strings.TrimSpace(input)
	if input == "" {
		return nil
	}

	key := []byte(strings.TrimSpace(fingerprintKey))
	if len(key) == 0 {
		key = []byte("gesta-sensitive-finding")
	}

	rules = normalizedSensitiveRules(rules)
	if len(rules) == 0 {
		return nil
	}

	seen := map[string]bool{}
	findings := make([]SensitiveFinding, 0, 2)
	for _, rule := range rules {
		if sensitiveDetectorType(rule.DetectorType) == "secret" {
			for _, loc := range smartSecretMatchIndexes(input, rule) {
				findings = appendSensitiveFinding(findings, seen, key, rule, input, loc[0], loc[1])
				if len(findings) >= sensitiveFindingLimit {
					return findings
				}
			}
			continue
		}
		pattern, err := regexp.Compile(rule.Pattern)
		if err != nil {
			continue
		}
		for _, loc := range pattern.FindAllStringIndex(input, -1) {
			if len(loc) != 2 || loc[0] < 0 || loc[1] <= loc[0] || loc[1] > len(input) {
				continue
			}
			findings = appendSensitiveFinding(findings, seen, key, rule, input, loc[0], loc[1])
			if len(findings) >= sensitiveFindingLimit {
				return findings
			}
		}
	}
	return findings
}

func normalizedSensitiveRules(rules []model.SensitiveRule) []model.SensitiveRule {
	if len(rules) == 0 {
		return nil
	}
	out := make([]model.SensitiveRule, 0, len(rules))
	for _, rule := range rules {
		if normalizeSensitiveToken(rule.Status) != "active" {
			continue
		}
		if source := normalizeSensitiveToken(rule.Source); source != "" && source != "user_prompt" {
			continue
		}
		if detector := sensitiveDetectorType(rule.DetectorType); detector != "" && detector != "regex" && detector != "secret" {
			continue
		}
		if sensitiveDetectorType(rule.DetectorType) != "secret" && strings.TrimSpace(rule.Pattern) == "" {
			continue
		}
		out = append(out, rule)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Priority != out[j].Priority {
			return out[i].Priority < out[j].Priority
		}
		return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
	})
	return out
}

func appendSensitiveFinding(findings []SensitiveFinding, seen map[string]bool, key []byte, rule model.SensitiveRule, input string, start, end int) []SensitiveFinding {
	if start < 0 || end <= start || end > len(input) {
		return findings
	}
	match := input[start:end]
	fingerprint := sensitiveFingerprint(key, rule.Category, match)
	if seen[fingerprint] {
		return findings
	}
	seen[fingerprint] = true
	sampleMode := sensitiveSampleMode(rule.SampleMode)
	return append(findings, SensitiveFinding{
		Category:    firstNonEmpty(normalizeSensitiveToken(rule.Category), "sensitive_data"),
		Severity:    firstNonEmpty(normalizeSensitiveToken(rule.Severity), "high"),
		Action:      firstNonEmpty(normalizeSensitiveToken(rule.Action), "block"),
		Confidence:  sensitiveConfidence(rule.Confidence),
		Fingerprint: fingerprint,
		Sample:      sensitiveSample(input, start, end, sampleMode),
		SampleMode:  sampleMode,
		RuleID:      strings.TrimSpace(rule.RuleID),
		RuleName:    strings.TrimSpace(rule.Name),
	})
}

func sensitiveFingerprint(key []byte, category, match string) string {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(category))
	mac.Write([]byte{0})
	mac.Write([]byte(match))
	return "hmac-sha256:" + hex.EncodeToString(mac.Sum(nil))
}

func sensitiveSample(input string, start, end int, sampleMode string) string {
	const contextBytes = 96

	if sampleMode == "fingerprint_only" {
		return ""
	}

	contextStart := start - contextBytes
	if contextStart < 0 {
		contextStart = 0
	}
	contextEnd := end + contextBytes
	if contextEnd > len(input) {
		contextEnd = len(input)
	}

	prefix := ""
	if contextStart > 0 {
		prefix = "... "
	}
	suffix := ""
	if contextEnd < len(input) {
		suffix = " ..."
	}

	sample := prefix + input[contextStart:contextEnd] + suffix
	sample = strings.ToValidUTF8(sample, "")
	if sampleMode == "redacted" {
		sample = RedactString(sample)
	}
	return strings.TrimSpace(sample)
}

func sensitiveSampleMode(value string) string {
	switch normalizeSensitiveToken(value) {
	case "redacted":
		return "redacted"
	case "fingerprint_only":
		return "fingerprint_only"
	default:
		return "original"
	}
}

func sensitiveConfidence(value float64) float64 {
	if value <= 0 {
		return 0.9
	}
	if value > 1 {
		return 1
	}
	return value
}

func normalizeSensitiveToken(value string) string {
	return strings.ReplaceAll(strings.ToLower(strings.TrimSpace(value)), "-", "_")
}

func sensitiveDetectorType(value string) string {
	switch normalizeSensitiveToken(value) {
	case "", "regex", "regexp":
		return "regex"
	case "secret", "smart_secret", "credential", "credentials":
		return "secret"
	default:
		return normalizeSensitiveToken(value)
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
