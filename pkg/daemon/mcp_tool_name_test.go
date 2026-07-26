package daemon

import (
	"strings"
	"testing"
)

func TestMCPServerIdentityUsesConfiguredName(t *testing.T) {
	serverID, serverName := mcpServerIdentity(" GitHub ")
	if serverID != "github" || serverName != "github" {
		t.Fatalf("identity = (%q, %q), want (github, github)", serverID, serverName)
	}
}

func TestMCPServerIdentityHidesUUIDDisplayName(t *testing.T) {
	const opaqueID = "03acf4c5-efa1-4ae6-9653-f1eda698c57c"
	serverID, serverName := mcpServerIdentity(opaqueID)
	if serverID != opaqueID || serverName != "" {
		t.Fatalf("identity = (%q, %q), want (%q, empty)", serverID, serverName, opaqueID)
	}
}

func TestMCPServerIdentityRejectsOversizedID(t *testing.T) {
	serverID, serverName := mcpServerIdentity(strings.Repeat("a", maxMCPServerIdentityBytes+1))
	if serverID != "" || serverName != "" {
		t.Fatalf("identity = (%q, %q), want empty", serverID, serverName)
	}
}
