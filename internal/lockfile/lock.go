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

func Acquire(path string, wait bool, options Options) (func(), error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create %s lock directory: %w", options.Label, err)
	}
	deadline := time.Now().Add(options.Wait)
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
			return func() {
				once.Do(func() {
					currentInfo, currentErr := os.Stat(path)
					if currentErr == nil && os.SameFile(createdInfo, currentInfo) {
						_ = os.Remove(path)
					}
					_ = lock.Close()
				})
			}, nil
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
