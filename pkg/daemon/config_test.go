package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSaveLoadInstallConfigDoesNotPersistAPIKey(t *testing.T) {
	apiKey := "enroll_secret_test_key"
	cfg := NewRuntimeConfig("https://control.example")
	cfg.CustomerID = "default"
	cfg.DaemonID = "daemon_123"
	cfg.Token = "dtok_123"

	path := filepath.Join(t.TempDir(), "state.json")
	if err := SaveConfig(path, cfg); err != nil {
		t.Fatalf("save config: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	if strings.Contains(string(data), apiKey) {
		t.Fatalf("persisted connect token in config: %s", data)
	}

	loaded, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if loaded.EffectiveServerURL() != "https://control.example" {
		t.Fatalf("server url mismatch: %q", loaded.EffectiveServerURL())
	}
	if loaded.CustomerID == "" {
		t.Fatal("expected fallback customer id for local/dev control planes")
	}
	if err := loaded.ValidateEnrolled(); err != nil {
		t.Fatalf("expected enrolled config to validate: %v", err)
	}
}

func TestSaveConfigPersistsCleanRuntimeState(t *testing.T) {
	cfg := NewDirectRuntimeConfig("https://control.example", "sk-test-key")
	cfg.ControlURL = "https://control.example"
	cfg.TeamID = "team_123"

	path := filepath.Join(t.TempDir(), "state.json")
	if err := SaveConfig(path, cfg); err != nil {
		t.Fatalf("save config: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	text := string(data)
	for _, unwanted := range []string{`"control_url"`, `"token"`, `"team_id"`} {
		if strings.Contains(text, unwanted) {
			t.Fatalf("state contains %s: %s", unwanted, text)
		}
	}
	if !strings.Contains(text, `"api_key": "sk-test-key"`) {
		t.Fatalf("state did not persist api_key: %s", text)
	}

	loaded, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if loaded.APIKey != "sk-test-key" || loaded.Token != "sk-test-key" {
		t.Fatalf("loaded api key/token mismatch: %#v", loaded)
	}
	if loaded.EffectiveServerURL() != "https://control.example" {
		t.Fatalf("server url = %q", loaded.EffectiveServerURL())
	}
	if loaded.TeamID != "" {
		t.Fatalf("team_id should not be persisted, got %q", loaded.TeamID)
	}
}

func TestLoadConfigMigratesLegacyTokenAndControlURL(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	legacy := `{
  "control_url": "https://legacy.example",
  "daemon_id": "daemon_legacy",
  "token": "dtok_legacy",
  "device_id": "dev_legacy",
  "user_name": "legacy-user"
}`
	if err := os.WriteFile(path, []byte(legacy), 0o600); err != nil {
		t.Fatalf("write legacy config: %v", err)
	}
	loaded, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if loaded.APIKey != "dtok_legacy" || loaded.Token != "dtok_legacy" {
		t.Fatalf("legacy api key/token mismatch: %#v", loaded)
	}
	if loaded.EffectiveServerURL() != "https://legacy.example" {
		t.Fatalf("legacy server url = %q", loaded.EffectiveServerURL())
	}
}

func TestDefaultStatePathLivesUnderDefaultDataDir(t *testing.T) {
	if got, want := DefaultStatePath(), filepath.Join(DefaultDataDir(), "state.json"); got != want {
		t.Fatalf("default state path mismatch: got %q want %q", got, want)
	}
}
