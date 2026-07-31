package eventqueue

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/gesta-run/gesta-agent/pkg/model"
	bolt "go.etcd.io/bbolt"
)

const quarantineReasonMaxBytes = 1000

// QuarantinedEventsError reports events removed from the active delivery queue
// after the control plane permanently rejected them.
type QuarantinedEventsError struct {
	EventIDs []string
	Reason   string
}

func (e *QuarantinedEventsError) Error() string {
	return fmt.Sprintf(
		"quarantined %d permanently rejected event(s): %s: %s",
		len(e.EventIDs),
		strings.Join(e.EventIDs, ", "),
		e.Reason,
	)
}

type quarantinedEventRecord struct {
	QuarantinedAt time.Time           `json:"quarantined_at"`
	Reason        string              `json:"reason"`
	Event         model.EventEnvelope `json:"event"`
}

func (q Queue) quarantineRejectedEvents(
	batch []queuedBatchEvent,
	rejectedEventIDs []string,
	reason string,
) ([]string, error) {
	rejected := make(map[string]struct{}, len(rejectedEventIDs))
	for _, eventID := range rejectedEventIDs {
		eventID = strings.TrimSpace(eventID)
		if eventID == "" {
			return nil, fmt.Errorf("rejected event ID is empty")
		}
		rejected[eventID] = struct{}{}
	}
	if len(rejected) == 0 {
		return nil, fmt.Errorf("rejected event IDs are empty")
	}

	byID := make(map[string]queuedBatchEvent, len(rejected))
	for _, queued := range batch {
		if _, ok := rejected[queued.event.EventID]; ok {
			byID[queued.event.EventID] = queued
		}
	}
	if len(byID) != len(rejected) {
		return nil, fmt.Errorf("rejected event IDs do not match the active queue batch")
	}

	if len(reason) > quarantineReasonMaxBytes {
		reason = reason[:quarantineReasonMaxBytes]
	}
	db, err := q.open()
	if err != nil {
		return nil, err
	}
	defer db.Close()

	moved := make([]string, 0, len(rejected))
	err = db.Update(func(tx *bolt.Tx) error {
		quarantine := tx.Bucket(queueQuarantineBucket)
		for _, queued := range batch {
			eventID := queued.event.EventID
			if _, ok := rejected[eventID]; !ok {
				continue
			}
			record, err := json.Marshal(quarantinedEventRecord{
				QuarantinedAt: q.currentTime(),
				Reason:        reason,
				Event:         queued.event,
			})
			if err != nil {
				return fmt.Errorf("encode quarantined event %s: %w", eventID, err)
			}
			if err := quarantine.Put([]byte(eventID), record); err != nil {
				return fmt.Errorf("store quarantined event %s: %w", eventID, err)
			}
			if err := deleteQueuedEvent(tx, queued.sequence, true); err != nil {
				return fmt.Errorf("remove quarantined event %s from active queue: %w", eventID, err)
			}
			moved = append(moved, eventID)
		}
		return nil
	})
	return moved, err
}
