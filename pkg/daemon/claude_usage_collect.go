package daemon

import (
	"sort"
	"time"
)

// collectClaudeUsageEvents scans every transcript, applies the baseline filter
// (so repeated cycles / restarts never double count), and returns the usage
// summary events plus the session-index events ready to be wrapped as
// EventEnvelopes by the adapter.
func collectClaudeUsageEvents(cfg Config, projectsDir string, observedAt time.Time) (usageEvents []map[string]interface{}, sessionEvents []map[string]interface{}, meta map[string]interface{}, err error) {
	return collectClaudeUsageEventsFromSessions(cfg, mergedClaudeSessions(projectsDir), observedAt)
}

// mergedClaudeSessions scans every transcript under projectsDir, parses each into
// a per-session usage record, and merges records that share a SessionID (one
// session can span multiple transcript files after a resume). The result is
// sorted by SessionID so downstream output is deterministic. Callers that need
// both usage and output events should load sessions once via this helper and fan
// them out, rather than re-scanning the transcript tree per feature.
func mergedClaudeSessions(projectsDir string) []claudeSessionUsage {
	var sessions []claudeSessionUsage
	for _, path := range findClaudeTranscripts(projectsDir) {
		session, ok := parseClaudeTranscript(path)
		if !ok {
			continue
		}
		sessions = append(sessions, session)
	}
	// Merge per-file results by SessionID before building payloads so usage is
	// summed, not silently collapsed to MAX by the per-bucket baseline.
	sessions = mergeClaudeSessionsByID(sessions)
	sort.Slice(sessions, func(i, j int) bool { return sessions[i].SessionID < sessions[j].SessionID })
	return sessions
}

func collectClaudeUsageEventsFromSessions(cfg Config, sessions []claudeSessionUsage, observedAt time.Time) (usageEvents []map[string]interface{}, sessionEvents []map[string]interface{}, meta map[string]interface{}, err error) {
	if observedAt.IsZero() {
		observedAt = time.Now().UTC()
	}

	var rawUsage []map[string]interface{}
	var rawSessions []map[string]interface{}
	for _, session := range sessions {
		keys := make([]claudeModelDayKey, 0, len(session.ByModelDay))
		for key := range session.ByModelDay {
			keys = append(keys, key)
		}
		sort.Slice(keys, func(i, j int) bool {
			if keys[i].Day != keys[j].Day {
				return keys[i].Day < keys[j].Day
			}
			return keys[i].Model < keys[j].Model
		})
		for _, key := range keys {
			rawUsage = append(rawUsage, claudeUsageSummaryPayload(session, key, session.ByModelDay[key]))
		}
		rawSessions = append(rawSessions, claudeSessionIndexPayload(session))
	}

	filteredUsage, filteredSessions, meta, err := filterClaudeSessionBaseline(cfg, rawUsage, rawSessions, observedAt)
	if err != nil {
		return nil, nil, nil, err
	}
	return filteredUsage, filteredSessions, meta, nil
}
