package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gesta-run/gesta-agent/pkg/daemon"
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
	processMemoryContext(context.Background(), config, agentHookEvent{}, "project context", map[string]interface{}{})
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
			FactID: "memory", Content: "The project uses Go.", GraphRankScore: 1, Score: 1,
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

	response := processMemoryContext(context.Background(), config, agentHookEvent{}, "project language", map[string]interface{}{})
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
