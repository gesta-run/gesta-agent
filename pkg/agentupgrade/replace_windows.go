//go:build windows

package agentupgrade

import "errors"

func replaceAgentBinary(_, _ string) error {
	return errors.New("automatic upgrades are not supported on Windows RC; rerun the current Connect command")
}

func AutomaticUpgradeSupported() bool {
	return false
}
