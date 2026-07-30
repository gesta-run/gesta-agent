package eventqueue

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gesta-run/gesta-agent/pkg/model"
)

func drainAll(q Queue) ([]model.EventEnvelope, error) {
	var drained []model.EventEnvelope
	err := q.Drain(func(events []model.EventEnvelope) error {
		drained = append(drained, events...)
		return nil
	})
	return drained, err
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

func TestQueueUsesProtocolV2DatabaseAndIgnoresLegacyQueue(t *testing.T) {
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
	if err := q.Append([]model.EventEnvelope{{
		EventID:   "evt_v2",
		EventType: "policy.decision",
		CreatedAt: time.Now().UTC(),
	}}); err != nil {
		t.Fatalf("append v2 queue: %v", err)
	}
	events, err := drainAll(q)
	if err != nil {
		t.Fatalf("read v2 queue: %v", err)
	}
	if len(events) != 1 || events[0].EventID != "evt_v2" {
		t.Fatalf("v2 queue events = %#v, want only evt_v2", events)
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
		events = append(events, currentEvent("evt_batch"))
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
		events = append(events, currentEvent("evt_batch"))
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
