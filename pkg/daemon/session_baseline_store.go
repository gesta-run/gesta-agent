package daemon

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"github.com/gesta-run/gesta-agent/pkg/atomicfile"
)

const sessionBaselineFile = "session-baseline.json"

type sessionBaselineStore struct {
	Version    int                            `json:"version"`
	Codex      codexSessionBaselineGroup      `json:"codex"`
	ClaudeCode claudeCodeSessionBaselineGroup `json:"claude_code"`
}

type codexSessionBaselineGroup struct {
	StateDBs map[string]codexSessionBaseline `json:"state_dbs"`
}

type claudeCodeSessionBaselineGroup struct {
	InitializedAt    string                     `json:"initialized_at,omitempty"`
	MCPInitializedAt string                     `json:"mcp_initialized_at,omitempty"`
	Sessions         map[string]baselineSession `json:"sessions"`
}

type codexSessionBaseline struct {
	InitializedAt string                     `json:"initialized_at"`
	StateDBHash   string                     `json:"state_db_hash,omitempty"`
	Sessions      map[string]baselineSession `json:"sessions"`
}

type baselineSession struct {
	UpdatedAt                   string   `json:"updated_at,omitempty"`
	TranscriptHash              string   `json:"transcript_hash,omitempty"`
	TranscriptMessageVersions   []string `json:"transcript_message_versions,omitempty"`
	TranscriptSequence          int64    `json:"transcript_sequence,omitempty"`
	TranscriptCursorInitialized bool     `json:"transcript_cursor_initialized,omitempty"`
	TotalTokens                 int64    `json:"total_tokens,omitempty"`
	InputTokens                 int64    `json:"input_tokens,omitempty"`
	OutputTokens                int64    `json:"output_tokens,omitempty"`
	CacheReadTokens             int64    `json:"cache_read_tokens,omitempty"`
	CacheWriteTokens            int64    `json:"cache_write_tokens,omitempty"`
	TokenAccounting             string   `json:"token_accounting,omitempty"`
	TokensObserved              bool     `json:"tokens_observed,omitempty"`
	// CacheObserved separates "this session's cache counters were genuinely zero"
	// from "this baseline predates cache accounting and never recorded them".
	CacheObserved bool `json:"cache_observed,omitempty"`
	// Previous* records the cursor value this session last advanced from. It lets
	// recovery reconstruct a lost usage delta after a crash between commits.
	PreviousTotalTokens      int64  `json:"previous_total_tokens,omitempty"`
	PreviousInputTokens      int64  `json:"previous_input_tokens,omitempty"`
	PreviousOutputTokens     int64  `json:"previous_output_tokens,omitempty"`
	PreviousCacheReadTokens  int64  `json:"previous_cache_read_tokens,omitempty"`
	PreviousCacheWriteTokens int64  `json:"previous_cache_write_tokens,omitempty"`
	PreviousObservedAt       string `json:"previous_observed_at,omitempty"`
	PreviousCacheObserved    bool   `json:"previous_cache_observed,omitempty"`
	// MCPToolCallCursorAt and MCPToolCallCursorEventIDs form a bounded cursor for
	// transcript MCP calls.
	MCPToolCallCursorAt       string   `json:"mcp_tool_call_cursor_at,omitempty"`
	MCPToolCallCursorEventIDs []string `json:"mcp_tool_call_cursor_event_ids,omitempty"`
}

func loadSessionBaselineStore(dataDir string) (sessionBaselineStore, error) {
	if dataDir == "" {
		dataDir = DefaultDataDir()
	}
	path := filepath.Join(dataDir, sessionBaselineFile)
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return newSessionBaselineStore(), nil
	}
	if err != nil {
		return sessionBaselineStore{}, err
	}
	var store sessionBaselineStore
	if err := json.Unmarshal(data, &store); err != nil {
		recoveredStore, recovered, recoverErr := decodeSessionBaselineStorePrefix(data)
		if recoverErr != nil {
			return sessionBaselineStore{}, err
		}
		if recovered {
			_ = saveSessionBaselineStore(dataDir, recoveredStore)
		}
		return recoveredStore, nil
	}
	normalizeSessionBaselineStore(&store)
	return store, nil
}

func decodeSessionBaselineStorePrefix(data []byte) (sessionBaselineStore, bool, error) {
	var store sessionBaselineStore
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := decoder.Decode(&store); err != nil {
		return sessionBaselineStore{}, false, err
	}
	remainder := string(data[decoder.InputOffset():])
	recovered := strings.TrimSpace(remainder) != ""
	normalizeSessionBaselineStore(&store)
	return store, recovered, nil
}

func saveSessionBaselineStore(dataDir string, store sessionBaselineStore) error {
	if dataDir == "" {
		dataDir = DefaultDataDir()
	}
	normalizeSessionBaselineStore(&store)
	return atomicfile.WriteJSON(filepath.Join(dataDir, sessionBaselineFile), store)
}

func newSessionBaselineStore() sessionBaselineStore {
	store := sessionBaselineStore{}
	normalizeSessionBaselineStore(&store)
	return store
}

func normalizeSessionBaselineStore(store *sessionBaselineStore) {
	if store.Version == 0 {
		store.Version = 1
	}
	if store.Codex.StateDBs == nil {
		store.Codex.StateDBs = map[string]codexSessionBaseline{}
	}
	if store.ClaudeCode.Sessions == nil {
		store.ClaudeCode.Sessions = map[string]baselineSession{}
	}
}
