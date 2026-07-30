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
	Commit func() error
}

func DefaultAdapters() []Adapter {
	return defaultAdaptersForOS(runtime.GOOS)
}

func defaultAdaptersForOS(goos string) []Adapter {
	adapters := []Adapter{
		CodexAdapter{},
	}
	if goos == "darwin" {
		adapters = append(adapters, CodexDesktopAdapter{})
	}
	return append(adapters, ClaudeCodeAdapter{})
}

func Collect(ctx context.Context, cfg Config) ([]model.EventEnvelope, []model.AdapterStatus, func() error) {
	return CollectWithAdapters(ctx, cfg, DefaultAdapters())
}

func CollectWithAdapters(ctx context.Context, cfg Config, adapters []Adapter) ([]model.EventEnvelope, []model.AdapterStatus, func() error) {
	type adapterCollection struct {
		result AdapterResult
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
				result: result,
				events: adapterEvents,
			}
		}(index, adapter)
	}
	wg.Wait()

	var events []model.EventEnvelope
	statuses := make([]model.AdapterStatus, 0, len(results))
	commits := make([]func() error, 0, len(results))
	for _, result := range results {
		statuses = append(statuses, result.result.Status)
		events = append(events, result.events...)
		if result.result.Commit != nil {
			commits = append(commits, result.result.Commit)
		}
	}
	events = append(events, systemEvent(cfg))
	return events, statuses, combineAdapterCommits(commits)
}

func combineAdapterCommits(commits []func() error) func() error {
	if len(commits) == 0 {
		return nil
	}
	return func() error {
		for _, commit := range commits {
			if err := commit(); err != nil {
				return err
			}
		}
		return nil
	}
}

func baseEvent(cfg Config, eventType, source, agentType string, payload map[string]interface{}) model.EventEnvelope {
	return model.EventEnvelope{
		EventID:      util.NewID("evt"),
		CustomerID:   cfg.CustomerID,
		DeploymentID: cfg.DeploymentID,
		DaemonID:     cfg.DaemonID,
		DeviceID:     cfg.DeviceID,
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
	if cmdCtx.Err() != nil {
		return string(out), cmdCtx.Err()
	}
	return string(out), err
}

func findFiles(pattern string) []string {
	matches, err := filepath.Glob(pattern)
	if err != nil {
		return nil
	}
	return matches
}
