package turnreceipt

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestPendingNoticeIsSessionScopedAndConsumedOnce(t *testing.T) {
	store := NewStore(t.TempDir())
	if err := store.SavePending(
		"codex",
		"raw-pending-session",
		OutputSummary{CodeLines: 1},
	); err != nil {
		t.Fatalf("SavePending: %v", err)
	}
	assertReceiptStorageExcludes(t, store.rootPath(), []string{
		"raw-pending-session",
	})

	if _, found, err := store.ConsumePending("claude_code", "raw-pending-session"); err != nil || found {
		t.Fatalf("other agent found = %v, err = %v", found, err)
	}
	if _, found, err := store.ConsumePending("codex", "other-session"); err != nil || found {
		t.Fatalf("other session found = %v, err = %v", found, err)
	}
	pending, found, err := store.ConsumePending("codex", "raw-pending-session")
	if err != nil || !found {
		t.Fatalf("ConsumePending found = %v, err = %v", found, err)
	}
	if pending.Output.CodeLines != 1 {
		t.Fatalf("output = %#v", pending.Output)
	}
	if pending.SchemaVersion != pendingSchemaVersion {
		t.Fatalf("schema version = %d, want %d", pending.SchemaVersion, pendingSchemaVersion)
	}
	if _, found, err := store.ConsumePending("codex", "raw-pending-session"); err != nil || found {
		t.Fatalf("second consume found = %v, err = %v", found, err)
	}
}

func TestPendingNoticeLatestValueWins(t *testing.T) {
	store := NewStore(t.TempDir())
	if err := store.SavePending("codex", "session-latest", OutputSummary{CodeLines: 1}); err != nil {
		t.Fatalf("SavePending first: %v", err)
	}
	if err := store.SavePending("codex", "session-latest", OutputSummary{CodeLines: 2}); err != nil {
		t.Fatalf("SavePending second: %v", err)
	}
	pending, found, err := store.ConsumePending("codex", "session-latest")
	if err != nil || !found {
		t.Fatalf("ConsumePending found = %v, err = %v", found, err)
	}
	if pending.Output.CodeLines != 2 {
		t.Fatalf("output = %#v, want 2 code lines", pending.Output)
	}
}

func TestPendingNoticeExpiresAndCleanupRemovesIt(t *testing.T) {
	store := NewStore(t.TempDir())
	now := time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return now.Add(-48 * time.Hour) }
	if err := store.SavePending("codex", "expired-pending", OutputSummary{DocWords: 1}); err != nil {
		t.Fatalf("SavePending: %v", err)
	}
	store.now = func() time.Time { return now }
	if err := store.CleanupExpired(); err != nil {
		t.Fatalf("CleanupExpired: %v", err)
	}
	if _, found, err := store.ConsumePending("codex", "expired-pending"); err != nil || found {
		t.Fatalf("expired pending found = %v, err = %v", found, err)
	}
}

func TestPendingNoticeCorruptionFailsClosedToStorageAndOpenToHook(t *testing.T) {
	store := NewStore(t.TempDir())
	if err := store.SavePending("codex", "corrupt-pending", OutputSummary{DocWords: 1}); err != nil {
		t.Fatalf("SavePending: %v", err)
	}
	path, ok := store.pendingPath("codex", "corrupt-pending")
	if !ok {
		t.Fatal("pending path unavailable")
	}
	if err := os.WriteFile(filepath.Join(path, "receipt.json"), []byte("{"), 0o600); err != nil {
		t.Fatalf("corrupt pending notice: %v", err)
	}
	if _, found, err := store.ConsumePending("codex", "corrupt-pending"); err == nil || !found {
		t.Fatalf("corrupt consume found = %v, err = %v", found, err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("corrupt pending path stat = %v, want not exist", err)
	}
}

func TestPendingNoticeAllowsOnlyOneConcurrentConsumer(t *testing.T) {
	store := NewStore(t.TempDir())
	if err := store.SavePending("codex", "concurrent-pending", OutputSummary{DocWords: 1}); err != nil {
		t.Fatalf("SavePending: %v", err)
	}

	const consumers = 16
	var foundCount atomic.Int64
	var wait sync.WaitGroup
	errs := make(chan error, consumers)
	for index := 0; index < consumers; index++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			_, found, err := store.ConsumePending("codex", "concurrent-pending")
			if found {
				foundCount.Add(1)
			}
			errs <- err
		}()
	}
	wait.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("ConsumePending: %v", err)
		}
	}
	if got := foundCount.Load(); got != 1 {
		t.Fatalf("consumers finding pending notice = %d, want 1", got)
	}
}

func TestPendingNoticeSkipsEmptyOutput(t *testing.T) {
	store := NewStore(t.TempDir())
	if err := store.SavePending("codex", "empty-pending", OutputSummary{}); err != nil {
		t.Fatalf("SavePending empty output: %v", err)
	}
	if _, found, err := store.ConsumePending("codex", "empty-pending"); err != nil || found {
		t.Fatalf("empty pending found = %v, err = %v", found, err)
	}
}
