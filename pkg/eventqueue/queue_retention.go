package eventqueue

import (
	"bytes"
	"time"

	bolt "go.etcd.io/bbolt"
)

func (q Queue) pruneExpired(tx *bolt.Tx) (int, error) {
	if err := pruneDeliveredEventIDs(tx, q.cutoff()); err != nil {
		return 0, err
	}
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

func pruneDeliveredEventIDs(tx *bolt.Tx, cutoff time.Time) error {
	ids := tx.Bucket(queueDeliveredIDsBucket)
	times := tx.Bucket(queueDeliveredTimeBucket)
	cursor := times.Cursor()
	for key, _ := cursor.First(); key != nil; key, _ = cursor.First() {
		deliveredAt, eventID, err := decodeDeliveredTimeKey(key)
		if err != nil {
			return err
		}
		if !deliveredAt.Before(cutoff) {
			return nil
		}
		keyCopy := append([]byte(nil), key...)
		eventIDCopy := append([]byte(nil), eventID...)
		if bytes.Equal(ids.Get(eventIDCopy), keyCopy) {
			if err := ids.Delete(eventIDCopy); err != nil {
				return err
			}
		}
		if err := times.Delete(keyCopy); err != nil {
			return err
		}
	}
	return nil
}
