package localactivity

import (
	"context"
	"encoding/json"
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
	"github.com/gesta-run/gesta-agent/pkg/turnreceipt"
)

type fakeMemoryService struct {
	workspace   model.MemoryWorkspace
	rememberErr error
}

func (f *fakeMemoryService) Search(_ context.Context, query string, limit int, workspace model.MemoryWorkspace) (model.MemorySearchResponse, error) {
	f.workspace = workspace
	return model.MemorySearchResponse{Memories: []model.Memory{{
		FactID: "memory", Content: query, RelevanceScore: 1, Score: 1,
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
	if response.Header().Get("Retry-After") != "5" {
		t.Fatalf("Retry-After = %q", response.Header().Get("Retry-After"))
	}
}

func TestMemorySearchDerivesWorkspaceWithoutForwardingPath(t *testing.T) {
	service := &fakeMemoryService{}
	store := activitydetail.NewStore(t.TempDir())
	detail, err := store.Begin("codex")
	if err != nil {
		t.Fatal(err)
	}
	handler := newHandlerWithMemory(store, "daemon", service)
	workingDirectory, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	workspacePath := filepath.Dir(workingDirectory)
	request := httptest.NewRequest(http.MethodPost, BaseURL+"/api/v1/memory/search", strings.NewReader(`{"query":"project constraints","limit":5}`))
	request.Host = Address
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Gesta-Cwd", workspacePath)
	request.Header.Set(ActivityHeaderName, detail.ActivityID)
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
		!strings.Contains(body, `"relevance_score":1`) || !strings.Contains(body, `"score":1`) ||
		strings.Contains(body, `"memory_id"`) {
		t.Fatalf("search response uses an ambiguous memory contract: %s", body)
	}
	recorded, err := store.Get(detail.ActivityID)
	if err != nil || recorded.MemoryRecallStatus != activitydetail.MemoryRecallSuccess || recorded.MemoryCount != 1 {
		t.Fatalf("recorded activity = %#v, err = %v", recorded, err)
	}
}

func TestActivityNoticeReportsMemoryRecallTimeout(t *testing.T) {
	store := activitydetail.NewStore(t.TempDir())
	detail, err := store.Begin("codex")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.RecordMemoryRecall(detail.ActivityID, activitydetail.MemoryRecallTimeout, nil); err != nil {
		t.Fatal(err)
	}
	recorded, err := store.Get(detail.ActivityID)
	if err != nil {
		t.Fatal(err)
	}
	want := "Gesta · Context 0 · Memory timeout · Last output 0 eLOC · [Details](" + ActivityURL(detail.ActivityID) + ")"
	if got := formatActivityNotice(recorded); got != want {
		t.Fatalf("notice = %q, want %q", got, want)
	}
}

func TestActivityNoticeReportsZeroValueCurrentActivity(t *testing.T) {
	store := activitydetail.NewStore(t.TempDir())
	detail, err := store.Begin("codex")
	if err != nil {
		t.Fatal(err)
	}
	handler := newHandlerWithMemory(store, "daemon", &fakeMemoryService{})
	request := httptest.NewRequest(http.MethodPost, NoticeURL(), nil)
	request.Host = Address
	request.Header.Set(ActivityHeaderName, detail.ActivityID)
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	var body noticeResponse
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	wantURL := ActivityURL(detail.ActivityID)
	wantNotice := "Gesta · Context 0 · Memory 0 · Last output 0 eLOC · [Details](" + wantURL + ")"
	if body.Notice != wantNotice || body.DetailsURL != wantURL {
		t.Fatalf("notice response = %#v, want notice %q and URL %q", body, wantNotice, wantURL)
	}
}

func TestActivityNoticeReportsMemoryDisabled(t *testing.T) {
	detail := activitydetail.Detail{
		ActivityID:         "activity_0123456789abcdef0123456789abcdef",
		MemoryRecallStatus: activitydetail.MemoryRecallDisabled,
	}
	want := "Gesta · Context 0 · Memory disabled · Last output 0 eLOC · [Details](" + ActivityURL(detail.ActivityID) + ")"
	if got := formatActivityNotice(detail); got != want {
		t.Fatalf("notice = %q, want %q", got, want)
	}
}

func TestActivityNoticeUsesCurrentContextMemoryAndLastOutput(t *testing.T) {
	store := activitydetail.NewStore(t.TempDir())
	detail, err := store.Begin("codex")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.RecordContext(detail.ActivityID, []activitydetail.ContextRuleMatch{{
		RuleID: "rule", Name: "Rule", MatchType: "keyword_any", Content: "Apply it.",
	}}); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordMemories(detail.ActivityID, []model.Memory{{FactID: "fact", Content: "Remember it."}}); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordOutput(detail.ActivityID, turnreceipt.OutputSummary{
		CodeLines: 10, DocWords: 8, OtherWords: 4,
	}); err != nil {
		t.Fatal(err)
	}
	handler := newHandlerWithMemory(store, "daemon", &fakeMemoryService{})
	request := httptest.NewRequest(http.MethodPost, NoticeURL(), nil)
	request.Host = Address
	request.Header.Set(ActivityHeaderName, detail.ActivityID)
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	var body noticeResponse
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	want := "Gesta · Context 1 · Memory 1 · Last output 11.5 eLOC · [Details](" + ActivityURL(detail.ActivityID) + ")"
	if body.Notice != want {
		t.Fatalf("notice = %q, want %q", body.Notice, want)
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
