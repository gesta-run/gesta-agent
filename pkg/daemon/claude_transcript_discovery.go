package daemon

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// claudeProjectsDir returns ~/.claude/projects.
func claudeProjectsDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".claude", "projects")
}

// findClaudeTranscripts returns every transcript jsonl file under the projects dir.
func findClaudeTranscripts(projectsDir string) []string {
	if projectsDir == "" {
		return nil
	}
	var paths []string
	_ = filepath.Walk(projectsDir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info == nil || info.IsDir() {
			return nil
		}
		if strings.HasSuffix(path, ".jsonl") {
			paths = append(paths, path)
		}
		return nil
	})
	sort.Strings(paths)
	return paths
}
