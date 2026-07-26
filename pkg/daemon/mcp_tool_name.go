package daemon

import "strings"

const maxMCPServerIdentityBytes = 255

func mcpToolParts(name string) (string, string) {
	trimmed := strings.TrimSpace(name)
	if !strings.HasPrefix(trimmed, "mcp__") {
		return "", ""
	}
	rest := strings.TrimPrefix(trimmed, "mcp__")
	var parts []string
	if strings.Contains(rest, "__") {
		parts = strings.SplitN(rest, "__", 2)
	} else if strings.Contains(rest, ".") {
		parts = strings.SplitN(rest, ".", 2)
	} else {
		return "", ""
	}
	server := normalizeMCPServerName(parts[0])
	tool := strings.TrimSpace(parts[1])
	if server == "" || tool == "" {
		return "", ""
	}
	return server, tool
}

func mcpServerIdentity(value string) (string, string) {
	serverID := normalizeMCPServerName(value)
	if serverID == "" || len([]byte(serverID)) > maxMCPServerIdentityBytes {
		return "", ""
	}
	if isUUIDShapedMCPServerID(serverID) {
		return serverID, ""
	}
	return serverID, serverID
}

func isUUIDShapedMCPServerID(value string) bool {
	if len(value) != 36 ||
		value[8] != '-' ||
		value[13] != '-' ||
		value[18] != '-' ||
		value[23] != '-' {
		return false
	}
	for index, char := range value {
		if index == 8 || index == 13 || index == 18 || index == 23 {
			continue
		}
		if (char < '0' || char > '9') &&
			(char < 'a' || char > 'f') &&
			(char < 'A' || char > 'F') {
			return false
		}
	}
	return true
}
