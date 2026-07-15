package daemon

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/gesta-run/gesta-agent/pkg/model"
)

const queueDrainBatchSize = 500

type Queue struct {
	path string
}

func NewQueue(dataDir string) Queue {
	return Queue{path: filepath.Join(dataDir, "queue.jsonl")}
}

func (q Queue) Append(events []model.EventEnvelope) error {
	if len(events) == 0 {
		return nil
	}
	return q.withLock(func() error {
		file, err := os.OpenFile(q.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
		if err != nil {
			return err
		}
		defer file.Close()
		enc := json.NewEncoder(file)
		for _, event := range events {
			if err := enc.Encode(event); err != nil {
				return err
			}
		}
		return nil
	})
}

func (q Queue) ReadAll() ([]model.EventEnvelope, error) {
	return readQueueFile(q.path)
}

func (q Queue) Drain(send func([]model.EventEnvelope) error) error {
	rotated := false
	if err := q.withLock(func() error {
		if err := q.restoreInflightLocked(); err != nil {
			return err
		}
		if err := os.Rename(q.path, q.inflightPath()); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return nil
			}
			return err
		}
		rotated = true
		return nil
	}); err != nil {
		return err
	}
	if !rotated {
		return nil
	}

	events, err := readQueueFile(q.inflightPath())
	if err != nil {
		_ = q.requeueInflight()
		return err
	}
	if len(events) == 0 {
		return removeIfExists(q.inflightPath())
	}
	for len(events) > 0 {
		batchSize := queueDrainBatchSize
		if len(events) < batchSize {
			batchSize = len(events)
		}
		if err := send(events[:batchSize]); err != nil {
			if requeueErr := q.requeueInflight(); requeueErr != nil {
				return fmt.Errorf("%w; failed to requeue in-flight events: %v", err, requeueErr)
			}
			return err
		}
		events = events[batchSize:]
		if len(events) == 0 {
			break
		}
		if err := writeQueueFile(q.inflightPath(), events); err != nil {
			return err
		}
	}
	return removeIfExists(q.inflightPath())
}

func readQueueFile(path string) ([]model.EventEnvelope, error) {
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return []model.EventEnvelope{}, nil
	}
	if err != nil {
		return nil, err
	}
	defer file.Close()
	var events []model.EventEnvelope
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), 10*1024*1024)
	line := 0
	for scanner.Scan() {
		line++
		var event model.EventEnvelope
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			return nil, fmt.Errorf("read queue %s line %d: %w", path, line, err)
		}
		events = append(events, event)
	}
	return events, scanner.Err()
}

func writeQueueFile(path string, events []model.EventEnvelope) error {
	if len(events) == 0 {
		return os.WriteFile(path, nil, 0o600)
	}
	tmpPath := path + ".tmp"
	file, err := os.OpenFile(tmpPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	enc := json.NewEncoder(file)
	for _, event := range events {
		if err := enc.Encode(event); err != nil {
			_ = file.Close()
			_ = os.Remove(tmpPath)
			return err
		}
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	return os.Rename(tmpPath, path)
}

func (q Queue) Clear() error {
	return q.withLock(func() error {
		if err := os.WriteFile(q.path, nil, 0o600); err != nil {
			return err
		}
		return removeIfExists(q.inflightPath())
	})
}

func (q Queue) Size() int {
	events, err := q.ReadAll()
	if err != nil {
		return 0
	}
	return len(events)
}

func (q Queue) inflightPath() string {
	return q.path + ".inflight"
}

func (q Queue) lockPath() string {
	return q.path + ".lock"
}

func (q Queue) withLock(fn func() error) error {
	if err := os.MkdirAll(filepath.Dir(q.path), 0o700); err != nil {
		return err
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		lock, err := os.OpenFile(q.lockPath(), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err == nil {
			_ = lock.Close()
			defer os.Remove(q.lockPath())
			return fn()
		}
		if !errors.Is(err, os.ErrExist) {
			return err
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("queue lock timeout: %s", q.lockPath())
		}
		time.Sleep(25 * time.Millisecond)
	}
}

func (q Queue) requeueInflight() error {
	return q.withLock(func() error {
		return q.restoreInflightLocked()
	})
}

func (q Queue) restoreInflightLocked() error {
	inflight, err := os.ReadFile(q.inflightPath())
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	active, err := os.ReadFile(q.path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	var restored bytes.Buffer
	restored.Write(inflight)
	if len(inflight) > 0 && !bytes.HasSuffix(inflight, []byte("\n")) {
		restored.WriteByte('\n')
	}
	restored.Write(active)
	tmpPath := q.path + ".restore"
	if err := os.WriteFile(tmpPath, restored.Bytes(), 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, q.path); err != nil {
		return err
	}
	return removeIfExists(q.inflightPath())
}

func removeIfExists(path string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}
