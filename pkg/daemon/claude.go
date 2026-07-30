package daemon

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/gesta-run/gesta-agent/pkg/model"
	"github.com/gesta-run/gesta-agent/pkg/privacy"
	"github.com/gesta-run/gesta-agent/pkg/util"
)

type ClaudeCodeAdapter struct{}

func (ClaudeCodeAdapter) Name() string { return "claude_code" }

func (a ClaudeCodeAdapter) Collect(ctx context.Context, cfg Config) (AdapterResult, []model.EventEnvelope) {
	observedAt := time.Now().UTC()
	now := observedAt.Format(time.RFC3339)
	path, err := exec.LookPath("claude")
	if err != nil {
		return AdapterResult{Status: model.AdapterStatus{
			Name: a.Name(), Detected: false, Status: "not_found", UpdatedAt: now,
			MCPInventory: unsupportedMCPInventory(observedAt),
		}}, nil
	}
	version := safeVersion(ctx, "claude", "--version")
	status := model.AdapterStatus{
		Name: a.Name(), Detected: true, Version: version, Status: "ok", UpdatedAt: now,
		MCPInventory: unsupportedMCPInventory(observedAt),
	}
	events := []model.EventEnvelope{
		snapshotEvent(cfg, "agent.discovery", "daemon", "claude_code", map[string]interface{}{
			"binary_path_hash": util.ShortHash(path),
			"version":          version,
		}),
	}

	home, _ := os.UserHomeDir()
	claudeDir := filepath.Join(home, ".claude")
	if info, err := os.Stat(claudeDir); err == nil && info.IsDir() {
		events = append(events, snapshotEvent(cfg, "claude_code.config_snapshot", "claude_code", "claude_code", map[string]interface{}{
			"config_dir_present": true,
			"config_dir_hash":    util.ShortHash(claudeDir),
			"config_entry_count": dirEntryCount(claudeDir),
			"metadata_only":      true,
		}))
	}

	status.MCPInventory = claudeMCPInventory(claudeConfigPath(), observedAt)

	sessions := mergedClaudeSessions(claudeProjectsDir())
	adapterEvents, commit := claudeEventsFromSessions(cfg, sessions, observedAt)
	events = append(events, adapterEvents...)

	return AdapterResult{Status: status, Commit: commit}, events
}

func claudeEventsFromSessions(
	cfg Config,
	sessions []claudeSessionUsage,
	observedAt time.Time,
) ([]model.EventEnvelope, func() error) {
	collection, err := collectClaudeEventsFromSessions(cfg, sessions, observedAt)
	if err != nil {
		return []model.EventEnvelope{
			snapshotEvent(cfg, "adapter.warning", claudeCodeUsageSource, claudeCodeAgentType, map[string]interface{}{
				"scope": "session_baseline",
				"error": privacy.RedactAndTruncate(err.Error(), 2048),
			}),
		}, nil
	}
	var events []model.EventEnvelope
	if len(collection.Meta) > 0 {
		events = append(events, snapshotEvent(cfg, "claude_code.usage_summary", claudeCodeUsageSource, claudeCodeAgentType, collection.Meta))
	}
	for _, payload := range collection.UsageEvents {
		events = append(events, baseEvent(cfg, "usage.summary", claudeCodeUsageSource, claudeCodeAgentType, payload))
	}
	for _, payload := range collection.SessionEvents {
		event := baseEvent(cfg, transcriptChunkEventType, claudeCodeUsageSource, claudeCodeAgentType, payload)
		event.EventID = transcriptChunkEventID(payload)
		events = append(events, event)
	}
	events = append(events, collection.MCPEvents...)
	return events, collection.Commit
}

func dirEntryCount(dir string) int {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	return len(entries)
}
