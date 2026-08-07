package localactivity

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gesta-run/gesta-agent/pkg/activitydetail"
	"github.com/gesta-run/gesta-agent/pkg/turnreceipt"
)

func TestHandlerServesHealthAndSecurityHeaders(t *testing.T) {
	handler := newHandlerWithDaemonID(activitydetail.NewStore(t.TempDir()), "")
	request := httptest.NewRequest(http.MethodGet, BaseURL+"/healthz", nil)
	request.Host = Address
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusNoContent {
		t.Fatalf("health status = %d", response.Code)
	}
	if response.Header().Get(healthHeaderName) != healthHeaderValue {
		t.Fatalf("health marker = %q", response.Header().Get(healthHeaderName))
	}
	for _, header := range []string{
		"Content-Security-Policy",
		"Cache-Control",
		"X-Content-Type-Options",
		"Referrer-Policy",
	} {
		if response.Header().Get(header) == "" {
			t.Fatalf("missing security header %s", header)
		}
	}
}

func TestHandlerHealthIdentifiesConfiguredDaemon(t *testing.T) {
	handler := newHandlerWithDaemonID(activitydetail.NewStore(t.TempDir()), "daemon_test")
	request := httptest.NewRequest(http.MethodGet, BaseURL+"/healthz", nil)
	request.Host = Address
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusNoContent {
		t.Fatalf("health status = %d", response.Code)
	}
	if got := response.Header().Get(daemonHeaderName); got != "daemon_test" {
		t.Fatalf("daemon header = %q, want daemon_test", got)
	}
}

func TestHandlerRendersEscapedActivityDetailWithoutRemoteResources(t *testing.T) {
	store := activitydetail.NewStore(t.TempDir())
	detail, err := store.Create("claude_code", []turnreceipt.ContextRuleMatch{
		{
			RuleID:    "rule-review",
			Name:      `<script>alert("x")</script> Review Standards`,
			MatchType: "regex",
			Priority:  80,
			Content:   "Review <script>alert('content')</script>\nPreserve this line.",
		},
		{
			RuleID:    "rule-delete",
			Name:      "Deletion Operations",
			MatchType: "keyword_any",
			Priority:  70,
			Content:   "Confirm the deletion scope before proceeding.",
		},
	}, turnreceipt.OutputSummary{CodeLines: 42, DocWords: 18})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	handler := newHandlerWithDaemonID(store, "")
	request := httptest.NewRequest(http.MethodGet, ActivityURL(detail.ActivityID), nil)
	request.Host = Address
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("activity status = %d, body = %s", response.Code, response.Body.String())
	}
	body := response.Body.String()
	for _, expected := range []string{
		"Local activity detail",
		"Claude Code",
		"Review Standards",
		"Regex match",
		"Appended content",
		"Preserve this line.",
		`aria-label="Gesta"`,
		"--background: #0a0a0a",
		"42",
		"code lines",
	} {
		if !strings.Contains(body, expected) {
			t.Fatalf("activity body missing %q: %s", expected, body)
		}
	}
	if strings.Contains(body, `<script>alert`) {
		t.Fatalf("activity body did not escape rule name: %s", body)
	}
	if strings.Count(body, `<details class="rule"`) != 2 ||
		strings.Count(body, `<details class="rule" open`) != 1 {
		t.Fatalf("activity disclosure states are incorrect: %s", body)
	}
	for _, forbidden := range []string{
		"<script",
		"<link",
		`src="http`,
		`href="http`,
		"url(http",
		"@import",
	} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("activity body contains forbidden resource %q", forbidden)
		}
	}
}

func TestHandlerRendersEmbeddedUnavailablePage(t *testing.T) {
	handler := newHandlerWithDaemonID(activitydetail.NewStore(t.TempDir()), "")
	request := httptest.NewRequest(
		http.MethodGet,
		BaseURL+"/activity/activity_0123456789abcdef0123456789abcdef",
		nil,
	)
	request.Host = Address
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusNotFound)
	}
	body := response.Body.String()
	for _, expected := range []string{
		"Activity unavailable",
		`aria-label="Gesta"`,
		"--background: #0a0a0a",
	} {
		if !strings.Contains(body, expected) {
			t.Fatalf("unavailable body missing %q: %s", expected, body)
		}
	}
	if strings.Contains(body, "{{") {
		t.Fatalf("unavailable body contains an unrendered template action: %s", body)
	}
}

func TestHandlerRejectsInvalidHostMethodAndMissingActivity(t *testing.T) {
	handler := newHandlerWithDaemonID(activitydetail.NewStore(t.TempDir()), "")
	tests := []struct {
		name   string
		method string
		path   string
		host   string
		status int
	}{
		{
			name: "host", method: http.MethodGet, path: "/healthz",
			host: "example.com:3333", status: http.StatusMisdirectedRequest,
		},
		{
			name: "method", method: http.MethodPost, path: "/healthz",
			host: Address, status: http.StatusMethodNotAllowed,
		},
		{
			name: "missing", method: http.MethodGet, path: "/activity/activity_0123456789abcdef0123456789abcdef",
			host: Address, status: http.StatusNotFound,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(test.method, BaseURL+test.path, nil)
			request.Host = test.host
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != test.status {
				t.Fatalf("status = %d, want %d", response.Code, test.status)
			}
		})
	}
}

func TestHandlerHeadResponsesHaveNoBody(t *testing.T) {
	store := activitydetail.NewStore(t.TempDir())
	detail, err := store.Create("codex", []turnreceipt.ContextRuleMatch{{
		RuleID: "rule", Name: "Rule", MatchType: "keyword_any", Content: "Follow the rule.",
	}}, turnreceipt.OutputSummary{})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	handler := newHandlerWithDaemonID(store, "")
	for _, path := range []string{
		"/activity/" + detail.ActivityID,
		"/activity/activity_0123456789abcdef0123456789abcdef",
	} {
		request := httptest.NewRequest(http.MethodHead, BaseURL+path, nil)
		request.Host = Address
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Body.Len() != 0 {
			t.Fatalf("HEAD %s body = %q, want empty", path, response.Body.String())
		}
	}
}
