package eventqueue

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gesta-run/gesta-agent/pkg/model"
	bolt "go.etcd.io/bbolt"
)

func drainAll(q Queue) ([]model.EventEnvelope, error) {
	var drained []model.EventEnvelope
	err := q.Drain(func(events []model.EventEnvelope) error {
		drained = append(drained, events...)
		return nil
	})
	return drained, err
}

type rejectedEventsTestError struct {
	eventIDs []string
}

func (e rejectedEventsTestError) Error() string {
	return "permanently rejected"
}

func (e rejectedEventsTestError) RejectedEventIDs() []string {
	return e.eventIDs
}

func TestQueueAppendAndDrain(t *testing.T) {
	q := NewQueue(t.TempDir())
	now := time.Now().UTC()
	events := []model.EventEnvelope{
		{EventID: "evt_1", EventType: "test", CreatedAt: now},
		{EventID: "evt_2", EventType: "test", CreatedAt: now.Add(time.Second)},
	}
	if err := q.Append(events); err != nil {
		t.Fatalf("append: %v", err)
	}
	if got := q.Size(); got != 2 {
		t.Fatalf("queue size = %d, want 2", got)
	}
	read, err := drainAll(q)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(read) != 2 || read[0].EventID != "evt_1" || read[1].EventID != "evt_2" {
		t.Fatalf("events = %#v", read)
	}
	if got := q.Size(); got != 0 {
		t.Fatalf("queue size after drain = %d, want 0", got)
	}
}

func TestQueueDrainReturnsCorruptDatabaseError(t *testing.T) {
	q := NewQueue(t.TempDir())
	if err := os.WriteFile(q.path, []byte("not-a-bbolt-database"), 0o600); err != nil {
		t.Fatalf("write corrupt database: %v", err)
	}
	if _, err := drainAll(q); err == nil {
		t.Fatal("corrupt queue database was accepted")
	}
}

func TestQueueUsesCurrentProtocolDatabaseAndIgnoresLegacyQueue(t *testing.T) {
	dir := t.TempDir()
	legacy := model.EventEnvelope{
		EventID:   "evt_legacy",
		EventType: "policy.decision",
		CreatedAt: time.Now().UTC(),
	}
	if err := writeLegacyQueue(filepath.Join(dir, "queue.jsonl"), []model.EventEnvelope{legacy}); err != nil {
		t.Fatalf("write legacy queue: %v", err)
	}
	q := NewQueue(dir)
	if got, want := filepath.Base(q.path), "queue-v3.db"; got != want {
		t.Fatalf("queue database = %q, want %q", got, want)
	}
	if err := q.Append([]model.EventEnvelope{{
		EventID:   "evt_v3",
		EventType: "policy.decision",
		CreatedAt: time.Now().UTC(),
	}}); err != nil {
		t.Fatalf("append current queue: %v", err)
	}
	events, err := drainAll(q)
	if err != nil {
		t.Fatalf("read current queue: %v", err)
	}
	if len(events) != 1 || events[0].EventID != "evt_v3" {
		t.Fatalf("current queue events = %#v, want only evt_v3", events)
	}
	stats, err := q.LegacyStats()
	if err != nil {
		t.Fatalf("legacy stats: %v", err)
	}
	if stats.Events != 1 || stats.EventTypes["policy.decision"] != 1 {
		t.Fatalf("legacy stats = %#v", stats)
	}
}

func TestQueueAppendRemovesExpiredEvents(t *testing.T) {
	now := time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)
	q := NewQueue(t.TempDir())
	q.now = func() time.Time { return now }
	stats, err := q.AppendWithStats([]model.EventEnvelope{
		{EventID: "evt_expired", EventType: "policy.decision", CreatedAt: now.Add(-queueMaxAge - time.Second)},
		{EventID: "evt_current", EventType: "policy.decision", CreatedAt: now},
	})
	if err != nil {
		t.Fatalf("append: %v", err)
	}
	if stats.RemovedExpired != 1 || stats.QueuedEvents != 1 {
		t.Fatalf("stats = %#v", stats)
	}
}

func TestQueueEmptyAppendPrunesExistingEvents(t *testing.T) {
	initial := time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)
	q := NewQueue(t.TempDir())
	q.now = func() time.Time { return initial }
	if err := q.Append([]model.EventEnvelope{{
		EventID:   "evt_expired",
		EventType: "policy.decision",
		CreatedAt: initial,
	}}); err != nil {
		t.Fatalf("seed queue: %v", err)
	}
	q.now = func() time.Time { return initial.Add(queueMaxAge + time.Second) }
	stats, err := q.AppendWithStats(nil)
	if err != nil {
		t.Fatalf("prune empty append: %v", err)
	}
	if stats.RemovedExpired != 1 || stats.QueuedEvents != 0 {
		t.Fatalf("stats = %#v", stats)
	}
}

func TestQueueStatsReadsMetadataWithoutPruning(t *testing.T) {
	initial := time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)
	q := NewQueue(t.TempDir())
	q.now = func() time.Time { return initial }
	if err := q.Append([]model.EventEnvelope{{
		EventID: "evt_old", EventType: "test", CreatedAt: initial,
	}}); err != nil {
		t.Fatalf("append: %v", err)
	}
	q.now = func() time.Time { return initial.Add(queueMaxAge + time.Hour) }
	stats, err := q.Stats()
	if err != nil {
		t.Fatalf("stats: %v", err)
	}
	if stats.QueuedEvents != 1 || stats.RemovedExpired != 0 {
		t.Fatalf("stats unexpectedly compacted queue: %#v", stats)
	}
}

func TestQueueAppendDeduplicatesSnapshotsKeepingNewest(t *testing.T) {
	now := time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)
	q := NewQueue(t.TempDir())
	q.now = func() time.Time { return now }
	stats, err := q.AppendWithStats([]model.EventEnvelope{
		{EventID: "evt_snapshot_same", EventType: "agent.discovery", CreatedAt: now.Add(-time.Hour)},
		{EventID: "evt_policy", EventType: "policy.decision", CreatedAt: now.Add(-30 * time.Minute)},
		{EventID: "evt_snapshot_same", EventType: "agent.discovery", CreatedAt: now},
	})
	if err != nil {
		t.Fatalf("append: %v", err)
	}
	if stats.RemovedDuplicate != 1 || stats.QueuedEvents != 2 {
		t.Fatalf("stats = %#v", stats)
	}
	events, err := drainAll(q)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(events) != 2 || events[0].EventID != "evt_policy" || !events[1].CreatedAt.Equal(now) {
		t.Fatalf("events = %#v, want policy followed by newest snapshot", events)
	}
}

func TestQueueAppendDeduplicatesQueuedEventsByEventID(t *testing.T) {
	now := time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)
	q := NewQueue(t.TempDir())
	q.now = func() time.Time { return now }
	stats, err := q.AppendWithStats([]model.EventEnvelope{
		{EventID: "evt_tool_call", EventType: "tool.call", CreatedAt: now},
		{EventID: "evt_tool_call", EventType: "tool.call", CreatedAt: now.Add(time.Second)},
	})
	if err != nil {
		t.Fatalf("append: %v", err)
	}
	if stats.RemovedDuplicate != 1 || stats.QueuedEvents != 1 {
		t.Fatalf("stats = %#v", stats)
	}
	events, err := drainAll(q)
	if err != nil {
		t.Fatalf("drain: %v", err)
	}
	if len(events) != 1 || !events[0].CreatedAt.Equal(now) {
		t.Fatalf("events = %#v, want first immutable event", events)
	}
}

func TestQueueRemembersDeliveredEventIDs(t *testing.T) {
	now := time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)
	q := NewQueue(t.TempDir())
	q.now = func() time.Time { return now }
	event := model.EventEnvelope{EventID: "evt_delivered", EventType: "tool.call", CreatedAt: now}
	if err := q.Append([]model.EventEnvelope{event}); err != nil {
		t.Fatalf("append: %v", err)
	}
	if _, err := drainAll(q); err != nil {
		t.Fatalf("drain: %v", err)
	}
	stats, err := q.AppendWithStats([]model.EventEnvelope{event})
	if err != nil {
		t.Fatalf("append delivered event: %v", err)
	}
	if stats.RemovedDuplicate != 1 || stats.QueuedEvents != 0 {
		t.Fatalf("stats = %#v", stats)
	}
}

func TestQueueBackfillsEventIDsWithoutDeletingExistingEvents(t *testing.T) {
	dir := t.TempDir()
	q := NewQueue(dir)
	now := time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)
	event := model.EventEnvelope{EventID: "evt_existing", EventType: "tool.call", CreatedAt: now}
	encoded, err := json.Marshal(event)
	if err != nil {
		t.Fatalf("marshal event: %v", err)
	}
	db, err := bolt.Open(q.path, 0o600, nil)
	if err != nil {
		t.Fatalf("open legacy queue: %v", err)
	}
	err = db.Update(func(tx *bolt.Tx) error {
		for _, bucket := range [][]byte{
			queueEventsBucket,
			queueTimesBucket,
			queueMetaBucket,
		} {
			if _, err := tx.CreateBucket(bucket); err != nil {
				return err
			}
		}
		sequence := encodeUint64(1)
		if err := tx.Bucket(queueEventsBucket).Put(sequence, encodeQueuedEvent(now, encoded)); err != nil {
			return err
		}
		if err := tx.Bucket(queueTimesBucket).Put(queueTimeKey(now, sequence), nil); err != nil {
			return err
		}
		if err := tx.Bucket(queueMetaBucket).Put(queueMetaCountKey, encodeUint64(1)); err != nil {
			return err
		}
		if err := tx.Bucket(queueMetaBucket).Put(queueMetaBytesKey, encodeUint64(uint64(len(encoded)+1))); err != nil {
			return err
		}
		return tx.Bucket(queueMetaBucket).Put(queueMetaNextKey, sequence)
	})
	if closeErr := db.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		t.Fatalf("seed legacy queue: %v", err)
	}

	q.now = func() time.Time { return now }
	stats, err := q.AppendWithStats([]model.EventEnvelope{event})
	if err != nil {
		t.Fatalf("append existing event: %v", err)
	}
	if stats.RemovedDuplicate != 1 || stats.QueuedEvents != 1 {
		t.Fatalf("stats = %#v", stats)
	}
}

func TestQueueForgetsDeliveredEventIDsAfterRetention(t *testing.T) {
	now := time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)
	q := NewQueue(t.TempDir())
	q.now = func() time.Time { return now }
	event := model.EventEnvelope{EventID: "evt_delivered", EventType: "tool.call", CreatedAt: now}
	if err := q.Append([]model.EventEnvelope{event}); err != nil {
		t.Fatalf("append: %v", err)
	}
	if _, err := drainAll(q); err != nil {
		t.Fatalf("drain: %v", err)
	}
	q.now = func() time.Time { return now.Add(queueMaxAge + time.Second) }
	event.CreatedAt = q.currentTime()
	stats, err := q.AppendWithStats([]model.EventEnvelope{event})
	if err != nil {
		t.Fatalf("append after retention: %v", err)
	}
	if stats.RemovedDuplicate != 0 || stats.QueuedEvents != 1 {
		t.Fatalf("stats = %#v", stats)
	}
}

func TestQueueAppendEvictsOldestEventsAtCapacity(t *testing.T) {
	now := time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)
	q := NewQueue(t.TempDir())
	q.now = func() time.Time { return now }
	newest := model.EventEnvelope{
		EventID:   "evt_newest",
		EventType: "policy.decision",
		CreatedAt: now,
		Payload:   map[string]interface{}{"value": strings.Repeat("x", 64)},
	}
	line, err := json.Marshal(newest)
	if err != nil {
		t.Fatalf("marshal newest: %v", err)
	}
	q.maxBytes = int64(len(line) + 1)
	stats, err := q.AppendWithStats([]model.EventEnvelope{
		{
			EventID:   "evt_oldest",
			EventType: "policy.decision",
			CreatedAt: now.Add(-time.Hour),
			Payload:   map[string]interface{}{"value": strings.Repeat("x", 64)},
		},
		newest,
	})
	if err != nil {
		t.Fatalf("append: %v", err)
	}
	if stats.RemovedCapacity != 1 || stats.QueuedEvents != 1 {
		t.Fatalf("stats = %#v", stats)
	}
	events, err := drainAll(q)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(events) != 1 || events[0].EventID != "evt_newest" {
		t.Fatalf("events = %#v, want only newest", events)
	}
}

func TestQueueDrainKeepsEventsAppendedDuringSend(t *testing.T) {
	q := NewQueue(t.TempDir())
	if err := q.Append([]model.EventEnvelope{currentEvent("evt_1")}); err != nil {
		t.Fatalf("append: %v", err)
	}
	if err := q.Drain(func(events []model.EventEnvelope) error {
		if len(events) != 1 || events[0].EventID != "evt_1" {
			t.Fatalf("sent events = %+v", events)
		}
		return q.Append([]model.EventEnvelope{currentEvent("evt_2")})
	}); err != nil {
		t.Fatalf("drain: %v", err)
	}
	read, err := drainAll(q)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(read) != 1 || read[0].EventID != "evt_2" {
		t.Fatalf("concurrent event was not retained: %+v", read)
	}
}

func TestQueueDrainSendsEventsInBatches(t *testing.T) {
	q := NewQueue(t.TempDir())
	events := make([]model.EventEnvelope, 0, queueDrainBatchSize+3)
	for index := 0; index < queueDrainBatchSize+3; index++ {
		events = append(events, currentEvent(fmt.Sprintf("evt_batch_%d", index)))
	}
	if err := q.Append(events); err != nil {
		t.Fatalf("append: %v", err)
	}
	var batchSizes []int
	if err := q.Drain(func(events []model.EventEnvelope) error {
		batchSizes = append(batchSizes, len(events))
		return nil
	}); err != nil {
		t.Fatalf("drain: %v", err)
	}
	if len(batchSizes) != 2 || batchSizes[0] != queueDrainBatchSize || batchSizes[1] != 3 {
		t.Fatalf("batch sizes = %v", batchSizes)
	}
}

func TestQueueDrainKeepsOnlyUnsentBatchOnFailure(t *testing.T) {
	sendErr := errors.New("send failed")
	q := NewQueue(t.TempDir())
	events := make([]model.EventEnvelope, 0, queueDrainBatchSize+1)
	for index := 0; index < queueDrainBatchSize+1; index++ {
		events = append(events, currentEvent(fmt.Sprintf("evt_batch_%d", index)))
	}
	events[queueDrainBatchSize].EventID = "evt_remaining"
	if err := q.Append(events); err != nil {
		t.Fatalf("append: %v", err)
	}
	calls := 0
	err := q.Drain(func([]model.EventEnvelope) error {
		calls++
		if calls == 2 {
			return sendErr
		}
		return nil
	})
	if !errors.Is(err, sendErr) {
		t.Fatalf("drain error = %v, want %v", err, sendErr)
	}
	read, readErr := drainAll(q)
	if readErr != nil {
		t.Fatalf("read: %v", readErr)
	}
	if len(read) != 1 || read[0].EventID != "evt_remaining" {
		t.Fatalf("remaining events = %+v", read)
	}
}

func TestQueueDrainPreservesBatchOnFirstSendFailure(t *testing.T) {
	sendErr := errors.New("send failed")
	q := NewQueue(t.TempDir())
	if err := q.Append([]model.EventEnvelope{currentEvent("evt_1")}); err != nil {
		t.Fatalf("append: %v", err)
	}
	if err := q.Drain(func([]model.EventEnvelope) error { return sendErr }); !errors.Is(err, sendErr) {
		t.Fatalf("drain error = %v, want %v", err, sendErr)
	}
	if got := q.Size(); got != 1 {
		t.Fatalf("queue size = %d, want 1", got)
	}
}

func TestQueueDrainQuarantinesPermanentRejectionAndContinues(t *testing.T) {
	now := time.Date(2026, 7, 31, 12, 0, 0, 0, time.UTC)
	q := NewQueue(t.TempDir())
	q.now = func() time.Time { return now }
	bad := model.EventEnvelope{
		EventID:   "evt_rejected",
		EventType: "session.transcript.chunk",
		CreatedAt: now,
		Payload:   map[string]interface{}{"content": "preserve me"},
	}
	good := model.EventEnvelope{
		EventID:   "evt_deliverable",
		EventType: "usage.delta",
		CreatedAt: now.Add(time.Second),
		Payload:   map[string]interface{}{"total_tokens": 1},
	}
	if err := q.Append([]model.EventEnvelope{bad, good}); err != nil {
		t.Fatalf("append: %v", err)
	}

	var delivered []string
	err := q.Drain(func(events []model.EventEnvelope) error {
		for _, event := range events {
			if event.EventID == bad.EventID {
				return rejectedEventsTestError{eventIDs: []string{bad.EventID}}
			}
		}
		for _, event := range events {
			delivered = append(delivered, event.EventID)
		}
		return nil
	})
	var quarantineErr *QuarantinedEventsError
	if !errors.As(err, &quarantineErr) {
		t.Fatalf("drain error = %v, want QuarantinedEventsError", err)
	}
	if len(quarantineErr.EventIDs) != 1 || quarantineErr.EventIDs[0] != bad.EventID {
		t.Fatalf("quarantined event IDs = %v", quarantineErr.EventIDs)
	}
	if quarantineErr.Reason != "permanently rejected" {
		t.Fatalf("quarantine reason = %q", quarantineErr.Reason)
	}
	if len(delivered) != 1 || delivered[0] != good.EventID {
		t.Fatalf("delivered events = %v, want %s", delivered, good.EventID)
	}

	stats, err := q.Stats()
	if err != nil {
		t.Fatalf("stats: %v", err)
	}
	if stats.QueuedEvents != 0 || stats.QuarantinedEvents != 1 {
		t.Fatalf("queue stats = %#v, want zero active and one quarantined", stats)
	}

	db, err := q.open()
	if err != nil {
		t.Fatalf("open queue: %v", err)
	}
	var record quarantinedEventRecord
	err = db.View(func(tx *bolt.Tx) error {
		encoded := tx.Bucket(queueQuarantineBucket).Get([]byte(bad.EventID))
		if encoded == nil {
			return errors.New("quarantined event is missing")
		}
		return json.Unmarshal(encoded, &record)
	})
	if closeErr := db.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		t.Fatalf("read quarantined event: %v", err)
	}
	if record.Event.EventID != bad.EventID || record.Event.Payload["content"] != "preserve me" {
		t.Fatalf("quarantined record = %#v", record)
	}
	if record.Reason != "permanently rejected" || !record.QuarantinedAt.Equal(now) {
		t.Fatalf("quarantine metadata = %#v", record)
	}

	stats, err = q.AppendWithStats([]model.EventEnvelope{bad})
	if err != nil {
		t.Fatalf("reappend quarantined event: %v", err)
	}
	if stats.RemovedDuplicate != 1 || stats.QueuedEvents != 0 || stats.QuarantinedEvents != 1 {
		t.Fatalf("reappend stats = %#v", stats)
	}
}

func TestQueueDrainDoesNotQuarantineUnknownEventID(t *testing.T) {
	q := NewQueue(t.TempDir())
	if err := q.Append([]model.EventEnvelope{currentEvent("evt_active")}); err != nil {
		t.Fatalf("append: %v", err)
	}
	err := q.Drain(func([]model.EventEnvelope) error {
		return rejectedEventsTestError{eventIDs: []string{"evt_not_in_batch"}}
	})
	if err == nil || !strings.Contains(err.Error(), "do not match the active queue batch") {
		t.Fatalf("drain error = %v, want batch mismatch", err)
	}
	stats, statsErr := q.Stats()
	if statsErr != nil {
		t.Fatalf("stats: %v", statsErr)
	}
	if stats.QueuedEvents != 1 || stats.QuarantinedEvents != 0 {
		t.Fatalf("queue stats = %#v, want active event preserved", stats)
	}
}

func TestQueueConcurrentDrainsCannotSendSameBatch(t *testing.T) {
	q := NewQueue(t.TempDir())
	q.lockTimeout = 100 * time.Millisecond
	if err := q.Append([]model.EventEnvelope{currentEvent("evt_1")}); err != nil {
		t.Fatalf("append: %v", err)
	}

	entered := make(chan struct{})
	release := make(chan struct{})
	firstDone := make(chan error, 1)
	var sends atomic.Int32
	go func() {
		firstDone <- q.Drain(func([]model.EventEnvelope) error {
			sends.Add(1)
			close(entered)
			<-release
			return nil
		})
	}()
	<-entered
	secondErr := q.Drain(func([]model.EventEnvelope) error {
		sends.Add(1)
		return nil
	})
	if secondErr == nil || !strings.Contains(secondErr.Error(), "drain lock timeout") {
		t.Fatalf("second drain error = %v, want lock timeout", secondErr)
	}
	close(release)
	if err := <-firstDone; err != nil {
		t.Fatalf("first drain: %v", err)
	}
	if got := sends.Load(); got != 1 {
		t.Fatalf("send calls = %d, want 1", got)
	}
}

func TestQueueStaleDrainLockFileDoesNotBlock(t *testing.T) {
	q := NewQueue(t.TempDir())
	if err := os.WriteFile(q.path+".drain.lock", []byte("stale"), 0o600); err != nil {
		t.Fatalf("write stale lock file: %v", err)
	}
	if err := q.Append([]model.EventEnvelope{currentEvent("evt_1")}); err != nil {
		t.Fatalf("append: %v", err)
	}
	if err := q.Drain(func([]model.EventEnvelope) error { return nil }); err != nil {
		t.Fatalf("drain with stale lock file: %v", err)
	}
}

func currentEvent(eventID string) model.EventEnvelope {
	return model.EventEnvelope{
		EventID:   eventID,
		EventType: "test",
		CreatedAt: time.Now().UTC(),
	}
}

func writeLegacyQueue(path string, events []model.EventEnvelope) error {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	encoder := json.NewEncoder(file)
	for _, event := range events {
		if err := encoder.Encode(event); err != nil {
			_ = file.Close()
			return err
		}
	}
	return file.Close()
}
