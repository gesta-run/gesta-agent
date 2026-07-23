package daemon

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func readClaudeSettings(t *testing.T, path string) map[string]interface{} {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read settings: %v", err)
	}
	var root map[string]interface{}
	if err := json.Unmarshal(data, &root); err != nil {
		t.Fatalf("settings json invalid: %v", err)
	}
	return root
}

func claudeEventGroups(t *testing.T, root map[string]interface{}, event string) []interface{} {
	t.Helper()
	hooks, ok := root["hooks"].(map[string]interface{})
	if !ok {
		t.Fatalf("hooks not an object: %#v", root["hooks"])
	}
	groups, ok := hooks[event].([]interface{})
	if !ok {
		t.Fatalf("%s not an array: %#v", event, hooks[event])
	}
	return groups
}

func claudeGroupCommand(t *testing.T, group interface{}) string {
	t.Helper()
	groupMap, ok := group.(map[string]interface{})
	if !ok {
		t.Fatalf("group not an object: %#v", group)
	}
	hooks, ok := groupMap["hooks"].([]interface{})
	if !ok || len(hooks) == 0 {
		t.Fatalf("group has no hooks: %#v", group)
	}
	hookMap, ok := hooks[0].(map[string]interface{})
	if !ok {
		t.Fatalf("hook not an object: %#v", hooks[0])
	}
	command, _ := hookMap["command"].(string)
	return command
}

func TestInstallClaudeCodePolicyHookPreservesExistingHooks(t *testing.T) {
	tmp := t.TempDir()
	home := filepath.Join(tmp, "home")
	claudeDir := filepath.Join(home, ".claude")
	if err := os.MkdirAll(claudeDir, 0o700); err != nil {
		t.Fatalf("mkdir claude dir: %v", err)
	}
	t.Setenv("HOME", home)

	existing := `{
  "model": "claude-opus-4-8",
  "hooks": {
    "PreToolUse": [
      {
        "matcher": "Write",
        "hooks": [
          {"type": "command", "command": "/tmp/existing-hook"}
        ]
      }
    ],
    "PostToolUse": [
      {
        "matcher": "*",
        "hooks": [
          {"type": "command", "command": "'/old/gesta-agent' claude-hook"}
        ]
      },
      {
        "matcher": "*",
        "hooks": [
          {"type": "command", "command": "/tmp/existing-post-hook"}
        ]
      }
    ]
  }
}`
	if err := os.WriteFile(filepath.Join(claudeDir, "settings.json"), []byte(existing), 0o600); err != nil {
		t.Fatalf("write existing settings: %v", err)
	}

	path, err := InstallClaudeCodePolicyHook("/tmp/gesta-agent")
	if err != nil {
		t.Fatalf("InstallClaudeCodePolicyHook: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read settings: %v", err)
	}
	if !strings.Contains(string(data), "/tmp/existing-hook") {
		t.Fatalf("existing hook was not preserved: %s", data)
	}
	if !strings.Contains(string(data), "claude-hook") {
		t.Fatalf("gesta hook was not installed: %s", data)
	}

	root := readClaudeSettings(t, path)
	if root["model"] != "claude-opus-4-8" {
		t.Fatalf("unknown top-level key not preserved: %#v", root["model"])
	}

	pre := claudeEventGroups(t, root, "PreToolUse")
	if len(pre) != 2 {
		t.Fatalf("PreToolUse groups = %d, want 2", len(pre))
	}
	firstPre, ok := pre[0].(map[string]interface{})
	if !ok {
		t.Fatalf("first PreToolUse group not an object: %#v", pre[0])
	}
	if firstPre["matcher"] != "*" {
		t.Fatalf("first PreToolUse matcher = %#v, want wildcard", firstPre["matcher"])
	}
	if got := claudeGroupCommand(t, pre[0]); !strings.Contains(got, "claude-hook") {
		t.Fatalf("first PreToolUse hook = %q, want Gesta claude-hook", got)
	}
	post := claudeEventGroups(t, root, "PostToolUse")
	if len(post) != 2 {
		t.Fatalf("PostToolUse groups = %d, want Gesta plus user hook", len(post))
	}
	if got := claudeGroupCommand(t, post[0]); !strings.Contains(got, "claude-hook") {
		t.Fatalf("first PostToolUse hook = %q, want Gesta claude-hook", got)
	}
	if got := claudeGroupCommand(t, post[1]); got != "/tmp/existing-post-hook" {
		t.Fatalf("user PostToolUse hook was not preserved: %q", got)
	}
	hooks := root["hooks"].(map[string]interface{})
	if _, ok := hooks["PostToolUseFailure"]; ok {
		t.Fatalf("PostToolUseFailure should not be installed: %#v", hooks["PostToolUseFailure"])
	}

	starts := claudeEventGroups(t, root, "SessionStart")
	if len(starts) != 1 {
		t.Fatalf("SessionStart groups = %d, want 1", len(starts))
	}
	firstStart, ok := starts[0].(map[string]interface{})
	if !ok {
		t.Fatalf("first SessionStart group not an object: %#v", starts[0])
	}
	if _, has := firstStart["matcher"]; has {
		t.Fatalf("SessionStart group should have no matcher: %#v", firstStart)
	}
	if got := claudeGroupCommand(t, starts[0]); !strings.Contains(got, "claude-hook") {
		t.Fatalf("first SessionStart hook = %q, want Gesta claude-hook", got)
	}

	ups := claudeEventGroups(t, root, "UserPromptSubmit")
	if len(ups) != 1 {
		t.Fatalf("UserPromptSubmit groups = %d, want 1", len(ups))
	}
	firstUps, ok := ups[0].(map[string]interface{})
	if !ok {
		t.Fatalf("first UserPromptSubmit group not an object: %#v", ups[0])
	}
	if _, has := firstUps["matcher"]; has {
		t.Fatalf("UserPromptSubmit group should have no matcher: %#v", firstUps)
	}
	if got := claudeGroupCommand(t, ups[0]); !strings.Contains(got, "claude-hook") {
		t.Fatalf("first UserPromptSubmit hook = %q, want Gesta claude-hook", got)
	}
}

func TestInstallClaudeCodePolicyHookDeduplicatesExistingGestaHook(t *testing.T) {
	tmp := t.TempDir()
	home := filepath.Join(tmp, "home")
	claudeDir := filepath.Join(home, ".claude")
	if err := os.MkdirAll(claudeDir, 0o700); err != nil {
		t.Fatalf("mkdir claude dir: %v", err)
	}
	t.Setenv("HOME", home)

	existing := `{
  "hooks": {
    "PreToolUse": [
      {
        "matcher": "Bash",
        "hooks": [
          {"type": "command", "command": "'/old/gesta-agent' claude-hook"}
        ]
      },
      {
        "matcher": "Write",
        "hooks": [
          {"type": "command", "command": "/tmp/existing-hook"}
        ]
      }
    ],
    "UserPromptSubmit": [
      {
        "hooks": [
          {"type": "command", "command": "'/old/gesta-agent' claude-hook"}
        ]
      },
      {
        "hooks": [
          {"type": "command", "command": "/tmp/existing-user-prompt-hook"}
        ]
      }
    ]
  }
}`
	if err := os.WriteFile(filepath.Join(claudeDir, "settings.json"), []byte(existing), 0o600); err != nil {
		t.Fatalf("write existing settings: %v", err)
	}

	path, err := InstallClaudeCodePolicyHook("/tmp/gesta-agent")
	if err != nil {
		t.Fatalf("InstallClaudeCodePolicyHook: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read settings: %v", err)
	}
	if strings.Contains(string(data), "/old/gesta-agent") {
		t.Fatalf("old Gesta hook was not removed: %s", data)
	}

	root := readClaudeSettings(t, path)
	pre := claudeEventGroups(t, root, "PreToolUse")
	if len(pre) != 2 {
		t.Fatalf("PreToolUse groups = %d, want 2: %s", len(pre), data)
	}
	if got := claudeGroupCommand(t, pre[0]); got != "'/tmp/gesta-agent' claude-hook" {
		t.Fatalf("first PreToolUse hook = %q", got)
	}
	if !strings.Contains(string(data), "/tmp/existing-hook") {
		t.Fatalf("unrelated PreToolUse hook was not preserved: %s", data)
	}

	ups := claudeEventGroups(t, root, "UserPromptSubmit")
	if len(ups) != 2 {
		t.Fatalf("UserPromptSubmit groups = %d, want 2: %s", len(ups), data)
	}
	firstUps, ok := ups[0].(map[string]interface{})
	if !ok {
		t.Fatalf("first UserPromptSubmit group not an object: %#v", ups[0])
	}
	if _, has := firstUps["matcher"]; has {
		t.Fatalf("first UserPromptSubmit group should have no matcher: %#v", firstUps)
	}
	if got := claudeGroupCommand(t, ups[0]); got != "'/tmp/gesta-agent' claude-hook" {
		t.Fatalf("first UserPromptSubmit hook = %q", got)
	}
	if !strings.Contains(string(data), "/tmp/existing-user-prompt-hook") {
		t.Fatalf("existing UserPromptSubmit hook was not preserved: %s", data)
	}
}

func TestInstallClaudeCodePolicyHookRejectsNonObjectSettings(t *testing.T) {
	tmp := t.TempDir()
	home := filepath.Join(tmp, "home")
	claudeDir := filepath.Join(home, ".claude")
	if err := os.MkdirAll(claudeDir, 0o700); err != nil {
		t.Fatalf("mkdir claude dir: %v", err)
	}
	t.Setenv("HOME", home)

	if err := os.WriteFile(filepath.Join(claudeDir, "settings.json"), []byte("[1, 2, 3]"), 0o600); err != nil {
		t.Fatalf("write existing settings: %v", err)
	}
	if _, err := InstallClaudeCodePolicyHook("/tmp/gesta-agent"); err == nil {
		t.Fatalf("expected error for non-object settings.json")
	}
	data, err := os.ReadFile(filepath.Join(claudeDir, "settings.json"))
	if err != nil {
		t.Fatalf("read settings: %v", err)
	}
	if strings.TrimSpace(string(data)) != "[1, 2, 3]" {
		t.Fatalf("non-object settings was clobbered: %s", data)
	}
}
