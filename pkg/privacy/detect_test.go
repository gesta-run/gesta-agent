package privacy

import (
	"strings"
	"testing"

	"github.com/gesta-run/gesta-agent/pkg/model"
)

func TestDetectSensitiveTextStoresOriginalSampleAndFingerprints(t *testing.T) {
	secret := "sk-" + strings.Repeat("a", 32)

	findings := DetectSensitiveTextWithRules("please use "+secret+" for this test", "tenant-key", []model.SensitiveRule{openAIKeyRule()})

	if len(findings) != 1 {
		t.Fatalf("findings = %d, want 1: %#v", len(findings), findings)
	}
	finding := findings[0]
	if finding.Category != "openai_api_key" || finding.Severity != "critical" {
		t.Fatalf("unexpected finding: %#v", finding)
	}
	if finding.RuleID != "srule_openai_api_key" || finding.RuleName == "" {
		t.Fatalf("finding should include matched rule metadata: %#v", finding)
	}
	if finding.Action != "block" || finding.SampleMode != "original" {
		t.Fatalf("finding action/sample mode = %q/%q, want block/original", finding.Action, finding.SampleMode)
	}
	if !strings.HasPrefix(finding.Fingerprint, "hmac-sha256:") {
		t.Fatalf("fingerprint = %q, want hmac-sha256 prefix", finding.Fingerprint)
	}
	if !strings.Contains(finding.Sample, secret) {
		t.Fatalf("sample should keep original secret for audit review: %q", finding.Sample)
	}
	if strings.Contains(finding.Sample, "[REDACTED]") {
		t.Fatalf("sample should not redact original text: %q", finding.Sample)
	}

	again := DetectSensitiveTextWithRules("please use "+secret, "tenant-key", []model.SensitiveRule{openAIKeyRule()})
	if len(again) != 1 || again[0].Fingerprint != finding.Fingerprint {
		t.Fatalf("fingerprint should be stable for same key and secret: %#v", again)
	}
	otherTenant := DetectSensitiveTextWithRules("please use "+secret, "other-tenant-key", []model.SensitiveRule{openAIKeyRule()})
	if len(otherTenant) != 1 || otherTenant[0].Fingerprint == finding.Fingerprint {
		t.Fatalf("fingerprint should change when HMAC key changes: %#v", otherTenant)
	}
}

func TestDetectSensitiveTextFindsCredentialAssignment(t *testing.T) {
	findings := DetectSensitiveTextWithRules("token=abcdefghi1234567890", "tenant-key", []model.SensitiveRule{credentialAssignmentRule()})

	if len(findings) != 1 {
		t.Fatalf("findings = %d, want 1: %#v", len(findings), findings)
	}
	if findings[0].Category != "credential_assignment" {
		t.Fatalf("category = %q, want credential_assignment", findings[0].Category)
	}
	if !strings.Contains(findings[0].Sample, "abcdefghi1234567890") {
		t.Fatalf("sample should keep original assignment value: %q", findings[0].Sample)
	}
}

func TestDetectSensitiveTextWithCustomRuleFingerprintOnlyRecord(t *testing.T) {
	findings := DetectSensitiveTextWithRules("customer_secret_123 should be logged", "tenant-key", []model.SensitiveRule{
		{
			RuleID:       "srule_custom_customer_secret",
			Name:         "Customer secret IDs",
			Status:       "active",
			Source:       "user_prompt",
			DetectorType: "regex",
			Pattern:      `customer_secret_[0-9]+`,
			Category:     "customer_secret",
			Severity:     "medium",
			Action:       "record",
			SampleMode:   "fingerprint_only",
			Confidence:   0.77,
			Priority:     1,
		},
	})

	if len(findings) != 1 {
		t.Fatalf("findings = %d, want 1: %#v", len(findings), findings)
	}
	finding := findings[0]
	if finding.RuleID != "srule_custom_customer_secret" ||
		finding.RuleName != "Customer secret IDs" ||
		finding.Category != "customer_secret" ||
		finding.Severity != "medium" ||
		finding.Action != "record" ||
		finding.SampleMode != "fingerprint_only" ||
		finding.Confidence != 0.77 {
		t.Fatalf("unexpected custom finding: %#v", finding)
	}
	if finding.Sample != "" {
		t.Fatalf("fingerprint_only rule should not retain sample, got %q", finding.Sample)
	}
	if !strings.HasPrefix(finding.Fingerprint, "hmac-sha256:") {
		t.Fatalf("fingerprint = %q, want hmac-sha256 prefix", finding.Fingerprint)
	}
}

func TestDetectSensitiveTextWithSmartSecretRuleFindsAuth0Secret(t *testing.T) {
	secret := "test_9xLmR7QpV2nB4sC8dF6gH1jK3zT5wY0aE9rU2iO4pS6dV8kN0m"
	findings := DetectSensitiveTextWithRules("please use "+secret+" for auth0", "tenant-key", []model.SensitiveRule{smartSecretRule("")})

	if len(findings) != 1 {
		t.Fatalf("findings = %d, want 1: %#v", len(findings), findings)
	}
	finding := findings[0]
	if finding.Category != "auth0_secret" || finding.Action != "block" || finding.SampleMode != "redacted" {
		t.Fatalf("unexpected smart secret finding: %#v", finding)
	}
	if strings.Contains(finding.Sample, secret) {
		t.Fatalf("redacted smart secret sample leaked original secret: %q", finding.Sample)
	}
	if !strings.Contains(finding.Sample, "[REDACTED]") {
		t.Fatalf("redacted smart secret sample = %q, want redaction marker", finding.Sample)
	}
}

func TestDetectSensitiveTextWithSmartSecretRuleUsesOptionalContextPattern(t *testing.T) {
	secret := "test_9xLmR7QpV2nB4sC8dF6gH1jK3zT5wY0aE9rU2iO4pS6dV8kN0m"
	rule := smartSecretRule(`(?i)auth0`)

	if findings := DetectSensitiveTextWithRules(secret, "tenant-key", []model.SensitiveRule{rule}); len(findings) != 0 {
		t.Fatalf("bare findings = %d, want 0: %#v", len(findings), findings)
	}
	findings := DetectSensitiveTextWithRules("AUTH0_CLIENT_SECRET="+secret, "tenant-key", []model.SensitiveRule{rule})
	if len(findings) != 1 {
		t.Fatalf("auth0 assignment findings = %d, want 1: %#v", len(findings), findings)
	}
}

func TestDetectSensitiveTextWithSmartSecretRuleIgnoresCommonNonSecrets(t *testing.T) {
	input := strings.Join([]string{
		"commit 0123456789abcdef0123456789abcdef01234567",
		"trace 550e8400-e29b-41d4-a716-446655440000",
		"thisisaverylongbutordinaryenglishsentence",
	}, "\n")

	if findings := DetectSensitiveTextWithRules(input, "tenant-key", []model.SensitiveRule{smartSecretRule("")}); len(findings) != 0 {
		t.Fatalf("findings = %d, want 0: %#v", len(findings), findings)
	}
}

func TestDetectSensitiveTextWithSmartSecretRuleIgnoresPublicResourceURLs(t *testing.T) {
	input := strings.Join([]string{
		"https://drive.google.com/drive/folders/0AEq3-bv1ilsyUk9PVA",
		"We have multiple unrelated products. One of them is CloudPilot AI.",
		"This is a shared resource link, not a token.",
	}, "\n")

	if findings := DetectSensitiveTextWithRules(input, "tenant-key", []model.SensitiveRule{smartSecretRule("")}); len(findings) != 0 {
		t.Fatalf("public URL findings = %d, want 0: %#v", len(findings), findings)
	}
}

func openAIKeyRule() model.SensitiveRule {
	return model.SensitiveRule{
		RuleID:       "srule_openai_api_key",
		Name:         "OpenAI API keys",
		Status:       "active",
		Source:       "user_prompt",
		DetectorType: "regex",
		Pattern:      `\bsk-[A-Za-z0-9_-]{20,}\b`,
		Category:     "openai_api_key",
		Severity:     "critical",
		Action:       "block",
		SampleMode:   "original",
		Confidence:   0.99,
		Priority:     1,
	}
}

func credentialAssignmentRule() model.SensitiveRule {
	return model.SensitiveRule{
		RuleID:       "srule_credential_assignment",
		Name:         "Credential assignments",
		Status:       "active",
		Source:       "user_prompt",
		DetectorType: "regex",
		Pattern:      `(?i)\b(api[_-]?key|access[_-]?token|auth[_-]?token|token|secret|password|passwd)\s*[:=]\s*["']?[A-Za-z0-9._~+/=-]{8,}["']?`,
		Category:     "credential_assignment",
		Severity:     "high",
		Action:       "block",
		SampleMode:   "original",
		Confidence:   0.88,
		Priority:     1,
	}
}

func smartSecretRule(pattern string) model.SensitiveRule {
	return model.SensitiveRule{
		RuleID:       "srule_auth0_secret",
		Name:         "Auth0 secrets",
		Status:       "active",
		Source:       "user_prompt",
		DetectorType: "secret",
		Pattern:      pattern,
		Category:     "auth0_secret",
		Severity:     "critical",
		Action:       "block",
		SampleMode:   "redacted",
		Confidence:   0.9,
		Priority:     1,
	}
}
