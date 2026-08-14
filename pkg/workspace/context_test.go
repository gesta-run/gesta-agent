package workspace

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolveUsesOnlyDirectoryNameAndFirstLevelChildren(t *testing.T) {
	workingDirectory, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	pkgDirectory := filepath.Dir(workingDirectory)
	context := Resolve(pkgDirectory)
	if context.CWDName != "pkg" {
		t.Fatalf("CWDName = %q, want pkg", context.CWDName)
	}
	if !contains(context.ChildDirs, "agent") || !contains(context.ChildDirs, "model") {
		t.Fatalf("ChildDirs = %#v", context.ChildDirs)
	}
	for _, child := range context.ChildDirs {
		if filepath.IsAbs(child) {
			t.Fatalf("absolute child path leaked: %q", child)
		}
	}
}

func TestResolveRejectsUnscopedAndRelativePaths(t *testing.T) {
	if got := Resolve("relative/path"); got.CWDName != "" || len(got.ChildDirs) != 0 {
		t.Fatalf("relative path resolved to %#v", got)
	}
	if got := Resolve(t.TempDir()); got.CWDName != "" || len(got.ChildDirs) != 0 {
		t.Fatalf("temporary path resolved to %#v", got)
	}
}

func contains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
