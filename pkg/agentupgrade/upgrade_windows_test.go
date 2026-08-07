//go:build windows

package agentupgrade

import (
	"context"
	"strings"
	"testing"

	"github.com/gesta-run/gesta-agent/pkg/model"
)

func TestAutomaticUpgradeSupported(t *testing.T) {
	if AutomaticUpgradeSupported() {
		t.Fatal("automatic upgrades must remain disabled for the Windows RC")
	}
}

func TestApplyUpgradeFailsBeforeDownloadOnWindows(t *testing.T) {
	err := ApplyAgentUpgradeToPath(context.Background(), model.AgentUpgradePolicy{}, "")
	if err == nil || !strings.Contains(err.Error(), "not supported on Windows RC") {
		t.Fatalf("ApplyAgentUpgradeToPath error = %v, want unsupported platform", err)
	}
}
