//go:build windows

package agentupgrade

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/windows"
)

const (
	windowsUpgradeWait          = 2 * time.Minute
	windowsReplacementRetryWait = 30 * time.Second
)

func RunUpgradeHelper(args []string) error {
	fs := flag.NewFlagSet(HelperCommand, flag.ContinueOnError)
	parentPID := fs.Int("parent-pid", 0, "parent process ID")
	sourcePath := fs.String("source", "", "staged agent path")
	targetPath := fs.String("target", "", "installed agent path")
	workingDirectory := fs.String("working-dir", "", "agent working directory")
	statePath := fs.String("state-path", "", "upgrade state path")
	targetVersion := fs.String("target-version", "", "upgrade target version")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *parentPID <= 0 || strings.TrimSpace(*sourcePath) == "" || strings.TrimSpace(*targetPath) == "" {
		return errors.New("parent-pid, source, and target are required")
	}
	if (strings.TrimSpace(*statePath) == "") != (strings.TrimSpace(*targetVersion) == "") {
		return errors.New("state-path and target-version must be provided together")
	}
	if err := verifyWindowsUpgradeHelperPath(*sourcePath); err != nil {
		return err
	}
	if err := waitForWindowsProcessExit(*parentPID, windowsUpgradeWait); err != nil {
		return err
	}

	restartArgs := fs.Args()
	if err := retryWindowsAgentReplacement(*sourcePath, *targetPath, windowsReplacementRetryWait); err != nil {
		resultErr := recordWindowsUpgradeResult(*statePath, *targetVersion, err)
		if len(restartArgs) > 0 {
			if restartErr := startWindowsAgent(*targetPath, restartArgs, *workingDirectory); restartErr != nil {
				combinedErr := errors.Join(err, restartErr)
				return recordWindowsUpgradeResult(*statePath, *targetVersion, combinedErr)
			}
		}
		return resultErr
	}
	_ = scheduleWindowsFileDeletion(*sourcePath)
	if len(restartArgs) == 0 {
		return recordWindowsUpgradeResult(*statePath, *targetVersion, nil)
	}
	if err := startWindowsAgent(*targetPath, restartArgs, *workingDirectory); err != nil {
		backupPath := *targetPath + ".prev"
		if restoreErr := moveWindowsFile(backupPath, *targetPath); restoreErr != nil {
			return recordWindowsUpgradeResult(*statePath, *targetVersion, errors.Join(err, fmt.Errorf("restore previous binary: %w", restoreErr)))
		}
		resultErr := recordWindowsUpgradeResult(*statePath, *targetVersion, err)
		if restoreStartErr := startWindowsAgent(*targetPath, restartArgs, *workingDirectory); restoreStartErr != nil {
			combinedErr := errors.Join(err, fmt.Errorf("restart previous binary: %w", restoreStartErr))
			return recordWindowsUpgradeResult(*statePath, *targetVersion, combinedErr)
		}
		return resultErr
	}
	return recordWindowsUpgradeResult(*statePath, *targetVersion, nil)
}

func recordWindowsUpgradeResult(statePath, targetVersion string, resultErr error) error {
	statePath = strings.TrimSpace(statePath)
	if statePath == "" {
		return resultErr
	}
	state, err := LoadUpgradeState(statePath)
	if err != nil {
		return errors.Join(resultErr, fmt.Errorf("load upgrade state: %w", err))
	}
	state.Enabled = true
	state.TargetVersion = strings.TrimSpace(targetVersion)
	if resultErr != nil {
		state.State = "failed"
		state.Error = resultErr.Error()
	} else {
		state.State = "succeeded"
		state.Error = ""
		state.LastSucceededAt = time.Now().UTC()
	}
	if err := SaveUpgradeState(statePath, state); err != nil {
		return errors.Join(resultErr, fmt.Errorf("save upgrade state: %w", err))
	}
	return resultErr
}

func verifyWindowsUpgradeHelperPath(sourcePath string) error {
	executablePath, err := os.Executable()
	if err != nil {
		return err
	}
	executablePath, err = filepath.Abs(executablePath)
	if err != nil {
		return err
	}
	sourcePath, err = filepath.Abs(sourcePath)
	if err != nil {
		return err
	}
	if !strings.EqualFold(filepath.Clean(executablePath), filepath.Clean(sourcePath)) {
		return errors.New("upgrade helper source does not match the running executable")
	}
	return nil
}

func waitForWindowsProcessExit(pid int, timeout time.Duration) error {
	handle, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(pid))
	if errors.Is(err, windows.ERROR_INVALID_PARAMETER) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("open parent process: %w", err)
	}
	defer windows.CloseHandle(handle)
	waitMilliseconds := uint32(timeout / time.Millisecond)
	event, err := windows.WaitForSingleObject(handle, waitMilliseconds)
	if err != nil {
		return fmt.Errorf("wait for parent process: %w", err)
	}
	if event != windows.WAIT_OBJECT_0 {
		return fmt.Errorf("timed out waiting for parent process %d", pid)
	}
	return nil
}

func retryWindowsAgentReplacement(sourcePath, targetPath string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	var lastErr error
	for {
		lastErr = replaceWindowsAgentFromHelper(sourcePath, targetPath)
		if lastErr == nil {
			return nil
		}
		if time.Now().After(deadline) {
			return lastErr
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func replaceWindowsAgentFromHelper(sourcePath, targetPath string) error {
	nextPath := targetPath + ".next"
	backupPath := targetPath + ".prev"
	_ = os.Remove(nextPath)
	if err := copyWindowsUpgradeFile(sourcePath, nextPath); err != nil {
		return fmt.Errorf("prepare replacement agent: %w", err)
	}
	defer os.Remove(nextPath)
	if err := moveWindowsFile(targetPath, backupPath); err != nil {
		return fmt.Errorf("backup current agent binary: %w", err)
	}
	if err := moveWindowsFile(nextPath, targetPath); err != nil {
		if restoreErr := moveWindowsFile(backupPath, targetPath); restoreErr != nil {
			return fmt.Errorf("install upgraded agent binary: %w; restore previous binary: %v", err, restoreErr)
		}
		return fmt.Errorf("install upgraded agent binary: %w", err)
	}
	return nil
}

func copyWindowsUpgradeFile(sourcePath, targetPath string) error {
	source, err := os.Open(sourcePath)
	if err != nil {
		return err
	}
	defer source.Close()
	target, err := os.OpenFile(targetPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o700)
	if err != nil {
		return err
	}
	if _, err := io.Copy(target, source); err != nil {
		_ = target.Close()
		return err
	}
	if err := target.Sync(); err != nil {
		_ = target.Close()
		return err
	}
	return target.Close()
}

func moveWindowsFile(sourcePath, targetPath string) error {
	source, err := windows.UTF16PtrFromString(sourcePath)
	if err != nil {
		return err
	}
	target, err := windows.UTF16PtrFromString(targetPath)
	if err != nil {
		return err
	}
	return windows.MoveFileEx(source, target, windows.MOVEFILE_REPLACE_EXISTING|windows.MOVEFILE_WRITE_THROUGH)
}

func scheduleWindowsFileDeletion(path string) error {
	source, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	return windows.MoveFileEx(source, nil, windows.MOVEFILE_DELAY_UNTIL_REBOOT)
}

func startWindowsAgent(path string, args []string, workingDirectory string) error {
	cmd := exec.Command(path, args...)
	if strings.TrimSpace(workingDirectory) != "" {
		cmd.Dir = workingDirectory
	}
	configureDetachedWindowsProcess(cmd)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("restart upgraded Windows agent: %w", err)
	}
	return cmd.Process.Release()
}
