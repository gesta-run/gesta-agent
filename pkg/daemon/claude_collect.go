package daemon

import (
	"sort"
	"time"

	"github.com/gesta-run/gesta-agent/pkg/model"
)

// collectClaudeEvents scans every transcript once, builds usage/session/MCP
// observations, and returns an after-queue baseline commit.
func collectClaudeEvents(cfg Config, projectsDir string, observedAt time.Time) (claudeBaselineResult, error) {
	return collectClaudeEventsFromSessions(cfg, mergedClaudeSessions(projectsDir), observedAt)
}

// mergedClaudeSessions scans every transcript under projectsDir, parses each
// into a per-session record, and merges records that share a SessionID.
func mergedClaudeSessions(projectsDir string) []claudeSessionUsage {
	var sessions []claudeSessionUsage
	for _, path := range findClaudeTranscripts(projectsDir) {
		session, ok := parseClaudeTranscript(path)
		if !ok {
			continue
		}
		sessions = append(sessions, session)
	}
	sessions = mergeClaudeSessionsByID(sessions)
	sort.Slice(sessions, func(i, j int) bool { return sessions[i].SessionID < sessions[j].SessionID })
	return sessions
}

func collectClaudeEventsFromSessions(
	cfg Config,
	sessions []claudeSessionUsage,
	observedAt time.Time,
) (claudeBaselineResult, error) {
	if observedAt.IsZero() {
		observedAt = time.Now().UTC()
	}

	var rawUsage []map[string]interface{}
	var rawSessions []map[string]interface{}
	var rawMCPEvents []model.EventEnvelope
	for _, session := range sessions {
		keys := sortedClaudeModelDayKeys(session.ByModelDay)
		for _, key := range keys {
			rawUsage = append(rawUsage, claudeUsageSummaryPayload(session, key, session.ByModelDay[key]))
		}
		rawSessions = append(rawSessions, claudeSessionIndexPayload(session))
		rawMCPEvents = append(rawMCPEvents, claudeMCPToolCallEvents(cfg, session)...)
	}
	return filterClaudeSessionBaseline(cfg, rawUsage, rawSessions, rawMCPEvents, observedAt)
}

func sortedClaudeModelDayKeys(byModelDay map[claudeModelDayKey]claudeAssistantUsage) []claudeModelDayKey {
	keys := make([]claudeModelDayKey, 0, len(byModelDay))
	for key := range byModelDay {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].Day != keys[j].Day {
			return keys[i].Day < keys[j].Day
		}
		return keys[i].Model < keys[j].Model
	})
	return keys
}
