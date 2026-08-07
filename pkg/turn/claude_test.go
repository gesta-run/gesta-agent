package turn

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCollectClaudeEmitsOnlyNewTurnsAfterCommit(t *testing.T) {
	dataDir := t.TempDir()
	observedAt := time.Date(2026, 8, 4, 10, 0, 0, 0, time.UTC)
	historical := ClaudeSession{
		SessionIDHash: "0123456789abcdef",
		FirstEventAt:  observedAt.Add(-time.Hour),
		Turns: []ClaudeTurn{{
			TurnID: "old", StartedAt: observedAt.Add(-time.Hour), EndedAt: observedAt.Add(-59 * time.Minute),
			Tokens: TokenTotals{Input: 10, Output: 5}, Evidence: []Evidence{{Text: "old task", Weight: 5}},
		}},
	}
	_, commit, err := CollectClaude(Config{DataDir: dataDir, DaemonID: "daemon"}, []ClaudeSession{historical}, observedAt)
	if err != nil {
		t.Fatalf("initialize: %v", err)
	}
	if err := commit(); err != nil {
		t.Fatalf("commit initialization: %v", err)
	}

	historical.Turns = append(historical.Turns, ClaudeTurn{
		TurnID: "deploy", Status: "aborted", StartedAt: observedAt.Add(time.Minute), EndedAt: observedAt.Add(2 * time.Minute), Model: "claude-opus-4-1",
		Tokens:   TokenTotals{Input: 20, Output: 10, CacheRead: 5},
		Evidence: []Evidence{{Text: "deploy to production", Weight: 5}, {Text: "kubectl apply", Weight: 7}},
	})
	events, commit, err := CollectClaude(Config{DataDir: dataDir, DaemonID: "daemon"}, []ClaudeSession{historical}, observedAt.Add(3*time.Minute))
	if err != nil || len(events) != 1 {
		t.Fatalf("events=%d err=%v, want one", len(events), err)
	}
	if events[0].Status != "aborted" || events[0].WorkType != "SRE" || events[0].Tokens.Total() != 30 || events[0].Tokens.BilledTotal() != 35 {
		t.Fatalf("event=%+v", events[0])
	}
	retry, _, err := CollectClaude(Config{DataDir: dataDir, DaemonID: "daemon"}, []ClaudeSession{historical}, observedAt.Add(4*time.Minute))
	if err != nil || len(retry) != 1 {
		t.Fatalf("uncommitted retry events=%d err=%v, want one", len(retry), err)
	}
	if err := commit(); err != nil {
		t.Fatalf("commit new turn: %v", err)
	}
	again, noOpCommit, err := CollectClaude(Config{DataDir: dataDir, DaemonID: "daemon"}, []ClaudeSession{historical}, observedAt.Add(5*time.Minute))
	if err != nil || len(again) != 0 {
		t.Fatalf("committed retry events=%d err=%v, want zero", len(again), err)
	}
	if noOpCommit != nil {
		t.Fatal("unchanged collection must not rewrite the cursor")
	}
	store, err := loadClaudeCursorStore(dataDir)
	if err != nil {
		t.Fatalf("load cursor: %v", err)
	}
	sessionCursor := store.Sessions[historical.SessionIDHash]
	if sessionCursor.LastEndedAt == "" || len(sessionCursor.SeenTurnHashes) != 1 {
		t.Fatalf("cursor=%+v, want a bounded timestamp watermark", sessionCursor)
	}
	cursor, err := os.ReadFile(filepath.Join(dataDir, claudeCursorFile))
	if err != nil {
		t.Fatalf("read cursor: %v", err)
	}
	if strings.Contains(string(cursor), "deploy") || strings.Contains(string(cursor), "kubectl") {
		t.Fatalf("cursor leaked classifier evidence: %s", cursor)
	}
}
