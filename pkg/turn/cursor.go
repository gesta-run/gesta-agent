package turn

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"github.com/gesta-run/gesta-agent/pkg/atomicfile"
)

type cursorStore struct {
	InitializedAt string            `json:"initialized_at"`
	Sessions      map[string]Cursor `json:"sessions"`
}

type Cursor struct {
	RolloutPathHash  string      `json:"rollout_path_hash"`
	Offset           int64       `json:"offset"`
	Model            string      `json:"model,omitempty"`
	LastTokens       TokenTotals `json:"last_tokens"`
	AwaitingBaseline bool        `json:"awaiting_baseline,omitempty"`
	Active           *activeTurn `json:"active,omitempty"`
}

type activeTurn struct {
	TurnIDHash    string         `json:"turn_id_hash"`
	StartedAt     time.Time      `json:"started_at"`
	Baseline      TokenTotals    `json:"baseline"`
	Latest        TokenTotals    `json:"latest"`
	Scores        map[string]int `json:"scores"`
	Inherited     bool           `json:"inherited,omitempty"`
	CounterReset  bool           `json:"counter_reset,omitempty"`
	ResetPrevious TokenTotals    `json:"reset_previous,omitempty"`
	ResetCurrent  TokenTotals    `json:"reset_current,omitempty"`
}

func loadCursorStore(dataDir string) (cursorStore, error) {
	path := filepath.Join(dataDir, cursorFile)
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return cursorStore{Sessions: map[string]Cursor{}}, nil
	}
	if err != nil {
		return cursorStore{}, err
	}
	var store cursorStore
	if err := json.Unmarshal(data, &store); err != nil {
		return cursorStore{}, err
	}
	if store.Sessions == nil {
		store.Sessions = map[string]Cursor{}
	}
	return store, nil
}

func saveCursorStore(dataDir string, store cursorStore) error {
	if store.Sessions == nil {
		store.Sessions = map[string]Cursor{}
	}
	return atomicfile.WriteJSON(filepath.Join(dataDir, cursorFile), store)
}

func cloneCursorStore(store cursorStore) cursorStore {
	next := cursorStore{InitializedAt: store.InitializedAt, Sessions: make(map[string]Cursor, len(store.Sessions))}
	for key, cursor := range store.Sessions {
		if cursor.Active != nil {
			active := *cursor.Active
			active.Scores = make(map[string]int, len(cursor.Active.Scores))
			for label, score := range cursor.Active.Scores {
				active.Scores[label] = score
			}
			cursor.Active = &active
		}
		next.Sessions[key] = cursor
	}
	return next
}
