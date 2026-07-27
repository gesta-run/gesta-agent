package daemon

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/gesta-run/gesta-agent/pkg/model"
	bolt "go.etcd.io/bbolt"
)

const (
	queueDrainBatchSize           = 500
	queueMaxAge                   = 30 * 24 * time.Hour
	queueMaxBytes           int64 = 512 * 1024 * 1024
	defaultQueueLockTimeout       = 5 * time.Second
)

var compactableSnapshotEventTypes = map[string]struct{}{
	"adapter.warning":             {},
	"agent.discovery":             {},
	"claude_code.config_snapshot": {},
	"claude_code.usage_summary":   {},
	"codex.usage_summary":         {},
	"daemon.system_snapshot":      {},
}

type Queue struct {
	path        string
	legacyPath  string
	now         func() time.Time
	maxAge      time.Duration
	maxBytes    int64
	lockTimeout time.Duration
}

type QueueStats struct {
	QueuedEvents     int
	QueuedBytes      int64
	RemovedExpired   int
	RemovedDuplicate int
	RemovedCapacity  int
	OldestQueuedAt   time.Time
}

type LegacyQueueStats struct {
	Events         int
	Bytes          int64
	OldestQueuedAt time.Time
	NewestQueuedAt time.Time
	EventTypes     map[string]int
}

func NewQueue(dataDir string) Queue {
	return Queue{
		path:        filepath.Join(dataDir, "queue-v2.db"),
		legacyPath:  filepath.Join(dataDir, "queue.jsonl"),
		now:         time.Now,
		maxAge:      queueMaxAge,
		maxBytes:    queueMaxBytes,
		lockTimeout: defaultQueueLockTimeout,
	}
}

func (q Queue) Append(events []model.EventEnvelope) error {
	_, err := q.AppendWithStats(events)
	return err
}

func (q Queue) AppendWithStats(events []model.EventEnvelope) (QueueStats, error) {
	db, err := q.open()
	if err != nil {
		return QueueStats{}, err
	}
	defer db.Close()

	var stats QueueStats
	err = db.Update(func(tx *bolt.Tx) error {
		if err := initializeQueueBuckets(tx); err != nil {
			return err
		}
		stats.RemovedExpired, err = q.pruneExpired(tx)
		if err != nil {
			return err
		}
		for _, event := range events {
			if event.CreatedAt.IsZero() || event.CreatedAt.Before(q.cutoff()) {
				stats.RemovedExpired++
				continue
			}
			removedDuplicate, insertErr := insertQueuedEvent(tx, event)
			if insertErr != nil {
				return insertErr
			}
			if removedDuplicate {
				stats.RemovedDuplicate++
			}
		}
		stats.RemovedCapacity, err = q.evictOverCapacity(tx)
		if err != nil {
			return err
		}
		stats = mergeQueueStats(stats, queueStatsFromTx(tx))
		return nil
	})
	return stats, err
}

func (q Queue) ReadAll() ([]model.EventEnvelope, error) {
	db, err := q.open()
	if err != nil {
		return nil, err
	}
	defer db.Close()

	var events []model.EventEnvelope
	err = db.View(func(tx *bolt.Tx) error {
		bucket := tx.Bucket(queueEventsBucket)
		if bucket == nil {
			return nil
		}
		return bucket.ForEach(func(_, value []byte) error {
			event, _, err := decodeQueuedEvent(value)
			if err != nil {
				return err
			}
			events = append(events, event)
			return nil
		})
	})
	return events, err
}

func (q Queue) Stats() (QueueStats, error) {
	db, err := q.open()
	if err != nil {
		return QueueStats{}, err
	}
	defer db.Close()

	var stats QueueStats
	err = db.View(func(tx *bolt.Tx) error {
		stats = queueStatsFromTx(tx)
		return nil
	})
	return stats, err
}

func (q Queue) Drain(send func([]model.EventEnvelope) error) error {
	unlock, err := q.acquireDrainLock()
	if err != nil {
		return err
	}
	defer unlock()

	watermark, err := q.drainWatermark()
	if err != nil {
		return err
	}
	if watermark == nil {
		return nil
	}
	for {
		batch, err := q.nextDrainBatch(watermark)
		if err != nil {
			return err
		}
		if len(batch) == 0 {
			return nil
		}
		events := make([]model.EventEnvelope, len(batch))
		for index := range batch {
			events[index] = batch[index].event
		}
		if err := send(events); err != nil {
			return err
		}
		if err := q.acknowledge(batch); err != nil {
			return fmt.Errorf("acknowledge queue batch: %w", err)
		}
	}
}

func (q Queue) Clear() error {
	db, err := q.open()
	if err != nil {
		return err
	}
	defer db.Close()
	return db.Update(resetQueueBuckets)
}

func (q Queue) Size() int {
	stats, err := q.Stats()
	if err != nil {
		return 0
	}
	return stats.QueuedEvents
}

func (q Queue) open() (*bolt.DB, error) {
	if err := ensurePrivateDirectory(filepath.Dir(q.path)); err != nil {
		return nil, err
	}
	db, err := bolt.Open(q.path, 0o600, &bolt.Options{Timeout: q.effectiveLockTimeout()})
	if err != nil {
		return nil, fmt.Errorf("open queue database %s: %w", q.path, err)
	}
	if err := db.Update(initializeQueueBuckets); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("initialize queue database %s: %w", q.path, err)
	}
	return db, nil
}

func (q Queue) effectiveLockTimeout() time.Duration {
	if q.lockTimeout > 0 {
		return q.lockTimeout
	}
	return defaultQueueLockTimeout
}

func (q Queue) cutoff() time.Time {
	now := time.Now()
	if q.now != nil {
		now = q.now()
	}
	maxAge := q.maxAge
	if maxAge <= 0 {
		maxAge = queueMaxAge
	}
	return now.UTC().Add(-maxAge)
}

func (q Queue) effectiveMaxBytes() int64 {
	if q.maxBytes > 0 {
		return q.maxBytes
	}
	return queueMaxBytes
}

func mergeQueueStats(changes, current QueueStats) QueueStats {
	current.RemovedExpired = changes.RemovedExpired
	current.RemovedDuplicate = changes.RemovedDuplicate
	current.RemovedCapacity = changes.RemovedCapacity
	return current
}

func ensurePrivateDirectory(path string) error {
	return os.MkdirAll(path, 0o700)
}
