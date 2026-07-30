package daemon

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestFindCodexDesktopBundleUsesFirstValidBundle(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "Missing.app")
	bundlePath := filepath.Join(t.TempDir(), "Codex.app")
	infoPath := filepath.Join(bundlePath, "Contents", "Info.plist")
	if err := os.MkdirAll(filepath.Dir(infoPath), 0o700); err != nil {
		t.Fatalf("create bundle directory: %v", err)
	}
	if err := os.WriteFile(infoPath, []byte("test"), 0o600); err != nil {
		t.Fatalf("write Info.plist: %v", err)
	}
	reader := func(_ context.Context, _ string, key string) string {
		switch key {
		case "CFBundleIdentifier":
			return codexDesktopBundleIdentifier
		case "CFBundleShortVersionString":
			return "26.721.41059"
		default:
			return ""
		}
	}

	gotPath, gotVersion := findCodexDesktopBundle(context.Background(), []string{missing, bundlePath}, reader)
	if gotPath != bundlePath || gotVersion != "26.721.41059" {
		t.Fatalf("bundle = (%q, %q), want (%q, %q)", gotPath, gotVersion, bundlePath, "26.721.41059")
	}
}

func TestFindCodexDesktopBundleRejectsWrongIdentifier(t *testing.T) {
	bundlePath := filepath.Join(t.TempDir(), "ChatGPT.app")
	infoPath := filepath.Join(bundlePath, "Contents", "Info.plist")
	if err := os.MkdirAll(filepath.Dir(infoPath), 0o700); err != nil {
		t.Fatalf("create bundle directory: %v", err)
	}
	if err := os.WriteFile(infoPath, []byte("test"), 0o600); err != nil {
		t.Fatalf("write Info.plist: %v", err)
	}
	reader := func(_ context.Context, _ string, key string) string {
		if key == "CFBundleIdentifier" {
			return "com.openai.chat"
		}
		return "26.721.41059"
	}

	gotPath, gotVersion := findCodexDesktopBundle(context.Background(), []string{bundlePath}, reader)
	if gotPath != "" || gotVersion != "" {
		t.Fatalf("bundle = (%q, %q), want no Codex Desktop bundle", gotPath, gotVersion)
	}
}

func TestCodexDesktopBundleVersionPrefersShortVersion(t *testing.T) {
	var keys []string
	reader := func(_ context.Context, _ string, key string) string {
		keys = append(keys, key)
		switch key {
		case "CFBundleShortVersionString":
			return "26.721.41059"
		case "CFBundleVersion":
			return "5848"
		default:
			return ""
		}
	}

	if got := codexDesktopBundleVersion(context.Background(), "/Applications/Codex.app/Contents/Info.plist", reader); got != "26.721.41059" {
		t.Fatalf("bundle version = %q, want short version", got)
	}
	if len(keys) != 1 {
		t.Fatalf("read plist keys = %#v, want only the short version key", keys)
	}
}

func TestCodexDesktopBundleVersionFallsBackToBuildVersion(t *testing.T) {
	reader := func(_ context.Context, _ string, key string) string {
		if key == "CFBundleVersion" {
			return "5848"
		}
		return ""
	}

	if got := codexDesktopBundleVersion(context.Background(), "/Applications/Codex.app/Contents/Info.plist", reader); got != "5848" {
		t.Fatalf("bundle version = %q, want build version fallback", got)
	}
}

func TestCodexDesktopAdapterReportsDetectedBundle(t *testing.T) {
	bundlePath := filepath.Join(t.TempDir(), "Codex.app")
	infoPath := filepath.Join(bundlePath, "Contents", "Info.plist")
	if err := os.MkdirAll(filepath.Dir(infoPath), 0o700); err != nil {
		t.Fatalf("create bundle directory: %v", err)
	}
	if err := os.WriteFile(infoPath, []byte("test"), 0o600); err != nil {
		t.Fatalf("write Info.plist: %v", err)
	}
	adapter := CodexDesktopAdapter{
		bundleCandidates: []string{bundlePath},
		readPlistValue: func(_ context.Context, _ string, key string) string {
			switch key {
			case "CFBundleIdentifier":
				return codexDesktopBundleIdentifier
			case "CFBundleShortVersionString":
				return "26.721.41059"
			default:
				return ""
			}
		},
	}

	result, events := adapter.Collect(context.Background(), Config{DaemonID: "daemon_test"})
	if !result.Status.Detected || result.Status.Status != "ok" {
		t.Fatalf("status = %+v, want detected and ok", result.Status)
	}
	if result.Status.Name != "codex_desktop" || result.Status.Version != "26.721.41059" {
		t.Fatalf("status = %+v, want Codex Desktop version", result.Status)
	}
	if result.Status.MCPInventory != nil {
		t.Fatalf("MCP inventory = %+v, want none for an installation detector", result.Status.MCPInventory)
	}
	if len(events) != 1 || events[0].AgentType != "codex_desktop" {
		t.Fatalf("events = %+v, want one Codex Desktop discovery event", events)
	}
}

func TestCodexDesktopAdapterReportsNoMCPInventoryWhenNotFound(t *testing.T) {
	adapter := CodexDesktopAdapter{
		bundleCandidates: []string{filepath.Join(t.TempDir(), "Missing.app")},
	}

	result, events := adapter.Collect(context.Background(), Config{})
	if result.Status.Detected || result.Status.Status != "not_found" {
		t.Fatalf("status = %+v, want not found", result.Status)
	}
	if result.Status.MCPInventory != nil {
		t.Fatalf("MCP inventory = %+v, want none for an installation detector", result.Status.MCPInventory)
	}
	if len(events) != 0 {
		t.Fatalf("events = %+v, want none", events)
	}
}
