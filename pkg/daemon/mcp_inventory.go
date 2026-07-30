package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/gesta-run/gesta-agent/internal/mcpmeta"
	"github.com/gesta-run/gesta-agent/pkg/model"
	"github.com/gesta-run/gesta-agent/pkg/util"
)

type mcpListParseResult struct {
	Servers    []model.MCPServerConfiguration
	Recognized bool
}

func parseMCPServersFromListOutput(output string) mcpListParseResult {
	serversByName := map[string]model.MCPServerConfiguration{}
	recognizedFormat := false
	inHealthSection := false
	for _, line := range strings.Split(output, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		lower := strings.ToLower(trimmed)
		switch {
		case strings.Contains(lower, "no mcp"):
			recognizedFormat = true
			continue
		case strings.HasPrefix(lower, "name "):
			recognizedFormat = true
			inHealthSection = false
			continue
		case strings.HasPrefix(lower, "checking ") && strings.Contains(lower, "mcp"):
			recognizedFormat = true
			inHealthSection = true
			continue
		}

		fields := strings.Fields(trimmed)
		if len(fields) == 0 {
			continue
		}
		name := mcpmeta.NormalizeServerName(fields[0])
		if name == "" || strings.Trim(name, "-") == "" {
			continue
		}
		enabled, hasStatus := mcpEnabledFromFields(fields)
		isHealthRow := inHealthSection && strings.HasSuffix(fields[0], ":")
		if !hasStatus && !isHealthRow {
			continue
		}
		recognizedFormat = true
		key := strings.ToLower(name)
		if existing, ok := serversByName[key]; !ok || hasStatus {
			serversByName[key] = model.MCPServerConfiguration{
				Name:    name,
				Enabled: enabled || existing.Enabled,
			}
		}
	}

	servers := make([]model.MCPServerConfiguration, 0, len(serversByName))
	for _, server := range serversByName {
		servers = append(servers, server)
	}
	sort.Slice(servers, func(i, j int) bool {
		return strings.ToLower(servers[i].Name) < strings.ToLower(servers[j].Name)
	})
	return mcpListParseResult{Servers: servers, Recognized: recognizedFormat}
}

func mcpEnabledFromFields(fields []string) (bool, bool) {
	for _, field := range fields[1:] {
		switch strings.ToLower(strings.Trim(field, "✓✗✔:")) {
		case "enabled", "connected":
			return true, true
		case "disabled":
			return false, true
		}
	}
	return true, false
}

func mcpInventoryFromListOutput(output string, observedAt time.Time) *model.MCPInventoryStatus {
	parsed := parseMCPServersFromListOutput(output)
	if !parsed.Recognized {
		return &model.MCPInventoryStatus{
			ScanStatus: "error",
			ObservedAt: observedAt.UTC().Format(time.RFC3339Nano),
			ErrorCode:  "parse_failed",
		}
	}
	canonical, _ := json.Marshal(parsed.Servers)
	return &model.MCPInventoryStatus{
		ScanStatus: "ok",
		ObservedAt: observedAt.UTC().Format(time.RFC3339Nano),
		Hash:       util.HashString(string(canonical)),
		Servers:    parsed.Servers,
	}
}

func failedMCPInventory(err error, observedAt time.Time) *model.MCPInventoryStatus {
	code := "command_failed"
	if errors.Is(err, context.DeadlineExceeded) {
		code = "timeout"
	}
	return &model.MCPInventoryStatus{
		ScanStatus: "error",
		ObservedAt: observedAt.UTC().Format(time.RFC3339Nano),
		ErrorCode:  code,
	}
}

func unsupportedMCPInventory(observedAt time.Time) *model.MCPInventoryStatus {
	return &model.MCPInventoryStatus{
		ScanStatus: "unsupported",
		ObservedAt: observedAt.UTC().Format(time.RFC3339Nano),
		ErrorCode:  "unsupported",
	}
}
