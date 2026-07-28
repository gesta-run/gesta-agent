package codexapp

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

func TestAutomaticExecutableCandidatesPreferDesktopBundlesBeforePATH(t *testing.T) {
	desktop := writeTestExecutable(t, "desktop-codex", "#!/bin/sh\nexit 0\n", 0o700)
	path := writeTestExecutable(t, "path-codex", "#!/bin/sh\nexit 0\n", 0o700)

	candidates := automaticExecutableCandidates(
		"darwin",
		[]executableCandidate{{Path: desktop, Source: "ChatGPT.app test bundle"}},
		func(name string) (string, error) {
			if name != "codex" {
				t.Fatalf("LookPath name = %q, want codex", name)
			}
			return path, nil
		},
	)

	if len(candidates) != 2 {
		t.Fatalf("candidate count = %d, want 2: %#v", len(candidates), candidates)
	}
	if candidates[0].Path != desktop || candidates[1].Path != path {
		t.Fatalf("candidate order = %#v, want Desktop then PATH", candidates)
	}
}

func TestAutomaticExecutableCandidatesUsePATHWithoutDesktop(t *testing.T) {
	path := writeTestExecutable(t, "path-codex", "#!/bin/sh\nexit 0\n", 0o700)

	candidates := automaticExecutableCandidates(
		"linux",
		[]executableCandidate{{
			Path:   writeTestExecutable(t, "desktop-codex", "#!/bin/sh\nexit 0\n", 0o700),
			Source: "ignored Desktop bundle",
		}},
		func(string) (string, error) { return path, nil },
	)

	if len(candidates) != 1 || candidates[0].Path != path || candidates[0].Source != "PATH" {
		t.Fatalf("candidates = %#v, want only PATH", candidates)
	}
}

func TestResolveExecutableCandidatesRejectsNonExecutableExplicitFile(t *testing.T) {
	bin := writeTestExecutable(t, "codex", "#!/bin/sh\nexit 0\n", 0o600)
	t.Setenv("GESTA_CODEX_BIN", bin)
	t.Setenv("CODEX_BIN", "")

	_, err := resolveExecutableCandidates()
	if err == nil {
		t.Fatal("resolveExecutableCandidates should reject a non-executable explicit file")
	}
	if !strings.Contains(err.Error(), "GESTA_CODEX_BIN") {
		t.Fatalf("error = %q, want GESTA_CODEX_BIN", err)
	}
}

func TestAutomaticExecutableCandidatesIgnoreMissingAndNonExecutableBundles(t *testing.T) {
	nonExecutable := writeTestExecutable(t, "non-executable-codex", "#!/bin/sh\nexit 0\n", 0o600)

	candidates := automaticExecutableCandidates(
		"darwin",
		[]executableCandidate{
			{Path: filepath.Join(t.TempDir(), "missing"), Source: "missing"},
			{Path: nonExecutable, Source: "non-executable"},
		},
		func(string) (string, error) { return "", errors.New("not found") },
	)

	if len(candidates) != 0 {
		t.Fatalf("candidates = %#v, want none", candidates)
	}
}
