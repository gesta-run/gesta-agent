package localactivity

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gesta-run/gesta-agent/pkg/activitydetail"
	"github.com/gesta-run/gesta-agent/pkg/memoryproxy"
	"github.com/gesta-run/gesta-agent/pkg/model"
)

type fakeMemoryService struct {
	workspace   model.MemoryWorkspace
	rememberErr error
}

func (f *fakeMemoryService) Search(_ context.Context, query string, limit int, workspace model.MemoryWorkspace) (model.MemorySearchResponse, error) {
	f.workspace = workspace
	return model.MemorySearchResponse{Memories: []model.Memory{{
		FactID: "memory", Content: query, GraphRankScore: 1, Score: 1,
	}}}, nil
}

func (f *fakeMemoryService) Remember(_ context.Context, _ string, workspace model.MemoryWorkspace) (model.MemoryRememberResponse, error) {
	f.workspace = workspace
	if f.rememberErr != nil {
		return model.MemoryRememberResponse{}, f.rememberErr
	}
	return model.MemoryRememberResponse{Status: "stored", EpisodeID: "episode"}, nil
}

func TestMemoryRememberReturnsConflictWhileWriteIsInProgress(t *testing.T) {
	service := &fakeMemoryService{rememberErr: fmt.Errorf("proxy: %w", memoryproxy.ErrInProgress)}
	handler := newHandlerWithMemory(activitydetail.NewStore(t.TempDir()), "daemon", service)
	request := httptest.NewRequest(http.MethodPost, BaseURL+"/api/v1/memory/remember", strings.NewReader(`{"content":"stable fact"}`))
	request.Host = Address
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), "memory_write_in_progress") {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
}

func TestMemorySearchDerivesWorkspaceWithoutForwardingPath(t *testing.T) {
	service := &fakeMemoryService{}
	handler := newHandlerWithMemory(activitydetail.NewStore(t.TempDir()), "daemon", service)
	workingDirectory, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	workspacePath := filepath.Dir(workingDirectory)
	request := httptest.NewRequest(http.MethodPost, BaseURL+"/api/v1/memory/search", strings.NewReader(`{"query":"project constraints","limit":5}`))
	request.Host = Address
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Gesta-Cwd", workspacePath)
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	if service.workspace.CWDName != "pkg" {
		t.Fatalf("workspace = %#v", service.workspace)
	}
	if strings.Contains(response.Body.String(), workspacePath) {
		t.Fatalf("response leaked full workspace path: %s", response.Body.String())
	}
	if body := response.Body.String(); !strings.Contains(body, `"fact_id":"memory"`) ||
		!strings.Contains(body, `"graph_rank_score":1`) || !strings.Contains(body, `"score":1`) ||
		strings.Contains(body, `"memory_id"`) {
		t.Fatalf("search response uses an ambiguous memory contract: %s", body)
	}
}

func TestMemoryRememberReturnsEpisodeID(t *testing.T) {
	service := &fakeMemoryService{}
	handler := newHandlerWithMemory(activitydetail.NewStore(t.TempDir()), "daemon", service)
	request := httptest.NewRequest(
		http.MethodPost,
		BaseURL+"/api/v1/memory/remember",
		strings.NewReader(`{"content":"stable fact"}`),
	)
	request.Host = Address
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	if body := response.Body.String(); !strings.Contains(body, `"episode_id":"episode"`) ||
		strings.Contains(body, `"memory_id"`) {
		t.Fatalf("remember response uses an ambiguous memory contract: %s", body)
	}
}

func TestMemoryEndpointRejectsBrowserOrigin(t *testing.T) {
	handler := newHandlerWithMemory(activitydetail.NewStore(t.TempDir()), "daemon", &fakeMemoryService{})
	request := httptest.NewRequest(http.MethodPost, BaseURL+"/api/v1/memory/search", strings.NewReader(`{"query":"x"}`))
	request.Host = Address
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Origin", "https://example.com")
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusForbidden)
	}
}

func TestHealthAdvertisesMemoryProxyCapability(t *testing.T) {
	withMemory := newHandlerWithMemory(activitydetail.NewStore(t.TempDir()), "daemon", &fakeMemoryService{})
	request := httptest.NewRequest(http.MethodGet, BaseURL+"/healthz", nil)
	request.Host = Address
	response := httptest.NewRecorder()
	withMemory.ServeHTTP(response, request)
	if response.Header().Get(memoryHeaderName) != memoryHeaderValue {
		t.Fatalf("memory capability header = %q", response.Header().Get(memoryHeaderName))
	}

	withoutMemory := newHandlerWithMemory(activitydetail.NewStore(t.TempDir()), "daemon", nil)
	response = httptest.NewRecorder()
	withoutMemory.ServeHTTP(response, request)
	if response.Header().Get(memoryHeaderName) != "" {
		t.Fatalf("memory capability must not be advertised without a proxy")
	}
}
