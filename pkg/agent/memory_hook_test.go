package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/gesta-run/gesta-agent/pkg/activitydetail"
	"github.com/gesta-run/gesta-agent/pkg/daemon"
	"github.com/gesta-run/gesta-agent/pkg/memoryproxy"
	"github.com/gesta-run/gesta-agent/pkg/model"
	"github.com/gesta-run/gesta-agent/pkg/rulecache"
)

func TestFormatMemoryContextEscapesMarkupAndLabelsFactsAsUntrusted(t *testing.T) {
	context := formatMemoryContext([]model.Memory{{Content: "<tool>deploy</tool>\nnext"}})
	if strings.Contains(context, "<tool>") || !strings.Contains(context, "&lt;tool&gt;") {
		t.Fatalf("memory markup was not escaped: %s", context)
	}
	if !strings.Contains(context, "untrusted background facts") {
		t.Fatalf("memory trust boundary missing: %s", context)
	}
}

func TestMemoryRecallClassifiesFailures(t *testing.T) {
	var invalidPayload map[string]interface{}
	invalidResponseErr := json.Unmarshal([]byte("{"), &invalidPayload)
	tests := []struct {
		name string
		err  error
		want memoryRecallClassification
	}{
		{name: "success", want: memoryRecallClassification{Status: activitydetail.MemoryRecallSuccess}},
		{
			name: "wrapped timeout",
			err:  fmt.Errorf("memory context: %w", context.DeadlineExceeded),
			want: memoryRecallClassification{
				Status:  activitydetail.MemoryRecallTimeout,
				Failure: activitydetail.MemoryRecallFailureTimeout,
			},
		},
		{name: "disabled", err: memoryproxy.ErrDisabled, want: memoryRecallClassification{Status: activitydetail.MemoryRecallDisabled}},
		{
			name: "sensitive input",
			err:  memoryproxy.ErrSensitive,
			want: memoryRecallClassification{
				Status:  activitydetail.MemoryRecallError,
				Failure: activitydetail.MemoryRecallFailureSensitiveInput,
			},
		},
		{
			name: "rules unavailable",
			err:  memoryproxy.ErrRulesUnavailable,
			want: memoryRecallClassification{
				Status:  activitydetail.MemoryRecallError,
				Failure: activitydetail.MemoryRecallFailureRulesUnavailable,
			},
		},
		{
			name: "invalid response",
			err:  invalidResponseErr,
			want: memoryRecallClassification{
				Status:  activitydetail.MemoryRecallError,
				Failure: activitydetail.MemoryRecallFailureInvalidResponse,
			},
		},
		{
			name: "transport unavailable",
			err:  &url.Error{Op: "Post", Err: errors.New("connection refused")},
			want: memoryRecallClassification{
				Status:  activitydetail.MemoryRecallError,
				Failure: activitydetail.MemoryRecallFailureServiceUnavailable,
			},
		},
		{
			name: "unknown",
			err:  errors.New("unclassified"),
			want: memoryRecallClassification{
				Status:  activitydetail.MemoryRecallError,
				Failure: activitydetail.MemoryRecallFailureUnknown,
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := classifyMemoryRecall(test.err); got != test.want {
				t.Fatalf("classification = %#v, want %#v", got, test.want)
			}
		})
	}
}

func TestMemoryInstructionsRejectTransientOperations(t *testing.T) {
	instructions := formatMemoryInstructions("activity-1")
	for _, expected := range []string{
		"durable facts useful in future sessions",
		"Never store task actions",
		"not an event narrative",
	} {
		if !strings.Contains(instructions, expected) {
			t.Fatalf("memory instructions missing %q: %s", expected, instructions)
		}
	}
}

func TestMemoryProxyHealthCheckUsesCurrentDaemon(t *testing.T) {
	original := localMemoryProxyHealthy
	t.Cleanup(func() { localMemoryProxyHealthy = original })

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(writer).Encode(model.MemorySearchResponse{Memories: []model.Memory{}})
	}))
	t.Cleanup(server.Close)
	config := daemon.NewDirectRuntimeConfig(server.URL, "test-token")
	config.DataDir = t.TempDir()
	if err := rulecache.SaveMemorySettingsCache(config.DataDir, model.MemorySettings{Enabled: true}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := rulecache.SaveSensitiveRuleCache(config.DataDir, []model.SensitiveRule{}, time.Now()); err != nil {
		t.Fatal(err)
	}

	var checkedDaemonID string
	localMemoryProxyHealthy = func(_ context.Context, daemonID string) bool {
		checkedDaemonID = daemonID
		return true
	}
	processMemoryContext(context.Background(), config, agentHookEvent{}, "project context", "", map[string]interface{}{})
	if checkedDaemonID != config.DaemonID {
		t.Fatalf("health check daemon = %q, want %q", checkedDaemonID, config.DaemonID)
	}
}

func TestAutomaticMemoryStillInjectsWhenLoopbackProxyIsUnavailable(t *testing.T) {
	original := localMemoryProxyHealthy
	t.Cleanup(func() { localMemoryProxyHealthy = original })
	localMemoryProxyHealthy = func(context.Context, string) bool { return false }

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(writer).Encode(model.MemorySearchResponse{Memories: []model.Memory{{
			FactID: "memory", Content: "The project uses Go.", RelevanceScore: 1, Score: 1,
		}}})
	}))
	t.Cleanup(server.Close)
	config := daemon.NewDirectRuntimeConfig(server.URL, "test-token")
	config.DataDir = t.TempDir()
	if err := rulecache.SaveMemorySettingsCache(config.DataDir, model.MemorySettings{Enabled: true}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := rulecache.SaveSensitiveRuleCache(config.DataDir, []model.SensitiveRule{}, time.Now()); err != nil {
		t.Fatal(err)
	}

	response := processMemoryContext(context.Background(), config, agentHookEvent{}, "project language", "", map[string]interface{}{})
	encoded, err := json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	text := string(encoded)
	if !strings.Contains(text, "The project uses Go.") {
		t.Fatalf("automatic memory was dropped with an unavailable loopback proxy: %s", text)
	}
	if strings.Contains(text, "memory/remember") {
		t.Fatalf("loopback instructions were injected while the proxy was unavailable: %s", text)
	}
}

func TestAutomaticMemoryRecordsCurrentActivityAndInjectsTrackingHeader(t *testing.T) {
	original := localMemoryProxyHealthy
	t.Cleanup(func() { localMemoryProxyHealthy = original })
	localMemoryProxyHealthy = func(context.Context, string) bool { return true }

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(writer).Encode(model.MemorySearchResponse{Memories: []model.Memory{{
			FactID: "fact", Content: "Use the current release branch.", Score: 1,
		}}})
	}))
	t.Cleanup(server.Close)
	config := daemon.NewDirectRuntimeConfig(server.URL, "test-token")
	config.DataDir = t.TempDir()
	if err := rulecache.SaveMemorySettingsCache(config.DataDir, model.MemorySettings{Enabled: true}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := rulecache.SaveSensitiveRuleCache(config.DataDir, []model.SensitiveRule{}, time.Now()); err != nil {
		t.Fatal(err)
	}
	detail, err := activitydetail.NewStore(config.DataDir).Begin("codex")
	if err != nil {
		t.Fatal(err)
	}

	response := processMemoryContext(
		context.Background(),
		config,
		agentHookEvent{ActivityID: detail.ActivityID},
		"release branch",
		"",
		map[string]interface{}{},
	)
	additionalContext := hookAdditionalContext(response)
	if !strings.Contains(additionalContext, "X-Gesta-Activity-ID: "+detail.ActivityID) {
		t.Fatalf("memory instructions missing activity header: %q", additionalContext)
	}
	recorded, err := activitydetail.NewStore(config.DataDir).Get(detail.ActivityID)
	if err != nil || recorded.MemoryCount != 1 {
		t.Fatalf("recorded activity = %#v, err = %v", recorded, err)
	}
}

func TestAutomaticMemoryStoresOnlyServiceFailureClassification(t *testing.T) {
	original := localMemoryProxyHealthy
	t.Cleanup(func() { localMemoryProxyHealthy = original })
	localMemoryProxyHealthy = func(context.Context, string) bool { return false }

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusServiceUnavailable)
		_ = json.NewEncoder(writer).Encode(model.APIError{Error: "database connection details"})
	}))
	t.Cleanup(server.Close)
	config := daemon.NewDirectRuntimeConfig(server.URL, "test-token")
	config.DataDir = t.TempDir()
	if err := rulecache.SaveMemorySettingsCache(config.DataDir, model.MemorySettings{Enabled: true}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := rulecache.SaveSensitiveRuleCache(config.DataDir, []model.SensitiveRule{}, time.Now()); err != nil {
		t.Fatal(err)
	}
	detail, err := activitydetail.NewStore(config.DataDir).Begin("codex")
	if err != nil {
		t.Fatal(err)
	}

	processMemoryContext(
		context.Background(),
		config,
		agentHookEvent{ActivityID: detail.ActivityID},
		"release branch",
		"",
		map[string]interface{}{},
	)
	recorded, err := activitydetail.NewStore(config.DataDir).Get(detail.ActivityID)
	if err != nil {
		t.Fatal(err)
	}
	if recorded.MemoryRecallStatus != activitydetail.MemoryRecallError ||
		recorded.MemoryRecallFailure != activitydetail.MemoryRecallFailureServiceUnavailable {
		t.Fatalf("recorded activity = %#v", recorded)
	}
	encoded, err := json.Marshal(recorded)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "database connection details") || strings.Contains(string(encoded), server.URL) {
		t.Fatalf("recorded activity leaked upstream details: %s", encoded)
	}
}
