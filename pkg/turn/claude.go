package turn

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/gesta-run/gesta-agent/pkg/atomicfile"
	"github.com/gesta-run/gesta-agent/pkg/util"
)

const claudeCursorFile = "claude-turn-cursors-v1.json"

type claudeCursorStore struct {
	InitializedAt string                         `json:"initialized_at"`
	Sessions      map[string]claudeSessionCursor `json:"sessions"`
}

type claudeSessionCursor struct {
	LastEndedAt    string          `json:"last_ended_at,omitempty"`
	SeenTurnHashes map[string]bool `json:"seen_turn_hashes,omitempty"`
}

func CollectClaude(cfg Config, sessions []ClaudeSession, observedAt time.Time) ([]Usage, func() error, error) {
	if observedAt.IsZero() {
		observedAt = time.Now().UTC()
	}
	store, err := loadClaudeCursorStore(cfg.DataDir)
	if err != nil {
		return nil, nil, err
	}
	firstCollection := strings.TrimSpace(store.InitializedAt) == ""
	initializedAt, _ := time.Parse(time.RFC3339Nano, store.InitializedAt)
	next := cloneClaudeCursorStore(store)
	dirty := firstCollection
	sort.Slice(sessions, func(i, j int) bool { return sessions[i].SessionIDHash < sessions[j].SessionIDHash })

	var events []Usage
	for _, session := range sessions {
		if strings.TrimSpace(session.SessionIDHash) == "" {
			continue
		}
		cursor, exists := next.Sessions[session.SessionIDHash]
		if cursor.SeenTurnHashes == nil {
			cursor.SeenTurnHashes = map[string]bool{}
		}
		emit := !session.SeedOnly && !firstCollection && (exists || !session.FirstEventAt.Before(initializedAt))
		if !exists {
			dirty = true
		}
		sort.Slice(session.Turns, func(i, j int) bool {
			if !session.Turns[i].EndedAt.Equal(session.Turns[j].EndedAt) {
				return session.Turns[i].EndedAt.Before(session.Turns[j].EndedAt)
			}
			return session.Turns[i].TurnID < session.Turns[j].TurnID
		})
		for _, turn := range session.Turns {
			turnIDHash := util.HashString(strings.TrimSpace(turn.TurnID))
			if turn.TurnID == "" || turn.Tokens.Total() <= 0 || turn.StartedAt.IsZero() || turn.EndedAt.Before(turn.StartedAt) {
				continue
			}
			if !claudeTurnAfterCursor(cursor, turn.EndedAt, turnIDHash) {
				continue
			}
			if emit && !turn.Inherited {
				status := strings.ToLower(strings.TrimSpace(turn.Status))
				if status != "aborted" {
					status = "completed"
				}
				events = append(events, Usage{
					EventID:       stableEventID(cfg.DaemonID, session.SessionIDHash, turnIDHash),
					SessionIDHash: session.SessionIDHash,
					TurnIDHash:    turnIDHash,
					Status:        status,
					StartedAt:     turn.StartedAt.UTC(),
					EndedAt:       turn.EndedAt.UTC(),
					Model:         turn.Model,
					Repo:          turn.Repo,
					ModelProvider: turn.ModelProvider,
					Tokens:        turn.Tokens,
					WorkType:      classifyEvidence(turn.Evidence),
					TotalEncoding: cfg.TotalEncoding,
				})
			}
			advanceClaudeCursor(&cursor, turn.EndedAt, turnIDHash)
			dirty = true
		}
		next.Sessions[session.SessionIDHash] = cursor
	}
	if firstCollection {
		next.InitializedAt = observedAt.UTC().Format(time.RFC3339Nano)
	}
	if !dirty {
		return events, nil, nil
	}
	return events, func() error { return saveClaudeCursorStore(cfg.DataDir, next) }, nil
}

func claudeTurnAfterCursor(cursor claudeSessionCursor, endedAt time.Time, turnIDHash string) bool {
	lastEndedAt, err := time.Parse(time.RFC3339Nano, cursor.LastEndedAt)
	if err != nil {
		return !cursor.SeenTurnHashes[turnIDHash]
	}
	endedAt = endedAt.UTC()
	return endedAt.After(lastEndedAt) || (endedAt.Equal(lastEndedAt) && !cursor.SeenTurnHashes[turnIDHash])
}

func advanceClaudeCursor(cursor *claudeSessionCursor, endedAt time.Time, turnIDHash string) {
	value := endedAt.UTC().Format(time.RFC3339Nano)
	if cursor.LastEndedAt != value {
		cursor.LastEndedAt = value
		cursor.SeenTurnHashes = map[string]bool{}
	}
	cursor.SeenTurnHashes[turnIDHash] = true
}

func loadClaudeCursorStore(dataDir string) (claudeCursorStore, error) {
	data, err := os.ReadFile(filepath.Join(dataDir, claudeCursorFile))
	if os.IsNotExist(err) {
		return claudeCursorStore{Sessions: map[string]claudeSessionCursor{}}, nil
	}
	if err != nil {
		return claudeCursorStore{}, err
	}
	var store claudeCursorStore
	if err := json.Unmarshal(data, &store); err != nil {
		return claudeCursorStore{}, err
	}
	if store.Sessions == nil {
		store.Sessions = map[string]claudeSessionCursor{}
	}
	return store, nil
}

func cloneClaudeCursorStore(store claudeCursorStore) claudeCursorStore {
	next := claudeCursorStore{InitializedAt: store.InitializedAt, Sessions: make(map[string]claudeSessionCursor, len(store.Sessions))}
	for sessionID, cursor := range store.Sessions {
		seen := make(map[string]bool, len(cursor.SeenTurnHashes))
		for turnID, value := range cursor.SeenTurnHashes {
			seen[turnID] = value
		}
		next.Sessions[sessionID] = claudeSessionCursor{LastEndedAt: cursor.LastEndedAt, SeenTurnHashes: seen}
	}
	return next
}

func saveClaudeCursorStore(dataDir string, store claudeCursorStore) error {
	return atomicfile.WriteJSON(filepath.Join(dataDir, claudeCursorFile), store)
}
