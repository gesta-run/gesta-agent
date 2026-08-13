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
	hookLauncherSource := fs.String("hook-launcher-source", "", "staged hook launcher path")
	hookLauncherTarget := fs.String("hook-launcher-target", "", "installed hook launcher path")
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
	if (strings.TrimSpace(*hookLauncherSource) == "") != (strings.TrimSpace(*hookLauncherTarget) == "") {
		return errors.New("hook-launcher-source and hook-launcher-target must be provided together")
	}
	if err := verifyWindowsUpgradeHelperPath(*sourcePath); err != nil {
		return err
	}
	if err := waitForWindowsProcessExit(*parentPID, windowsUpgradeWait); err != nil {
		return err
	}

	replacements := []windowsUpgradeFile{{sourcePath: *sourcePath, targetPath: *targetPath}}
	if strings.TrimSpace(*hookLauncherSource) != "" {
		replacements = append(replacements, windowsUpgradeFile{
			sourcePath: *hookLauncherSource,
			targetPath: *hookLauncherTarget,
		})
	}
	defer func() {
		for _, replacement := range replacements {
			_ = scheduleWindowsFileDeletion(replacement.sourcePath)
		}
	}()
	restartArgs := fs.Args()
	if err := retryWindowsUpgradeBundle(replacements, windowsReplacementRetryWait); err != nil {
		resultErr := recordWindowsUpgradeResult(*statePath, *targetVersion, err)
		if len(restartArgs) > 0 {
			if restartErr := startWindowsAgent(*targetPath, restartArgs, *workingDirectory); restartErr != nil {
				combinedErr := errors.Join(err, restartErr)
				return recordWindowsUpgradeResult(*statePath, *targetVersion, combinedErr)
			}
		}
		return resultErr
	}
	if len(restartArgs) == 0 {
		return recordWindowsUpgradeResult(*statePath, *targetVersion, nil)
	}
	if err := startWindowsAgent(*targetPath, restartArgs, *workingDirectory); err != nil {
		if restoreErr := restoreWindowsUpgradeBackups(replacements); restoreErr != nil {
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

type windowsUpgradeFile struct {
	sourcePath string
	targetPath string
}

func retryWindowsAgentReplacement(sourcePath, targetPath string, timeout time.Duration) error {
	return retryWindowsUpgradeBundle([]windowsUpgradeFile{{sourcePath: sourcePath, targetPath: targetPath}}, timeout)
}

func retryWindowsUpgradeBundle(replacements []windowsUpgradeFile, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	var lastErr error
	for {
		lastErr = replaceWindowsUpgradeBundleFromHelper(replacements)
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
	return replaceWindowsUpgradeBundleFromHelper([]windowsUpgradeFile{{sourcePath: sourcePath, targetPath: targetPath}})
}

func replaceWindowsUpgradeBundleFromHelper(replacements []windowsUpgradeFile) error {
	if len(replacements) == 0 {
		return errors.New("upgrade bundle is empty")
	}
	for _, replacement := range replacements {
		nextPath := replacement.targetPath + ".next"
		_ = os.Remove(nextPath)
		if err := copyWindowsUpgradeFile(replacement.sourcePath, nextPath); err != nil {
			return fmt.Errorf("prepare replacement %s: %w", filepath.Base(replacement.targetPath), err)
		}
		defer os.Remove(nextPath)
	}

	backedUp := make([]windowsUpgradeFile, 0, len(replacements))
	for _, replacement := range replacements {
		backupPath := replacement.targetPath + ".prev"
		if _, err := os.Stat(replacement.targetPath); errors.Is(err, os.ErrNotExist) {
			if err := os.Remove(backupPath); err != nil && !errors.Is(err, os.ErrNotExist) {
				restoreErr := restoreWindowsUpgradeBackups(backedUp)
				return errors.Join(
					fmt.Errorf("remove stale backup %s: %w", filepath.Base(replacement.targetPath), err),
					restoreErr,
				)
			}
			backedUp = append(backedUp, replacement)
			continue
		} else if err != nil {
			restoreErr := restoreWindowsUpgradeBackups(backedUp)
			return errors.Join(
				fmt.Errorf("inspect current %s: %w", filepath.Base(replacement.targetPath), err),
				restoreErr,
			)
		}
		if err := moveWindowsFile(replacement.targetPath, backupPath); err != nil {
			restoreErr := restoreWindowsUpgradeBackups(backedUp)
			return errors.Join(
				fmt.Errorf("backup current %s: %w", filepath.Base(replacement.targetPath), err),
				restoreErr,
			)
		}
		backedUp = append(backedUp, replacement)
	}

	for _, replacement := range replacements {
		if err := moveWindowsFile(replacement.targetPath+".next", replacement.targetPath); err != nil {
			restoreErr := restoreWindowsUpgradeBackups(backedUp)
			if restoreErr != nil {
				return fmt.Errorf("install upgraded %s: %w; restore previous bundle: %v", filepath.Base(replacement.targetPath), err, restoreErr)
			}
			return fmt.Errorf("install upgraded %s: %w", filepath.Base(replacement.targetPath), err)
		}
	}
	return nil
}

func restoreWindowsUpgradeBackups(replacements []windowsUpgradeFile) error {
	var restoreErrors []error
	for _, replacement := range replacements {
		backupPath := replacement.targetPath + ".prev"
		if _, err := os.Stat(backupPath); err == nil {
			if err := moveWindowsFile(backupPath, replacement.targetPath); err != nil {
				restoreErrors = append(restoreErrors, fmt.Errorf("restore %s: %w", filepath.Base(replacement.targetPath), err))
			}
		} else if errors.Is(err, os.ErrNotExist) {
			if err := os.Remove(replacement.targetPath); err != nil && !errors.Is(err, os.ErrNotExist) {
				restoreErrors = append(restoreErrors, fmt.Errorf("remove new %s: %w", filepath.Base(replacement.targetPath), err))
			}
		} else {
			restoreErrors = append(restoreErrors, fmt.Errorf("inspect backup %s: %w", filepath.Base(replacement.targetPath), err))
		}
	}
	return errors.Join(restoreErrors...)
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
