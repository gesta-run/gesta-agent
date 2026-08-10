package hookinstall

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/gesta-run/gesta-agent/pkg/atomicfile"
)

type hookFileUpdate struct {
	path string
	data []byte
}

// UninstallPolicyHooks removes only hook entries and trusted hook state owned
// by Gesta. User-defined hooks, unknown fields, and Codex feature flags are
// preserved. All files are parsed before any update is written.
func UninstallPolicyHooks() ([]string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}

	updates, err := planCodexPolicyHookRemoval(home)
	if err != nil {
		return nil, err
	}
	claudeUpdates, err := planClaudePolicyHookRemoval(home)
	if err != nil {
		return nil, err
	}
	updates = append(updates, claudeUpdates...)

	paths := make([]string, 0, len(updates))
	for _, update := range updates {
		if err := atomicfile.Write(update.path, update.data); err != nil {
			return paths, err
		}
		paths = append(paths, update.path)
	}
	return paths, nil
}

func planCodexPolicyHookRemoval(home string) ([]hookFileUpdate, error) {
	hookPath := filepath.Join(home, ".codex", "hooks.json")
	updates := make([]hookFileUpdate, 0, 2)
	data, err := os.ReadFile(hookPath)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("read %s: %w", hookPath, err)
	}
	if err == nil && len(strings.TrimSpace(string(data))) > 0 {
		var hooks codexHooksFile
		if err := json.Unmarshal(data, &hooks); err != nil {
			return nil, fmt.Errorf("existing %s is not a valid hooks file; refusing to overwrite: %w", hookPath, err)
		}
		changed := false
		for eventName, groups := range hooks.Hooks {
			filtered := make([]codexHookGroup, 0, len(groups))
			for _, group := range groups {
				if codexHookGroupRunsGesta(group) {
					changed = true
					continue
				}
				filtered = append(filtered, group)
			}
			if len(filtered) == 0 {
				delete(hooks.Hooks, eventName)
			} else {
				hooks.Hooks[eventName] = filtered
			}
		}
		if changed {
			encoded, err := marshalHookJSON(hookPath, hooks)
			if err != nil {
				return nil, err
			}
			updates = append(updates, hookFileUpdate{path: hookPath, data: encoded})
		}
	}

	configPath := filepath.Join(home, ".codex", "config.toml")
	configData, err := os.ReadFile(configPath)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("read %s: %w", configPath, err)
	}
	if err == nil {
		updated := string(configData)
		for _, event := range codexPolicyHookEvents {
			updated = removeCodexPolicyHookState(updated, hookPath, event.stateEventName)
		}
		for _, event := range retiredCodexPolicyHookEvents {
			updated = removeCodexPolicyHookState(updated, hookPath, event.stateEventName)
		}
		if updated != string(configData) {
			updates = append(updates, hookFileUpdate{path: configPath, data: []byte(updated)})
		}
	}
	return updates, nil
}

func planClaudePolicyHookRemoval(home string) ([]hookFileUpdate, error) {
	settingsPath := filepath.Join(home, ".claude", "settings.json")
	data, err := os.ReadFile(settingsPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", settingsPath, err)
	}
	if len(strings.TrimSpace(string(data))) == 0 {
		return nil, nil
	}

	root := map[string]interface{}{}
	if err := json.Unmarshal(data, &root); err != nil {
		return nil, fmt.Errorf("existing %s is not a JSON object; refusing to overwrite: %w", settingsPath, err)
	}
	hooks, ok := root["hooks"].(map[string]interface{})
	if !ok {
		return nil, nil
	}
	changed := false
	for eventName, value := range hooks {
		groups, ok := value.([]interface{})
		if !ok {
			continue
		}
		filtered := make([]interface{}, 0, len(groups))
		for _, group := range groups {
			if claudeHookGroupRunsGesta(group) {
				changed = true
				continue
			}
			filtered = append(filtered, group)
		}
		if len(filtered) == 0 {
			delete(hooks, eventName)
		} else {
			hooks[eventName] = filtered
		}
	}
	if !changed {
		return nil, nil
	}
	if len(hooks) == 0 {
		delete(root, "hooks")
	} else {
		root["hooks"] = hooks
	}
	encoded, err := marshalHookJSON(settingsPath, root)
	if err != nil {
		return nil, err
	}
	return []hookFileUpdate{{path: settingsPath, data: encoded}}, nil
}

func marshalHookJSON(path string, value interface{}) ([]byte, error) {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal JSON for %s: %w", path, err)
	}
	return append(data, '\n'), nil
}
