package eventqueue

import (
	"time"

	bolt "go.etcd.io/bbolt"
)

func backfillQueuedEventIDs(tx *bolt.Tx) error {
	meta := tx.Bucket(queueMetaBucket)
	if decodeUint64(meta.Get(queueMetaEventIDsKey)) >= 1 {
		return nil
	}
	events := tx.Bucket(queueEventsBucket)
	cursor := events.Cursor()
	for sequence, stored := cursor.First(); sequence != nil; sequence, stored = cursor.Next() {
		event, _, err := decodeQueuedEvent(stored)
		if err != nil {
			return err
		}
		if event.EventID == "" {
			continue
		}
		sequenceCopy := append([]byte(nil), sequence...)
		if err := tx.Bucket(queueEventIDsBucket).Put([]byte(event.EventID), sequenceCopy); err != nil {
			return err
		}
		if err := tx.Bucket(queueEventSeqsBucket).Put(sequenceCopy, []byte(event.EventID)); err != nil {
			return err
		}
	}
	return meta.Put(queueMetaEventIDsKey, encodeUint64(1))
}

func recordDeliveredEventID(tx *bolt.Tx, eventID string, deliveredAt time.Time) error {
	id := []byte(eventID)
	ids := tx.Bucket(queueDeliveredIDsBucket)
	times := tx.Bucket(queueDeliveredTimeBucket)
	if previousKey := ids.Get(id); previousKey != nil {
		if err := times.Delete(previousKey); err != nil {
			return err
		}
	}
	timeKey := deliveredTimeKey(deliveredAt, eventID)
	if err := ids.Put(id, timeKey); err != nil {
		return err
	}
	return times.Put(timeKey, nil)
}
