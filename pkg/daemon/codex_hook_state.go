package daemon

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func codexUserPromptSubmitHookActive() bool {
	home, err := os.UserHomeDir()
	if err != nil {
		return false
	}
	codexDir := filepath.Join(home, ".codex")
	configPath := filepath.Join(codexDir, "config.toml")
	configData, err := os.ReadFile(configPath)
	if err != nil {
		return false
	}
	configText := string(configData)
	features := codexTomlSection(configText, "[features]")
	if enabled, ok := codexTomlBool(features, "hooks"); !ok || !enabled {
		return false
	}

	hookPath := filepath.Join(codexDir, "hooks.json")
	hookData, err := os.ReadFile(hookPath)
	if err != nil {
		return false
	}
	var hooks codexHooksFile
	if err := json.Unmarshal(hookData, &hooks); err != nil {
		return false
	}
	for index, group := range hooks.Hooks["UserPromptSubmit"] {
		if strings.TrimSpace(group.Matcher) != "" || !codexHookGroupRunsGesta(group) {
			continue
		}
		if codexHookStateTrusted(configText, hookPath, "user_prompt_submit", index) {
			return true
		}
	}
	return false
}

func codexHookStateTrusted(configText, hookPath, eventName string, groupIndex int) bool {
	header := "[hooks.state." + tomlBasicString(fmt.Sprintf("%s:%s:%d:0", hookPath, eventName, groupIndex)) + "]"
	section := codexTomlSection(configText, header)
	if section == "" {
		return false
	}
	if enabled, ok := codexTomlBool(section, "enabled"); ok && !enabled {
		return false
	}
	hash, ok := codexTomlString(section, "trusted_hash")
	return ok && strings.TrimSpace(hash) != ""
}

func codexTomlSection(text, header string) string {
	start := strings.Index(text, header)
	if start < 0 {
		return ""
	}
	rest := text[start:]
	if next := strings.Index(rest[len(header):], "\n["); next >= 0 {
		return rest[:len(header)+next]
	}
	return rest
}

func codexTomlBool(section, key string) (bool, bool) {
	value, ok := codexTomlValue(section, key)
	if !ok {
		return false, false
	}
	switch strings.ToLower(value) {
	case "true":
		return true, true
	case "false":
		return false, true
	default:
		return false, false
	}
}

func codexTomlString(section, key string) (string, bool) {
	value, ok := codexTomlValue(section, key)
	if !ok || len(value) < 2 || value[0] != '"' || value[len(value)-1] != '"' {
		return "", false
	}
	return strings.Trim(value, `"`), true
}

func codexTomlValue(section, key string) (string, bool) {
	for _, line := range splitConfigLines(section) {
		trimmed := strings.TrimSpace(line)
		if !isTomlAssignment(trimmed, key) {
			continue
		}
		parts := strings.SplitN(trimmed, "=", 2)
		if len(parts) != 2 {
			return "", false
		}
		return strings.TrimSpace(parts[1]), true
	}
	return "", false
}
