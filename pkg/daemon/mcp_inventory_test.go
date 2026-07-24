package daemon

import (
	"reflect"
	"testing"
	"time"

	"github.com/gesta-run/gesta-agent/pkg/model"
)

func TestMCPServersFromListOutput(t *testing.T) {
	output := `Name       Command                                                            Args  Env  Cwd  Status   Auth
node_repl  /Applications/Codex.app/Contents/Resources/cua_node/bin/node_repl  -     -    -    enabled  Unsupported

Name       Url                            Bearer Token Env Var  Status   Auth
anysearch  https://api.anysearch.com/mcp  -                     enabled  Bearer token
notion     https://mcp.notion.com/mcp     -                     enabled  OAuth

Checking MCP server health...
anysearch: https://api.anysearch.com/mcp
auth0:
`

	got := parseMCPServersFromListOutput(output).Servers
	want := []model.MCPServerConfiguration{
		{Name: "anysearch", Enabled: true},
		{Name: "auth0", Enabled: true},
		{Name: "node_repl", Enabled: true},
		{Name: "notion", Enabled: true},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("servers = %#v, want %#v", got, want)
	}
}

func TestMCPServersFromListOutputPreservesDisabledState(t *testing.T) {
	output := `Name          Command  Args  Env  Cwd  Status    Auth
node_repl     node     -     -    -    enabled   Unsupported
computer-use  app      -     -    -    disabled  Unsupported
codex_apps    node     -     -    -    enabled   Unsupported
`
	got := parseMCPServersFromListOutput(output).Servers
	want := []model.MCPServerConfiguration{
		{Name: "computer-use", Enabled: false},
		{Name: "node_repl", Enabled: true},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("servers = %#v, want %#v", got, want)
	}
}

func TestMCPInventoryFromListOutputRejectsUnknownFormat(t *testing.T) {
	inventory := mcpInventoryFromListOutput("unexpected successful output", time.Now().UTC())
	if inventory.ScanStatus != "error" || inventory.ErrorCode != "parse_failed" {
		t.Fatalf("inventory = %+v, want parse_failed", inventory)
	}
}

func TestMCPInventoryFromListOutputAcceptsExplicitEmptyInventory(t *testing.T) {
	inventory := mcpInventoryFromListOutput("No MCP servers configured.", time.Now().UTC())
	if inventory.ScanStatus != "ok" || len(inventory.Servers) != 0 {
		t.Fatalf("inventory = %+v, want successful empty inventory", inventory)
	}
}
