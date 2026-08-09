package hookinstall

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"

	"github.com/gesta-run/gesta-agent/pkg/atomicfile"
)

const gestaCodexHookStatus = "Checking Gesta policy"

var codexPolicyHookEvents = []struct {
	hookEventName  string
	stateEventName string
	matcher        string
}{
	{hookEventName: "SessionStart", stateEventName: "session_start"},
	{hookEventName: "PreToolUse", stateEventName: "pre_tool_use", matcher: "*"},
	{hookEventName: "Stop", stateEventName: "stop"},
	{hookEventName: "UserPromptSubmit", stateEventName: "user_prompt_submit"},
}

var retiredCodexPolicyHookEvents = []struct {
	hookEventName  string
	stateEventName string
}{
	{hookEventName: "PostToolUse", stateEventName: "post_tool_use"},
}

func InstallCodexPolicyHook(agentPath string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	agentPath, err = filepath.Abs(agentPath)
	if err != nil {
		return "", err
	}
	hookPath := filepath.Join(home, ".codex", "hooks.json")
	if err := os.MkdirAll(filepath.Dir(hookPath), 0o700); err != nil {
		return "", err
	}

	var hooks codexHooksFile
	data, readErr := os.ReadFile(hookPath)
	if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
		return "", fmt.Errorf("read %s: %w", hookPath, readErr)
	}
	if readErr == nil && len(strings.TrimSpace(string(data))) > 0 {
		if err := json.Unmarshal(data, &hooks); err != nil {
			return "", fmt.Errorf("existing %s is not a valid hooks file; refusing to overwrite: %w", hookPath, err)
		}
	}
	if hooks.Hooks == nil {
		hooks.Hooks = map[string][]codexHookGroup{}
	}

	command, commandWindows, trustedCommand := codexHookCommands(agentPath, runtime.GOOS)
	for _, event := range retiredCodexPolicyHookEvents {
		groups := hooks.Hooks[event.hookEventName]
		filtered := make([]codexHookGroup, 0, len(groups))
		for _, existing := range groups {
			if !codexHookGroupRunsGesta(existing) {
				filtered = append(filtered, existing)
			}
		}
		if len(filtered) == 0 {
			delete(hooks.Hooks, event.hookEventName)
		} else {
			hooks.Hooks[event.hookEventName] = filtered
		}
	}
	for _, event := range codexPolicyHookEvents {
		group := codexPolicyHookGroupWithMatcher(command, commandWindows, event.matcher)
		groups := hooks.Hooks[event.hookEventName]
		filtered := make([]codexHookGroup, 0, len(groups)+1)
		filtered = append(filtered, group)
		for _, existing := range groups {
			if codexHookGroupRunsGesta(existing) {
				continue
			}
			filtered = append(filtered, existing)
		}
		hooks.Hooks[event.hookEventName] = filtered
	}

	if err := atomicfile.WriteJSON(hookPath, hooks); err != nil {
		return "", err
	}
	if err := ensureCodexPolicyHookConfig(hookPath, trustedCommand); err != nil {
		return "", err
	}
	return hookPath, nil
}

func codexPolicyHookGroupWithMatcher(command, commandWindows, matcher string) codexHookGroup {
	group := codexHookGroup{
		Hooks: []codexHookCommand{{
			Type:           "command",
			Command:        command,
			CommandWindows: commandWindows,
			Timeout:        30,
			StatusMessage:  gestaCodexHookStatus,
		}},
	}
	if strings.TrimSpace(matcher) != "" {
		group.Matcher = matcher
	}
	return group
}

func codexHookGroupRunsGesta(group codexHookGroup) bool {
	for _, hook := range group.Hooks {
		for _, rawCommand := range []string{hook.Command, hook.CommandWindows} {
			command := strings.TrimSpace(rawCommand)
			if strings.Contains(command, "gesta-agent") && strings.Contains(command, "codex-hook") {
				return true
			}
		}
	}
	return false
}

func shellQuote(value string) string {
	return quoteCommandPath(value, runtime.GOOS)
}

func codexHookCommands(agentPath, goos string) (command, commandWindows, trustedCommand string) {
	command = quoteCommandPath(agentPath, goos) + " codex-hook"
	trustedCommand = command
	if goos == "windows" {
		commandWindows = "& " + quotePowerShellLiteral(agentPath) + " codex-hook"
		trustedCommand = commandWindows
	}
	return command, commandWindows, trustedCommand
}

func quotePowerShellLiteral(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "''") + "'"
}

func quoteCommandPath(value, goos string) string {
	if goos == "windows" {
		return `"` + strings.ReplaceAll(value, `"`, `\"`) + `"`
	}
	if value == "" {
		return "''"
	}
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}

func CodexHooksDisabled() (bool, string) {
	home, err := os.UserHomeDir()
	if err != nil {
		return false, ""
	}
	configPath := filepath.Join(home, ".codex", "config.toml")
	data, err := os.ReadFile(configPath)
	if err != nil {
		return false, configPath
	}
	return regexp.MustCompile(`(?m)^\s*hooks\s*=\s*false\s*$`).Match(data), configPath
}
