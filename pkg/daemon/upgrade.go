package daemon

import (
	"path/filepath"
	"time"

	"github.com/gesta-run/gesta-agent/pkg/agentupgrade"
	"github.com/gesta-run/gesta-agent/pkg/model"
)

const upgradeFailureRetryAfter = time.Hour

func (r *Runner) applyUpgradeFromHeartbeat(resp model.HeartbeatResponse) error {
	if resp.Upgrade == nil {
		return nil
	}
	decision := agentupgrade.DecideAgentUpgrade(*resp.Upgrade, model.DaemonVersion)
	if decision.Mode == "off" {
		return nil
	}
	if decision.Mode == "notify" {
		r.logger.Info("agent upgrade available",
			"target_version", resp.Upgrade.TargetVersion,
			"current_version", model.DaemonVersion,
			"reason", decision.Reason,
		)
		return nil
	}
	if !decision.ShouldApply {
		r.logger.Info("agent upgrade skipped",
			"mode", decision.Mode,
			"target_version", resp.Upgrade.TargetVersion,
			"current_version", model.DaemonVersion,
			"reason", decision.Reason,
		)
		return nil
	}
	if agentupgrade.AutoUpdateDisabled() {
		r.logger.Warn("agent auto-upgrade disabled locally",
			"target_version", resp.Upgrade.TargetVersion,
			"env", "GESTA_AGENT_AUTO_UPDATE",
		)
		return nil
	}
	statePath := filepath.Join(r.cfg.DataDir, "upgrade-state.json")
	state, _ := agentupgrade.LoadUpgradeState(statePath)
	now := time.Now().UTC()
	if state.State == "failed" &&
		state.TargetVersion == resp.Upgrade.TargetVersion &&
		!state.LastAttemptAt.IsZero() &&
		now.Sub(state.LastAttemptAt) < upgradeFailureRetryAfter {
		r.logger.Warn("agent upgrade retry suppressed",
			"target_version", resp.Upgrade.TargetVersion,
			"retry_after", upgradeFailureRetryAfter.String(),
			"error", state.Error,
		)
		return nil
	}

	state.Enabled = true
	state.TargetVersion = resp.Upgrade.TargetVersion
	state.State = "running"
	state.Error = ""
	state.LastCheckedAt = now
	state.LastAttemptAt = now
	_ = agentupgrade.SaveUpgradeState(statePath, state)

	r.logger.Info("agent upgrade started",
		"target_version", resp.Upgrade.TargetVersion,
		"current_version", model.DaemonVersion,
		"url", resp.Upgrade.URL,
	)
	if err := agentupgrade.ApplyAgentUpgrade(*resp.Upgrade); err != nil {
		state.State = "failed"
		state.Error = err.Error()
		_ = agentupgrade.SaveUpgradeState(statePath, state)
		r.logger.Error("agent upgrade failed", "target_version", resp.Upgrade.TargetVersion, "error", err)
		return nil
	}
	state.State = "succeeded"
	state.Error = ""
	state.LastSucceededAt = time.Now().UTC()
	_ = agentupgrade.SaveUpgradeState(statePath, state)
	r.logger.Info("agent upgrade applied", "target_version", resp.Upgrade.TargetVersion)
	return ErrUpgradeApplied
}
