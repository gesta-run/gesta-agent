//go:build windows

package eventqueue

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/sys/windows"
)

func (q Queue) acquireDrainLock() (func(), error) {
	if err := ensurePrivateDirectory(filepath.Dir(q.path)); err != nil {
		return nil, err
	}
	lockPath := q.path + ".drain.lock"
	lock, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	deadline := time.Now().Add(q.effectiveLockTimeout())
	var overlapped windows.Overlapped
	for {
		err = windows.LockFileEx(
			windows.Handle(lock.Fd()),
			windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY,
			0,
			1,
			0,
			&overlapped,
		)
		if err == nil {
			return func() {
				_ = windows.UnlockFileEx(windows.Handle(lock.Fd()), 0, 1, 0, &overlapped)
				_ = lock.Close()
			}, nil
		}
		if !errors.Is(err, windows.ERROR_LOCK_VIOLATION) &&
			!errors.Is(err, windows.ERROR_IO_PENDING) {
			_ = lock.Close()
			return nil, err
		}
		if time.Now().After(deadline) {
			_ = lock.Close()
			return nil, fmt.Errorf("queue drain lock timeout: %s", lockPath)
		}
		time.Sleep(25 * time.Millisecond)
	}
}
