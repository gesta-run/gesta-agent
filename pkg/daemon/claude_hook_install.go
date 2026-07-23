package daemon

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

var claudePolicyHookEvents = []struct {
	hookEventName string
	matcher       string
}{
	{hookEventName: "SessionStart"},
	{hookEventName: "PreToolUse", matcher: "*"},
	{hookEventName: "PostToolUse", matcher: "*"},
	{hookEventName: "UserPromptSubmit"},
}

var retiredClaudePolicyHookEvents = []string{"PostToolUseFailure"}

// InstallClaudeCodePolicyHook registers the Gesta policy hook in
// ~/.claude/settings.json. It preserves all unknown top-level keys and unknown
// fields on existing hook groups/commands by operating on a generic JSON tree.
func InstallClaudeCodePolicyHook(agentPath string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	agentPath, err = filepath.Abs(agentPath)
	if err != nil {
		return "", err
	}
	claudeDir := filepath.Join(home, ".claude")
	if err := os.MkdirAll(claudeDir, 0o700); err != nil {
		return "", err
	}
	settingsPath := filepath.Join(claudeDir, "settings.json")

	root := map[string]interface{}{}
	data, readErr := os.ReadFile(settingsPath)
	if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
		// Only a missing file is safe to treat as "start fresh". Any other read
		// error (EACCES, transient I/O, …) must abort so we never overwrite the
		// user's existing ~/.claude/settings.json with only the Gesta hooks.
		return "", fmt.Errorf("read %s: %w", settingsPath, readErr)
	}
	if readErr == nil && len(strings.TrimSpace(string(data))) > 0 {
		if err := json.Unmarshal(data, &root); err != nil {
			return "", fmt.Errorf("existing %s is not a JSON object; refusing to overwrite: %w", settingsPath, err)
		}
	}

	hooks, _ := root["hooks"].(map[string]interface{})
	if hooks == nil {
		hooks = map[string]interface{}{}
	}

	command := shellQuote(agentPath) + " claude-hook"
	for _, event := range retiredClaudePolicyHookEvents {
		existing, _ := hooks[event].([]interface{})
		filtered := make([]interface{}, 0, len(existing))
		for _, group := range existing {
			if !claudeHookGroupRunsGesta(group) {
				filtered = append(filtered, group)
			}
		}
		if len(filtered) == 0 {
			delete(hooks, event)
		} else {
			hooks[event] = filtered
		}
	}
	for _, event := range claudePolicyHookEvents {
		gestaGroup := claudePolicyHookGroup(command, event.matcher)
		existing, _ := hooks[event.hookEventName].([]interface{})
		merged := make([]interface{}, 0, len(existing)+1)
		merged = append(merged, gestaGroup)
		for _, group := range existing {
			if claudeHookGroupRunsGesta(group) {
				continue
			}
			merged = append(merged, group)
		}
		hooks[event.hookEventName] = merged
	}
	root["hooks"] = hooks

	data, err = json.MarshalIndent(root, "", "  ")
	if err != nil {
		return "", err
	}
	data = append(data, '\n')
	if err := os.WriteFile(settingsPath, data, 0o600); err != nil {
		return "", err
	}
	return settingsPath, nil
}

func claudePolicyHookGroup(command, matcher string) map[string]interface{} {
	group := map[string]interface{}{
		"hooks": []interface{}{
			map[string]interface{}{
				"type":    "command",
				"command": command,
				"timeout": 30,
			},
		},
	}
	if strings.TrimSpace(matcher) != "" {
		group["matcher"] = matcher
	}
	return group
}

func claudeHookGroupRunsGesta(group interface{}) bool {
	groupMap, ok := group.(map[string]interface{})
	if !ok {
		return false
	}
	hooks, ok := groupMap["hooks"].([]interface{})
	if !ok {
		return false
	}
	for _, hook := range hooks {
		hookMap, ok := hook.(map[string]interface{})
		if !ok {
			continue
		}
		command, _ := hookMap["command"].(string)
		command = strings.TrimSpace(command)
		// Match on the Gesta-specific `claude-hook` subcommand rather than the
		// binary path: the agent may be installed under any name/path, and `run`
		// reinstalls on every daemon start, so a path-substring check would
		// accumulate duplicate Gesta groups on each restart.
		if strings.HasSuffix(command, " claude-hook") || command == "claude-hook" {
			return true
		}
	}
	return false
}
