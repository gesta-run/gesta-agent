package mcpmeta

import "strings"

const maxServerIdentityBytes = 255

func NormalizeServerName(value string) string {
	value = strings.Trim(strings.TrimSpace(value), "`\"'")
	value = strings.TrimRight(value, ":")
	value = strings.TrimSpace(value)
	lower := strings.ToLower(value)
	if lower == "" || lower == "checking" || lower == "codex_apps" || lower == "codex-apps" {
		return ""
	}
	return lower
}

func ServerIdentity(value string) (string, string) {
	serverID := NormalizeServerName(value)
	if serverID == "" || len([]byte(serverID)) > maxServerIdentityBytes {
		return "", ""
	}
	if isUUIDShapedServerID(serverID) {
		return serverID, ""
	}
	return serverID, serverID
}

func isUUIDShapedServerID(value string) bool {
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
