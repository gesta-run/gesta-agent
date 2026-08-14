package workspace

import (
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/gesta-run/gesta-agent/pkg/model"
)

const maxChildDirs = 32

var ignoredDirectories = map[string]struct{}{
	".cache": {}, ".git": {}, ".next": {}, "build": {}, "coverage": {},
	"dist": {}, "node_modules": {}, "target": {}, "vendor": {},
}

func Resolve(rawPath string) model.MemoryWorkspace {
	path := strings.TrimSpace(rawPath)
	if path == "" || !filepath.IsAbs(path) {
		return model.MemoryWorkspace{}
	}
	path = filepath.Clean(path)
	info, err := os.Stat(path)
	if err != nil || !info.IsDir() || unscopedPath(path) {
		return model.MemoryWorkspace{}
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		return model.MemoryWorkspace{CWDName: filepath.Base(path), ChildDirs: []string{}}
	}
	childDirs := make([]string, 0, len(entries))
	for _, entry := range entries {
		name := entry.Name()
		if !entry.IsDir() || strings.HasPrefix(name, ".") {
			continue
		}
		if _, ignored := ignoredDirectories[strings.ToLower(name)]; ignored {
			continue
		}
		childDirs = append(childDirs, name)
		if len(childDirs) == maxChildDirs {
			break
		}
	}
	sort.Strings(childDirs)
	return model.MemoryWorkspace{CWDName: filepath.Base(path), ChildDirs: childDirs}
}

func unscopedPath(path string) bool {
	if path == string(filepath.Separator) || filepath.Base(path) == "." {
		return true
	}
	if home, err := os.UserHomeDir(); err == nil && samePath(path, home) {
		return true
	}
	temp := filepath.Clean(os.TempDir())
	return samePath(path, temp) || strings.HasPrefix(path, temp+string(filepath.Separator))
}

func samePath(left, right string) bool {
	return strings.EqualFold(filepath.Clean(left), filepath.Clean(right))
}
