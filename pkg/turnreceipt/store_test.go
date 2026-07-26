package turnreceipt

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gesta-run/gesta-agent/pkg/contextmatch"
)

func TestStoreAggregatesContextMatchesAndIdempotentOutput(t *testing.T) {
	dataDir := t.TempDir()
	store := NewStore(dataDir)
	if err := store.Begin("claude_code", "raw-session-id", ""); err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if err := store.RecordContextMatches(
		"claude_code",
		"raw-session-id",
		"",
		testContextMatches(2),
	); err != nil {
		t.Fatalf("RecordContextMatches: %v", err)
	}
	first := OutputSummary{CodeLines: 12, DocWords: 30}
	if err := store.RecordOutput("claude_code", "raw-session-id", "", "raw-tool-use-1", first); err != nil {
		t.Fatalf("RecordOutput first: %v", err)
	}
	if err := store.RecordOutput("claude_code", "raw-session-id", "", "raw-tool-use-1", first); err != nil {
		t.Fatalf("RecordOutput duplicate: %v", err)
	}
	if err := store.RecordOutput(
		"claude_code",
		"raw-session-id",
		"",
		"raw-tool-use-2",
		OutputSummary{TestLines: 7, DocWords: 5},
	); err != nil {
		t.Fatalf("RecordOutput second: %v", err)
	}

	assertReceiptStorageExcludes(t, store.rootPath(), []string{
		"raw-session-id",
		"raw-tool-use-1",
		"raw-tool-use-2",
	})

	receipt, found, err := store.Consume("claude_code", "raw-session-id", "")
	if err != nil {
		t.Fatalf("Consume: %v", err)
	}
	if !found {
		t.Fatal("receipt not found")
	}
	if len(receipt.ContextMatches) != 2 {
		t.Fatalf("context match receipt = %#v", receipt)
	}
	want := OutputSummary{CodeLines: 12, TestLines: 7, DocWords: 35}
	if receipt.Output != want {
		t.Fatalf("output = %#v, want %#v", receipt.Output, want)
	}
	if _, found, err := store.Consume("claude_code", "raw-session-id", ""); err != nil || found {
		t.Fatalf("second Consume found = %v, err = %v; want silent miss", found, err)
	}
	if err := store.RecordOutput(
		"claude_code",
		"raw-session-id",
		"",
		"late-tool-use",
		OutputSummary{CodeLines: 99},
	); err != nil {
		t.Fatalf("late RecordOutput: %v", err)
	}
	if _, found, err := store.Consume("claude_code", "raw-session-id", ""); err != nil || found {
		t.Fatalf("late output recreated receipt: found = %v, err = %v", found, err)
	}
}

func TestStoreKeepsConcurrentOutputFragments(t *testing.T) {
	store := NewStore(t.TempDir())
	if err := store.Begin("claude_code", "session-concurrent", ""); err != nil {
		t.Fatalf("Begin: %v", err)
	}
	const fragments = 64
	var wait sync.WaitGroup
	errs := make(chan error, fragments)
	for index := 0; index < fragments; index++ {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			errs <- store.RecordOutput(
				"claude_code",
				"session-concurrent",
				"",
				"tool-use-"+strconv.Itoa(index),
				OutputSummary{CodeLines: 1},
			)
		}(index)
	}
	wait.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("RecordOutput concurrent: %v", err)
		}
	}
	receipt, found, err := store.Consume("claude_code", "session-concurrent", "")
	if err != nil || !found {
		t.Fatalf("Consume found = %v, err = %v", found, err)
	}
	if receipt.Output.CodeLines != fragments {
		t.Fatalf("code lines = %d, want %d", receipt.Output.CodeLines, fragments)
	}
}

func TestStoreEnforcesOutputFragmentLimitConcurrently(t *testing.T) {
	store := NewStore(t.TempDir())
	if err := store.Begin("claude_code", "session-concurrent-limit", ""); err != nil {
		t.Fatalf("Begin: %v", err)
	}
	for index := 0; index < maxOutputFragments-1; index++ {
		if err := store.RecordOutput(
			"claude_code",
			"session-concurrent-limit",
			"",
			"existing-"+strconv.Itoa(index),
			OutputSummary{CodeLines: 1},
		); err != nil {
			t.Fatalf("RecordOutput existing %d: %v", index, err)
		}
	}

	const contenders = 32
	var wait sync.WaitGroup
	errs := make(chan error, contenders)
	for index := 0; index < contenders; index++ {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			errs <- store.RecordOutput(
				"claude_code",
				"session-concurrent-limit",
				"",
				"contender-"+strconv.Itoa(index),
				OutputSummary{CodeLines: 1},
			)
		}(index)
	}
	wait.Wait()
	close(errs)

	successes := 0
	limited := 0
	for err := range errs {
		switch {
		case err == nil:
			successes++
		case errors.Is(err, ErrOutputFragmentLimit):
			limited++
		default:
			t.Fatalf("RecordOutput contender: %v", err)
		}
	}
	if successes != 1 || limited != contenders-1 {
		t.Fatalf("successes = %d, limited = %d; want 1 and %d", successes, limited, contenders-1)
	}
	receipt, found, err := store.Consume("claude_code", "session-concurrent-limit", "")
	if err != nil || !found {
		t.Fatalf("Consume found = %v, err = %v", found, err)
	}
	if receipt.Output.CodeLines != maxOutputFragments {
		t.Fatalf("code lines = %d, want %d", receipt.Output.CodeLines, maxOutputFragments)
	}
}

func TestBeginReplacesAbandonedActiveTurn(t *testing.T) {
	store := NewStore(t.TempDir())
	if err := store.Begin("claude_code", "session-reset", ""); err != nil {
		t.Fatalf("Begin first: %v", err)
	}
	if err := store.RecordContextMatches(
		"claude_code",
		"session-reset",
		"",
		testContextMatches(1),
	); err != nil {
		t.Fatalf("RecordContextMatches: %v", err)
	}
	if err := store.RecordOutput(
		"claude_code",
		"session-reset",
		"",
		"old-tool",
		OutputSummary{CodeLines: 99},
	); err != nil {
		t.Fatalf("RecordOutput: %v", err)
	}
	if err := store.Begin("claude_code", "session-reset", ""); err != nil {
		t.Fatalf("Begin second: %v", err)
	}
	receipt, found, err := store.Consume("claude_code", "session-reset", "")
	if err != nil || !found {
		t.Fatalf("Consume found = %v, err = %v", found, err)
	}
	if len(receipt.ContextMatches) != 0 || !receipt.Output.Empty() {
		t.Fatalf("abandoned turn leaked into new receipt: %#v", receipt)
	}
}

func TestCleanupExpiredIsBoundedToExpiredReceipts(t *testing.T) {
	dataDir := t.TempDir()
	store := NewStore(dataDir)
	now := time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return now.Add(-48 * time.Hour) }
	if err := store.Begin("codex", "expired-session", "expired-turn"); err != nil {
		t.Fatalf("Begin expired: %v", err)
	}
	store.now = func() time.Time { return now }
	if err := store.Begin("codex", "current-session", "current-turn"); err != nil {
		t.Fatalf("Begin current: %v", err)
	}
	if err := store.CleanupExpired(); err != nil {
		t.Fatalf("CleanupExpired: %v", err)
	}
	if _, found, err := store.Consume("codex", "expired-session", "expired-turn"); err != nil || found {
		t.Fatalf("expired receipt found = %v, err = %v", found, err)
	}
	if _, found, err := store.Consume("codex", "current-session", "current-turn"); err != nil || !found {
		t.Fatalf("current receipt found = %v, err = %v", found, err)
	}
}

func TestExpiredReceiptCannotBeConsumedWithoutCleanup(t *testing.T) {
	store := NewStore(t.TempDir())
	now := time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return now.Add(-48 * time.Hour) }
	if err := store.Begin("codex", "expired-consume-session", "expired-consume-turn"); err != nil {
		t.Fatalf("Begin expired: %v", err)
	}

	store.now = func() time.Time { return now }
	if _, found, err := store.Consume(
		"codex",
		"expired-consume-session",
		"expired-consume-turn",
	); err != nil || found {
		t.Fatalf("expired receipt found = %v, err = %v", found, err)
	}
}

func TestCleanupExpiredRemovesStaleOrphanDirectory(t *testing.T) {
	store := NewStore(t.TempDir())
	now := time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return now }
	path, ok := store.receiptPath("codex", "orphan-session", "orphan-turn")
	if !ok {
		t.Fatal("receipt path was not created")
	}
	if err := os.MkdirAll(filepath.Join(path, "output"), 0o700); err != nil {
		t.Fatalf("mkdir orphan: %v", err)
	}
	old := now.Add(-receiptTTL - time.Hour)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatalf("chtimes orphan: %v", err)
	}
	if err := store.CleanupExpired(); err != nil {
		t.Fatalf("CleanupExpired: %v", err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("orphan stat error = %v, want not exist", err)
	}
}

func TestCleanupExpiredRemovesClaimedActiveReceipt(t *testing.T) {
	store := NewStore(t.TempDir())
	now := time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return now.Add(-48 * time.Hour) }
	if err := store.Begin("claude_code", "claimed-active-session", ""); err != nil {
		t.Fatalf("Begin: %v", err)
	}
	activePath, ok := store.receiptPath("claude_code", "claimed-active-session", "")
	if !ok {
		t.Fatal("active receipt path unavailable")
	}
	claimPath := activePath + ".consuming-orphan"
	if err := os.Rename(activePath, claimPath); err != nil {
		t.Fatalf("claim active receipt: %v", err)
	}

	store.now = func() time.Time { return now }
	if err := store.CleanupExpired(); err != nil {
		t.Fatalf("CleanupExpired: %v", err)
	}
	if _, err := os.Stat(claimPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("claimed active receipt stat error = %v, want not exist", err)
	}
}

func TestCleanupExpiredCapsRemovalWork(t *testing.T) {
	store := NewStore(t.TempDir())
	now := time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return now }
	const extra = 20
	for index := 0; index < maxCleanupRemovals+extra; index++ {
		path, ok := store.receiptPath("codex", "bounded-cleanup-session", "turn-"+strconv.Itoa(index))
		if !ok {
			t.Fatal("receipt path was not created")
		}
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatalf("mkdir orphan %d: %v", index, err)
		}
		old := now.Add(-receiptTTL - time.Hour)
		if err := os.Chtimes(path, old, old); err != nil {
			t.Fatalf("chtimes orphan %d: %v", index, err)
		}
	}
	if err := store.CleanupExpired(); err != nil {
		t.Fatalf("CleanupExpired: %v", err)
	}
	sessionPath := filepath.Dir(mustReceiptPath(t, store, "codex", "bounded-cleanup-session", "any-turn"))
	entries, err := os.ReadDir(sessionPath)
	if err != nil {
		t.Fatalf("read session directory: %v", err)
	}
	if len(entries) != extra {
		t.Fatalf("remaining entries = %d, want %d", len(entries), extra)
	}
	if err := store.CleanupExpired(); err != nil {
		t.Fatalf("second CleanupExpired: %v", err)
	}
	if _, err := os.Stat(sessionPath); err == nil {
		entries, readErr := os.ReadDir(sessionPath)
		if readErr != nil {
			t.Fatalf("read session directory after second cleanup: %v", readErr)
		}
		if len(entries) != 0 {
			t.Fatalf("entries after second cleanup = %d, want 0", len(entries))
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stat session after second cleanup: %v", err)
	}
}

func TestCleanupExpiredResumesAfterVisitLimit(t *testing.T) {
	store := NewStore(t.TempDir())
	now := time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return now }
	root := store.rootPath()
	for index := 0; index < maxCleanupVisits; index++ {
		path := filepath.Join(root, "codex", fmt.Sprintf("%04d", index), "active")
		if err := store.writeReceipt(path, store.newReceipt()); err != nil {
			t.Fatalf("write current receipt %d: %v", index, err)
		}
	}
	expiredPath := filepath.Join(root, "codex", "zzzz", "active")
	expired := store.newReceipt()
	expired.ExpiresAt = now.Add(-time.Hour)
	if err := store.writeReceipt(expiredPath, expired); err != nil {
		t.Fatalf("write expired receipt: %v", err)
	}

	if err := store.CleanupExpired(); err != nil {
		t.Fatalf("first CleanupExpired: %v", err)
	}
	if _, err := os.Stat(expiredPath); err != nil {
		t.Fatalf("expired receipt was removed before cursor resumed: %v", err)
	}
	if err := store.CleanupExpired(); err != nil {
		t.Fatalf("second CleanupExpired: %v", err)
	}
	if _, err := os.Stat(expiredPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expired receipt stat error = %v, want not exist", err)
	}
}

func TestCleanupExpiredRemovesOrphanedStableLockWithoutReceipts(t *testing.T) {
	store := NewStore(t.TempDir())
	now := time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return now }
	lockPath := store.lockPath(filepath.Join(store.rootPath(), "codex", "orphan", "active"))
	if err := os.MkdirAll(filepath.Dir(lockPath), 0o700); err != nil {
		t.Fatalf("mkdir lock root: %v", err)
	}
	if err := os.WriteFile(lockPath, nil, 0o600); err != nil {
		t.Fatalf("write orphan lock: %v", err)
	}
	old := now.Add(-receiptLockStaleAfter - time.Second)
	if err := os.Chtimes(lockPath, old, old); err != nil {
		t.Fatalf("age orphan lock: %v", err)
	}

	if err := store.CleanupExpired(); err != nil {
		t.Fatalf("CleanupExpired: %v", err)
	}
	if _, err := os.Stat(lockPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("orphan lock stat error = %v, want not exist", err)
	}
}

func TestStoreBoundsOutputFragments(t *testing.T) {
	store := NewStore(t.TempDir())
	if err := store.Begin("claude_code", "session-limit", ""); err != nil {
		t.Fatalf("Begin: %v", err)
	}
	for index := 0; index < maxOutputFragments; index++ {
		if err := store.RecordOutput(
			"claude_code",
			"session-limit",
			"",
			"tool-"+strconv.Itoa(index),
			OutputSummary{CodeLines: 1},
		); err != nil {
			t.Fatalf("RecordOutput %d: %v", index, err)
		}
	}
	err := store.RecordOutput(
		"claude_code",
		"session-limit",
		"",
		"tool-over-limit",
		OutputSummary{CodeLines: 1},
	)
	if !errors.Is(err, ErrOutputFragmentLimit) {
		t.Fatalf("over-limit error = %v, want %v", err, ErrOutputFragmentLimit)
	}
	if err := store.RecordOutput(
		"claude_code",
		"session-limit",
		"",
		"tool-0",
		OutputSummary{CodeLines: 2},
	); err != nil {
		t.Fatalf("idempotent replacement at limit: %v", err)
	}
	path, ok := store.receiptPath("claude_code", "session-limit", "")
	if !ok {
		t.Fatal("receipt path was not created")
	}
	if err := os.WriteFile(
		filepath.Join(path, "output", ".turn-receipt-stale.tmp"),
		[]byte("incomplete"),
		0o600,
	); err != nil {
		t.Fatalf("write stale temp file: %v", err)
	}
	receipt, found, err := store.Consume("claude_code", "session-limit", "")
	if err != nil || !found {
		t.Fatalf("Consume found = %v, err = %v", found, err)
	}
	if receipt.Output.CodeLines != maxOutputFragments+1 {
		t.Fatalf("code lines = %d, want %d", receipt.Output.CodeLines, maxOutputFragments+1)
	}
}

func TestStoreBoundsAndSanitizesContextMatches(t *testing.T) {
	store := NewStore(t.TempDir())
	if err := store.Begin("codex", "session-policy-limit", "turn-policy-limit"); err != nil {
		t.Fatalf("Begin: %v", err)
	}
	matches := testContextMatches(maxContextMatches + 100)
	matches = append(matches,
		ContextRuleMatch{RuleID: "always", Name: "Always", MatchType: "always", Content: "Always"},
		ContextRuleMatch{RuleID: "duplicate-0", Name: "Duplicate", MatchType: "regex", Content: "Duplicate"},
		ContextRuleMatch{RuleID: "duplicate-0", Name: "Duplicate", MatchType: "regex", Content: "Duplicate"},
	)
	if err := store.RecordContextMatches(
		"codex",
		"session-policy-limit",
		"turn-policy-limit",
		matches,
	); err != nil {
		t.Fatalf("RecordContextMatches: %v", err)
	}
	receipt, found, err := store.Consume(
		"codex",
		"session-policy-limit",
		"turn-policy-limit",
	)
	if err != nil || !found {
		t.Fatalf("Consume found = %v, err = %v", found, err)
	}
	if len(receipt.ContextMatches) != maxContextMatches {
		t.Fatalf(
			"context match count = %d, want %d",
			len(receipt.ContextMatches),
			maxContextMatches,
		)
	}
}

func TestNormalizeContextMatchesPreservesExactBoundedContent(t *testing.T) {
	matches := NormalizeContextMatches([]ContextRuleMatch{
		{
			RuleID:    "review",
			Name:      "Review",
			MatchType: "REGEX",
			Content:   "\nReview the complete diff.\nKeep this line.\n",
		},
		{
			RuleID:    "too-large",
			Name:      "Too large",
			MatchType: "keyword_any",
			Content:   strings.Repeat("界", contextmatch.MaxContextContent),
		},
		{
			RuleID:    "empty",
			Name:      "Empty",
			MatchType: "keyword_any",
			Content:   " \n\t ",
		},
	})

	if len(matches) != 1 {
		t.Fatalf("normalized matches = %#v", matches)
	}
	if matches[0].Content != "Review the complete diff.\nKeep this line." {
		t.Fatalf("normalized content = %q", matches[0].Content)
	}
}

func TestNormalizeContextMatchesAcceptsMatcherMaximum(t *testing.T) {
	content := strings.Repeat("界", contextmatch.MaxContextContent)
	matches := NormalizeContextMatches([]ContextRuleMatch{{
		RuleID:    "maximum",
		Name:      "Maximum",
		MatchType: "keyword_any",
		Content:   content,
	}})
	if len(matches) != 1 || matches[0].Content != content {
		t.Fatalf("maximum content was not preserved: %d matches", len(matches))
	}
}

func TestWriteReceiptRejectsRecordOverHardLimit(t *testing.T) {
	store := NewStore(t.TempDir())
	err := store.writeReceipt(filepath.Join(t.TempDir(), "receipt"), Receipt{
		SchemaVersion: schemaVersion,
		ContextMatches: []ContextRuleMatch{{
			RuleID:    "oversized",
			Name:      "Oversized",
			MatchType: "keyword_any",
			Content:   strings.Repeat("x", maxReceiptBytes),
		}},
	})
	if err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("writeReceipt error = %v, want size error", err)
	}
}

func testContextMatches(count int) []ContextRuleMatch {
	matches := make([]ContextRuleMatch, 0, count)
	for index := 0; index < count; index++ {
		matches = append(matches, ContextRuleMatch{
			RuleID:    "duplicate-" + strconv.Itoa(index),
			Name:      "Rule " + strconv.Itoa(index),
			MatchType: "keyword_any",
			Priority:  100 - index,
			Content:   "Follow rule " + strconv.Itoa(index) + ".",
		})
	}
	return matches
}

func assertReceiptStorageExcludes(t *testing.T, root string, forbidden []string) {
	t.Helper()
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		for _, value := range forbidden {
			if strings.Contains(path, value) {
				t.Fatalf("receipt path leaked %q: %s", value, path)
			}
		}
		if entry.IsDir() {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, value := range forbidden {
			if strings.Contains(string(data), value) {
				t.Fatalf("receipt file leaked %q: %s", value, data)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk receipt storage: %v", err)
	}
}

func mustReceiptPath(t *testing.T, store Store, agentType, sessionID, turnID string) string {
	t.Helper()
	path, ok := store.receiptPath(agentType, sessionID, turnID)
	if !ok {
		t.Fatal("receipt path was not created")
	}
	return path
}
