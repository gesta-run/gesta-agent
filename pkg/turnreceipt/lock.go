package turnreceipt

import (
	"errors"
	"os"
	"time"

	"github.com/gesta-run/gesta-agent/pkg/lockfile"
)

var receiptLockOptions = lockfile.Options{
	Label:        "turn receipt",
	Wait:         receiptLockWait,
	StaleAfter:   receiptLockStaleAfter,
	PollInterval: receiptLockPollInterval,
}

func (s Store) withReceiptLock(path string, fn func() error) error {
	unlock, err := acquireReceiptLock(s.lockPath(path), true)
	if err != nil {
		return err
	}
	defer unlock()
	return fn()
}

func (s Store) tryReceiptLock(path string) (func(), bool, error) {
	unlock, err := acquireReceiptLock(s.lockPath(path), false)
	if errors.Is(err, os.ErrExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return unlock, true, nil
}

func acquireReceiptLock(lockPath string, wait bool) (func(), error) {
	return lockfile.Acquire(lockPath, wait, receiptLockOptions)
}

func removeStaleReceiptLock(path string, now time.Time) (bool, error) {
	return lockfile.RemoveStale(path, now, receiptLockOptions)
}
