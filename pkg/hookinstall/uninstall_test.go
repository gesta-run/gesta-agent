package hookinstall

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestUninstallPolicyHooksPreservesUserConfiguration(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	codexDir := filepath.Join(home, ".codex")
	claudeDir := filepath.Join(home, ".claude")
	if err := os.MkdirAll(codexDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(claudeDir, 0o700); err != nil {
		t.Fatal(err)
	}
	agentPath := filepath.Join(home, ".gesta", "bin", "gesta-agent")
	if _, err := InstallCodexPolicyHook(agentPath); err != nil {
		t.Fatalf("InstallCodexPolicyHook: %v", err)
	}
	if _, err := InstallClaudeCodePolicyHook(agentPath); err != nil {
		t.Fatalf("InstallClaudeCodePolicyHook: %v", err)
	}

	codexPath := filepath.Join(codexDir, "hooks.json")
	codexData, err := os.ReadFile(codexPath)
	if err != nil {
		t.Fatal(err)
	}
	codexData = []byte(strings.Replace(
		string(codexData),
		`"hooks": {`,
		`"custom": {"kept": true}, "hooks": {"Notification": [{"hooks": [{"type": "command", "command": "notify-user"}]}],`,
		1,
	))
	if err := os.WriteFile(codexPath, codexData, 0o600); err != nil {
		t.Fatal(err)
	}

	claudePath := filepath.Join(claudeDir, "settings.json")
	claudeData, err := os.ReadFile(claudePath)
	if err != nil {
		t.Fatal(err)
	}
	claudeData = []byte(strings.Replace(
		string(claudeData),
		`"hooks": {`,
		`"theme": "dark", "hooks": {"Notification": [{"hooks": [{"type": "command", "command": "notify-user"}]}],`,
		1,
	))
	if err := os.WriteFile(claudePath, claudeData, 0o600); err != nil {
		t.Fatal(err)
	}

	paths, err := UninstallPolicyHooks()
	if err != nil {
		t.Fatalf("UninstallPolicyHooks: %v", err)
	}
	if len(paths) != 3 {
		t.Fatalf("updated paths = %v, want Codex hooks/config and Claude settings", paths)
	}

	assertFileContains(t, codexPath, `"command": "notify-user"`)
	assertFileContains(t, codexPath, `"kept": true`)
	assertFileNotContains(t, codexPath, "gesta-agent")
	codexConfigPath := filepath.Join(codexDir, "config.toml")
	assertFileContains(t, codexConfigPath, "hooks = true")
	assertFileNotContains(t, codexConfigPath, "[hooks.state.")
	assertFileContains(t, claudePath, `"command": "notify-user"`)
	assertFileContains(t, claudePath, `"theme": "dark"`)
	assertFileNotContains(t, claudePath, "gesta-agent")

	paths, err = UninstallPolicyHooks()
	if err != nil {
		t.Fatalf("second UninstallPolicyHooks: %v", err)
	}
	if len(paths) != 0 {
		t.Fatalf("second updated paths = %v, want none", paths)
	}
}

func TestUninstallPolicyHooksValidatesAllFilesBeforeWriting(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(home, ".codex"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(home, ".claude"), 0o700); err != nil {
		t.Fatal(err)
	}
	agentPath := filepath.Join(home, ".gesta", "bin", "gesta-agent")
	if _, err := InstallCodexPolicyHook(agentPath); err != nil {
		t.Fatalf("InstallCodexPolicyHook: %v", err)
	}
	codexPath := filepath.Join(home, ".codex", "hooks.json")
	before, err := os.ReadFile(codexPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".claude", "settings.json"), []byte("not-json"), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := UninstallPolicyHooks(); err == nil {
		t.Fatal("UninstallPolicyHooks succeeded with invalid Claude settings")
	}
	after, err := os.ReadFile(codexPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatal("Codex hooks changed before Claude settings validation failed")
	}
}

func assertFileContains(t *testing.T, path, expected string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), expected) {
		t.Fatalf("%s does not contain %q:\n%s", path, expected, data)
	}
}

func assertFileNotContains(t *testing.T, path, unexpected string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), unexpected) {
		t.Fatalf("%s contains %q:\n%s", path, unexpected, data)
	}
}
