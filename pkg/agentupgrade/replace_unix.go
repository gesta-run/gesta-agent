//go:build !windows

package agentupgrade

import (
	"fmt"
	"os"
	"syscall"
)

func replaceAgentBinary(tmpPath, targetPath string) error {
	info, err := os.Stat(targetPath)
	if err != nil {
		return fmt.Errorf("stat current agent binary: %w", err)
	}
	mode := info.Mode().Perm()
	if mode == 0 {
		mode = 0o755
	}
	backupPath := targetPath + ".prev"
	_ = os.Remove(backupPath)
	if err := os.Rename(targetPath, backupPath); err != nil {
		return fmt.Errorf("backup current agent binary: %w; automatic upgrades need write access to %s, rerun the installer once with sudo to normalize ownership", err, targetPath)
	}
	if err := os.Rename(tmpPath, targetPath); err != nil {
		_ = os.Rename(backupPath, targetPath)
		return fmt.Errorf("install upgraded agent binary: %w", err)
	}
	if os.Geteuid() == 0 {
		if stat, ok := info.Sys().(*syscall.Stat_t); ok {
			if err := os.Chown(targetPath, int(stat.Uid), int(stat.Gid)); err != nil {
				return fmt.Errorf("preserve upgraded agent ownership: %w", err)
			}
		}
	}
	return os.Chmod(targetPath, mode|0o111)
}

func AutomaticUpgradeSupported() bool {
	return true
}
