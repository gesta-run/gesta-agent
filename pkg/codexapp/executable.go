package codexapp

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

type executableCandidate struct {
	Path     string
	Source   string
	Explicit bool
}

func resolveExecutableCandidates() ([]executableCandidate, error) {
	for _, key := range []string{"GESTA_CODEX_BIN", "CODEX_BIN"} {
		if candidate := strings.TrimSpace(os.Getenv(key)); candidate != "" {
			if !isExecutableFile(candidate) {
				return nil, fmt.Errorf("%s does not point to an executable Codex file: %s", key, candidate)
			}
			return prepareExecutableCandidates(runtime.GOOS, []executableCandidate{{
				Path:     candidate,
				Source:   key,
				Explicit: true,
			}})
		}
	}

	candidates := automaticExecutableCandidates(
		runtime.GOOS,
		defaultDesktopExecutableCandidates(),
		exec.LookPath,
	)
	prepared, err := prepareExecutableCandidates(runtime.GOOS, candidates)
	if err != nil {
		return nil, err
	}
	if len(prepared) > 0 {
		return prepared, nil
	}
	return nil, errors.New("codex executable was not found; set GESTA_CODEX_BIN")
}

// PrepareExecutable resolves Codex and prewarms any platform-specific executable cache.
func PrepareExecutable() (string, error) {
	candidates, err := resolveExecutableCandidates()
	if err != nil {
		return "", err
	}
	return candidates[0].Path, nil
}

func defaultDesktopExecutableCandidates() []executableCandidate {
	candidates := []executableCandidate{
		{
			Path:   "/Applications/ChatGPT.app/Contents/Resources/codex",
			Source: "ChatGPT.app system bundle",
		},
		{
			Path:   "/Applications/Codex.app/Contents/Resources/codex",
			Source: "Codex.app system bundle",
		},
	}
	if home, err := os.UserHomeDir(); err == nil && strings.TrimSpace(home) != "" {
		candidates = append(candidates,
			executableCandidate{
				Path:   filepath.Join(home, "Applications", "ChatGPT.app", "Contents", "Resources", "codex"),
				Source: "ChatGPT.app user bundle",
			},
			executableCandidate{
				Path:   filepath.Join(home, "Applications", "Codex.app", "Contents", "Resources", "codex"),
				Source: "Codex.app user bundle",
			},
		)
	}
	return candidates
}

func automaticExecutableCandidates(
	goos string,
	desktopCandidates []executableCandidate,
	lookPath func(string) (string, error),
) []executableCandidate {
	var candidates []executableCandidate
	if goos == "darwin" {
		for _, candidate := range desktopCandidates {
			if isExecutableFile(candidate.Path) {
				candidates = appendUniqueCandidate(candidates, candidate)
			}
		}
	}
	if candidate, err := lookPath("codex"); err == nil && strings.TrimSpace(candidate) != "" {
		candidates = appendUniqueCandidate(candidates, executableCandidate{
			Path:   candidate,
			Source: "PATH",
		})
	}
	return candidates
}

func appendUniqueCandidate(candidates []executableCandidate, candidate executableCandidate) []executableCandidate {
	candidate.Path = filepath.Clean(strings.TrimSpace(candidate.Path))
	for _, existing := range candidates {
		if existing.Path == candidate.Path {
			return candidates
		}
	}
	return append(candidates, candidate)
}

func isExecutableFile(path string) bool {
	info, err := os.Stat(path)
	if err != nil || info.IsDir() || !info.Mode().IsRegular() {
		return false
	}
	return runtime.GOOS == "windows" || info.Mode().Perm()&0o111 != 0
}
