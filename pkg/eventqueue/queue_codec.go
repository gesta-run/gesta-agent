package eventqueue

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"time"

	"github.com/gesta-run/gesta-agent/pkg/model"
	bolt "go.etcd.io/bbolt"
)

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

func deliveredTimeKey(deliveredAt time.Time, eventID string) []byte {
	key := make([]byte, 8+len(eventID))
	orderedTimestamp := uint64(deliveredAt.UnixNano()) ^ (uint64(1) << 63)
	binary.BigEndian.PutUint64(key[:8], orderedTimestamp)
	copy(key[8:], eventID)
	return key
}

func decodeDeliveredTimeKey(key []byte) (time.Time, []byte, error) {
	if len(key) <= 8 {
		return time.Time{}, nil, fmt.Errorf("invalid delivered event time key length %d", len(key))
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
