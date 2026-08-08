package daemon

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/gesta-run/gesta-agent/pkg/model"
	"github.com/gesta-run/gesta-agent/pkg/privacy"
	turnusage "github.com/gesta-run/gesta-agent/pkg/turn"
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
	var events []model.EventEnvelope
	var commits []func() error
	turnEvents, turnCommit, turnErr := turnusage.CollectClaude(turnusage.Config{
		DataDir: cfg.DataDir, DaemonID: cfg.DaemonID, TotalEncoding: cfg.TurnUsageTotal,
	}, claudeTurnSessions(sessions), observedAt)
	if turnErr != nil {
		events = append(events, snapshotEvent(cfg, "adapter.warning", claudeCodeUsageSource, claudeCodeAgentType, map[string]interface{}{
			"scope": "turn_usage", "error": privacy.RedactAndTruncate(turnErr.Error(), 2048),
		}))
	} else {
		for _, usage := range turnEvents {
			event := baseEvent(cfg, turnusage.EventType, claudeCodeUsageSource, claudeCodeAgentType, usage.Payload())
			event.EventID = usage.EventID
			event.CreatedAt = usage.EndedAt
			events = append(events, event)
		}
		if turnCommit != nil {
			commits = append(commits, turnCommit)
		}
	}
	collection, err := collectClaudeEventsFromSessions(cfg, sessions, observedAt)
	if err != nil {
		events = append(events, snapshotEvent(cfg, "adapter.warning", claudeCodeUsageSource, claudeCodeAgentType, map[string]interface{}{
			"scope": "session_baseline",
			"error": privacy.RedactAndTruncate(err.Error(), 2048),
		}))
		return events, combineAdapterCommits(commits)
	}
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
	if collection.Commit != nil {
		commits = append(commits, collection.Commit)
	}
	return events, combineAdapterCommits(commits)
}

func claudeTurnSessions(sessions []claudeSessionUsage) []turnusage.ClaudeSession {
	out := make([]turnusage.ClaudeSession, 0, len(sessions))
	for _, session := range sessions {
		converted := turnusage.ClaudeSession{
			SessionIDHash: util.ShortHash(session.SessionID),
			FirstEventAt:  session.FirstEventAt,
			Turns:         make([]turnusage.ClaudeTurn, 0, len(session.Turns)),
		}
		for _, turn := range session.Turns {
			converted.Turns = append(converted.Turns, turnusage.ClaudeTurn{
				TurnID: turn.TurnID, Status: turn.Status, StartedAt: turn.StartedAt, EndedAt: turn.EndedAt,
				Model: turn.Model, Repo: claudeRepoHash(session.CWD), ModelProvider: "anthropic",
				Tokens: turnusage.TokenTotals{
					Input: turn.Usage.InputTokens, Output: turn.Usage.OutputTokens,
					CacheRead: turn.Usage.CacheReadTokens, CacheWrite: turn.Usage.CacheCreationTokens,
				},
				Evidence: turn.Evidence,
			})
		}
		out = append(out, converted)
	}
	return out
}

func dirEntryCount(dir string) int {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	return len(entries)
}
