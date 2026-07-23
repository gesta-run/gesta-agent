package daemon

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/gesta-run/gesta-agent/pkg/model"
	"github.com/gesta-run/gesta-agent/pkg/privacy"
	"github.com/gesta-run/gesta-agent/pkg/util"
)

type ClaudeCodeAdapter struct{}

func (ClaudeCodeAdapter) Name() string { return "claude_code" }

func (a ClaudeCodeAdapter) Collect(ctx context.Context, cfg Config) (AdapterResult, []model.EventEnvelope) {
	now := time.Now().UTC().Format(time.RFC3339)
	path, err := exec.LookPath("claude")
	if err != nil {
		return AdapterResult{Status: model.AdapterStatus{Name: a.Name(), Detected: false, Status: "not_found", UpdatedAt: now}}, nil
	}
	version := safeVersion(ctx, "claude", "--version")
	status := model.AdapterStatus{Name: a.Name(), Detected: true, Version: version, Status: "ok", UpdatedAt: now}
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

	if mcpOutput, err := commandOutput(ctx, "claude", "mcp", "list"); err == nil {
		payload := commandOutputMetadata(mcpOutput)
		servers := mcpServersFromListOutput(mcpOutput)
		payload["command"] = "claude mcp list"
		payload["servers"] = servers
		payload["server_count"] = len(servers)
		events = append(events, snapshotEvent(cfg, "mcp.inventory", "claude_code", "claude_code", payload))
	} else {
		events = append(events, snapshotEvent(cfg, "adapter.warning", "claude_code", "claude_code", map[string]interface{}{
			"message": "claude mcp list is not available or did not complete; deep event stream support must be validated separately",
			"error":   privacy.RedactAndTruncate(err.Error(), 2048),
		}))
	}

	sessions := mergedClaudeSessions(claudeProjectsDir())
	usageEvents, _ := claudeUsageEventsFromSessions(cfg, sessions, time.Now().UTC())
	events = append(events, usageEvents...)

	return AdapterResult{Status: status}, events
}

// claudeUsageEvents scans the Claude Code transcripts under projectsDir, applies
// the per-session baseline so repeated cycles never double count, and returns the
// usage.summary + agent_sessions session-index events. usage.summary events drive
// the generic usage-delta machinery (BuildUsageDeltaEvents) and the control
// plane's usage_events / agent_sessions tables, matching the shapes Codex emits.
func claudeUsageEvents(cfg Config, projectsDir string, observedAt time.Time) []model.EventEnvelope {
	if projectsDir == "" {
		return nil
	}
	events, _ := claudeUsageEventsFromSessions(cfg, mergedClaudeSessions(projectsDir), observedAt)
	return events
}

// claudeUsageEventsFromSessions returns usage/session-index events plus the set
// of hashed session ids whose transcripts advanced in this collection cycle.
func claudeUsageEventsFromSessions(cfg Config, sessions []claudeSessionUsage, observedAt time.Time) ([]model.EventEnvelope, map[string]bool) {
	usagePayloads, sessionPayloads, meta, err := collectClaudeUsageEventsFromSessions(cfg, sessions, observedAt)
	if err != nil {
		return []model.EventEnvelope{
			snapshotEvent(cfg, "adapter.warning", claudeCodeUsageSource, claudeCodeAgentType, map[string]interface{}{
				"scope": "session_baseline",
				"error": privacy.RedactAndTruncate(err.Error(), 2048),
			}),
		}, nil
	}
	var events []model.EventEnvelope
	if len(meta) > 0 {
		events = append(events, snapshotEvent(cfg, "claude_code.usage_summary", claudeCodeUsageSource, claudeCodeAgentType, meta))
	}
	for _, payload := range usagePayloads {
		events = append(events, baseEvent(cfg, "usage.summary", claudeCodeUsageSource, claudeCodeAgentType, payload))
	}
	active := make(map[string]bool, len(sessionPayloads))
	for _, payload := range sessionPayloads {
		event := baseEvent(cfg, "session.transcript", claudeCodeUsageSource, claudeCodeAgentType, payload)
		event.EventID = claudeSessionEventID(payload)
		events = append(events, event)
		if id := firstPayloadString(payload, "session_id", "session_id_hash"); id != "" {
			active[id] = true
		}
	}
	return events, active
}

func claudeSessionEventID(payload map[string]interface{}) string {
	parts := []string{
		"claude_code.session",
		firstPayloadString(payload, "session_id", "session_id_hash"),
		firstPayloadString(payload, "updated_at"),
		firstPayloadString(payload, "transcript_hash"),
	}
	return "evt_" + util.ShortHash(strings.Join(parts, "\x00"))
}

func dirEntryCount(dir string) int {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	return len(entries)
}
