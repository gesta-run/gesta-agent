package lockfile

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestAcquireSerializesSameProcessCallers(t *testing.T) {
	path := filepath.Join(t.TempDir(), "store.lock")
	options := testOptions()
	const callers = 48
	var wait sync.WaitGroup
	errs := make(chan error, callers)
	for range callers {
		wait.Add(1)
		go func() {
			defer wait.Done()
			unlock, err := Acquire(path, true, options)
			if err == nil {
				unlock()
			}
			errs <- err
		}()
	}
	wait.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("Acquire: %v", err)
		}
	}
}

func TestAcquireNonWaitingCallerReturnsExists(t *testing.T) {
	path := filepath.Join(t.TempDir(), "store.lock")
	options := testOptions()
	unlock, err := Acquire(path, true, options)
	if err != nil {
		t.Fatalf("first Acquire: %v", err)
	}
	defer unlock()
	if _, err := Acquire(path, false, options); !errors.Is(err, os.ErrExist) {
		t.Fatalf("second Acquire error = %v, want os.ErrExist", err)
	}
}

func TestAcquirePreservesWaitDeadline(t *testing.T) {
	path := filepath.Join(t.TempDir(), "store.lock")
	options := testOptions()
	unlock, err := Acquire(path, true, options)
	if err != nil {
		t.Fatalf("first Acquire: %v", err)
	}
	defer unlock()

	options.Wait = 20 * time.Millisecond
	started := time.Now()
	if _, err := Acquire(path, true, options); err == nil {
		t.Fatal("second Acquire succeeded while lock was held")
	}
	if elapsed := time.Since(started); elapsed < options.Wait {
		t.Fatalf("Acquire returned after %s, before wait deadline %s", elapsed, options.Wait)
	}
}

func testOptions() Options {
	return Options{
		Label:        "test",
		Wait:         2 * time.Second,
		StaleAfter:   time.Minute,
		PollInterval: 10 * time.Millisecond,
	}
}
