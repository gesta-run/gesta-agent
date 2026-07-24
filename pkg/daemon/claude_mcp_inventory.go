package daemon

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/gesta-run/gesta-agent/pkg/model"
	"github.com/gesta-run/gesta-agent/pkg/util"
)

const claudeMCPConfigMaxBytes = 8 * 1024 * 1024

type claudeMCPConfigFile struct {
	MCPServers map[string]json.RawMessage        `json:"mcpServers"`
	Projects   map[string]claudeMCPProjectConfig `json:"projects"`
}

type claudeMCPProjectConfig struct {
	MCPServers map[string]json.RawMessage `json:"mcpServers"`
}

func claudeConfigPath() string {
	home, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(home) == "" {
		return ""
	}
	return filepath.Join(home, ".claude.json")
}

func claudeMCPInventory(path string, observedAt time.Time) *model.MCPInventoryStatus {
	if strings.TrimSpace(path) == "" {
		return claudeMCPInventoryError("config_path_unavailable", observedAt)
	}
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return claudeMCPInventoryFromConfigData([]byte(`{}`), observedAt)
	}
	if err != nil {
		return claudeMCPInventoryError("config_unreadable", observedAt)
	}
	defer file.Close()

	data, err := io.ReadAll(io.LimitReader(file, claudeMCPConfigMaxBytes+1))
	if err != nil {
		return claudeMCPInventoryError("config_unreadable", observedAt)
	}
	if len(data) > claudeMCPConfigMaxBytes {
		return claudeMCPInventoryError("config_too_large", observedAt)
	}
	return claudeMCPInventoryFromConfigData(data, observedAt)
}

func claudeMCPInventoryFromConfigData(data []byte, observedAt time.Time) *model.MCPInventoryStatus {
	var config claudeMCPConfigFile
	if err := json.Unmarshal(data, &config); err != nil {
		return claudeMCPInventoryError("config_parse_failed", observedAt)
	}

	serversByName := map[string]model.MCPServerConfiguration{}
	addServers := func(configured map[string]json.RawMessage) {
		for name := range configured {
			normalized := normalizeMCPServerName(name)
			if normalized == "" {
				continue
			}
			serversByName[normalized] = model.MCPServerConfiguration{
				Name:    normalized,
				Enabled: true,
			}
		}
	}
	addServers(config.MCPServers)
	for _, project := range config.Projects {
		addServers(project.MCPServers)
	}

	servers := make([]model.MCPServerConfiguration, 0, len(serversByName))
	for _, server := range serversByName {
		servers = append(servers, server)
	}
	sort.Slice(servers, func(i, j int) bool {
		return servers[i].Name < servers[j].Name
	})
	canonical, _ := json.Marshal(servers)
	return &model.MCPInventoryStatus{
		ScanStatus: "ok",
		ObservedAt: normalizedMCPObservedAt(observedAt),
		Hash:       util.HashString(string(canonical)),
		Servers:    servers,
	}
}

func claudeMCPInventoryError(code string, observedAt time.Time) *model.MCPInventoryStatus {
	return &model.MCPInventoryStatus{
		ScanStatus: "error",
		ObservedAt: normalizedMCPObservedAt(observedAt),
		ErrorCode:  code,
	}
}

func normalizedMCPObservedAt(observedAt time.Time) string {
	if observedAt.IsZero() {
		observedAt = time.Now().UTC()
	}
	return observedAt.UTC().Format(time.RFC3339Nano)
}
