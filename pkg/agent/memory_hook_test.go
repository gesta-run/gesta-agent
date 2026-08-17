package agent

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
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

func TestMemoryRecallStatusClassifiesFailures(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want activitydetail.MemoryRecallStatus
	}{
		{name: "success", want: activitydetail.MemoryRecallSuccess},
		{name: "timeout", err: context.DeadlineExceeded, want: activitydetail.MemoryRecallTimeout},
		{name: "disabled", err: memoryproxy.ErrDisabled, want: activitydetail.MemoryRecallDisabled},
		{name: "error", err: errors.New("unavailable"), want: activitydetail.MemoryRecallError},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := memoryRecallStatus(test.err); got != test.want {
				t.Fatalf("status = %q, want %q", got, test.want)
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
