package localactivity

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gesta-run/gesta-agent/pkg/activitydetail"
	"github.com/gesta-run/gesta-agent/pkg/turnreceipt"
)

func TestActivityNoticeReportsZeroValueCurrentActivity(t *testing.T) {
	store := activitydetail.NewStore(t.TempDir())
	detail, err := store.Begin("codex")
	if err != nil {
		t.Fatal(err)
	}
	response := requestActivityNotice(t, store, detail.ActivityID)
	wantURL := ActivityURL(detail.ActivityID)
	wantNotice := "Gesta · Context 0 · Last output 0 eLOC · [Details](" + wantURL + ")"
	if response.Notice != wantNotice || response.DetailsURL != wantURL {
		t.Fatalf("notice response = %#v, want notice %q and URL %q", response, wantNotice, wantURL)
	}
}

func TestActivityNoticeUsesCurrentContextAndLastOutput(t *testing.T) {
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
	if err := store.RecordOutput(detail.ActivityID, turnreceipt.OutputSummary{
		CodeLines: 10, DocWords: 8, OtherWords: 4,
	}); err != nil {
		t.Fatal(err)
	}
	response := requestActivityNotice(t, store, detail.ActivityID)
	want := "Gesta · Context 1 · Last output 11.5 eLOC · [Details](" + ActivityURL(detail.ActivityID) + ")"
	if response.Notice != want {
		t.Fatalf("notice = %q, want %q", response.Notice, want)
	}
}

func requestActivityNotice(t *testing.T, store activitydetail.Store, activityID string) noticeResponse {
	t.Helper()
	handler := newHandlerWithDaemonID(store, "")
	request := httptest.NewRequest(http.MethodPost, "/api/v1/activity/notice", nil)
	request.Host = Address
	request.Header.Set(ActivityHeaderName, activityID)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("notice status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	var response noticeResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	return response
}
