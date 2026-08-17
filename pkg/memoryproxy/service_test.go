package memoryproxy

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gesta-run/gesta-agent/pkg/daemon"
	"github.com/gesta-run/gesta-agent/pkg/model"
	"github.com/gesta-run/gesta-agent/pkg/rulecache"
)

func TestSearchUsesDaemonCredentialAndWorkspaceMetadata(t *testing.T) {
	var received model.MemorySearchRequest
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/api/v1/memory/search" {
			t.Errorf("path = %q", request.URL.Path)
		}
		if request.Header.Get("Authorization") != "Bearer test-token" {
			t.Errorf("Authorization = %q", request.Header.Get("Authorization"))
		}
		if err := json.NewDecoder(request.Body).Decode(&received); err != nil {
			t.Errorf("decode request: %v", err)
		}
		_ = json.NewEncoder(writer).Encode(model.MemorySearchResponse{Memories: []model.Memory{}})
	}))
	defer server.Close()

	config := daemon.NewDirectRuntimeConfig(server.URL, "test-token")
	config.DataDir = t.TempDir()
	if err := rulecache.SaveMemorySettingsCache(config.DataDir, model.MemorySettings{Enabled: true}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := rulecache.SaveSensitiveRuleCache(config.DataDir, []model.SensitiveRule{}, time.Now()); err != nil {
		t.Fatal(err)
	}
	workspace := model.MemoryWorkspace{CWDName: "gesta", ChildDirs: []string{"pkg"}}
	if _, err := New(config).Search(context.Background(), "project constraints", 5, workspace); err != nil {
		t.Fatal(err)
	}
	if received.DaemonID != config.DaemonID || received.Workspace.CWDName != "gesta" {
		t.Fatalf("request = %#v", received)
	}
}

func TestContextForwardsRecallQueryAndSuppliedOrganizationContext(t *testing.T) {
	var received model.MemoryContextRequest
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/api/v1/memory/context" {
			t.Errorf("path = %q", request.URL.Path)
		}
		if err := json.NewDecoder(request.Body).Decode(&received); err != nil {
			t.Errorf("decode request: %v", err)
		}
		_ = json.NewEncoder(writer).Encode(model.MemorySearchResponse{Memories: []model.Memory{}})
	}))
	defer server.Close()

	config := daemon.NewDirectRuntimeConfig(server.URL, "test-token")
	config.DataDir = t.TempDir()
	if err := rulecache.SaveMemorySettingsCache(config.DataDir, model.MemorySettings{Enabled: true}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := rulecache.SaveSensitiveRuleCache(config.DataDir, []model.SensitiveRule{}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := New(config).Context(
		context.Background(),
		"Current request with recent history",
		"Matched release context",
		model.MemoryWorkspace{CWDName: "gesta"},
	); err != nil {
		t.Fatal(err)
	}
	if received.Prompt != "Current request with recent history" || received.Context != "Matched release context" {
		t.Fatalf("context request = %#v", received)
	}
}

func TestMemoryFailsClosedWhenSensitiveRulesAreUnavailable(t *testing.T) {
	config := daemon.NewDirectRuntimeConfig("http://127.0.0.1:1", "test-token")
	config.DataDir = t.TempDir()
	if err := rulecache.SaveMemorySettingsCache(config.DataDir, model.MemorySettings{Enabled: true}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := New(config).Search(context.Background(), "query", 5, model.MemoryWorkspace{}); err != ErrRulesUnavailable {
		t.Fatalf("error = %v, want %v", err, ErrRulesUnavailable)
	}
}

func TestMemoryIsDisabledWithoutCompleteAgentCredential(t *testing.T) {
	config := daemon.NewDirectRuntimeConfig("http://127.0.0.1:1", "test-token")
	config.DataDir = t.TempDir()
	if err := rulecache.SaveMemorySettingsCache(config.DataDir, model.MemorySettings{Enabled: true}, time.Now()); err != nil {
		t.Fatal(err)
	}
	config.Token = ""
	config.APIKey = ""
	if New(config).Enabled() {
		t.Fatal("memory must remain disabled without an agent credential")
	}
}

func TestRememberPreservesWriteInProgressSemantics(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.WriteHeader(http.StatusConflict)
		_ = json.NewEncoder(writer).Encode(model.APIError{Error: "memory write in progress"})
	}))
	defer server.Close()

	config := daemon.NewDirectRuntimeConfig(server.URL, "test-token")
	config.DataDir = t.TempDir()
	if err := rulecache.SaveMemorySettingsCache(config.DataDir, model.MemorySettings{Enabled: true}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := rulecache.SaveSensitiveRuleCache(config.DataDir, []model.SensitiveRule{}, time.Now()); err != nil {
		t.Fatal(err)
	}
	_, err := New(config).Remember(context.Background(), "stable fact", model.MemoryWorkspace{})
	if !errors.Is(err, ErrInProgress) || PublicError(err) != "memory_write_in_progress" {
		t.Fatalf("error = %v, public = %q", err, PublicError(err))
	}
}

func TestRememberUsesStableRequestIDForSafeRetry(t *testing.T) {
	requestIDs := []string{}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var received model.MemoryRememberRequest
		if err := json.NewDecoder(request.Body).Decode(&received); err != nil {
			t.Errorf("decode request: %v", err)
		}
		requestIDs = append(requestIDs, received.RequestID)
		_ = json.NewEncoder(writer).Encode(model.MemoryRememberResponse{Status: "stored", EpisodeID: "episode"})
	}))
	defer server.Close()

	config := daemon.NewDirectRuntimeConfig(server.URL, "test-token")
	config.DataDir = t.TempDir()
	if err := rulecache.SaveMemorySettingsCache(config.DataDir, model.MemorySettings{Enabled: true}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := rulecache.SaveSensitiveRuleCache(config.DataDir, []model.SensitiveRule{}, time.Now()); err != nil {
		t.Fatal(err)
	}
	service := New(config)
	workspace := model.MemoryWorkspace{CWDName: "Gesta", ChildDirs: []string{"pkg", "docs"}}
	for attempt := 0; attempt < 2; attempt++ {
		if _, err := service.Remember(context.Background(), "stable fact\r\n", workspace); err != nil {
			t.Fatal(err)
		}
	}
	if len(requestIDs) != 2 || requestIDs[0] != requestIDs[1] {
		t.Fatalf("request IDs = %#v", requestIDs)
	}
	otherWorkspaceID := memoryRequestID(
		config.DaemonID,
		"stable fact",
		model.MemoryWorkspace{CWDName: "other"},
	)
	if requestIDs[0] == otherWorkspaceID {
		t.Fatal("different workspaces must not share a retry identity")
	}
}
