package turnreceipt

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

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
	if err := os.MkdirAll(filepath.Dir(lockPath), 0o700); err != nil {
		return nil, fmt.Errorf("create turn receipt lock directory: %w", err)
	}
	deadline := time.Now().Add(receiptLockWait)
	for {
		lock, err := os.OpenFile(lockPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err == nil {
			createdInfo, statErr := lock.Stat()
			if statErr != nil {
				_ = lock.Close()
				_ = os.Remove(lockPath)
				return nil, fmt.Errorf("stat created turn receipt lock: %w", statErr)
			}
			var once sync.Once
			return func() {
				once.Do(func() {
					currentInfo, currentErr := os.Stat(lockPath)
					if currentErr == nil && os.SameFile(createdInfo, currentInfo) {
						_ = os.Remove(lockPath)
					}
					_ = lock.Close()
				})
			}, nil
		}
		if errors.Is(err, os.ErrNotExist) {
			if mkdirErr := os.MkdirAll(filepath.Dir(lockPath), 0o700); mkdirErr != nil {
				return nil, fmt.Errorf("recreate turn receipt lock directory: %w", mkdirErr)
			}
			if time.Now().After(deadline) {
				return nil, fmt.Errorf("turn receipt lock timeout: %s", lockPath)
			}
			continue
		}
		if !errors.Is(err, os.ErrExist) {
			return nil, fmt.Errorf("create turn receipt lock: %w", err)
		}
		removed, removeErr := removeStaleReceiptLock(lockPath, time.Now())
		if removeErr != nil {
			return nil, removeErr
		}
		if removed {
			continue
		}
		if !wait {
			return nil, os.ErrExist
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("turn receipt lock timeout: %s", lockPath)
		}
		time.Sleep(receiptLockPollInterval)
	}
}

func removeStaleReceiptLock(path string, now time.Time) (bool, error) {
	info, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return true, nil
	}
	if err != nil {
		return false, fmt.Errorf("stat turn receipt lock: %w", err)
	}
	if !info.ModTime().Before(now.Add(-receiptLockStaleAfter)) {
		return false, nil
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return false, fmt.Errorf("remove stale turn receipt lock: %w", err)
	}
	return true, nil
}
