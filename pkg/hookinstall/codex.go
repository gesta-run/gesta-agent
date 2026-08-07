package hookinstall

import (
	"crypto/sha256"
	"encoding/hex"
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

	command := shellQuote(agentPath) + " codex-hook"
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
		group := codexPolicyHookGroupWithMatcher(command, event.matcher)
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
	if err := ensureCodexPolicyHookConfig(hookPath, command); err != nil {
		return "", err
	}
	return hookPath, nil
}

func codexPolicyHookGroupWithMatcher(command, matcher string) codexHookGroup {
	group := codexHookGroup{
		Hooks: []codexHookCommand{{
			Type:          "command",
			Command:       command,
			Timeout:       30,
			StatusMessage: gestaCodexHookStatus,
		}},
	}
	if strings.TrimSpace(matcher) != "" {
		group.Matcher = matcher
	}
	return group
}

func codexHookGroupRunsGesta(group codexHookGroup) bool {
	for _, hook := range group.Hooks {
		command := strings.TrimSpace(hook.Command)
		if strings.Contains(command, "gesta-agent") && strings.Contains(command, "codex-hook") {
			return true
		}
	}
	return false
}

func ensureCodexPolicyHookConfig(hookPath, command string) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	configPath := filepath.Join(home, ".codex", "config.toml")
	data, err := os.ReadFile(configPath)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	updated := updateCodexHookConfig(string(data), hookPath, command)
	if updated == string(data) {
		return nil
	}
	return atomicfile.Write(configPath, []byte(updated))
}

func updateCodexHookConfig(configText, hookPath, command string) string {
	updated := ensureCodexFeatureFlags(configText)
	for _, event := range retiredCodexPolicyHookEvents {
		updated = removeCodexPolicyHookState(updated, hookPath, event.stateEventName)
	}
	for _, event := range codexPolicyHookEvents {
		updated = ensureCodexPolicyHookState(updated, hookPath, event.stateEventName, codexHookTrustedHash(event.stateEventName, command, event.matcher))
	}
	if !strings.HasSuffix(updated, "\n") {
		updated += "\n"
	}
	return updated
}

func removeCodexPolicyHookState(configText, hookPath, eventName string) string {
	header := codexHookStateHeader(hookPath, eventName)
	lines := splitConfigLines(configText)
	out := make([]string, 0, len(lines))
	inRetiredState := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if isTomlHeader(trimmed) {
			inRetiredState = trimmed == header
		}
		if !inRetiredState {
			out = append(out, line)
		}
	}
	return strings.Join(out, "\n")
}

func ensureCodexFeatureFlags(configText string) string {
	lines := splitConfigLines(configText)
	out := make([]string, 0, len(lines)+4)
	inFeatures := false
	sawFeatures := false
	sawHooks := false

	flushFeatures := func() {
		if !inFeatures {
			return
		}
		if !sawHooks {
			out = append(out, "hooks = true")
		}
	}

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		isHeader := isTomlHeader(trimmed)
		if isHeader {
			if inFeatures {
				flushFeatures()
			}
			inFeatures = trimmed == "[features]"
			if inFeatures {
				sawFeatures = true
				sawHooks = false
			}
		}
		if inFeatures && !isHeader {
			switch {
			case isTomlAssignment(trimmed, "hooks"):
				if !sawHooks {
					out = append(out, "hooks = true")
					sawHooks = true
				}
				continue
			case isTomlAssignment(trimmed, "codex_hooks"):
				continue
			}
		}
		out = append(out, line)
	}
	if inFeatures {
		flushFeatures()
	}
	if !sawFeatures {
		if len(out) > 0 && strings.TrimSpace(out[len(out)-1]) != "" {
			out = append(out, "")
		}
		out = append(out, "[features]", "hooks = true")
	}
	return strings.Join(out, "\n")
}

func ensureCodexPolicyHookState(configText, hookPath, eventName, trustedHash string) string {
	header := codexHookStateHeader(hookPath, eventName)
	lines := splitConfigLines(configText)
	out := make([]string, 0, len(lines))
	inGestaState := false
	foundGestaState := false
	sawTrustedHash := false

	flushState := func() {
		if inGestaState && !sawTrustedHash {
			out = append(out, `trusted_hash = "`+trustedHash+`"`)
			sawTrustedHash = true
		}
	}

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if isTomlHeader(trimmed) {
			if inGestaState {
				flushState()
			}
			inGestaState = trimmed == header
			if inGestaState {
				foundGestaState = true
				sawTrustedHash = false
			}
		}
		if inGestaState && trimmed == "enabled = false" {
			continue
		}
		if inGestaState && isTomlAssignment(trimmed, "trusted_hash") {
			if !sawTrustedHash {
				out = append(out, `trusted_hash = "`+trustedHash+`"`)
				sawTrustedHash = true
			}
			continue
		}
		out = append(out, line)
	}
	if inGestaState {
		flushState()
	}
	if !foundGestaState {
		if len(out) > 0 && strings.TrimSpace(out[len(out)-1]) != "" {
			out = append(out, "")
		}
		out = append(out, header, `trusted_hash = "`+trustedHash+`"`)
	}
	return strings.Join(out, "\n")
}

func codexHookTrustedHash(eventName, command string, matcher string) string {
	identity := map[string]interface{}{
		"event_name": eventName,
		"hooks": []interface{}{
			map[string]interface{}{
				"async":         false,
				"command":       command,
				"statusMessage": gestaCodexHookStatus,
				"timeout":       30,
				"type":          "command",
			},
		},
	}
	if strings.TrimSpace(matcher) != "" {
		identity["matcher"] = matcher
	}
	data, err := json.Marshal(identity)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func codexHookStateHeader(hookPath, eventName string) string {
	return "[hooks.state." + tomlBasicString(hookPath+":"+eventName+":0:0") + "]"
}

func splitConfigLines(configText string) []string {
	if configText == "" {
		return nil
	}
	return strings.Split(configText, "\n")
}

func isTomlHeader(trimmed string) bool {
	return strings.HasPrefix(trimmed, "[") && strings.HasSuffix(trimmed, "]")
}

func isTomlAssignment(trimmed, key string) bool {
	if !strings.HasPrefix(trimmed, key) {
		return false
	}
	return strings.HasPrefix(strings.TrimSpace(strings.TrimPrefix(trimmed, key)), "=")
}

func tomlBasicString(value string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range value {
		switch r {
		case '\\':
			b.WriteString(`\\`)
		case '"':
			b.WriteString(`\"`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}

func shellQuote(value string) string {
	return quoteCommandPath(value, runtime.GOOS)
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
