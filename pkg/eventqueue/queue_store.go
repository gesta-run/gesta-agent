package eventqueue

import (
	"bytes"
	"encoding/json"

	"github.com/gesta-run/gesta-agent/pkg/model"
	bolt "go.etcd.io/bbolt"
)

var (
	queueEventsBucket        = []byte("events")
	queueTimesBucket         = []byte("event_times")
	queueEventIDsBucket      = []byte("events_by_id")
	queueEventSeqsBucket     = []byte("events_by_sequence")
	queueDeliveredIDsBucket  = []byte("delivered_by_id")
	queueDeliveredTimeBucket = []byte("delivered_by_time")
	queueMetaBucket          = []byte("meta")
	queueMetaCountKey        = []byte("count")
	queueMetaBytesKey        = []byte("bytes")
	queueMetaNextKey         = []byte("next_sequence")
	queueMetaEventIDsKey     = []byte("event_id_index_version")
)

type queuedBatchEvent struct {
	sequence []byte
	event    model.EventEnvelope
}

func initializeQueueBuckets(tx *bolt.Tx) error {
	for _, name := range [][]byte{
		queueEventsBucket,
		queueTimesBucket,
		queueEventIDsBucket,
		queueEventSeqsBucket,
		queueDeliveredIDsBucket,
		queueDeliveredTimeBucket,
		queueMetaBucket,
	} {
		if _, err := tx.CreateBucketIfNotExists(name); err != nil {
			return err
		}
	}
	return backfillQueuedEventIDs(tx)
}

func insertQueuedEvent(tx *bolt.Tx, event model.EventEnvelope) (bool, error) {
	removedDuplicate := false
	if event.EventID != "" {
		eventID := []byte(event.EventID)
		if tx.Bucket(queueDeliveredIDsBucket).Get(eventID) != nil {
			return true, nil
		}
		if queuedSequence := tx.Bucket(queueEventIDsBucket).Get(eventID); queuedSequence != nil {
			if !isCompactableSnapshot(event) {
				return true, nil
			}
			sequence := append([]byte(nil), queuedSequence...)
			if err := deleteQueuedEvent(tx, sequence, true); err != nil {
				return false, err
			}
			removedDuplicate = true
		}
	}

	encoded, err := json.Marshal(event)
	if err != nil {
		return false, err
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
	if event.EventID != "" {
		if err := tx.Bucket(queueEventIDsBucket).Put([]byte(event.EventID), sequence); err != nil {
			return false, err
		}
		if err := tx.Bucket(queueEventSeqsBucket).Put(sequence, []byte(event.EventID)); err != nil {
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
	eventID := tx.Bucket(queueEventSeqsBucket).Get(sequence)
	if eventID != nil {
		eventIDCopy := append([]byte(nil), eventID...)
		indexedSequence := tx.Bucket(queueEventIDsBucket).Get(eventIDCopy)
		if bytes.Equal(indexedSequence, sequence) {
			if err := tx.Bucket(queueEventIDsBucket).Delete(eventIDCopy); err != nil {
				return err
			}
		}
		if err := tx.Bucket(queueEventSeqsBucket).Delete(sequence); err != nil {
			return err
		}
	}
	if err := events.Delete(sequence); err != nil {
		return err
	}
	return incrementQueueMeta(tx, -1, -int64(encodedBytes+1))
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
			if queued.event.EventID != "" {
				if err := recordDeliveredEventID(tx, queued.event.EventID, q.currentTime()); err != nil {
					return err
				}
			}
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
