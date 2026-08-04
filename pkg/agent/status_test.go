package agent

import (
	"strings"
	"testing"

	"github.com/gesta-run/gesta-agent/pkg/daemon"
)

func TestStatusOutputReportsAuthWithoutCredential(t *testing.T) {
	secret := "sk-secret-value"
	output := statusOutput(daemon.Config{
		APIKey:        secret,
		DaemonID:      "daemon_test",
		ServerURL:     "https://control.example",
		PolicyVersion: "policy-v1",
		DataDir:       "/tmp/gesta-test",
	})

	if strings.Contains(output, secret) || strings.Contains(output, "api_key=") {
		t.Fatalf("status output leaked API key: %s", output)
	}
	if !strings.Contains(output, "auth=configured\n") {
		t.Fatalf("status output did not report configured auth: %s", output)
	}
	for _, serverOwned := range []string{"customer_id=", "deployment_id=", "enrollment_key_id="} {
		if strings.Contains(output, serverOwned) {
			t.Fatalf("status output retained %s: %s", serverOwned, output)
		}
	}
}

func TestStatusOutputReportsMissingAuth(t *testing.T) {
	if output := statusOutput(daemon.Config{}); !strings.Contains(output, "auth=not_configured\n") {
		t.Fatalf("status output did not report missing auth: %s", output)
	}
}
