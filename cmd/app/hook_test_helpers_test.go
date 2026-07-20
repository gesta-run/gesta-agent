package app

import (
	"testing"

	"github.com/gesta-run/gesta-agent/pkg/daemon"
	"github.com/gesta-run/gesta-agent/pkg/model"
)

func readSingleQueuedEvent(t *testing.T, cfg daemon.Config) model.EventEnvelope {
	t.Helper()
	events, err := daemon.NewQueue(cfg.DataDir).ReadAll()
	if err != nil {
		t.Fatalf("read queued events: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("queued events = %d, want 1: %#v", len(events), events)
	}
	return events[0]
}
