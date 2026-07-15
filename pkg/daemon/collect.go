package daemon

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/gesta-run/gesta-agent/pkg/model"
	"github.com/gesta-run/gesta-agent/pkg/util"
)

type Adapter interface {
	Name() string
	Collect(context.Context, Config) (AdapterResult, []model.EventEnvelope)
}

type AdapterResult struct {
	Status model.AdapterStatus
}

func DefaultAdapters() []Adapter {
	return []Adapter{
		CodexAdapter{},
		ClaudeCodeAdapter{},
		GitCommitsAdapter{},
	}
}

func DefaultAdapterNames() []string {
	adapters := DefaultAdapters()
	names := make([]string, 0, len(adapters))
	for _, adapter := range adapters {
		names = append(names, adapter.Name())
	}
	return names
}

func Collect(ctx context.Context, cfg Config) ([]model.EventEnvelope, []model.AdapterStatus) {
	return CollectWithAdapters(ctx, cfg, DefaultAdapters())
}

func CollectWithAdapters(ctx context.Context, cfg Config, adapters []Adapter) ([]model.EventEnvelope, []model.AdapterStatus) {
	type adapterCollection struct {
		result model.AdapterStatus
		events []model.EventEnvelope
	}
	results := make([]adapterCollection, len(adapters))
	var wg sync.WaitGroup
	for index, adapter := range adapters {
		wg.Add(1)
		go func(index int, adapter Adapter) {
			defer wg.Done()
			result, adapterEvents := adapter.Collect(ctx, cfg)
			results[index] = adapterCollection{
				result: result.Status,
				events: adapterEvents,
			}
		}(index, adapter)
	}
	wg.Wait()

	var events []model.EventEnvelope
	statuses := make([]model.AdapterStatus, 0, len(results))
	for _, result := range results {
		statuses = append(statuses, result.result)
		events = append(events, result.events...)
	}
	events = append(events, systemEvent(cfg))
	return events, statuses
}

func baseEvent(cfg Config, eventType, source, agentType string, payload map[string]interface{}) model.EventEnvelope {
	return model.EventEnvelope{
		EventID:      util.NewID("evt"),
		CustomerID:   cfg.CustomerID,
		DeploymentID: cfg.DeploymentID,
		DaemonID:     cfg.DaemonID,
		DeviceID:     cfg.DeviceID,
		UserID:       cfg.UserID,
		UserName:     cfg.EffectiveUserName(),
		TeamID:       cfg.TeamID,
		EventType:    eventType,
		Source:       source,
		AgentType:    agentType,
		CreatedAt:    time.Now().UTC(),
		Payload:      payload,
	}
}

// snapshotEvent makes recurring machine-state observations idempotent. Unlike
// session activity, these payloads describe a point-in-time configuration or
// capability and should only be retained when their observable contents change.
func snapshotEvent(cfg Config, eventType, source, agentType string, payload map[string]interface{}) model.EventEnvelope {
	event := baseEvent(cfg, eventType, source, agentType, payload)
	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		return event
	}
	event.EventID = "evt_snapshot_" + util.HashString(strings.Join([]string{
		cfg.DaemonID,
		eventType,
		source,
		agentType,
		string(payloadJSON),
	}, "\x00"))
	return event
}

func systemEvent(cfg Config) model.EventEnvelope {
	hostname, _ := os.Hostname()
	return snapshotEvent(cfg, "daemon.system_snapshot", "daemon", "", map[string]interface{}{
		"hostname":       hostname,
		"os":             runtime.GOOS,
		"arch":           runtime.GOARCH,
		"host_type":      cfg.HostType,
		"daemon_version": model.DaemonVersion,
		"data_dir_hash":  util.ShortHash(cfg.DataDir),
	})
}

func commandOutput(ctx context.Context, name string, args ...string) (string, error) {
	cmdCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(cmdCtx, name, args...)
	cmd.Env = os.Environ()
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func commandOutputMetadata(output string) map[string]interface{} {
	trimmed := strings.TrimSpace(output)
	lineCount := 0
	if trimmed != "" {
		lineCount = strings.Count(trimmed, "\n") + 1
	}
	return map[string]interface{}{
		"output_hash":   util.HashString(output),
		"byte_count":    len(output),
		"line_count":    lineCount,
		"metadata_only": true,
	}
}

func mcpServersFromListOutput(output string) []string {
	seen := map[string]bool{}
	var servers []string
	for _, line := range strings.Split(output, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		lower := strings.ToLower(trimmed)
		if strings.HasPrefix(lower, "name ") || strings.Contains(lower, "no mcp") {
			continue
		}
		fields := strings.Fields(trimmed)
		if len(fields) == 0 {
			continue
		}
		name := normalizeMCPServerName(fields[0])
		if name == "" || strings.Trim(name, "-") == "" {
			continue
		}
		if !seen[name] {
			seen[name] = true
			servers = append(servers, name)
		}
	}
	return servers
}

func normalizeMCPServerName(value string) string {
	value = strings.Trim(strings.TrimSpace(value), "`\"'")
	value = strings.TrimRight(value, ":")
	value = strings.TrimSpace(value)
	lower := strings.ToLower(value)
	if lower == "" || lower == "checking" {
		return ""
	}
	return value
}

func findFiles(pattern string) []string {
	matches, err := filepath.Glob(pattern)
	if err != nil {
		return nil
	}
	return matches
}
