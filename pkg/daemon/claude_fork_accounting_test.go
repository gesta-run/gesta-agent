package daemon

import (
	"strings"
	"testing"
	"time"

	turnusage "github.com/gesta-run/gesta-agent/pkg/turn"
)

func TestPrepareClaudeForkAccountingKeepsOnlyUniqueForkUsage(t *testing.T) {
	dataDir := t.TempDir()
	base := time.Date(2026, 8, 26, 10, 0, 0, 0, time.UTC)
	parent := claudeForkTestSession("parent", base, "shared", 10)
	accounted, commit, err := prepareClaudeForkAccounting(dataDir, []claudeSessionUsage{parent}, base)
	if err != nil || len(accounted) != 1 || accounted[0].AccountingSeedOnly {
		t.Fatalf("initial accounting = %#v, commit=%v, err=%v", accounted, commit != nil, err)
	}
	if err := commit(); err != nil {
		t.Fatalf("commit initial accounting: %v", err)
	}

	child := claudeForkTestSession("child", base, "shared", 10)
	child = appendClaudeForkTestUsage(child, base.Add(time.Minute), "child-only", 20)
	accounted, commit, err = prepareClaudeForkAccounting(dataDir, []claudeSessionUsage{parent, child}, base.Add(2*time.Minute))
	if err != nil {
		t.Fatalf("child accounting: %v", err)
	}
	assertClaudeForkUsage(t, accounted, "parent", 10, 1)
	assertClaudeForkUsage(t, accounted, "child", 20, 1)
	if err := commit(); err != nil {
		t.Fatalf("commit child accounting: %v", err)
	}

	grandchild := claudeForkTestSession("grandchild", base, "shared", 10)
	grandchild = appendClaudeForkTestUsage(grandchild, base.Add(time.Minute), "child-only", 20)
	grandchild = appendClaudeForkTestUsage(grandchild, base.Add(2*time.Minute), "grandchild-only", 30)
	sibling := claudeForkTestSession("sibling", base, "shared", 10)
	sibling = appendClaudeForkTestUsage(sibling, base.Add(2*time.Minute), "sibling-only", 40)
	accounted, commit, err = prepareClaudeForkAccounting(
		dataDir,
		[]claudeSessionUsage{parent, child, grandchild, sibling},
		base.Add(3*time.Minute),
	)
	if err != nil {
		t.Fatalf("nested accounting: %v", err)
	}
	assertClaudeForkUsage(t, accounted, "parent", 10, 1)
	assertClaudeForkUsage(t, accounted, "child", 20, 1)
	assertClaudeForkUsage(t, accounted, "grandchild", 30, 1)
	assertClaudeForkUsage(t, accounted, "sibling", 40, 1)
	if err := commit(); err != nil {
		t.Fatalf("commit nested accounting: %v", err)
	}

	accounted, _, err = prepareClaudeForkAccounting(dataDir, []claudeSessionUsage{grandchild}, base.Add(4*time.Minute))
	if err != nil {
		t.Fatalf("accounting after parent cleanup: %v", err)
	}
	assertClaudeForkUsage(t, accounted, "grandchild", 30, -1)
}

func TestClaudeAccountingCutoverRebasesWithoutDelta(t *testing.T) {
	baseline := claudeCodeSessionBaselineGroup{Sessions: map[string]baselineSession{
		"bucket": {
			TokensObserved:  true,
			TotalTokens:     100,
			InputTokens:     60,
			OutputTokens:    40,
			TokenAccounting: "legacy",
		},
	}}
	payload := map[string]interface{}{
		"session_id":                        "bucket",
		"total_tokens":                      int64(30),
		"input_tokens":                      int64(20),
		"output_tokens":                     int64(10),
		"token_accounting":                  claudeTokenAccounting,
		internalAccountingCutoverPayloadKey: true,
	}
	filtered, updates, ignored := filterClaudeUsageBaseline(baseline, []map[string]interface{}{payload})
	if len(filtered) != 0 || len(updates) != 1 || ignored != 1 {
		t.Fatalf("cutover filter = %#v, updates=%#v, ignored=%d", filtered, updates, ignored)
	}
	if !addBaselineSession(baseline.Sessions, updates[0]) {
		t.Fatal("cutover did not update baseline")
	}
	got := baseline.Sessions["bucket"]
	if got.TotalTokens != 30 || got.PreviousTotalTokens != 30 || got.TokenAccounting != claudeTokenAccounting {
		t.Fatalf("rebased baseline = %#v", got)
	}
}

func TestPrepareClaudeForkAccountingSeedsNewHistoricalSession(t *testing.T) {
	dataDir := t.TempDir()
	initializedAt := time.Date(2026, 8, 26, 10, 0, 0, 0, time.UTC)
	_, commit, err := prepareClaudeForkAccounting(dataDir, nil, initializedAt)
	if err != nil || commit == nil {
		t.Fatalf("initialize accounting: commit=%v, err=%v", commit != nil, err)
	}
	if err := commit(); err != nil {
		t.Fatalf("commit accounting initialization: %v", err)
	}

	historical := claudeForkTestSession("historical-child", initializedAt.Add(-time.Hour), "copied", 10)
	accounted, commit, err := prepareClaudeForkAccounting(dataDir, []claudeSessionUsage{historical}, initializedAt.Add(time.Minute))
	if err != nil || len(accounted) != 1 || !accounted[0].AccountingSeedOnly {
		t.Fatalf("historical accounting = %#v, commit=%v, err=%v", accounted, commit != nil, err)
	}
	if err := commit(); err != nil {
		t.Fatalf("commit historical session: %v", err)
	}

	accounted, _, err = prepareClaudeForkAccounting(dataDir, []claudeSessionUsage{historical}, initializedAt.Add(2*time.Minute))
	if err != nil || len(accounted) != 1 || accounted[0].AccountingSeedOnly {
		t.Fatalf("known historical accounting = %#v, err=%v", accounted, err)
	}
}

func TestClaudeFallbackIdentitiesSurviveForkSessionIDChange(t *testing.T) {
	home := t.TempDir()
	parentID := "parent-session"
	childID := "child-session"
	linesForSession := func(sessionID string) []string {
		return []string{
			strings.ReplaceAll(claudeUserLine("shared prompt"), claudeSessionUUID, sessionID),
			strings.ReplaceAll(
				claudeAssistantLine("claude-test", "2026-06-20T10:01:00.000Z", 10, 5, 0, 0),
				claudeSessionUUID,
				sessionID,
			),
		}
	}
	parent, parentOK := parseClaudeTranscript(writeClaudeTranscript(t, home, parentID, linesForSession(parentID)))
	child, childOK := parseClaudeTranscript(writeClaudeTranscript(t, home, childID, linesForSession(childID)))
	if !parentOK || !childOK || len(parent.Invocations) != 1 || len(child.Invocations) != 1 ||
		len(parent.Turns) != 1 || len(child.Turns) != 1 {
		t.Fatalf("parsed parent=%#v child=%#v", parent, child)
	}
	if parent.Invocations[0].InvocationID != child.Invocations[0].InvocationID {
		t.Fatalf("invocation identities differ: %q != %q", parent.Invocations[0].InvocationID, child.Invocations[0].InvocationID)
	}
	if parent.Turns[0].TurnID != child.Turns[0].TurnID {
		t.Fatalf("turn identities differ: %q != %q", parent.Turns[0].TurnID, child.Turns[0].TurnID)
	}
}

func TestPrepareClaudeForkAccountingPreservesSessionMetadata(t *testing.T) {
	dataDir := t.TempDir()
	base := time.Date(2026, 8, 26, 10, 0, 0, 0, time.UTC)
	session := claudeForkTestSession("session", base, "billable", 10)
	session.AssistantEvents = 2
	session.Models = []string{"claude-billable", "claude-zero-usage"}
	session.LastEventAt = base.Add(5 * time.Minute)

	accounted, _, err := prepareClaudeForkAccounting(dataDir, []claudeSessionUsage{session}, base.Add(10*time.Minute))
	if err != nil || len(accounted) != 1 {
		t.Fatalf("accounting = %#v, err=%v", accounted, err)
	}
	got := accounted[0]
	if got.AssistantEvents != 2 || len(got.Models) != 2 || !got.FirstEventAt.Equal(base) ||
		!got.LastEventAt.Equal(base.Add(5*time.Minute)) {
		t.Fatalf("session metadata changed: %#v", got)
	}
}

func claudeForkTestSession(sessionID string, observedAt time.Time, identity string, tokens int64) claudeSessionUsage {
	return appendClaudeForkTestUsage(claudeSessionUsage{
		SessionID:  sessionID,
		ByModelDay: map[claudeModelDayKey]claudeAssistantUsage{},
	}, observedAt, identity, tokens)
}

func appendClaudeForkTestUsage(session claudeSessionUsage, observedAt time.Time, identity string, tokens int64) claudeSessionUsage {
	usage := claudeAssistantUsage{InputTokens: tokens}
	session.Invocations = append(session.Invocations, claudeInvocationUsage{
		InvocationID: identity,
		ObservedAt:   observedAt,
		Model:        "claude-test",
		Usage:        usage,
	})
	session.Turns = append(session.Turns, claudeTurnUsage{
		TurnID: identity, Status: "completed", StartedAt: observedAt, EndedAt: observedAt,
		Model: "claude-test", Usage: usage, Evidence: []turnusage.Evidence{{Text: identity, Weight: 1}},
	})
	rebuildClaudeInvocationTotals(&session)
	if session.FirstEventAt.IsZero() || observedAt.Before(session.FirstEventAt) {
		session.FirstEventAt = observedAt
	}
	if observedAt.After(session.LastEventAt) {
		session.LastEventAt = observedAt
	}
	return session
}

func assertClaudeForkUsage(t *testing.T, sessions []claudeSessionUsage, sessionID string, tokens int64, turns int) {
	t.Helper()
	for _, session := range sessions {
		if session.SessionID != sessionID {
			continue
		}
		uniqueTurns := 0
		for _, turn := range session.Turns {
			if !turn.AccountingInherited {
				uniqueTurns++
			}
		}
		if session.Total.TotalTokens() != tokens || (turns >= 0 && uniqueTurns != turns) {
			t.Fatalf("session %s = total %d, unique turns %d", sessionID, session.Total.TotalTokens(), uniqueTurns)
		}
		return
	}
	t.Fatalf("session %s not found", sessionID)
}
