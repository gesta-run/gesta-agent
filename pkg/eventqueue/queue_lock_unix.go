//go:build darwin || linux

package eventqueue

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"
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
	for {
		err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return func() {
				_ = syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
				_ = lock.Close()
			}, nil
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EAGAIN) {
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
