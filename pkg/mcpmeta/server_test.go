package mcpmeta

import (
	"strings"
	"testing"
)

func TestMCPServerIdentityUsesConfiguredName(t *testing.T) {
	serverID, serverName := ServerIdentity(" GitHub ")
	if serverID != "github" || serverName != "github" {
		t.Fatalf("identity = (%q, %q), want (github, github)", serverID, serverName)
	}
}

func TestMCPServerIdentityHidesUUIDDisplayName(t *testing.T) {
	const opaqueID = "03acf4c5-efa1-4ae6-9653-f1eda698c57c"
	serverID, serverName := ServerIdentity(opaqueID)
	if serverID != opaqueID || serverName != "" {
		t.Fatalf("identity = (%q, %q), want (%q, empty)", serverID, serverName, opaqueID)
	}
}

func TestMCPServerIdentityRejectsOversizedID(t *testing.T) {
	serverID, serverName := ServerIdentity(strings.Repeat("a", maxServerIdentityBytes+1))
	if serverID != "" || serverName != "" {
		t.Fatalf("identity = (%q, %q), want empty", serverID, serverName)
	}
}
