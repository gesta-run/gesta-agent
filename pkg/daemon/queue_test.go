package daemon

import (
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gesta-run/gesta-agent/pkg/model"
)

func TestQueueAppendReadClear(t *testing.T) {
	q := NewQueue(t.TempDir())
	events := []model.EventEnvelope{
		{EventID: "evt_1", EventType: "test", CreatedAt: time.Now()},
		{EventID: "evt_2", EventType: "test", CreatedAt: time.Now()},
	}
	if err := q.Append(events); err != nil {
		t.Fatalf("append: %v", err)
	}
	if got := q.Size(); got != 2 {
		t.Fatalf("expected size 2, got %d", got)
	}
	read, err := q.ReadAll()
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(read) != 2 {
		t.Fatalf("expected 2 events, got %d", len(read))
	}
	if err := q.Clear(); err != nil {
		t.Fatalf("clear: %v", err)
	}
	if got := q.Size(); got != 0 {
		t.Fatalf("expected size 0 after clear, got %d", got)
	}
}

func TestQueueReadAllReturnsMalformedLineError(t *testing.T) {
	dir := t.TempDir()
	q := NewQueue(dir)
	if err := os.WriteFile(q.path, []byte("{\"event_id\":\"evt_1\"}\nnot-json\n"), 0o600); err != nil {
		t.Fatalf("write queue: %v", err)
	}
	_, err := q.ReadAll()
	if err == nil {
		t.Fatal("expected malformed queue line error")
	}
	if !strings.Contains(err.Error(), "line 2") {
		t.Fatalf("expected line number in error, got %v", err)
	}
}

func TestQueueDrainKeepsEventsAppendedDuringSend(t *testing.T) {
	q := NewQueue(t.TempDir())
	if err := q.Append([]model.EventEnvelope{{EventID: "evt_1", EventType: "test", CreatedAt: time.Now()}}); err != nil {
		t.Fatalf("append: %v", err)
	}
	if err := q.Drain(func(events []model.EventEnvelope) error {
		if len(events) != 1 || events[0].EventID != "evt_1" {
			t.Fatalf("sent events = %+v", events)
		}
		return q.Append([]model.EventEnvelope{{EventID: "evt_2", EventType: "test", CreatedAt: time.Now()}})
	}); err != nil {
		t.Fatalf("drain: %v", err)
	}
	read, err := q.ReadAll()
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(read) != 1 || read[0].EventID != "evt_2" {
		t.Fatalf("expected concurrently appended event to remain queued, got %+v", read)
	}
}

func TestQueueDrainSendsEventsInBatches(t *testing.T) {
	q := NewQueue(t.TempDir())
	events := make([]model.EventEnvelope, 0, queueDrainBatchSize+3)
	for i := 0; i < queueDrainBatchSize+3; i++ {
		events = append(events, model.EventEnvelope{EventID: "evt_batch", EventType: "test", CreatedAt: time.Now()})
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
	if got := q.Size(); got != 0 {
		t.Fatalf("expected empty queue after batched drain, got %d", got)
	}
}

func TestQueueDrainRequeuesOnlyRemainingBatchOnSendFailure(t *testing.T) {
	sendErr := errors.New("send failed")
	q := NewQueue(t.TempDir())
	events := make([]model.EventEnvelope, 0, queueDrainBatchSize+1)
	for i := 0; i < queueDrainBatchSize+1; i++ {
		events = append(events, model.EventEnvelope{EventID: "evt_" + string(rune('a'+i%26)), EventType: "test", CreatedAt: time.Now()})
	}
	events[queueDrainBatchSize].EventID = "evt_remaining"
	if err := q.Append(events); err != nil {
		t.Fatalf("append: %v", err)
	}
	calls := 0
	err := q.Drain(func(events []model.EventEnvelope) error {
		calls++
		if calls == 2 {
			return sendErr
		}
		return nil
	})
	if !errors.Is(err, sendErr) {
		t.Fatalf("drain error = %v, want %v", err, sendErr)
	}
	read, err := q.ReadAll()
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(read) != 1 || read[0].EventID != "evt_remaining" {
		t.Fatalf("expected only failed remaining event queued, got %+v", read)
	}
}

func TestQueueDrainRequeuesInflightEventsOnSendFailure(t *testing.T) {
	sendErr := errors.New("send failed")
	q := NewQueue(t.TempDir())
	if err := q.Append([]model.EventEnvelope{{EventID: "evt_1", EventType: "test", CreatedAt: time.Now()}}); err != nil {
		t.Fatalf("append: %v", err)
	}
	err := q.Drain(func(_ []model.EventEnvelope) error {
		if appendErr := q.Append([]model.EventEnvelope{{EventID: "evt_2", EventType: "test", CreatedAt: time.Now()}}); appendErr != nil {
			t.Fatalf("append during send: %v", appendErr)
		}
		return sendErr
	})
	if !errors.Is(err, sendErr) {
		t.Fatalf("drain error = %v, want %v", err, sendErr)
	}
	read, err := q.ReadAll()
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(read) != 2 || read[0].EventID != "evt_1" || read[1].EventID != "evt_2" {
		t.Fatalf("expected failed in-flight and concurrent events to remain queued, got %+v", read)
	}
}

func TestQueueDrainSendsStaleInflightAfterCrash(t *testing.T) {
	q := NewQueue(t.TempDir())
	stale := model.EventEnvelope{EventID: "evt_inflight", EventType: "test", CreatedAt: time.Now()}
	if err := writeQueueEvents(q.inflightPath(), []model.EventEnvelope{stale}); err != nil {
		t.Fatalf("write inflight: %v", err)
	}
	var sent []model.EventEnvelope
	if err := q.Drain(func(events []model.EventEnvelope) error {
		sent = append(sent, events...)
		return nil
	}); err != nil {
		t.Fatalf("drain: %v", err)
	}
	if len(sent) != 1 || sent[0].EventID != "evt_inflight" {
		t.Fatalf("expected stale inflight event to be sent, got %+v", sent)
	}
	if _, err := os.Stat(q.inflightPath()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected inflight file removed, stat err=%v", err)
	}
}

func TestQueueDrainPrependsStaleInflightBeforeActiveQueue(t *testing.T) {
	q := NewQueue(t.TempDir())
	stale := model.EventEnvelope{EventID: "evt_inflight", EventType: "test", CreatedAt: time.Now()}
	active := model.EventEnvelope{EventID: "evt_active", EventType: "test", CreatedAt: time.Now()}
	if err := writeQueueEvents(q.inflightPath(), []model.EventEnvelope{stale}); err != nil {
		t.Fatalf("write inflight: %v", err)
	}
	if err := q.Append([]model.EventEnvelope{active}); err != nil {
		t.Fatalf("append active: %v", err)
	}
	var sent []model.EventEnvelope
	if err := q.Drain(func(events []model.EventEnvelope) error {
		sent = append(sent, events...)
		return nil
	}); err != nil {
		t.Fatalf("drain: %v", err)
	}
	if len(sent) != 2 || sent[0].EventID != "evt_inflight" || sent[1].EventID != "evt_active" {
		t.Fatalf("expected stale inflight before active queue, got %+v", sent)
	}
}

func writeQueueEvents(path string, events []model.EventEnvelope) error {
	q := Queue{path: path}
	return q.Append(events)
}
