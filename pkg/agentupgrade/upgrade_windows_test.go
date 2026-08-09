//go:build windows

package agentupgrade

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
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
