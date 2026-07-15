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

	// Load the merged sessions once, then fan out to the usage/session-index
	// events and the output.summary events so the transcript tree is parsed a
	// single time per collection cycle. activeSessions is the set of sessions the
	// baseline filter judged to have new activity this cycle; output summaries are
	// gated on it so dormant historical sessions are not re-diffed (see
	// claudeOutputSummaryEvents).
	sessions := mergedClaudeSessions(claudeProjectsDir())
	usageEvents, activeSessions := claudeUsageEventsFromSessions(cfg, sessions, time.Now().UTC())
	events = append(events, usageEvents...)
	events = append(events, claudeOutputSummaryEvents(ctx, cfg, sessions, activeSessions)...)

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

// claudeUsageEventsFromSessions returns the usage/session-index events plus the
// set of hashed session ids the baseline filter judged active this cycle (i.e.
// the sessions whose transcripts advanced). The active set gates output.summary
// emission so dormant sessions are not re-diffed against a shared worktree.
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

// claudeOutputSummaryEvents diffs each Claude Code session's worktree against the
// baseline captured by the hook (see CaptureOutputBaseline) and emits one
// output.summary event per session that produced measurable output. This mirrors
// the Codex path in codex.go — without it, Claude Code sessions capture baselines
// but never emit the delta, so the "Output produced" ledger stays empty.
//
// Only sessions in activeSessions (hashed ids the baseline filter judged to have
// advanced this cycle) are considered. This matches Codex's "new activity only"
// semantics: diffing every historical transcript each cycle would re-run git
// subprocesses unboundedly and, worse, mis-attribute a repo's current diff to
// dormant sessions sharing that worktree — a stale session whose baseline has
// been pruned falls through to the HEAD-diff fallback and is otherwise credited
// the entire uncommitted diff, double-counting output across sessions.
//
// The session id is hashed to match both the baseline key and the session-index
// event; the daemon's output cursor (FilterOutputSummaryEvents) dedups repeated
// cycles, and sessions outside a git repo or with no delta are skipped.
func claudeOutputSummaryEvents(ctx context.Context, cfg Config, sessions []claudeSessionUsage, activeSessions map[string]bool) []model.EventEnvelope {
	var events []model.EventEnvelope
	for _, session := range sessions {
		if session.CWD == "" {
			continue
		}
		sessionHash := util.ShortHash(session.SessionID)
		if !activeSessions[sessionHash] {
			continue
		}
		primaryModel := ""
		if len(session.Models) > 0 {
			primaryModel = session.Models[0]
		}
		event, ok := outputSummaryEvent(ctx, cfg, claudeCodeAgentType, sessionHash, session.CWD, session.FirstEventAt, session.Title, primaryModel)
		if ok {
			events = append(events, event)
		}
	}
	return events
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
