package daemon

import "testing"

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
