package daemon

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gesta-run/gesta-agent/pkg/model"
)

func TestClientEnrollWithAPIKeySendsAuthAndPayload(t *testing.T) {
	const apiKey = "enroll_secret_test_key"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/enroll" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "" {
			t.Fatalf("connect token should not be sent as bearer token, got %q", got)
		}
		if got := r.Header.Get("X-API-Key"); got != apiKey {
			t.Fatalf("unexpected x-api-key header: %q", got)
		}
		var req model.EnrollmentRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if req.APIKey != "" {
			t.Fatalf("connect token should not be duplicated in JSON payload")
		}
		if req.UserName != "alice" || req.UserID != "alice" || req.DeviceID != "dev_123" {
			t.Fatalf("unexpected enrollment request: %#v", req)
		}
		_ = json.NewEncoder(w).Encode(model.EnrollmentResponse{
			DaemonID:      "daemon_123",
			Token:         "dtok_123",
			PolicyVersion: "bootstrap-v0",
		})
	}))
	defer server.Close()

	client := NewClient(server.URL, "")
	resp, err := client.EnrollWithAPIKey(model.EnrollmentRequest{
		CustomerID: "cust_123",
		DeviceID:   "dev_123",
		UserID:     "alice",
		UserName:   "alice",
	}, apiKey)
	if err != nil {
		t.Fatalf("enroll: %v", err)
	}
	if resp.DaemonID != "daemon_123" || resp.Token != "dtok_123" {
		t.Fatalf("unexpected response: %#v", resp)
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
		if _, ok := payload["offline_queue_size"]; ok {
			t.Fatalf("heartbeat exposed offline_queue_size: %#v", payload)
		}
		_ = json.NewEncoder(w).Encode(model.HeartbeatResponse{
			Upgrade: &model.AgentUpgradePolicy{
				Mode:          "auto",
				TargetVersion: "9.9.9",
				URL:           "https://example.com/gesta-agent",
				SHA256:        "abc123",
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
	})
	if err != nil {
		t.Fatalf("heartbeat: %v", err)
	}
	if resp.Upgrade == nil || resp.Upgrade.Mode != "auto" || resp.Upgrade.TargetVersion != "9.9.9" {
		t.Fatalf("heartbeat upgrade response = %#v", resp.Upgrade)
	}
}

func TestClientSendEventsDropsUnmatchedPolicyDecisions(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.URL.Path != "/api/v1/events" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		var batch model.EventBatch
		if err := json.NewDecoder(r.Body).Decode(&batch); err != nil {
			t.Fatalf("decode events: %v", err)
		}
		if len(batch.Events) != 2 {
			t.Fatalf("events sent = %d, want 2: %#v", len(batch.Events), batch.Events)
		}
		if batch.Events[0].EventID != "evt_matched" || batch.Events[1].EventID != "evt_system" {
			t.Fatalf("unexpected events sent: %#v", batch.Events)
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
			EventType: "policy.decision",
			Payload: map[string]interface{}{
				"decision": "block",
				"rule_ids": []interface{}{"rule_block"},
			},
		},
		{
			EventID:   "evt_system",
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
