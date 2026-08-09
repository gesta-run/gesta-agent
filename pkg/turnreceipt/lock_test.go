package turnreceipt

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestStaleLockOwnerCannotRemoveReplacementLock(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows cannot replace a lock file while its owner still has it open")
	}
	lockPath := filepath.Join(t.TempDir(), "receipt.lock")
	unlockFirst, err := acquireReceiptLock(lockPath, true)
	if err != nil {
		t.Fatalf("acquire first lock: %v", err)
	}
	old := time.Now().Add(-receiptLockStaleAfter - time.Second)
	if err := os.Chtimes(lockPath, old, old); err != nil {
		t.Fatalf("age first lock: %v", err)
	}

	unlockSecond, err := acquireReceiptLock(lockPath, true)
	if err != nil {
		t.Fatalf("acquire replacement lock: %v", err)
	}
	unlockFirst()
	if _, err := os.Stat(lockPath); err != nil {
		t.Fatalf("first owner removed replacement lock: %v", err)
	}
	unlockSecond()
	if _, err := os.Stat(lockPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("replacement lock stat error = %v, want not exist", err)
	}
}
