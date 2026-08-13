//go:build windows

package processutil

import (
	"os/exec"
	"syscall"

	"golang.org/x/sys/windows"
)

// ConfigureBackgroundCommand prevents console-subsystem children from creating
// a visible console window when they are launched by the background agent.
func ConfigureBackgroundCommand(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.CreationFlags |= windows.CREATE_NO_WINDOW
}
