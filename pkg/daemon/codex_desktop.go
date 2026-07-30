package daemon

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gesta-run/gesta-agent/pkg/model"
	"github.com/gesta-run/gesta-agent/pkg/util"
)

const codexDesktopBundleIdentifier = "com.openai.codex"

type plistValueReader func(context.Context, string, string) string

type CodexDesktopAdapter struct {
	bundleCandidates []string
	readPlistValue   plistValueReader
}

func (CodexDesktopAdapter) Name() string { return "codex_desktop" }

func (a CodexDesktopAdapter) Collect(ctx context.Context, cfg Config) (AdapterResult, []model.EventEnvelope) {
	now := time.Now().UTC().Format(time.RFC3339)
	candidates := a.bundleCandidates
	if len(candidates) == 0 {
		candidates = defaultCodexDesktopBundleCandidates()
	}
	reader := a.readPlistValue
	if reader == nil {
		reader = macOSPlistValue
	}
	bundlePath, version := findCodexDesktopBundle(ctx, candidates, reader)
	if bundlePath == "" {
		return AdapterResult{Status: model.AdapterStatus{
			Name: a.Name(), Detected: false, Status: "not_found", UpdatedAt: now,
		}}, nil
	}

	status := model.AdapterStatus{
		Name: a.Name(), Detected: true, Version: version, Status: "ok", UpdatedAt: now,
	}
	events := []model.EventEnvelope{
		snapshotEvent(cfg, "agent.discovery", "daemon", a.Name(), map[string]interface{}{
			"bundle_path_hash": util.ShortHash(bundlePath),
			"version":          version,
		}),
	}
	return AdapterResult{Status: status}, events
}

func defaultCodexDesktopBundleCandidates() []string {
	home, _ := os.UserHomeDir()
	candidates := []string{
		"/Applications/Codex.app",
		"/Applications/ChatGPT.app",
	}
	if home != "" {
		candidates = append(candidates,
			filepath.Join(home, "Applications", "Codex.app"),
			filepath.Join(home, "Applications", "ChatGPT.app"),
		)
	}
	return candidates
}

func findCodexDesktopBundle(
	ctx context.Context,
	candidates []string,
	readValue plistValueReader,
) (string, string) {
	for _, candidate := range candidates {
		infoPath := filepath.Join(candidate, "Contents", "Info.plist")
		info, err := os.Stat(infoPath)
		if err != nil || info.IsDir() {
			continue
		}
		if identifier := strings.TrimSpace(readValue(ctx, infoPath, "CFBundleIdentifier")); identifier != codexDesktopBundleIdentifier {
			continue
		}
		return candidate, codexDesktopBundleVersion(ctx, infoPath, readValue)
	}
	return "", ""
}

func codexDesktopBundleVersion(ctx context.Context, infoPath string, readValue plistValueReader) string {
	if version := strings.TrimSpace(readValue(ctx, infoPath, "CFBundleShortVersionString")); version != "" {
		return version
	}
	return strings.TrimSpace(readValue(ctx, infoPath, "CFBundleVersion"))
}

func macOSPlistValue(ctx context.Context, path, key string) string {
	output, err := commandOutput(ctx, "/usr/bin/plutil", "-extract", key, "raw", "-o", "-", path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(output)
}
