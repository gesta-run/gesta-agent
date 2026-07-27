package daemon

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"time"

	"github.com/gesta-run/gesta-agent/pkg/model"
	bolt "go.etcd.io/bbolt"
)

var (
	queueEventsBucket       = []byte("events")
	queueTimesBucket        = []byte("event_times")
	queueSnapshotsIDBucket  = []byte("snapshots_by_id")
	queueSnapshotsSeqBucket = []byte("snapshots_by_sequence")
	queueMetaBucket         = []byte("meta")
	queueMetaCountKey       = []byte("count")
	queueMetaBytesKey       = []byte("bytes")
	queueMetaNextKey        = []byte("next_sequence")
)

type queuedBatchEvent struct {
	sequence []byte
	event    model.EventEnvelope
}

func initializeQueueBuckets(tx *bolt.Tx) error {
	for _, name := range [][]byte{
		queueEventsBucket,
		queueTimesBucket,
		queueSnapshotsIDBucket,
		queueSnapshotsSeqBucket,
		queueMetaBucket,
	} {
		if _, err := tx.CreateBucketIfNotExists(name); err != nil {
			return err
		}
	}
	return nil
}

func resetQueueBuckets(tx *bolt.Tx) error {
	for _, name := range [][]byte{
		queueEventsBucket,
		queueTimesBucket,
		queueSnapshotsIDBucket,
		queueSnapshotsSeqBucket,
		queueMetaBucket,
	} {
		if err := tx.DeleteBucket(name); err != nil && err != bolt.ErrBucketNotFound {
			return err
		}
	}
	return initializeQueueBuckets(tx)
}

func insertQueuedEvent(tx *bolt.Tx, event model.EventEnvelope) (bool, error) {
	encoded, err := json.Marshal(event)
	if err != nil {
		return false, err
	}
	removedDuplicate := false
	if isCompactableSnapshot(event) {
		snapshotSequence := tx.Bucket(queueSnapshotsIDBucket).Get([]byte(event.EventID))
		if snapshotSequence != nil {
			sequence := append([]byte(nil), snapshotSequence...)
			if err := deleteQueuedEvent(tx, sequence, true); err != nil {
				return false, err
			}
			removedDuplicate = true
		}
	}

	sequence, err := nextQueueSequence(tx)
	if err != nil {
		return false, err
	}
	stored := encodeQueuedEvent(event.CreatedAt, encoded)
	if err := tx.Bucket(queueEventsBucket).Put(sequence, stored); err != nil {
		return false, err
	}
	if err := tx.Bucket(queueTimesBucket).Put(queueTimeKey(event.CreatedAt, sequence), nil); err != nil {
		return false, err
	}
	if isCompactableSnapshot(event) {
		if err := tx.Bucket(queueSnapshotsIDBucket).Put([]byte(event.EventID), sequence); err != nil {
			return false, err
		}
		if err := tx.Bucket(queueSnapshotsSeqBucket).Put(sequence, []byte(event.EventID)); err != nil {
			return false, err
		}
	}
	if err := incrementQueueMeta(tx, 1, int64(len(encoded)+1)); err != nil {
		return false, err
	}
	return removedDuplicate, nil
}

func deleteQueuedEvent(tx *bolt.Tx, sequence []byte, removeTimeIndex bool) error {
	events := tx.Bucket(queueEventsBucket)
	stored := events.Get(sequence)
	if stored == nil {
		return nil
	}
	event, encodedBytes, err := decodeQueuedEvent(stored)
	if err != nil {
		return err
	}
	if removeTimeIndex {
		if err := tx.Bucket(queueTimesBucket).Delete(queueTimeKey(event.CreatedAt, sequence)); err != nil {
			return err
		}
	}
	snapshotID := tx.Bucket(queueSnapshotsSeqBucket).Get(sequence)
	if snapshotID != nil {
		snapshotIDCopy := append([]byte(nil), snapshotID...)
		if err := tx.Bucket(queueSnapshotsIDBucket).Delete(snapshotIDCopy); err != nil {
			return err
		}
		if err := tx.Bucket(queueSnapshotsSeqBucket).Delete(sequence); err != nil {
			return err
		}
	}
	if err := events.Delete(sequence); err != nil {
		return err
	}
	return incrementQueueMeta(tx, -1, -int64(encodedBytes+1))
}

func (q Queue) pruneExpired(tx *bolt.Tx) (int, error) {
	times := tx.Bucket(queueTimesBucket)
	cutoff := q.cutoff()
	removed := 0
	for {
		key, _ := times.Cursor().First()
		if key == nil {
			return removed, nil
		}
		createdAt, sequence, err := decodeQueueTimeKey(key)
		if err != nil {
			return removed, err
		}
		if !createdAt.Before(cutoff) {
			return removed, nil
		}
		keyCopy := append([]byte(nil), key...)
		sequenceCopy := append([]byte(nil), sequence...)
		if err := times.Delete(keyCopy); err != nil {
			return removed, err
		}
		if err := deleteQueuedEvent(tx, sequenceCopy, false); err != nil {
			return removed, err
		}
		removed++
	}
}

func (q Queue) evictOverCapacity(tx *bolt.Tx) (int, error) {
	removed := 0
	for queueMetaInt64(tx, queueMetaBytesKey) > q.effectiveMaxBytes() {
		times := tx.Bucket(queueTimesBucket)
		key, _ := times.Cursor().First()
		if key == nil {
			return removed, nil
		}
		_, sequence, err := decodeQueueTimeKey(key)
		if err != nil {
			return removed, err
		}
		keyCopy := append([]byte(nil), key...)
		sequenceCopy := append([]byte(nil), sequence...)
		if err := times.Delete(keyCopy); err != nil {
			return removed, err
		}
		if err := deleteQueuedEvent(tx, sequenceCopy, false); err != nil {
			return removed, err
		}
		removed++
	}
	return removed, nil
}

func (q Queue) drainWatermark() ([]byte, error) {
	db, err := q.open()
	if err != nil {
		return nil, err
	}
	defer db.Close()

	var watermark []byte
	err = db.Update(func(tx *bolt.Tx) error {
		if _, err := q.pruneExpired(tx); err != nil {
			return err
		}
		if sequence, _ := tx.Bucket(queueEventsBucket).Cursor().Last(); sequence != nil {
			watermark = append([]byte(nil), sequence...)
		}
		return nil
	})
	return watermark, err
}

func (q Queue) nextDrainBatch(watermark []byte) ([]queuedBatchEvent, error) {
	db, err := q.open()
	if err != nil {
		return nil, err
	}
	defer db.Close()

	var batch []queuedBatchEvent
	err = db.Update(func(tx *bolt.Tx) error {
		if _, err := q.pruneExpired(tx); err != nil {
			return err
		}
		cursor := tx.Bucket(queueEventsBucket).Cursor()
		for sequence, stored := cursor.First(); sequence != nil && len(batch) < queueDrainBatchSize; sequence, stored = cursor.Next() {
			if bytes.Compare(sequence, watermark) > 0 {
				break
			}
			event, _, err := decodeQueuedEvent(stored)
			if err != nil {
				return err
			}
			batch = append(batch, queuedBatchEvent{
				sequence: append([]byte(nil), sequence...),
				event:    event,
			})
		}
		return nil
	})
	return batch, err
}

func (q Queue) acknowledge(batch []queuedBatchEvent) error {
	db, err := q.open()
	if err != nil {
		return err
	}
	defer db.Close()
	return db.Update(func(tx *bolt.Tx) error {
		for _, queued := range batch {
			if err := deleteQueuedEvent(tx, queued.sequence, true); err != nil {
				return err
			}
		}
		return nil
	})
}

func queueStatsFromTx(tx *bolt.Tx) QueueStats {
	stats := QueueStats{
		QueuedEvents: int(queueMetaInt64(tx, queueMetaCountKey)),
		QueuedBytes:  queueMetaInt64(tx, queueMetaBytesKey),
	}
	if times := tx.Bucket(queueTimesBucket); times != nil {
		if key, _ := times.Cursor().First(); key != nil {
			if createdAt, _, err := decodeQueueTimeKey(key); err == nil {
				stats.OldestQueuedAt = createdAt
			}
		}
	}
	return stats
}

func nextQueueSequence(tx *bolt.Tx) ([]byte, error) {
	meta := tx.Bucket(queueMetaBucket)
	next := decodeUint64(meta.Get(queueMetaNextKey)) + 1
	encoded := encodeUint64(next)
	if err := meta.Put(queueMetaNextKey, encoded); err != nil {
		return nil, err
	}
	return encoded, nil
}

func incrementQueueMeta(tx *bolt.Tx, countDelta, bytesDelta int64) error {
	meta := tx.Bucket(queueMetaBucket)
	count := queueMetaInt64(tx, queueMetaCountKey) + countDelta
	bytes := queueMetaInt64(tx, queueMetaBytesKey) + bytesDelta
	if count < 0 || bytes < 0 {
		return fmt.Errorf("queue metadata underflow: count=%d bytes=%d", count, bytes)
	}
	if err := meta.Put(queueMetaCountKey, encodeUint64(uint64(count))); err != nil {
		return err
	}
	return meta.Put(queueMetaBytesKey, encodeUint64(uint64(bytes)))
}

func queueMetaInt64(tx *bolt.Tx, key []byte) int64 {
	meta := tx.Bucket(queueMetaBucket)
	if meta == nil {
		return 0
	}
	return int64(decodeUint64(meta.Get(key)))
}

func encodeQueuedEvent(createdAt time.Time, encoded []byte) []byte {
	stored := make([]byte, 8+len(encoded))
	binary.BigEndian.PutUint64(stored[:8], uint64(createdAt.UnixNano()))
	copy(stored[8:], encoded)
	return stored
}

func decodeQueuedEvent(stored []byte) (model.EventEnvelope, int, error) {
	if len(stored) < 8 {
		return model.EventEnvelope{}, 0, fmt.Errorf("queue record is truncated")
	}
	var event model.EventEnvelope
	if err := json.Unmarshal(stored[8:], &event); err != nil {
		return model.EventEnvelope{}, 0, fmt.Errorf("decode queue record: %w", err)
	}
	return event, len(stored) - 8, nil
}

func queueTimeKey(createdAt time.Time, sequence []byte) []byte {
	key := make([]byte, 16)
	orderedTimestamp := uint64(createdAt.UnixNano()) ^ (uint64(1) << 63)
	binary.BigEndian.PutUint64(key[:8], orderedTimestamp)
	copy(key[8:], sequence)
	return key
}

func decodeQueueTimeKey(key []byte) (time.Time, []byte, error) {
	if len(key) != 16 {
		return time.Time{}, nil, fmt.Errorf("invalid queue time key length %d", len(key))
	}
	timestamp := int64(binary.BigEndian.Uint64(key[:8]) ^ (uint64(1) << 63))
	return time.Unix(0, timestamp).UTC(), key[8:], nil
}

func encodeUint64(value uint64) []byte {
	encoded := make([]byte, 8)
	binary.BigEndian.PutUint64(encoded, value)
	return encoded
}

func decodeUint64(encoded []byte) uint64 {
	if len(encoded) != 8 {
		return 0
	}
	return binary.BigEndian.Uint64(encoded)
}

func isCompactableSnapshot(event model.EventEnvelope) bool {
	_, compactable := compactableSnapshotEventTypes[event.EventType]
	return compactable && event.EventID != ""
}
