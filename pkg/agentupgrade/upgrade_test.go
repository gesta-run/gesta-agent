package agentupgrade

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gesta-run/gesta-agent/pkg/model"
)

func TestDecideAgentUpgrade(t *testing.T) {
	policy := model.AgentUpgradePolicy{
		Mode:          "auto",
		TargetVersion: "0.0.1-rc22",
		URL:           "https://updates.example/bin/gesta-agent-darwin-arm64",
		SHA256:        strings.Repeat("a", 64),
	}
	decision := DecideAgentUpgrade(policy, "0.0.1-rc21")
	if !decision.ShouldApply || decision.Mode != "auto" {
		t.Fatalf("decision = %+v, want auto apply", decision)
	}

	decision = DecideAgentUpgrade(policy, "0.0.1-rc22")
	if decision.ShouldApply || decision.Reason == "" {
		t.Fatalf("decision = %+v, want skip current version", decision)
	}

	policy.Mode = "notify"
	decision = DecideAgentUpgrade(policy, "0.0.1-rc21")
	if decision.ShouldApply || decision.Mode != "notify" {
		t.Fatalf("decision = %+v, want notify only", decision)
	}
}

func TestApplyAgentUpgradeToPathRejectsBadChecksum(t *testing.T) {
	const targetVersion = "9.9.9"
	newAgent := "#!/bin/sh\nif [ \"$1\" = \"version\" ]; then echo " + targetVersion + "; fi\n"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(newAgent))
	}))
	defer server.Close()

	targetPath := filepath.Join(t.TempDir(), "gesta-agent")
	if err := os.WriteFile(targetPath, []byte("#!/bin/sh\necho old\n"), 0o755); err != nil {
		t.Fatalf("write target: %v", err)
	}
	err := ApplyAgentUpgradeToPath(context.Background(), model.AgentUpgradePolicy{
		TargetVersion: targetVersion,
		URL:           server.URL,
		SHA256:        strings.Repeat("0", 64),
	}, targetPath)
	if err == nil {
		t.Fatal("expected checksum mismatch")
	}
}
