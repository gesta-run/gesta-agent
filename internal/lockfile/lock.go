package lockfile

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

type Options struct {
	Label        string
	Wait         time.Duration
	StaleAfter   time.Duration
	PollInterval time.Duration
}

type processLock struct {
	token chan struct{}
	refs  int
}

var processLockRegistry = struct {
	sync.Mutex
	locks map[string]*processLock
}{
	locks: make(map[string]*processLock),
}

func Acquire(path string, wait bool, options Options) (func(), error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create %s lock directory: %w", options.Label, err)
	}
	if err := retireStaleProcessLock(path, time.Now(), options); err != nil {
		return nil, err
	}
	deadline := time.Now().Add(options.Wait)
	releaseProcessLock, err := acquireProcessLock(path, wait, deadline)
	if errors.Is(err, os.ErrExist) {
		return nil, os.ErrExist
	}
	if err != nil {
		return nil, fmt.Errorf("%s lock timeout: %s", options.Label, path)
	}
	processLockHeld := true
	defer func() {
		if processLockHeld {
			releaseProcessLock()
		}
	}()

	for {
		lock, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err == nil {
			createdInfo, statErr := lock.Stat()
			if statErr != nil {
				_ = lock.Close()
				_ = os.Remove(path)
				return nil, fmt.Errorf("stat created %s lock: %w", options.Label, statErr)
			}
			var once sync.Once
			unlock := func() {
				once.Do(func() {
					currentInfo, currentErr := os.Stat(path)
					if currentErr == nil && os.SameFile(createdInfo, currentInfo) {
						_ = os.Remove(path)
					}
					_ = lock.Close()
					releaseProcessLock()
				})
			}
			processLockHeld = false
			return unlock, nil
		}
		if errors.Is(err, os.ErrNotExist) {
			if mkdirErr := os.MkdirAll(filepath.Dir(path), 0o700); mkdirErr != nil {
				return nil, fmt.Errorf("recreate %s lock directory: %w", options.Label, mkdirErr)
			}
			if time.Now().After(deadline) {
				return nil, fmt.Errorf("%s lock timeout: %s", options.Label, path)
			}
			continue
		}
		if !errors.Is(err, os.ErrExist) {
			return nil, fmt.Errorf("create %s lock: %w", options.Label, err)
		}
		removed, removeErr := RemoveStale(path, time.Now(), options)
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
			return nil, fmt.Errorf("%s lock timeout: %s", options.Label, path)
		}
		time.Sleep(options.PollInterval)
	}
}

func retireStaleProcessLock(path string, now time.Time, options Options) error {
	info, err := os.Stat(path)
	if err != nil || !info.ModTime().Before(now.Add(-options.StaleAfter)) {
		return nil
	}
	removed, err := RemoveStale(path, now, options)
	if err != nil || !removed {
		return err
	}
	processLockRegistry.Lock()
	delete(processLockRegistry.locks, path)
	processLockRegistry.Unlock()
	return nil
}

func acquireProcessLock(path string, wait bool, deadline time.Time) (func(), error) {
	processLockRegistry.Lock()
	lock := processLockRegistry.locks[path]
	if lock == nil {
		lock = &processLock{token: make(chan struct{}, 1)}
		lock.token <- struct{}{}
		processLockRegistry.locks[path] = lock
	}
	lock.refs++
	processLockRegistry.Unlock()

	acquired := false
	select {
	case <-lock.token:
		acquired = true
	default:
	}
	if !acquired && wait {
		remaining := time.Until(deadline)
		if remaining > 0 {
			timer := time.NewTimer(remaining)
			select {
			case <-lock.token:
				acquired = true
				if !timer.Stop() {
					<-timer.C
				}
			case <-timer.C:
			}
		}
	}
	if !acquired {
		releaseProcessLockReference(path, lock)
		if wait {
			return nil, os.ErrDeadlineExceeded
		}
		return nil, os.ErrExist
	}

	var once sync.Once
	return func() {
		once.Do(func() {
			lock.token <- struct{}{}
			releaseProcessLockReference(path, lock)
		})
	}, nil
}

func releaseProcessLockReference(path string, lock *processLock) {
	processLockRegistry.Lock()
	defer processLockRegistry.Unlock()
	lock.refs--
	if lock.refs == 0 && processLockRegistry.locks[path] == lock {
		delete(processLockRegistry.locks, path)
	}
}

func RemoveStale(path string, now time.Time, options Options) (bool, error) {
	info, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return true, nil
	}
	if err != nil {
		return false, fmt.Errorf("stat %s lock: %w", options.Label, err)
	}
	if !info.ModTime().Before(now.Add(-options.StaleAfter)) {
		return false, nil
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return false, fmt.Errorf("remove stale %s lock: %w", options.Label, err)
	}
	return true, nil
}
