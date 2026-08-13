//go:build !windows

package processutil

import "os/exec"

func ConfigureBackgroundCommand(*exec.Cmd) {}
