package hookinstall

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInstallCodexPolicyHookPreservesExistingHooks(t *testing.T) {
	tmp := t.TempDir()
	home := filepath.Join(tmp, "home")
	codexDir := filepath.Join(home, ".codex")
	if err := os.MkdirAll(codexDir, 0o700); err != nil {
		t.Fatalf("mkdir codex dir: %v", err)
	}
	t.Setenv("HOME", home)

	existing := `{
  "hooks": {
    "PreToolUse": [
      {
        "matcher": "Bash",
        "hooks": [
          {"type": "command", "command": "/tmp/existing-hook"}
        ]
      }
    ],
    "PostToolUse": [
      {
        "matcher": "*",
        "hooks": [
          {"type": "command", "command": "'/old/gesta-agent' codex-hook"}
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
	if err := os.WriteFile(filepath.Join(codexDir, "hooks.json"), []byte(existing), 0o600); err != nil {
		t.Fatalf("write existing hooks: %v", err)
	}

	path, err := InstallCodexPolicyHook("/tmp/gesta-agent")
	if err != nil {
		t.Fatalf("InstallCodexPolicyHook: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read hooks: %v", err)
	}
	if !strings.Contains(string(data), "/tmp/existing-hook") {
		t.Fatalf("existing hook was not preserved: %s", data)
	}
	if !strings.Contains(string(data), "codex-hook") {
		t.Fatalf("gesta hook was not installed: %s", data)
	}

	var parsed codexHooksFile
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("hooks json invalid: %v", err)
	}
	if got := len(parsed.Hooks["PreToolUse"]); got != 2 {
		t.Fatalf("PreToolUse groups = %d, want 2", got)
	}
	if got := len(parsed.Hooks["SessionStart"]); got != 1 {
		t.Fatalf("SessionStart groups = %d, want 1", got)
	}
	if got := len(parsed.Hooks["Stop"]); got != 1 {
		t.Fatalf("Stop groups = %d, want 1", got)
	}
	if got := len(parsed.Hooks["PostToolUse"]); got != 1 {
		t.Fatalf("PostToolUse groups = %d, want only the user's hook", got)
	}
	if got := parsed.Hooks["PostToolUse"][0].Hooks[0].Command; got != "/tmp/existing-post-hook" {
		t.Fatalf("PostToolUse hook = %q, want user's hook", got)
	}
	if got := parsed.Hooks["SessionStart"][0].Matcher; got != "" {
		t.Fatalf("SessionStart matcher = %q, want omitted", got)
	}
	if got := parsed.Hooks["SessionStart"][0].Hooks[0].Command; !strings.Contains(got, "codex-hook") {
		t.Fatalf("first SessionStart hook = %q, want Gesta codex-hook", got)
	}
	if got := parsed.Hooks["Stop"][0].Matcher; got != "" {
		t.Fatalf("Stop matcher = %q, want omitted", got)
	}
	if got := parsed.Hooks["Stop"][0].Hooks[0].Command; !strings.Contains(got, "codex-hook") {
		t.Fatalf("first Stop hook = %q, want Gesta codex-hook", got)
	}
	if got := parsed.Hooks["PreToolUse"][0].Hooks[0].Command; !strings.Contains(got, "codex-hook") {
		t.Fatalf("first PreToolUse hook = %q, want Gesta codex-hook", got)
	}
	if got := len(parsed.Hooks["UserPromptSubmit"]); got != 1 {
		t.Fatalf("UserPromptSubmit groups = %d, want 1", got)
	}
	if got := parsed.Hooks["UserPromptSubmit"][0].Matcher; got != "" {
		t.Fatalf("UserPromptSubmit matcher = %q, want omitted", got)
	}
	if got := parsed.Hooks["UserPromptSubmit"][0].Hooks[0].Command; !strings.Contains(got, "codex-hook") {
		t.Fatalf("first UserPromptSubmit hook = %q, want Gesta codex-hook", got)
	}
}

func TestInstallCodexPolicyHookRefusesInvalidHooksFile(t *testing.T) {
	home := t.TempDir()
	codexDir := filepath.Join(home, ".codex")
	if err := os.MkdirAll(codexDir, 0o700); err != nil {
		t.Fatalf("mkdir codex dir: %v", err)
	}
	t.Setenv("HOME", home)
	hookPath := filepath.Join(codexDir, "hooks.json")
	original := []byte(`{"hooks":`)
	if err := os.WriteFile(hookPath, original, 0o600); err != nil {
		t.Fatalf("write invalid hooks file: %v", err)
	}

	if _, err := InstallCodexPolicyHook("/tmp/gesta-agent"); err == nil {
		t.Fatal("expected invalid hooks file to be rejected")
	}
	data, err := os.ReadFile(hookPath)
	if err != nil {
		t.Fatalf("read hooks file: %v", err)
	}
	if string(data) != string(original) {
		t.Fatalf("invalid hooks file was overwritten: %q", data)
	}
}

func TestInstallCodexPolicyHookPreservesUnknownFields(t *testing.T) {
	home := t.TempDir()
	codexDir := filepath.Join(home, ".codex")
	if err := os.MkdirAll(codexDir, 0o700); err != nil {
		t.Fatalf("mkdir codex dir: %v", err)
	}
	t.Setenv("HOME", home)
	hookPath := filepath.Join(codexDir, "hooks.json")
	existing := `{
  "futureTop": {"revision": 7},
  "hooks": {
    "PreToolUse": [
      {
        "matcher": "Bash",
        "futureGroup": "keep-group",
        "hooks": [
          {
            "type": "command",
            "command": "/tmp/existing-hook",
            "futureCommand": {"mode": "keep-command"}
          }
        ]
      }
    ]
  }
}`
	if err := os.WriteFile(hookPath, []byte(existing), 0o600); err != nil {
		t.Fatalf("write hooks file: %v", err)
	}

	path, err := InstallCodexPolicyHook("/tmp/gesta-agent")
	if err != nil {
		t.Fatalf("InstallCodexPolicyHook: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read hooks file: %v", err)
	}
	var hooks codexHooksFile
	if err := json.Unmarshal(data, &hooks); err != nil {
		t.Fatalf("decode hooks file: %v", err)
	}
	var topExtension struct {
		Revision int `json:"revision"`
	}
	if err := json.Unmarshal(hooks.Extra["futureTop"], &topExtension); err != nil || topExtension.Revision != 7 {
		t.Fatalf("top-level extension = %s, err = %v; want preserved", hooks.Extra["futureTop"], err)
	}
	if got := len(hooks.Hooks["PreToolUse"]); got != 2 {
		t.Fatalf("PreToolUse groups = %d, want Gesta and existing groups", got)
	}
	existingGroup := hooks.Hooks["PreToolUse"][1]
	if got := string(existingGroup.Extra["futureGroup"]); got != `"keep-group"` {
		t.Fatalf("group extension = %s, want preserved", got)
	}
	if got := len(existingGroup.Hooks); got != 1 {
		t.Fatalf("existing group hooks = %d, want 1", got)
	}
	var commandExtension struct {
		Mode string `json:"mode"`
	}
	if err := json.Unmarshal(existingGroup.Hooks[0].Extra["futureCommand"], &commandExtension); err != nil || commandExtension.Mode != "keep-command" {
		t.Fatalf("command extension = %s, err = %v; want preserved", existingGroup.Hooks[0].Extra["futureCommand"], err)
	}
}

func TestInstallCodexPolicyHookDeduplicatesExistingGestaHook(t *testing.T) {
	tmp := t.TempDir()
	home := filepath.Join(tmp, "home")
	codexDir := filepath.Join(home, ".codex")
	if err := os.MkdirAll(codexDir, 0o700); err != nil {
		t.Fatalf("mkdir codex dir: %v", err)
	}
	t.Setenv("HOME", home)

	existing := `{
	  "hooks": {
	    "PreToolUse": [
	      {
        "matcher": "*",
        "hooks": [
          {"type": "command", "command": "'/old/gesta-agent' codex-hook"}
        ]
      },
      {
        "matcher": "Bash",
        "hooks": [
	          {"type": "command", "command": "/tmp/existing-hook"}
	        ]
	      }
	    ],
	    "UserPromptSubmit": [
	      {
	        "matcher": "*",
	        "hooks": [
	          {"type": "command", "command": "'/old/gesta-agent' codex-hook"}
	        ]
	      },
	      {
	        "matcher": "*",
	        "hooks": [
	          {"type": "command", "command": "/tmp/existing-user-prompt-hook"}
	        ]
	      }
	    ]
	  }
	}`
	if err := os.WriteFile(filepath.Join(codexDir, "hooks.json"), []byte(existing), 0o600); err != nil {
		t.Fatalf("write existing hooks: %v", err)
	}

	path, err := InstallCodexPolicyHook("/tmp/gesta-agent")
	if err != nil {
		t.Fatalf("InstallCodexPolicyHook: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read hooks: %v", err)
	}
	var parsed codexHooksFile
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("hooks json invalid: %v", err)
	}
	if got := len(parsed.Hooks["PreToolUse"]); got != 2 {
		t.Fatalf("PreToolUse groups = %d, want 2: %s", got, data)
	}
	if strings.Contains(string(data), "/old/gesta-agent") {
		t.Fatalf("old Gesta hook was not removed: %s", data)
	}
	if got := parsed.Hooks["PreToolUse"][0].Hooks[0].Command; got != "'/tmp/gesta-agent' codex-hook" {
		t.Fatalf("first PreToolUse hook = %q", got)
	}
	if got := len(parsed.Hooks["UserPromptSubmit"]); got != 2 {
		t.Fatalf("UserPromptSubmit groups = %d, want 2: %s", got, data)
	}
	if got := parsed.Hooks["UserPromptSubmit"][0].Matcher; got != "" {
		t.Fatalf("first UserPromptSubmit matcher = %q, want omitted", got)
	}
	if got := parsed.Hooks["UserPromptSubmit"][0].Hooks[0].Command; got != "'/tmp/gesta-agent' codex-hook" {
		t.Fatalf("first UserPromptSubmit hook = %q", got)
	}
	if !strings.Contains(string(data), "/tmp/existing-user-prompt-hook") {
		t.Fatalf("existing UserPromptSubmit hook was not preserved: %s", data)
	}
}

func TestInstallCodexPolicyHookEnablesGestaHookState(t *testing.T) {
	tmp := t.TempDir()
	home := filepath.Join(tmp, "home")
	codexDir := filepath.Join(home, ".codex")
	if err := os.MkdirAll(codexDir, 0o700); err != nil {
		t.Fatalf("mkdir codex dir: %v", err)
	}
	t.Setenv("HOME", home)

	hookPath := filepath.Join(codexDir, "hooks.json")
	config := `[features]
hooks = true

[hooks.state."` + hookPath + `:pre_tool_use:0:0"]
trusted_hash = "sha256:old"
enabled = false

[hooks.state."` + hookPath + `:pre_tool_use:1:0"]
trusted_hash = "sha256:other"
enabled = false

[hooks.state."` + hookPath + `:user_prompt_submit:0:0"]
trusted_hash = "sha256:old-prompt"
enabled = false
`
	if err := os.WriteFile(filepath.Join(codexDir, "config.toml"), []byte(config), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	if _, err := InstallCodexPolicyHook("/tmp/gesta-agent"); err != nil {
		t.Fatalf("InstallCodexPolicyHook: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(codexDir, "config.toml"))
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	text := string(data)
	preHookHeader := codexHookStateHeader(hookPath, "pre_tool_use")
	expectedHash := codexHookTrustedHash("pre_tool_use", "'/tmp/gesta-agent' codex-hook", "*")
	if !strings.Contains(text, preHookHeader+"\n"+`trusted_hash = "`+expectedHash+`"`) {
		t.Fatalf("Gesta hook state was not trusted: %s", text)
	}
	promptHookHeader := codexHookStateHeader(hookPath, "user_prompt_submit")
	expectedPromptHash := codexHookTrustedHash("user_prompt_submit", "'/tmp/gesta-agent' codex-hook", "")
	if !strings.Contains(text, promptHookHeader+"\n"+`trusted_hash = "`+expectedPromptHash+`"`) {
		t.Fatalf("Gesta UserPromptSubmit hook state was not trusted: %s", text)
	}
	startHookHeader := codexHookStateHeader(hookPath, "session_start")
	expectedStartHash := codexHookTrustedHash("session_start", "'/tmp/gesta-agent' codex-hook", "")
	if !strings.Contains(text, startHookHeader+"\n"+`trusted_hash = "`+expectedStartHash+`"`) {
		t.Fatalf("Gesta SessionStart hook state was not trusted: %s", text)
	}
	stopHookHeader := codexHookStateHeader(hookPath, "stop")
	expectedStopHash := codexHookTrustedHash("stop", "'/tmp/gesta-agent' codex-hook", "")
	if !strings.Contains(text, stopHookHeader+"\n"+`trusted_hash = "`+expectedStopHash+`"`) {
		t.Fatalf("Gesta Stop hook state was not trusted: %s", text)
	}
	if strings.Contains(text, `trusted_hash = "sha256:old"`) || strings.Contains(tomlSection(text, preHookHeader), "enabled = false") {
		t.Fatalf("Gesta hook state still disabled: %s", text)
	}
	if strings.Contains(text, `trusted_hash = "sha256:old-prompt"`) || strings.Contains(tomlSection(text, promptHookHeader), "enabled = false") {
		t.Fatalf("Gesta UserPromptSubmit hook state still disabled: %s", text)
	}
	if strings.Contains(text, "codex_hooks") {
		t.Fatalf("deprecated codex_hooks feature was not removed: %s", text)
	}
	if !strings.Contains(text, `[hooks.state."`+hookPath+`:pre_tool_use:1:0"]`+"\n"+`trusted_hash = "sha256:other"`+"\n"+`enabled = false`) {
		t.Fatalf("unrelated hook state was changed: %s", text)
	}
}

func TestInstallCodexPolicyHookEnablesFeatureFlags(t *testing.T) {
	tmp := t.TempDir()
	home := filepath.Join(tmp, "home")
	codexDir := filepath.Join(home, ".codex")
	if err := os.MkdirAll(codexDir, 0o700); err != nil {
		t.Fatalf("mkdir codex dir: %v", err)
	}
	t.Setenv("HOME", home)

	config := `[features]
codex_hooks = true
hooks = false
memories = true

[desktop]
conversationDetailMode = "STEPS_COMMANDS"
`
	if err := os.WriteFile(filepath.Join(codexDir, "config.toml"), []byte(config), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	if _, err := InstallCodexPolicyHook("/tmp/gesta-agent"); err != nil {
		t.Fatalf("InstallCodexPolicyHook: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(codexDir, "config.toml"))
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	text := string(data)
	if !strings.Contains(text, "hooks = true") || strings.Contains(text, "hooks = false") || strings.Contains(text, "codex_hooks") {
		t.Fatalf("features were not enabled while preserving existing keys: %s", text)
	}
	if !strings.Contains(text, "memories = true") {
		t.Fatalf("existing feature keys were not preserved: %s", text)
	}
	if !strings.Contains(text, "[desktop]\nconversationDetailMode") {
		t.Fatalf("other config sections were not preserved: %s", text)
	}
}

func TestCodexPolicyHookTrustedHashMatchesCodexCanonicalIdentity(t *testing.T) {
	command := "'/Users/jwcesign/git/ideas/gesta/gesta-local/run/daemon/gesta-agent' codex-hook"
	want := "sha256:169abf5b0160468963a4fd7231a6fed97761f9f97b61db4f535a75b51226ccaa"
	if got := codexHookTrustedHash("pre_tool_use", command, "*"); got != want {
		t.Fatalf("trusted hash = %s, want %s", got, want)
	}
}

func TestCodexPolicyHookTrustedHashDoesNotHTMLEscapeWindowsCallOperator(t *testing.T) {
	command := `& 'C:\Users\jwces\.gesta\bin\gesta-agent.exe' codex-hook`
	want := "sha256:0212d7125b45c7a60d3e1a39ffeb1cb462fbccdc33a62d0f50053b55e33b1666"
	if got := codexHookTrustedHash("pre_tool_use", command, "*"); got != want {
		t.Fatalf("trusted hash = %s, want %s", got, want)
	}
}

func TestQuoteCommandPathForWindows(t *testing.T) {
	got := quoteCommandPath(`C:\Users\Jane Doe\.gesta\bin\gesta-agent.exe`, "windows")
	want := `"C:\Users\Jane Doe\.gesta\bin\gesta-agent.exe"`
	if got != want {
		t.Fatalf("quoteCommandPath = %q, want %q", got, want)
	}
}

func TestCodexHookCommandsForWindows(t *testing.T) {
	agentPath := `C:\Users\Jane O'Brien\.gesta\bin\gesta-agent.exe`
	command, commandWindows, trustedCommand := codexHookCommands(agentPath, "windows")
	if want := `"C:\Users\Jane O'Brien\.gesta\bin\gesta-agent.exe" codex-hook`; command != want {
		t.Fatalf("command = %q, want %q", command, want)
	}
	if want := `& 'C:\Users\Jane O''Brien\.gesta\bin\gesta-agent.exe' codex-hook`; commandWindows != want {
		t.Fatalf("commandWindows = %q, want %q", commandWindows, want)
	}
	if trustedCommand != commandWindows {
		t.Fatalf("trusted command = %q, want Windows command %q", trustedCommand, commandWindows)
	}
}

func TestCodexHookGroupRunsGestaWithWindowsCommandOnly(t *testing.T) {
	group := codexHookGroup{Hooks: []codexHookCommand{{
		Type:           "command",
		CommandWindows: `& 'C:\gesta-agent.exe' codex-hook`,
	}}}
	if !codexHookGroupRunsGesta(group) {
		t.Fatal("Windows-only GESTA hook was not recognized")
	}
}

func TestCodexHookCommandsForUnixPreserveExistingFormat(t *testing.T) {
	agentPath := `/opt/gesta agent/gesta-agent`
	command, commandWindows, trustedCommand := codexHookCommands(agentPath, "linux")
	if want := `'/opt/gesta agent/gesta-agent' codex-hook`; command != want {
		t.Fatalf("command = %q, want %q", command, want)
	}
	if commandWindows != "" {
		t.Fatalf("commandWindows = %q, want empty", commandWindows)
	}
	if trustedCommand != command {
		t.Fatalf("trusted command = %q, want command %q", trustedCommand, command)
	}
}

func TestCodexHookCommandJSONRoundTripPreservesWindowsCommand(t *testing.T) {
	want := codexHookCommand{
		Type:           "command",
		Command:        `"C:\gesta-agent.exe" codex-hook`,
		CommandWindows: `& 'C:\gesta-agent.exe' codex-hook`,
		Timeout:        30,
		StatusMessage:  gestaCodexHookStatus,
	}
	data, err := json.Marshal(want)
	if err != nil {
		t.Fatalf("marshal hook command: %v", err)
	}
	var got codexHookCommand
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal hook command: %v", err)
	}
	if got.Type != want.Type || got.Command != want.Command || got.CommandWindows != want.CommandWindows || got.Timeout != want.Timeout || got.StatusMessage != want.StatusMessage {
		t.Fatalf("round trip = %#v, want %#v", got, want)
	}
}

func TestCodexHooksDisabledDetectsFeatureFlag(t *testing.T) {
	tmp := t.TempDir()
	home := filepath.Join(tmp, "home")
	codexDir := filepath.Join(home, ".codex")
	if err := os.MkdirAll(codexDir, 0o700); err != nil {
		t.Fatalf("mkdir codex dir: %v", err)
	}
	t.Setenv("HOME", home)
	if err := os.WriteFile(filepath.Join(codexDir, "config.toml"), []byte("[features]\nhooks = false\n"), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	disabled, path := CodexHooksDisabled()
	if !disabled {
		t.Fatalf("expected hooks disabled for %s", path)
	}
}

func tomlSection(text, header string) string {
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
