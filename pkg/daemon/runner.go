package daemon

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/gesta-run/gesta-agent/pkg/activitydetail"
	"github.com/gesta-run/gesta-agent/pkg/agentupgrade"
	"github.com/gesta-run/gesta-agent/pkg/controlclient"
	"github.com/gesta-run/gesta-agent/pkg/eventqueue"
	"github.com/gesta-run/gesta-agent/pkg/model"
	"github.com/gesta-run/gesta-agent/pkg/rulecache"
	"github.com/gesta-run/gesta-agent/pkg/statecleanup"
	turnusage "github.com/gesta-run/gesta-agent/pkg/turn"
)

var ErrUpgradeApplied = errors.New("agent upgrade applied")

const localActivityCleanupInterval = time.Hour

type Runner struct {
	cfg                        Config
	client                     *controlclient.Client
	queue                      eventqueue.Queue
	logger                     *slog.Logger
	applyUpgrade               func(model.HeartbeatResponse) error
	nextLocalActivityCleanupAt time.Time
	legacyQueueReported        bool
	runtimeSettingsSynced      bool
}

func NewRunner(cfg Config) (*Runner, error) {
	if err := cfg.ValidateEnrolled(); err != nil {
		return nil, err
	}
	if cfg.DataDir == "" {
		cfg.DataDir = DefaultDataDir()
	}
	runner := &Runner{
		cfg:    cfg,
		client: controlclient.NewClient(cfg.EffectiveServerURL(), cfg.Token),
		queue:  eventqueue.NewQueue(cfg.DataDir),
		logger: slog.Default(),
	}
	// An older Control does not advertise all-tier turn totals and validates the
	// legacy effective total. Start in the backward-compatible wire mode; a new
	// Control upgrades it through the heartbeat capability before collection.
	runner.cfg.TurnUsageTotal = turnusage.TotalEncodingEffective
	if removedBytes, err := statecleanup.CleanupDeprecatedState(cfg.DataDir); err != nil {
		runner.logger.Warn("deprecated local state cleanup failed", "error", err)
	} else if removedBytes > 0 {
		runner.logger.Info("deprecated local state removed", "bytes", removedBytes)
	}
	return runner, nil
}

func (r *Runner) RunOnce(ctx context.Context) error {
	startedAt := time.Now()
	r.logCollectionStarted()
	r.reportLegacyQueue()
	r.cleanupLocalActivityDetails(startedAt)
	if err := r.ensureRuntimeSettings(); err != nil {
		return err
	}
	if err := r.SyncRules(); err != nil {
		r.logger.Warn("rule sync failed", "error", err)
	}
	adapters, err := r.collectAndQueue(ctx)
	if err != nil {
		return err
	}
	if err := r.flushWithHeartbeat(adapters); err != nil {
		return err
	}
	r.logger.Info("agent collection finished", "elapsed_ms", time.Since(startedAt).Milliseconds(), "queue_size", r.queue.Size())
	return nil
}

func (r *Runner) ensureRuntimeSettings() error {
	if r.runtimeSettingsSynced {
		return nil
	}
	response, err := r.sendHeartbeat("collecting", nil)
	if err != nil {
		r.logger.Warn("initial runtime settings sync failed", "error", err)
		return nil
	}
	return r.applyActionableUpgradeFromHeartbeat(response)
}

func (r *Runner) cleanupLocalActivityDetails(now time.Time) {
	if now.Before(r.nextLocalActivityCleanupAt) {
		return
	}
	r.nextLocalActivityCleanupAt = now.Add(localActivityCleanupInterval)
	if err := activitydetail.NewStore(r.cfg.DataDir).Cleanup(); err != nil {
		r.logger.Warn("local activity detail cleanup failed", "error", err)
	}
}

func (r *Runner) logCollectionStarted() {
	r.logger.Info("agent collection started",
		"daemon_id", r.cfg.DaemonID,
		"control_url", r.cfg.EffectiveServerURL(),
		"data_dir", r.cfg.DataDir,
	)
}

func (r *Runner) collectAndQueue(ctx context.Context) ([]model.AdapterStatus, error) {
	events, adapters, commitAdapters := Collect(ctx, r.cfg)
	r.logger.Info("agent collection completed",
		"events", len(events),
		"event_types", eventTypeCounts(events),
		"adapters", adapterStatusSummary(adapters),
	)
	usageDeltas, commitUsageDeltas, err := BuildUsageDeltaEvents(r.cfg, events, time.Now().UTC())
	if err != nil {
		return nil, fmt.Errorf("build usage deltas: %w", err)
	}
	queueEvents, internalEvents := filterInternalEvents(events)
	queueEvents = append(queueEvents, usageDeltas...)
	if len(usageDeltas) > 0 {
		r.logger.Info("usage deltas prepared", "deltas", len(usageDeltas))
	} else {
		r.logger.Info("usage deltas prepared", "deltas", 0, "note", "first observation may only update cursors")
	}
	if internalEvents > 0 {
		r.logger.Info("internal cursor events filtered", "events", internalEvents)
	}
	queueStats, err := r.queue.AppendWithStats(queueEvents)
	if err != nil {
		return nil, fmt.Errorf("queue events: %w", err)
	}
	r.logger.Info("events queued",
		"events", len(queueEvents),
		"queue_size", queueStats.QueuedEvents,
		"queue_bytes", queueStats.QueuedBytes,
		"oldest_created_at", queueStats.OldestQueuedAt,
		"removed_expired", queueStats.RemovedExpired,
		"removed_duplicate", queueStats.RemovedDuplicate,
		"removed_capacity", queueStats.RemovedCapacity,
		"quarantined_events", queueStats.QuarantinedEvents,
	)
	if commitAdapters != nil {
		if err := commitAdapters(); err != nil {
			return nil, fmt.Errorf("commit adapter cursors: %w", err)
		}
		r.logger.Info("adapter cursors committed")
	}
	if commitUsageDeltas != nil {
		if err := commitUsageDeltas(); err != nil {
			return nil, fmt.Errorf("commit usage deltas: %w", err)
		}
		r.logger.Info("usage cursors committed")
	}
	return adapters, nil
}

func (r *Runner) reportLegacyQueue() {
	if r.legacyQueueReported {
		return
	}
	r.legacyQueueReported = true
	stats, err := r.queue.LegacyStats()
	if err != nil {
		r.logger.Warn("legacy event queue inspection failed", "error", err)
		return
	}
	if stats.Bytes == 0 {
		return
	}
	r.logger.Warn("legacy event queue ignored",
		"events", stats.Events,
		"bytes", stats.Bytes,
		"oldest_created_at", stats.OldestQueuedAt,
		"newest_created_at", stats.NewestQueuedAt,
		"event_types", stats.EventTypes,
	)
}

func (r *Runner) flushWithHeartbeat(adapters []model.AdapterStatus) error {
	if resp, err := r.sendHeartbeat("collecting", adapters); err != nil {
		r.logger.Warn("heartbeat failed", "health", "collecting", "error", err)
	} else {
		r.logger.Info("heartbeat sent", "health", "collecting")
		if err := r.applyActionableUpgradeFromHeartbeat(resp); err != nil {
			return err
		}
	}
	if err := r.Flush(); err != nil {
		queueStats, statsErr := r.queue.Stats()
		if statsErr != nil {
			r.logger.Warn("event flush failed", "error", err, "queue_inspection_error", statsErr)
		} else {
			r.logger.Warn("event flush failed",
				"error", err,
				"queue_size", queueStats.QueuedEvents,
				"queue_bytes", queueStats.QueuedBytes,
				"oldest_created_at", queueStats.OldestQueuedAt,
				"oldest_age", time.Since(queueStats.OldestQueuedAt).Round(time.Second),
			)
		}
		if resp, heartbeatErr := r.sendHeartbeat("degraded", adapters); heartbeatErr != nil {
			r.logger.Warn("heartbeat failed", "health", "degraded", "error", heartbeatErr)
		} else {
			r.logger.Info("heartbeat sent", "health", "degraded")
			if upgradeErr := r.applyActionableUpgradeFromHeartbeat(resp); upgradeErr != nil {
				return upgradeErr
			}
		}
		return err
	}
	resp, err := r.sendHeartbeat("ok", adapters)
	if err != nil {
		return err
	}
	if err := r.handleUpgradeFromHeartbeat(resp); err != nil {
		return err
	}
	return nil
}

func (r *Runner) applyActionableUpgradeFromHeartbeat(resp model.HeartbeatResponse) error {
	if resp.Upgrade == nil {
		return nil
	}
	decision := agentupgrade.DecideAgentUpgrade(*resp.Upgrade, model.DaemonVersion)
	if !decision.ShouldApply {
		return nil
	}
	return r.handleUpgradeFromHeartbeat(resp)
}

func (r *Runner) handleUpgradeFromHeartbeat(resp model.HeartbeatResponse) error {
	if r.applyUpgrade != nil {
		return r.applyUpgrade(resp)
	}
	return r.applyUpgradeFromHeartbeat(resp)
}

func (r *Runner) SyncRules() error {
	var syncErrors []error
	rules, err := r.client.PolicyRules()
	if err != nil {
		syncErrors = append(syncErrors, fmt.Errorf("policy rules: %w", err))
	} else if err := rulecache.SavePolicyCache(r.cfg.DataDir, rules, time.Now().UTC()); err != nil {
		syncErrors = append(syncErrors, fmt.Errorf("save policy rules: %w", err))
	} else {
		r.logger.Info("policy rules synced", "rules", len(rules), "cache", rulecache.PolicyCachePath(r.cfg.DataDir))
	}

	sensitiveRules, err := r.client.SensitiveRules()
	if err != nil {
		r.logger.Warn("sensitive rules sync failed", "error", err)
	} else if err := rulecache.SaveSensitiveRuleCache(r.cfg.DataDir, sensitiveRules, time.Now().UTC()); err != nil {
		syncErrors = append(syncErrors, fmt.Errorf("save sensitive rules: %w", err))
	} else {
		r.logger.Info("sensitive rules synced", "rules", len(sensitiveRules), "cache", rulecache.SensitiveRuleCachePath(r.cfg.DataDir))
	}

	contextRules, err := r.client.ContextRules()
	if err != nil {
		r.logger.Warn("context rules sync failed", "error", err)
	} else if err := rulecache.SaveContextRuleCache(r.cfg.DataDir, contextRules, time.Now().UTC()); err != nil {
		syncErrors = append(syncErrors, fmt.Errorf("save context rules: %w", err))
	} else {
		r.logger.Info("context rules synced", "rules", len(contextRules.Rules), "cache", rulecache.ContextRuleCachePath(r.cfg.DataDir))
	}
	return errors.Join(syncErrors...)
}

func (r *Runner) sendHeartbeat(health string, adapters []model.AdapterStatus) (model.HeartbeatResponse, error) {
	hostname, _ := os.Hostname()
	response, err := r.client.Heartbeat(model.HeartbeatRequest{
		DaemonID:      r.cfg.DaemonID,
		DeviceID:      r.cfg.DeviceID,
		TeamID:        r.cfg.TeamID,
		Hostname:      hostname,
		HostType:      r.cfg.HostType,
		InstallMode:   r.cfg.InstallMode,
		OS:            RuntimeOS(),
		Arch:          RuntimeArch(),
		DaemonVersion: model.DaemonVersion,
		PolicyVersion: r.cfg.PolicyVersion,
		HealthStatus:  health,
		Adapters:      adapters,
	})
	if err != nil {
		return model.HeartbeatResponse{}, err
	}
	r.cfg.DailyWorkTimezone = strings.TrimSpace(response.DailyWorkTimezone)
	r.cfg.TurnUsageTotal = strings.TrimSpace(response.TurnUsageTotal)
	if r.cfg.TurnUsageTotal != turnusage.TotalEncodingAllTier {
		r.cfg.TurnUsageTotal = turnusage.TotalEncodingEffective
	}
	if response.OutputClassification == nil {
		return response, nil
	}
	if response.OutputClassification.Revision <= 0 {
		return model.HeartbeatResponse{}, fmt.Errorf(
			"save output classification settings: revision must be positive",
		)
	}
	if err := rulecache.SaveOutputClassificationCache(r.cfg.DataDir, *response.OutputClassification, time.Now().UTC()); err != nil {
		return model.HeartbeatResponse{}, fmt.Errorf("save output classification settings: %w", err)
	}
	r.runtimeSettingsSynced = true
	return response, nil
}

func (r *Runner) RunLoop(ctx context.Context, interval time.Duration) error {
	if interval <= 0 {
		interval = time.Minute
	}
	r.logger.Info("agent loop started",
		"interval", interval.String(),
		"usage_window", r.cfg.UsageWindow,
		"daemon_id", r.cfg.DaemonID,
		"control_url", r.cfg.EffectiveServerURL(),
	)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		if err := r.RunOnce(ctx); err != nil {
			if errors.Is(err, ErrUpgradeApplied) {
				r.logger.Info("agent loop restarting after upgrade")
				return ErrUpgradeApplied
			}
			r.logger.Error("daemon run once failed", "error", err)
		}
		select {
		case <-ctx.Done():
			r.logger.Info("agent loop stopped", "reason", ctx.Err())
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func (r *Runner) Flush() error {
	stats, err := r.queue.Stats()
	if err != nil {
		return fmt.Errorf("inspect event queue: %w", err)
	}
	if stats.QueuedEvents == 0 {
		r.logger.Info("event queue empty", "quarantined_events", stats.QuarantinedEvents)
		return nil
	}
	r.logger.Info("flushing event queue",
		"queued_events", stats.QueuedEvents,
		"queue_bytes", stats.QueuedBytes,
		"quarantined_events", stats.QuarantinedEvents,
		"oldest_created_at", stats.OldestQueuedAt,
		"oldest_age", time.Since(stats.OldestQueuedAt).Round(time.Second),
	)
	startedAt := time.Now()
	err = r.queue.Drain(func(events []model.EventEnvelope) error {
		return r.client.SendEventsForDaemon(events, r.cfg.DaemonID, r.cfg.DeviceID)
	})
	if err != nil {
		return err
	}
	r.logger.Info("event queue flushed", "sent_events", stats.QueuedEvents, "elapsed_ms", time.Since(startedAt).Milliseconds())
	return nil
}

func eventTypeCounts(events []model.EventEnvelope) map[string]int {
	counts := make(map[string]int)
	for _, event := range events {
		eventType := event.EventType
		if eventType == "" {
			eventType = "unknown"
		}
		counts[eventType]++
	}
	return counts
}

func adapterStatusSummary(adapters []model.AdapterStatus) []string {
	summary := make([]string, 0, len(adapters))
	for _, adapter := range adapters {
		name := adapter.Name
		if name == "" {
			name = "unknown"
		}
		status := adapter.Status
		if status == "" {
			status = "unknown"
		}
		summary = append(summary, name+"="+status)
	}
	sort.Strings(summary)
	return summary
}
