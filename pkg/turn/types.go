package turn

import (
	"strings"
	"time"

	"github.com/gesta-run/gesta-agent/pkg/util"
)

const (
	EventType              = "turn.usage"
	ClassifierVersion      = "turn-v1"
	cursorFile             = "turn-cursors-v1.json"
	TotalEncodingEffective = "effective"
	TotalEncodingAllTier   = "all_tier"
)

type Config struct {
	DataDir        string
	DaemonID       string
	TotalEncoding  string
	OnCounterReset func(CounterReset)
}

type CounterReset struct {
	SessionIDHash string
	TurnIDHash    string
	Previous      TokenTotals
	Current       TokenTotals
}

type CodexSession struct {
	SessionID string
	// LegacySessionID is the previously preferred shared session_id. It is
	// retained only to migrate pre-canonical cursors without replaying history.
	LegacySessionID       string
	ParentSessionID       string
	RolloutPath           string
	Title                 string
	Model                 string
	Repo                  string
	ModelProvider         string
	InheritedTurnIDHashes map[string]struct{}
	TotalEncoding         string
	OnCounterReset        func(CounterReset)
}

type Evidence struct {
	Text   string
	Weight int
}

type ClaudeSession struct {
	SessionIDHash string
	FirstEventAt  time.Time
	Turns         []ClaudeTurn
}

type ClaudeTurn struct {
	TurnID        string
	Status        string
	StartedAt     time.Time
	EndedAt       time.Time
	Model         string
	Repo          string
	Tokens        TokenTotals
	Evidence      []Evidence
	ModelProvider string
}

type TokenTotals struct {
	Input      int64 `json:"input_tokens"`
	Output     int64 `json:"output_tokens"`
	CacheRead  int64 `json:"cache_read_tokens"`
	CacheWrite int64 `json:"cache_write_tokens"`
}

func (t TokenTotals) Total() int64 {
	return nonNegative(t.Input) + nonNegative(t.Output) + nonNegative(t.CacheRead) + nonNegative(t.CacheWrite)
}

func (t TokenTotals) Delta(previous TokenTotals) TokenTotals {
	return TokenTotals{
		Input:      nonNegative(t.Input - previous.Input),
		Output:     nonNegative(t.Output - previous.Output),
		CacheRead:  nonNegative(t.CacheRead - previous.CacheRead),
		CacheWrite: nonNegative(t.CacheWrite - previous.CacheWrite),
	}
}

func (t TokenTotals) decreasedFrom(previous TokenTotals) bool {
	return t.Input < previous.Input ||
		t.Output < previous.Output ||
		t.CacheRead < previous.CacheRead ||
		t.CacheWrite < previous.CacheWrite
}

type Usage struct {
	EventID       string
	SessionIDHash string
	TurnIDHash    string
	Status        string
	Title         string
	StartedAt     time.Time
	EndedAt       time.Time
	Model         string
	Repo          string
	ModelProvider string
	Tokens        TokenTotals
	WorkType      string
	TotalEncoding string
}

func (u Usage) Payload() map[string]interface{} {
	total := u.Tokens.Total()
	if u.TotalEncoding == TotalEncodingEffective {
		total = nonNegative(u.Tokens.Input) + nonNegative(u.Tokens.Output)
	}
	payload := map[string]interface{}{
		"session_id_hash":              u.SessionIDHash,
		"turn_id_hash":                 u.TurnIDHash,
		"status":                       u.Status,
		"started_at":                   u.StartedAt.UTC().Format(time.RFC3339Nano),
		"ended_at":                     u.EndedAt.UTC().Format(time.RFC3339Nano),
		"total_tokens":                 total,
		"input_tokens":                 u.Tokens.Input,
		"output_tokens":                u.Tokens.Output,
		"cache_read_tokens":            u.Tokens.CacheRead,
		"cache_write_tokens":           u.Tokens.CacheWrite,
		"work_type":                    u.WorkType,
		"work_type_classifier_version": ClassifierVersion,
	}
	if u.Model != "" {
		payload["model"] = u.Model
	}
	if u.Title != "" {
		payload["title"] = u.Title
	}
	if u.Repo != "" {
		payload["repo"] = u.Repo
	}
	if u.ModelProvider != "" {
		payload["model_provider"] = u.ModelProvider
	}
	return payload
}

func stableEventID(daemonID, sessionIDHash, turnIDHash string) string {
	return "evt_" + util.HashString(strings.Join([]string{EventType, daemonID, sessionIDHash, turnIDHash}, "\x00"))
}

func nonNegative(value int64) int64 {
	if value < 0 {
		return 0
	}
	return value
}
