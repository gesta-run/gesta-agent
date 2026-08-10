//go:build !windows

package agentupgrade

import "errors"

const HelperCommand = "__upgrade-helper"

func RunUpgradeHelper([]string) error {
	return errors.New("upgrade helper is only available on Windows")
}
