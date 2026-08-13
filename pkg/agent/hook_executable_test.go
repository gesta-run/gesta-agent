package agent

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/gesta-run/gesta-agent/pkg/agentupgrade"
)

func TestPreferredHookExecutableUsesWindowsHelper(t *testing.T) {
	dir := t.TempDir()
	agentPath := filepath.Join(dir, "gesta-agent.exe")
	helperPath := filepath.Join(dir, agentupgrade.WindowsHookLauncherFilename)
	if err := os.WriteFile(helperPath, []byte("helper"), 0o700); err != nil {
		t.Fatal(err)
	}
	if got := preferredHookExecutable(agentPath, "windows"); got != helperPath {
		t.Fatalf("preferred hook executable = %q, want %q", got, helperPath)
	}
}

func TestPreferredHookExecutableFallsBackToAgent(t *testing.T) {
	agentPath := filepath.Join(t.TempDir(), "gesta-agent.exe")
	if got := preferredHookExecutable(agentPath, "windows"); got != agentPath {
		t.Fatalf("preferred hook executable = %q, want %q", got, agentPath)
	}
	if got := preferredHookExecutable(agentPath, "darwin"); got != agentPath {
		t.Fatalf("non-Windows hook executable = %q, want %q", got, agentPath)
	}
}
