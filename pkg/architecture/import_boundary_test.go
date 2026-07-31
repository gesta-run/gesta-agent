package architecture_test

import (
	"errors"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

const modulePath = "github.com/gesta-run/gesta-agent"

func TestRepositoryDoesNotContainRootInternalDirectory(t *testing.T) {
	internalPath := filepath.Join(repositoryRoot(t), "internal")
	if _, err := os.Lstat(internalPath); err == nil {
		t.Fatalf("removed root internal path exists: %s", internalPath)
	} else if !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("inspect removed root internal path %s: %v", internalPath, err)
	}
}

func TestRepositoryDoesNotImportInternalPackages(t *testing.T) {
	root := repositoryRoot(t)
	for _, sourceRoot := range []string{"cmd", "pkg"} {
		walkImports(t, filepath.Join(root, sourceRoot), func(path, imported string) {
			if isModuleInternalImport(imported) {
				t.Errorf("%s imports removed internal package %s", path, imported)
			}
		})
	}
}

func TestCommandImportsOnlyAgentPackage(t *testing.T) {
	root := filepath.Join(repositoryRoot(t), "cmd", "gesta-agent")
	walkImports(t, root, func(path, imported string) {
		if strings.HasPrefix(imported, modulePath+"/") &&
			imported != modulePath+"/pkg/agent" {
			t.Errorf("%s imports %s; command wiring must go through pkg/agent", path, imported)
		}
	})
}

func TestOnlyCommandImportsAgentPackage(t *testing.T) {
	root := filepath.Join(repositoryRoot(t), "pkg")
	agentRoot := filepath.Join(root, "agent") + string(filepath.Separator)
	walkImports(t, root, func(path, imported string) {
		if strings.HasPrefix(path, agentRoot) {
			return
		}
		if imported == modulePath+"/pkg/agent" ||
			strings.HasPrefix(imported, modulePath+"/pkg/agent/") {
			t.Errorf("%s imports application package %s", path, imported)
		}
	})
}

func TestModuleInternalImportDetection(t *testing.T) {
	tests := []struct {
		name     string
		imported string
		want     bool
	}{
		{name: "exact package", imported: modulePath + "/internal", want: true},
		{name: "child package", imported: modulePath + "/internal/atomicfile", want: true},
		{name: "similar package", imported: modulePath + "/internaltools", want: false},
		{name: "other module", imported: "example.com/project/internal/tool", want: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := isModuleInternalImport(test.imported); got != test.want {
				t.Fatalf("isModuleInternalImport(%q) = %t, want %t", test.imported, got, test.want)
			}
		})
	}
}

func isModuleInternalImport(imported string) bool {
	internalPath := modulePath + "/internal"
	return imported == internalPath || strings.HasPrefix(imported, internalPath+"/")
}

func repositoryRoot(t *testing.T) string {
	t.Helper()

	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve architecture test source path")
	}
	root, err := filepath.Abs(filepath.Join(filepath.Dir(currentFile), "../.."))
	if err != nil {
		t.Fatalf("resolve repository root: %v", err)
	}
	return root
}

func walkImports(t *testing.T, root string, visit func(path, imported string)) {
	t.Helper()

	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || filepath.Ext(path) != ".go" {
			return nil
		}
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
		if err != nil {
			return err
		}
		for _, spec := range file.Imports {
			imported, err := strconv.Unquote(spec.Path.Value)
			if err != nil {
				return err
			}
			visit(path, imported)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
