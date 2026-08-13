//go:build windows

package processutil

import (
	"os/exec"
	"testing"

	"golang.org/x/sys/windows"
)

func TestConfigureBackgroundCommandDisablesConsoleWindow(t *testing.T) {
	cmd := exec.Command("cmd.exe", "/c", "exit", "0")
	ConfigureBackgroundCommand(cmd)
	if cmd.SysProcAttr == nil || cmd.SysProcAttr.CreationFlags&windows.CREATE_NO_WINDOW == 0 {
		t.Fatalf("background command creation flags = %#v, want CREATE_NO_WINDOW", cmd.SysProcAttr)
	}
}
