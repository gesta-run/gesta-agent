package daemon

import "strings"

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
