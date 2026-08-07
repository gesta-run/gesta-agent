package agent

import (
	"context"
	"strings"
	"testing"
	"time"

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
	}, "/tmp/gesta-test/state.json")

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

func TestWaitForRuntimeRetriesUntilHealthy(t *testing.T) {
	previous := runtimeHealthy
	t.Cleanup(func() { runtimeHealthy = previous })
	calls := 0
	runtimeHealthy = func(context.Context, string) bool {
		calls++
		return calls == 2
	}
	if !waitForRuntime("daemon_test", time.Second) {
		t.Fatal("waitForRuntime did not observe healthy runtime")
	}
	if calls != 2 {
		t.Fatalf("health checks = %d, want 2", calls)
	}
}

func TestWaitForRuntimeDoesNotWaitByDefault(t *testing.T) {
	previous := runtimeHealthy
	t.Cleanup(func() { runtimeHealthy = previous })
	calls := 0
	runtimeHealthy = func(context.Context, string) bool {
		calls++
		return false
	}
	if waitForRuntime("daemon_test", 0) {
		t.Fatal("waitForRuntime unexpectedly reported healthy runtime")
	}
	if calls != 1 {
		t.Fatalf("health checks = %d, want 1", calls)
	}
}

func TestStatusOutputReportsMissingAuth(t *testing.T) {
	if output := statusOutput(daemon.Config{}, "/tmp/state.json"); !strings.Contains(output, "auth=not_configured\n") {
		t.Fatalf("status output did not report missing auth: %s", output)
	}
}
