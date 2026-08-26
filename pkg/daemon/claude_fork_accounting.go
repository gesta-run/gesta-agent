package daemon

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/gesta-run/gesta-agent/pkg/atomicfile"
	"github.com/gesta-run/gesta-agent/pkg/util"
)

const (
	claudeForkAccountingFile   = "claude-fork-accounting-v2.json"
	claudeTokenAccounting      = "claude_code_by_model_day_fork_dedup_v2"
	claudeForkAccountingSchema = 2
)

type claudeForkAccountingStore struct {
	Version          int                                         `json:"version"`
	InitializedAt    string                                      `json:"initialized_at"`
	NextSessionRank  uint64                                      `json:"next_session_rank"`
	SessionRanks     map[string]uint64                           `json:"session_ranks"`
	ActiveSessions   map[string]bool                             `json:"active_sessions"`
	InheritedOffsets map[string]map[string]claudeForkUsageOffset `json:"inherited_offsets"`
}

type claudeForkUsageOffset struct {
	InputTokens         int64 `json:"input_tokens,omitempty"`
	OutputTokens        int64 `json:"output_tokens,omitempty"`
	CacheCreationTokens int64 `json:"cache_creation_tokens,omitempty"`
	CacheReadTokens     int64 `json:"cache_read_tokens,omitempty"`
}

func prepareClaudeForkAccounting(
	dataDir string,
	sessions []claudeSessionUsage,
	observedAt time.Time,
) ([]claudeSessionUsage, func() error, error) {
	store, err := loadClaudeForkAccountingStore(dataDir)
	if err != nil {
		return nil, nil, err
	}
	cutover, err := claudeAccountingNeedsCutover(dataDir, store.Version)
	if err != nil {
		return nil, nil, err
	}

	next := cloneClaudeForkAccountingStore(store)
	next.Version = claudeForkAccountingSchema
	if next.InitializedAt == "" {
		next.InitializedAt = observedAt.UTC().Format(time.RFC3339Nano)
	}
	next.ActiveSessions = map[string]bool{}
	assignClaudeSessionRanks(&next, sessions)

	invocations, turns, mcpCalls := claudeAccountingOccurrences(sessions)
	invocationOwners := resolveClaudeAccountingOwners(invocations, next.SessionRanks)
	turnOwners := resolveClaudeAccountingOwners(turns, next.SessionRanks)
	mcpCallOwners := resolveClaudeAccountingOwners(mcpCalls, next.SessionRanks)
	next.InheritedOffsets = nextClaudeInheritedOffsets(store.InheritedOffsets, sessions, invocationOwners)

	initializedAt, _ := time.Parse(time.RFC3339Nano, store.InitializedAt)
	accounted := make([]claudeSessionUsage, 0, len(sessions))
	for _, session := range sessions {
		sessionID := util.ShortHash(session.SessionID)
		next.ActiveSessions[sessionID] = true
		filtered := session
		applyClaudeInheritedOffsets(&filtered, next.InheritedOffsets[sessionID])
		filtered.Turns = markClaudeInheritedTurns(session.Turns, sessionID, turnOwners)
		filtered.MCPToolCalls = markClaudeInheritedMCPCalls(session.MCPToolCalls, sessionID, mcpCallOwners)
		filtered.AccountingFirstEventAt = firstClaudeAccountedTurnAt(filtered.Turns)
		newHistoricalSession := !store.ActiveSessions[sessionID] &&
			!initializedAt.IsZero() &&
			!filtered.AccountingFirstEventAt.IsZero() &&
			filtered.AccountingFirstEventAt.Before(initializedAt)
		filtered.AccountingSeedOnly = cutover || newHistoricalSession
		accounted = append(accounted, filtered)
	}
	if reflect.DeepEqual(store, next) {
		return accounted, nil, nil
	}
	return accounted, func() error { return saveClaudeForkAccountingStore(dataDir, next) }, nil
}

func claudeAccountingNeedsCutover(dataDir string, version int) (bool, error) {
	if version == claudeForkAccountingSchema {
		return false, nil
	}
	baseline, err := loadSessionBaselineStore(dataDir)
	if err != nil {
		return false, err
	}
	return len(baseline.ClaudeCode.Sessions) > 0, nil
}

func assignClaudeSessionRanks(store *claudeForkAccountingStore, sessions []claudeSessionUsage) {
	ids := make([]string, 0, len(sessions))
	for _, session := range sessions {
		id := util.ShortHash(session.SessionID)
		if _, exists := store.SessionRanks[id]; !exists {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	for _, id := range ids {
		store.NextSessionRank++
		store.SessionRanks[id] = store.NextSessionRank
	}
}

func claudeAccountingOccurrences(sessions []claudeSessionUsage) (
	map[string]map[string]struct{},
	map[string]map[string]struct{},
	map[string]map[string]struct{},
) {
	invocations := map[string]map[string]struct{}{}
	turns := map[string]map[string]struct{}{}
	mcpCalls := map[string]map[string]struct{}{}
	for _, session := range sessions {
		sessionID := util.ShortHash(session.SessionID)
		for _, invocation := range session.Invocations {
			addClaudeAccountingOccurrence(invocations, util.HashString(invocation.InvocationID), sessionID)
		}
		for _, turn := range session.Turns {
			addClaudeAccountingOccurrence(turns, util.HashString(turn.TurnID), sessionID)
		}
		for _, call := range session.MCPToolCalls {
			addClaudeAccountingOccurrence(mcpCalls, util.HashString(claudeMCPToolCallKey(call)), sessionID)
		}
	}
	return invocations, turns, mcpCalls
}

func addClaudeAccountingOccurrence(occurrences map[string]map[string]struct{}, identity, sessionID string) {
	identity = strings.TrimSpace(identity)
	sessionID = strings.TrimSpace(sessionID)
	if identity == "" || sessionID == "" {
		return
	}
	if occurrences[identity] == nil {
		occurrences[identity] = map[string]struct{}{}
	}
	occurrences[identity][sessionID] = struct{}{}
}

func resolveClaudeAccountingOwners(
	occurrences map[string]map[string]struct{},
	sessionRanks map[string]uint64,
) map[string]string {
	owners := map[string]string{}
	for identity, sessions := range occurrences {
		if len(sessions) < 2 {
			continue
		}
		for sessionID := range sessions {
			owner := owners[identity]
			if owner == "" || sessionRanks[sessionID] < sessionRanks[owner] ||
				(sessionRanks[sessionID] == sessionRanks[owner] && sessionID < owner) {
				owners[identity] = sessionID
			}
		}
	}
	return owners
}

func nextClaudeInheritedOffsets(
	stored map[string]map[string]claudeForkUsageOffset,
	sessions []claudeSessionUsage,
	owners map[string]string,
) map[string]map[string]claudeForkUsageOffset {
	next := map[string]map[string]claudeForkUsageOffset{}
	for _, session := range sessions {
		sessionID := util.ShortHash(session.SessionID)
		observed := map[string]claudeForkUsageOffset{}
		for _, invocation := range session.Invocations {
			owner := owners[util.HashString(invocation.InvocationID)]
			if owner == "" || owner == sessionID {
				continue
			}
			key := claudeAccountingBucketKey(invocation.Model, invocation.ObservedAt)
			observed[key] = addClaudeForkUsageOffset(observed[key], invocation.Usage)
		}
		next[sessionID] = mergeClaudeUsageOffsets(stored[sessionID], observed)
		if len(next[sessionID]) == 0 {
			delete(next, sessionID)
		}
	}
	return next
}

func applyClaudeInheritedOffsets(session *claudeSessionUsage, offsets map[string]claudeForkUsageOffset) {
	if len(offsets) == 0 {
		return
	}
	byModelDay := make(map[claudeModelDayKey]claudeAssistantUsage, len(session.ByModelDay))
	total := claudeAssistantUsage{}
	for key, usage := range session.ByModelDay {
		offset := offsets[claudeAccountingBucketKeyFromDay(key.Model, key.Day)]
		usage = subtractClaudeForkUsageOffset(usage, offset)
		if !usage.isZero() {
			byModelDay[key] = usage
			total = total.add(usage)
		}
	}
	session.ByModelDay = byModelDay
	session.Total = total
}

func markClaudeInheritedTurns(turns []claudeTurnUsage, sessionID string, owners map[string]string) []claudeTurnUsage {
	marked := append([]claudeTurnUsage(nil), turns...)
	for i := range marked {
		owner := owners[util.HashString(marked[i].TurnID)]
		marked[i].AccountingInherited = owner != "" && owner != sessionID
	}
	return marked
}

func markClaudeInheritedMCPCalls(
	calls []claudeTranscriptToolCall,
	sessionID string,
	owners map[string]string,
) []claudeTranscriptToolCall {
	marked := append([]claudeTranscriptToolCall(nil), calls...)
	for i := range marked {
		owner := owners[util.HashString(claudeMCPToolCallKey(marked[i]))]
		marked[i].AccountingInherited = owner != "" && owner != sessionID
	}
	return marked
}

func firstClaudeAccountedTurnAt(turns []claudeTurnUsage) time.Time {
	first := time.Time{}
	for _, turn := range turns {
		if turn.AccountingInherited || turn.StartedAt.IsZero() {
			continue
		}
		if first.IsZero() || turn.StartedAt.Before(first) {
			first = turn.StartedAt
		}
	}
	return first
}

func claudeAccountingBucketKey(model string, observedAt time.Time) string {
	day := ""
	if !observedAt.IsZero() {
		day = observedAt.UTC().Format("2006-01-02")
	}
	return claudeAccountingBucketKeyFromDay(model, day)
}

func claudeAccountingBucketKeyFromDay(model, day string) string {
	return util.HashString(strings.Join([]string{model, day}, "\x00"))
}

func addClaudeForkUsageOffset(offset claudeForkUsageOffset, usage claudeAssistantUsage) claudeForkUsageOffset {
	offset.InputTokens += usage.InputTokens
	offset.OutputTokens += usage.OutputTokens
	offset.CacheCreationTokens += usage.CacheCreationTokens
	offset.CacheReadTokens += usage.CacheReadTokens
	return offset
}

func subtractClaudeForkUsageOffset(usage claudeAssistantUsage, offset claudeForkUsageOffset) claudeAssistantUsage {
	return claudeAssistantUsage{
		InputTokens:         max(0, usage.InputTokens-offset.InputTokens),
		OutputTokens:        max(0, usage.OutputTokens-offset.OutputTokens),
		CacheCreationTokens: max(0, usage.CacheCreationTokens-offset.CacheCreationTokens),
		CacheReadTokens:     max(0, usage.CacheReadTokens-offset.CacheReadTokens),
	}
}

func cloneClaudeUsageOffsets(offsets map[string]claudeForkUsageOffset) map[string]claudeForkUsageOffset {
	cloned := make(map[string]claudeForkUsageOffset, len(offsets))
	for key, offset := range offsets {
		cloned[key] = offset
	}
	return cloned
}

func mergeClaudeUsageOffsets(left, right map[string]claudeForkUsageOffset) map[string]claudeForkUsageOffset {
	merged := cloneClaudeUsageOffsets(left)
	for key, candidate := range right {
		current := merged[key]
		current.InputTokens = max(current.InputTokens, candidate.InputTokens)
		current.OutputTokens = max(current.OutputTokens, candidate.OutputTokens)
		current.CacheCreationTokens = max(current.CacheCreationTokens, candidate.CacheCreationTokens)
		current.CacheReadTokens = max(current.CacheReadTokens, candidate.CacheReadTokens)
		merged[key] = current
	}
	return merged
}

func loadClaudeForkAccountingStore(dataDir string) (claudeForkAccountingStore, error) {
	if dataDir == "" {
		dataDir = DefaultDataDir()
	}
	data, err := os.ReadFile(filepath.Join(dataDir, claudeForkAccountingFile))
	if os.IsNotExist(err) {
		return newClaudeForkAccountingStore(), nil
	}
	if err != nil {
		return claudeForkAccountingStore{}, err
	}
	store := newClaudeForkAccountingStore()
	if err := json.Unmarshal(data, &store); err != nil {
		return claudeForkAccountingStore{}, err
	}
	return store, nil
}

func newClaudeForkAccountingStore() claudeForkAccountingStore {
	return claudeForkAccountingStore{
		SessionRanks:     map[string]uint64{},
		ActiveSessions:   map[string]bool{},
		InheritedOffsets: map[string]map[string]claudeForkUsageOffset{},
	}
}

func cloneClaudeForkAccountingStore(store claudeForkAccountingStore) claudeForkAccountingStore {
	next := newClaudeForkAccountingStore()
	next.Version = store.Version
	next.InitializedAt = store.InitializedAt
	next.NextSessionRank = store.NextSessionRank
	for sessionID, rank := range store.SessionRanks {
		next.SessionRanks[sessionID] = rank
	}
	for sessionID, active := range store.ActiveSessions {
		next.ActiveSessions[sessionID] = active
	}
	for sessionID, offsets := range store.InheritedOffsets {
		next.InheritedOffsets[sessionID] = cloneClaudeUsageOffsets(offsets)
	}
	return next
}

func saveClaudeForkAccountingStore(dataDir string, store claudeForkAccountingStore) error {
	if dataDir == "" {
		dataDir = DefaultDataDir()
	}
	return atomicfile.WriteJSON(filepath.Join(dataDir, claudeForkAccountingFile), store)
}
