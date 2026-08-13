//go:build !windows

package agentupgrade

import (
	"context"

	"github.com/gesta-run/gesta-agent/pkg/model"
)

func hookLauncherUpgradeRequired(model.AgentUpgradePolicy) bool {
	return false
}

func stageHookLauncherUpgrade(context.Context, model.AgentUpgradePolicy, string) (replacementArtifact, error) {
	return replacementArtifact{}, nil
}
