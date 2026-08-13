//go:build windows

package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"unsafe"

	"github.com/gesta-run/gesta-agent/pkg/processutil"
	"golang.org/x/sys/windows"
)

func main() {
	if len(os.Args) < 2 || (os.Args[1] != "codex-hook" && os.Args[1] != "claude-hook") {
		fmt.Fprintln(os.Stderr, "gesta-agent-hook-launcher: expected codex-hook or claude-hook")
		os.Exit(2)
	}
	if err := runHook(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			os.Exit(exitErr.ExitCode())
		}
		fmt.Fprintf(os.Stderr, "gesta-agent-hook-launcher: %v\n", err)
		os.Exit(1)
	}
}

func runHook() error {
	launcherPath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolve executable: %w", err)
	}
	job, err := createKillOnCloseJob()
	if err != nil {
		return fmt.Errorf("create child job: %w", err)
	}
	defer windows.CloseHandle(job)

	agentPath := filepath.Join(filepath.Dir(launcherPath), "gesta-agent.exe")
	cmd := exec.Command(agentPath, os.Args[1:]...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	processutil.ConfigureBackgroundCommand(cmd)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start agent: %w", err)
	}
	process, err := windows.OpenProcess(
		windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE,
		false,
		uint32(cmd.Process.Pid),
	)
	if errors.Is(err, windows.ERROR_INVALID_PARAMETER) {
		// Very short hooks can finish before the launcher opens their process
		// handle. Preserve their real exit status instead of reporting a
		// launcher failure.
		return cmd.Wait()
	}
	if err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return fmt.Errorf("open child process: %w", err)
	}
	assignErr := windows.AssignProcessToJobObject(job, process)
	_ = windows.CloseHandle(process)
	if assignErr != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return fmt.Errorf("assign child process to job: %w", assignErr)
	}
	return cmd.Wait()
}

func createKillOnCloseJob() (windows.Handle, error) {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return 0, err
	}
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err := windows.SetInformationJobObject(
		job,
		windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)),
		uint32(unsafe.Sizeof(info)),
	); err != nil {
		_ = windows.CloseHandle(job)
		return 0, err
	}
	return job, nil
}
