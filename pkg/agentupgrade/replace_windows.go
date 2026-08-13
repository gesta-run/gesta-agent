//go:build windows

package agentupgrade

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"syscall"

	"golang.org/x/sys/windows"
)

const HelperCommand = "__upgrade-helper"

func replaceAgentBinary(tmpPath, targetPath string, options replacementOptions) error {
	stagedPath := targetPath + ".upgrade.exe"
	if err := os.Remove(stagedPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove stale staged upgrade: %w", err)
	}
	if err := os.Rename(tmpPath, stagedPath); err != nil {
		return fmt.Errorf("stage Windows agent upgrade: %w", err)
	}

	workingDirectory, _ := os.Getwd()
	args := []string{
		HelperCommand,
		"--parent-pid", strconv.Itoa(os.Getpid()),
		"--source", stagedPath,
		"--target", targetPath,
		"--working-dir", workingDirectory,
	}
	if options.StatePath != "" {
		args = append(args,
			"--state-path", options.StatePath,
			"--target-version", options.TargetVersion,
		)
	}
	if options.HookLauncher.SourcePath != "" {
		args = append(args,
			"--hook-launcher-source", options.HookLauncher.SourcePath,
			"--hook-launcher-target", options.HookLauncher.TargetPath,
		)
	}
	if restartArgs := windowsUpgradeRestartArgs(os.Args); len(restartArgs) > 0 {
		args = append(args, "--")
		args = append(args, restartArgs...)
	}
	cmd := exec.Command(stagedPath, args...)
	configureDetachedWindowsProcess(cmd)
	if err := cmd.Start(); err != nil {
		_ = os.Remove(stagedPath)
		return fmt.Errorf("start Windows upgrade helper: %w", err)
	}
	return cmd.Process.Release()
}

func UpgradeCompletesAfterExit() bool {
	return true
}

func windowsUpgradeRestartArgs(args []string) []string {
	if len(args) < 2 || args[1] != "run" {
		return nil
	}
	return append([]string(nil), args[1:]...)
}

func configureDetachedWindowsProcess(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: windows.CREATE_NEW_PROCESS_GROUP | windows.DETACHED_PROCESS,
		HideWindow:    true,
	}
}
