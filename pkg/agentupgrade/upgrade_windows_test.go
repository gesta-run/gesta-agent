//go:build windows

package agentupgrade

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/gesta-run/gesta-agent/pkg/model"
)

func TestReplaceWindowsAgentFromHelperPreservesBackup(t *testing.T) {
	directory := t.TempDir()
	sourcePath := filepath.Join(directory, "staged.exe")
	targetPath := filepath.Join(directory, "gesta-agent.exe")
	if err := os.WriteFile(sourcePath, []byte("new agent"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(targetPath, []byte("old agent"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := replaceWindowsAgentFromHelper(sourcePath, targetPath); err != nil {
		t.Fatalf("replace Windows agent: %v", err)
	}
	upgraded, err := os.ReadFile(targetPath)
	if err != nil || string(upgraded) != "new agent" {
		t.Fatalf("upgraded agent = %q, err=%v", upgraded, err)
	}
	backup, err := os.ReadFile(targetPath + ".prev")
	if err != nil || string(backup) != "old agent" {
		t.Fatalf("backup agent = %q, err=%v", backup, err)
	}
}

func TestReplaceWindowsUpgradeBundleInstallsMissingHookLauncher(t *testing.T) {
	directory := t.TempDir()
	agentSource := filepath.Join(directory, "agent-staged.exe")
	agentTarget := filepath.Join(directory, "gesta-agent.exe")
	hookSource := filepath.Join(directory, "hook-staged.exe")
	hookTarget := filepath.Join(directory, WindowsHookLauncherFilename)
	for path, content := range map[string]string{
		agentSource: "new agent",
		agentTarget: "old agent",
		hookSource:  "new hook launcher",
	} {
		if err := os.WriteFile(path, []byte(content), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	err := replaceWindowsUpgradeBundleFromHelper([]windowsUpgradeFile{
		{sourcePath: agentSource, targetPath: agentTarget},
		{sourcePath: hookSource, targetPath: hookTarget},
	})
	if err != nil {
		t.Fatalf("replace Windows upgrade bundle: %v", err)
	}
	if got, err := os.ReadFile(agentTarget); err != nil || string(got) != "new agent" {
		t.Fatalf("upgraded agent = %q, err=%v", got, err)
	}
	if got, err := os.ReadFile(hookTarget); err != nil || string(got) != "new hook launcher" {
		t.Fatalf("hook launcher = %q, err=%v", got, err)
	}
	if got, err := os.ReadFile(agentTarget + ".prev"); err != nil || string(got) != "old agent" {
		t.Fatalf("agent backup = %q, err=%v", got, err)
	}
	if _, err := os.Stat(hookTarget + ".prev"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("new hook launcher unexpectedly has backup: %v", err)
	}
}

func TestDecideAgentUpgradeRepairsMissingHookLauncherAtCurrentVersion(t *testing.T) {
	policy := model.AgentUpgradePolicy{
		Mode:          "auto",
		TargetVersion: "1.2.3",
		URL:           "https://updates.example/gesta-agent-windows-amd64.exe",
		SHA256:        strings.Repeat("a", 64),
		HookLauncher: &model.AgentUpgradeArtifact{
			URL:    "https://updates.example/gesta-agent-hook-launcher-windows-amd64.exe",
			SHA256: strings.Repeat("b", 64),
		},
	}
	decision := DecideAgentUpgrade(policy, "1.2.3")
	if !decision.ShouldApply || !strings.Contains(decision.Reason, "companion") {
		t.Fatalf("same-version migration decision = %+v, want companion repair", decision)
	}
}

func TestStageHookLauncherUpgradeRejectsBadChecksum(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("hook launcher"))
	}))
	defer server.Close()

	directory := t.TempDir()
	_, err := stageHookLauncherUpgrade(context.Background(), model.AgentUpgradePolicy{
		HookLauncher: &model.AgentUpgradeArtifact{
			URL:    server.URL + "/gesta-agent-hook-launcher-windows-amd64.exe",
			SHA256: strings.Repeat("0", 64),
		},
	}, filepath.Join(directory, "gesta-agent.exe"))
	if err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("bad hook launcher checksum error = %v", err)
	}
	staged, globErr := filepath.Glob(filepath.Join(directory, ".gesta-agent-hook-launcher-upgrade-*.exe"))
	if globErr != nil || len(staged) != 0 {
		t.Fatalf("staged hook launcher files = %v, err=%v", staged, globErr)
	}
}

func TestRecordWindowsUpgradeResult(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "upgrade-state.json")
	if err := SaveUpgradeState(statePath, UpgradeState{State: "staged", TargetVersion: "0.0.1-rc99"}); err != nil {
		t.Fatal(err)
	}
	failure := errors.New("replacement failed")
	if err := recordWindowsUpgradeResult(statePath, "0.0.1-rc99", failure); !errors.Is(err, failure) {
		t.Fatalf("record failure error = %v", err)
	}
	state, err := LoadUpgradeState(statePath)
	if err != nil || state.State != "failed" || state.Error != failure.Error() {
		t.Fatalf("failed state = %+v, err=%v", state, err)
	}
	if err := recordWindowsUpgradeResult(statePath, "0.0.1-rc99", nil); err != nil {
		t.Fatal(err)
	}
	state, err = LoadUpgradeState(statePath)
	if err != nil || state.State != "succeeded" || state.Error != "" || state.LastSucceededAt.IsZero() {
		t.Fatalf("succeeded state = %+v, err=%v", state, err)
	}
}

func TestWindowsUpgradeRestartArgsOnlyRestartsDaemon(t *testing.T) {
	want := []string{"run", "--interval", "1m"}
	if got := windowsUpgradeRestartArgs([]string{"gesta-agent.exe", "run", "--interval", "1m"}); !reflect.DeepEqual(got, want) {
		t.Fatalf("restart args = %#v, want %#v", got, want)
	}
	if got := windowsUpgradeRestartArgs([]string{"gesta-agent.exe", "upgrade", "--force"}); got != nil {
		t.Fatalf("manual upgrade restart args = %#v, want nil", got)
	}
}
