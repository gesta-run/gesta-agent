package daemon

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/gesta-run/gesta-agent/pkg/model"
)

func TestClaudeMCPInventoryReadsCentralConfigNamesOnly(t *testing.T) {
	observedAt := time.Date(2026, 7, 24, 8, 0, 0, 0, time.UTC)
	data := []byte(`{
		"mcpServers": {
			"GitHub": {
				"command": "npx",
				"args": ["server", "--token", "secret-command-token"],
				"env": {"API_KEY": "secret-environment-value"}
			}
		},
		"projects": {
			"/Users/private/customer-repo": {
				"mcpServers": {
					"github": {"url": "https://secret.example/mcp"},
					"Notion": {"headers": {"Authorization": "Bearer secret-header"}}
				}
			}
		}
	}`)

	inventory := claudeMCPInventoryFromConfigData(data, observedAt)
	want := []model.MCPServerConfiguration{
		{Name: "github", Enabled: true},
		{Name: "notion", Enabled: true},
	}
	if inventory.ScanStatus != "ok" || !reflect.DeepEqual(inventory.Servers, want) {
		t.Fatalf("inventory = %#v, want %#v", inventory, want)
	}
	serialized, err := json.Marshal(inventory)
	if err != nil {
		t.Fatalf("marshal inventory: %v", err)
	}
	text := string(serialized)
	for _, forbidden := range []string{
		"secret-command-token",
		"secret-environment-value",
		"secret.example",
		"secret-header",
		"/Users/private/customer-repo",
	} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("inventory leaked %q: %s", forbidden, text)
		}
	}
}

func TestClaudeMCPInventoryMissingConfigIsSuccessfulEmpty(t *testing.T) {
	inventory := claudeMCPInventory(filepath.Join(t.TempDir(), "missing.json"), time.Now().UTC())
	if inventory.ScanStatus != "ok" || inventory.ErrorCode != "" || len(inventory.Servers) != 0 {
		t.Fatalf("inventory = %#v, want successful empty inventory", inventory)
	}
	if inventory.Hash == "" {
		t.Fatal("empty inventory should have a stable hash")
	}
}

func TestClaudeMCPInventoryRejectsMalformedAndOversizedConfig(t *testing.T) {
	observedAt := time.Now().UTC()
	malformed := claudeMCPInventoryFromConfigData([]byte(`{"mcpServers":`), observedAt)
	if malformed.ScanStatus != "error" || malformed.ErrorCode != "config_parse_failed" {
		t.Fatalf("malformed inventory = %#v", malformed)
	}

	path := filepath.Join(t.TempDir(), ".claude.json")
	if err := os.WriteFile(path, []byte(strings.Repeat("x", claudeMCPConfigMaxBytes+1)), 0o600); err != nil {
		t.Fatalf("write oversized config: %v", err)
	}
	oversized := claudeMCPInventory(path, observedAt)
	if oversized.ScanStatus != "error" || oversized.ErrorCode != "config_too_large" {
		t.Fatalf("oversized inventory = %#v", oversized)
	}
}

func TestClaudeMCPInventoryDoesNotReadProjectMCPJSON(t *testing.T) {
	home := t.TempDir()
	configPath := filepath.Join(home, ".claude.json")
	if err := os.WriteFile(configPath, []byte(`{"projects":{}}`), 0o600); err != nil {
		t.Fatalf("write central config: %v", err)
	}
	projectDir := filepath.Join(home, "project")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatalf("mkdir project: %v", err)
	}
	if err := os.WriteFile(filepath.Join(projectDir, ".mcp.json"), []byte(`{
		"mcpServers":{"must-not-appear":{"command":"secret-project-command"}}
	}`), 0o600); err != nil {
		t.Fatalf("write project config: %v", err)
	}

	inventory := claudeMCPInventory(configPath, time.Now().UTC())
	if inventory.ScanStatus != "ok" || len(inventory.Servers) != 0 {
		t.Fatalf("inventory unexpectedly read project .mcp.json: %#v", inventory)
	}
}
