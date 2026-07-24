package daemon

import (
	"context"
	"testing"

	"github.com/gesta-run/gesta-agent/pkg/model"
)

type deferredCommitAdapter struct {
	committed *bool
}

func (deferredCommitAdapter) Name() string { return "deferred_commit" }

func (adapter deferredCommitAdapter) Collect(context.Context, Config) (AdapterResult, []model.EventEnvelope) {
	return AdapterResult{
		Status: model.AdapterStatus{Name: adapter.Name(), Status: "ok"},
		Commit: func() error {
			*adapter.committed = true
			return nil
		},
	}, nil
}

func TestCollectWithAdaptersDefersCursorCommits(t *testing.T) {
	committed := false
	_, _, commit := CollectWithAdapters(
		context.Background(),
		Config{DataDir: t.TempDir()},
		[]Adapter{deferredCommitAdapter{committed: &committed}},
	)
	if committed {
		t.Fatal("adapter cursor committed during collection")
	}
	if commit == nil {
		t.Fatal("expected deferred adapter commit")
	}
	if err := commit(); err != nil {
		t.Fatalf("commit adapter cursor: %v", err)
	}
	if !committed {
		t.Fatal("adapter cursor was not committed")
	}
}

func TestSnapshotEventIsStableForUnchangedPayload(t *testing.T) {
	cfg := Config{DaemonID: "daemon_test"}
	payload := map[string]interface{}{"version": "1.2.3", "enabled": true}

	first := snapshotEvent(cfg, "agent.discovery", "daemon", "codex", payload)
	second := snapshotEvent(cfg, "agent.discovery", "daemon", "codex", payload)
	if first.EventID != second.EventID {
		t.Fatalf("snapshot event IDs differ: %q != %q", first.EventID, second.EventID)
	}
}

func TestSnapshotEventChangesWithPayload(t *testing.T) {
	cfg := Config{DaemonID: "daemon_test"}
	first := snapshotEvent(cfg, "agent.discovery", "daemon", "codex", map[string]interface{}{"version": "1.2.3"})
	second := snapshotEvent(cfg, "agent.discovery", "daemon", "codex", map[string]interface{}{"version": "1.2.4"})
	if first.EventID == second.EventID {
		t.Fatal("snapshot event ID did not change with payload")
	}
}
