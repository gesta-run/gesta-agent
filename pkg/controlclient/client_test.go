package controlclient

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gesta-run/gesta-agent/pkg/model"
)

func TestStatusCodeUnwrapsHTTPStatusError(t *testing.T) {
	err := fmt.Errorf("remember memory: %w", &httpStatusError{statusCode: http.StatusConflict})
	status, ok := StatusCode(err)
	if !ok || status != http.StatusConflict {
		t.Fatalf("StatusCode() = %d, %t", status, ok)
	}
	if _, ok := StatusCode(errors.New("network failed")); ok {
		t.Fatal("non-HTTP errors must not expose a status code")
	}
}

func TestClientHeartbeatSendsDaemonTokenAndHostType(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/heartbeat" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer dtok_123" {
			t.Fatalf("unexpected authorization header: %q", got)
		}
		var payload map[string]interface{}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode heartbeat: %v", err)
		}
		if payload["host_type"] != "laptop" {
			t.Fatalf("expected host_type in heartbeat, got %#v", payload)
		}
		if payload["health_status"] != "ok" {
			t.Fatalf("expected ok heartbeat, got %#v", payload["health_status"])
		}
		if _, ok := payload["user_id"]; ok {
			t.Fatalf("heartbeat exposed user_id: %#v", payload)
		}
		if _, ok := payload["user_name"]; ok {
			t.Fatalf("heartbeat exposed user_name: %#v", payload)
		}
		if _, ok := payload["offline_queue_size"]; ok {
			t.Fatalf("heartbeat exposed offline_queue_size: %#v", payload)
		}
		adapters, ok := payload["adapters"].([]interface{})
		if !ok || len(adapters) != 1 {
			t.Fatalf("heartbeat adapters = %#v", payload["adapters"])
		}
		adapter, ok := adapters[0].(map[string]interface{})
		if !ok {
			t.Fatalf("heartbeat adapter = %#v", adapters[0])
		}
		inventory, ok := adapter["mcp_inventory"].(map[string]interface{})
		if !ok || inventory["scan_status"] != "ok" {
			t.Fatalf("heartbeat inventory = %#v", adapter["mcp_inventory"])
		}
		servers, ok := inventory["servers"].([]interface{})
		if !ok || len(servers) != 1 {
			t.Fatalf("heartbeat inventory servers = %#v", inventory["servers"])
		}
		_ = json.NewEncoder(w).Encode(model.HeartbeatResponse{
			Upgrade: &model.AgentUpgradePolicy{
				Mode:          "auto",
				TargetVersion: "9.9.9",
				URL:           "https://example.com/gesta-agent",
				SHA256:        "abc123",
			},
			OutputClassification: &model.OutputClassificationSettings{
				Revision:     4,
				CodeSuffixes: []string{".html"},
			},
		})
	}))
	defer server.Close()

	client := NewClient(server.URL, "dtok_123")
	resp, err := client.Heartbeat(model.HeartbeatRequest{
		DaemonVersion: model.DaemonVersion,
		PolicyVersion: model.DefaultPolicyVersion,
		HostType:      "laptop",
		HealthStatus:  "ok",
		Adapters: []model.AdapterStatus{{
			Name: "codex",
			MCPInventory: &model.MCPInventoryStatus{
				ScanStatus: "ok",
				ObservedAt: "2026-07-24T00:00:00Z",
				Servers: []model.MCPServerConfiguration{{
					Name: "vercel", Enabled: true,
				}},
			},
		}},
	})
	if err != nil {
		t.Fatalf("heartbeat: %v", err)
	}
	if resp.Upgrade == nil || resp.Upgrade.Mode != "auto" || resp.Upgrade.TargetVersion != "9.9.9" {
		t.Fatalf("heartbeat upgrade response = %#v", resp.Upgrade)
	}
	if resp.OutputClassification == nil || resp.OutputClassification.Revision != 4 {
		t.Fatalf("heartbeat classification response = %#v", resp.OutputClassification)
	}
}

func TestClientSendEventsDropsUnmatchedPolicyDecisions(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.URL.Path != "/api/v1/events" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		if got := r.Header.Get(model.EventProtocolHeader); got != model.EventProtocolVersion {
			t.Fatalf("%s = %q, want %q", model.EventProtocolHeader, got, model.EventProtocolVersion)
		}
		var batch struct {
			Events []map[string]interface{} `json:"events"`
		}
		if err := json.NewDecoder(r.Body).Decode(&batch); err != nil {
			t.Fatalf("decode events: %v", err)
		}
		if len(batch.Events) != 2 {
			t.Fatalf("events sent = %d, want 2: %#v", len(batch.Events), batch.Events)
		}
		if batch.Events[0]["event_id"] != "evt_matched" || batch.Events[1]["event_id"] != "evt_system" {
			t.Fatalf("unexpected events sent: %#v", batch.Events)
		}
		for _, event := range batch.Events {
			if _, ok := event["user_id"]; ok {
				t.Fatalf("event exposed user_id: %#v", event)
			}
			if _, ok := event["user_name"]; ok {
				t.Fatalf("event exposed user_name: %#v", event)
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	}))
	defer server.Close()

	client := NewClient(server.URL, "dtok_123")
	err := client.SendEvents([]model.EventEnvelope{
		{
			EventID:   "evt_unmatched",
			EventType: "policy.decision",
			Payload: map[string]interface{}{
				"decision": "allow",
				"rule_ids": []interface{}{},
			},
		},
		{
			EventID:   "evt_matched",
			UserID:    "spoofed-user",
			UserName:  "Spoofed User",
			EventType: "policy.decision",
			Payload: map[string]interface{}{
				"decision": "block",
				"rule_ids": []interface{}{"rule_block"},
			},
		},
		{
			EventID:   "evt_system",
			UserID:    "spoofed-user",
			UserName:  "Spoofed User",
			EventType: "daemon.system_snapshot",
			Payload:   map[string]interface{}{"daemon_version": model.DaemonVersion},
		},
	})
	if err != nil {
		t.Fatalf("SendEvents: %v", err)
	}
	if requests != 1 {
		t.Fatalf("requests = %d, want 1", requests)
	}
}

func TestClientSendEventsSkipsPostWhenOnlyUnmatchedPolicyDecisions(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		t.Fatalf("unexpected request: %s", r.URL.Path)
	}))
	defer server.Close()

	client := NewClient(server.URL, "dtok_123")
	if err := client.SendEvents([]model.EventEnvelope{
		{
			EventID:   "evt_unmatched",
			EventType: "policy.decision",
			Payload:   map[string]interface{}{"decision": "allow"},
		},
	}); err != nil {
		t.Fatalf("SendEvents: %v", err)
	}
	if requests != 0 {
		t.Fatalf("requests = %d, want 0", requests)
	}
}

func TestClientSendEventsForDaemonRebindsQueuedIdentity(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var batch model.EventBatch
		if err := json.NewDecoder(r.Body).Decode(&batch); err != nil {
			t.Fatalf("decode events: %v", err)
		}
		if len(batch.Events) != 2 {
			t.Fatalf("events sent = %d, want 2", len(batch.Events))
		}
		for _, event := range batch.Events {
			if event.DaemonID != "daemon_current" || event.DeviceID != "device_current" {
				t.Fatalf("event identity = (%q, %q), want current identity", event.DaemonID, event.DeviceID)
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]int{"accepted": len(batch.Events)})
	}))
	defer server.Close()

	events := []model.EventEnvelope{
		{EventID: "evt_stale", DaemonID: "daemon_stale", DeviceID: "device_stale", EventType: "tool.call"},
		{EventID: "evt_current", DaemonID: "daemon_current", DeviceID: "device_current", EventType: "turn.usage"},
	}
	client := NewClient(server.URL, "dtok_123")
	if err := client.SendEventsForDaemon(events, "daemon_current", "device_current"); err != nil {
		t.Fatalf("SendEventsForDaemon: %v", err)
	}
	if events[0].DaemonID != "daemon_stale" || events[0].DeviceID != "device_stale" {
		t.Fatalf("SendEventsForDaemon mutated queued event: %#v", events[0])
	}
}

func TestClientSendEventsSplitsRequestsByEncodedSize(t *testing.T) {
	requests := 0
	received := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read event request: %v", err)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		if len(body) > maxEventRequestBodyBytes {
			t.Errorf("event request bytes = %d, limit = %d", len(body), maxEventRequestBodyBytes)
		}
		var batch model.EventBatch
		if err := json.Unmarshal(body, &batch); err != nil {
			t.Errorf("decode event request: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		received += len(batch.Events)
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	}))
	defer server.Close()

	const eventCount = 70
	events := make([]model.EventEnvelope, 0, eventCount)
	for index := 0; index < eventCount; index++ {
		events = append(events, model.EventEnvelope{
			EventID:   fmt.Sprintf("evt_large_%d", index),
			EventType: "session.transcript.chunk",
			Payload: map[string]interface{}{
				"content": strings.Repeat("x", 128*1024),
			},
		})
	}

	client := NewClient(server.URL, "dtok_123")
	if err := client.SendEvents(events); err != nil {
		t.Fatalf("SendEvents: %v", err)
	}
	if requests < 2 {
		t.Fatalf("requests = %d, want multiple byte-bounded requests", requests)
	}
	if received != eventCount {
		t.Fatalf("received events = %d, want %d", received, eventCount)
	}
}

func TestClientSendEventsBisectsPayloadRejectedByProxy(t *testing.T) {
	requests := 0
	var accepted []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		var batch model.EventBatch
		if err := json.NewDecoder(r.Body).Decode(&batch); err != nil {
			t.Errorf("decode event request: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if len(batch.Events) > 2 {
			w.WriteHeader(http.StatusRequestEntityTooLarge)
			_ = json.NewEncoder(w).Encode(model.APIError{Error: "request body too large"})
			return
		}
		for _, event := range batch.Events {
			accepted = append(accepted, event.EventID)
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	}))
	defer server.Close()

	events := make([]model.EventEnvelope, 0, 5)
	for index := 0; index < 5; index++ {
		events = append(events, model.EventEnvelope{
			EventID:   fmt.Sprintf("evt_split_%d", index),
			EventType: "daemon.system_snapshot",
			Payload:   map[string]interface{}{"index": index},
		})
	}

	client := NewClient(server.URL, "dtok_123")
	if err := client.SendEvents(events); err != nil {
		t.Fatalf("SendEvents: %v", err)
	}
	if requests <= 1 {
		t.Fatalf("requests = %d, want adaptive retry requests", requests)
	}
	if len(accepted) != len(events) {
		t.Fatalf("accepted events = %v, want %d events", accepted, len(events))
	}
	for index, eventID := range accepted {
		want := fmt.Sprintf("evt_split_%d", index)
		if eventID != want {
			t.Fatalf("accepted[%d] = %q, want %q", index, eventID, want)
		}
	}
}

func TestClientSendEventsReportsSinglePermanentRejection(t *testing.T) {
	for _, statusCode := range []int{http.StatusBadRequest, http.StatusRequestEntityTooLarge} {
		t.Run(http.StatusText(statusCode), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				http.Error(w, "permanently rejected", statusCode)
			}))
			defer server.Close()

			client := NewClient(server.URL, "dtok_123")
			err := client.SendEvents([]model.EventEnvelope{{
				EventID:   "evt_rejected",
				EventType: "session.transcript.chunk",
				Payload:   map[string]interface{}{"content": "rejected"},
			}})
			if err == nil {
				t.Fatal("SendEvents accepted a permanently rejected event")
			}
			rejected, ok := err.(interface{ RejectedEventIDs() []string })
			if !ok {
				t.Fatalf("error type = %T, want rejected event error", err)
			}
			eventIDs := rejected.RejectedEventIDs()
			if len(eventIDs) != 1 || eventIDs[0] != "evt_rejected" {
				t.Fatalf("rejected event IDs = %v, want evt_rejected", eventIDs)
			}
		})
	}
}
