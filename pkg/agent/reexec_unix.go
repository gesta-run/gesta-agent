//go:build !windows

package agent

import (
	"fmt"
	"os"
	"syscall"
)

func reexecAgent() error {
	agentPath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolve upgraded agent executable: %w", err)
	}
	return syscall.Exec(agentPath, os.Args, os.Environ())
}
