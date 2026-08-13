//go:build windows

package agentupgrade

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/gesta-run/gesta-agent/pkg/model"
)

func hookLauncherUpgradeRequired(policy model.AgentUpgradePolicy) bool {
	artifact := policy.HookLauncher
	if artifact == nil || strings.TrimSpace(artifact.URL) == "" || normalizeSHA256(artifact.SHA256) == "" {
		return false
	}
	executablePath, err := os.Executable()
	if err != nil {
		return true
	}
	launcherPath := filepath.Join(filepath.Dir(executablePath), WindowsHookLauncherFilename)
	return verifyFileSHA256(launcherPath, artifact.SHA256) != nil
}

func stageHookLauncherUpgrade(ctx context.Context, policy model.AgentUpgradePolicy, targetPath string) (replacementArtifact, error) {
	artifact := policy.HookLauncher
	if artifact == nil {
		return replacementArtifact{}, nil
	}
	downloadURL := strings.TrimSpace(artifact.URL)
	if err := validateUpgradeURL(downloadURL); err != nil {
		return replacementArtifact{}, err
	}
	expectedSHA := normalizeSHA256(artifact.SHA256)
	if expectedSHA == "" {
		return replacementArtifact{}, errors.New("hook launcher sha256 is required")
	}
	dir := filepath.Dir(targetPath)
	tmp, err := os.CreateTemp(dir, ".gesta-agent-hook-launcher-upgrade-*.exe")
	if err != nil {
		return replacementArtifact{}, err
	}
	tmpPath := tmp.Name()
	_ = tmp.Close()
	if err := downloadUpgradeFile(ctx, downloadURL, tmpPath); err != nil {
		_ = os.Remove(tmpPath)
		return replacementArtifact{}, err
	}
	if err := verifyFileSHA256(tmpPath, expectedSHA); err != nil {
		_ = os.Remove(tmpPath)
		return replacementArtifact{}, err
	}
	return replacementArtifact{
		SourcePath: tmpPath,
		TargetPath: filepath.Join(dir, WindowsHookLauncherFilename),
	}, nil
}
