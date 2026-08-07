//go:build windows

package agent

import "errors"

func reexecAgent() error {
	return errors.New("automatic upgrades are not supported on Windows RC; rerun the current Connect command")
}
