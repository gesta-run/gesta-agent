package codexapp

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
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

func TestIsWindowsStoreExecutable(t *testing.T) {
	if !isWindowsStoreExecutable(`C:\Program Files\WindowsApps\OpenAI.Codex_1.0_x64__test\app\resources\codex.exe`) {
		t.Fatal("WindowsApps Codex executable was not recognized")
	}
	if isWindowsStoreExecutable(`C:\Tools\codex.exe`) {
		t.Fatal("ordinary Codex executable was recognized as a WindowsApps executable")
	}
}

func TestCacheWindowsStoreExecutableCopiesVersionedBinary(t *testing.T) {
	source := filepath.Join(t.TempDir(), "codex.exe")
	contents := []byte("fake codex app-server")
	if err := os.WriteFile(source, contents, 0o700); err != nil {
		t.Fatalf("write source: %v", err)
	}
	cacheRoot := t.TempDir()
	t.Setenv("LOCALAPPDATA", cacheRoot)
	t.Setenv("XDG_CACHE_HOME", cacheRoot)

	cached, err := cacheWindowsStoreExecutable(source)
	if err != nil {
		t.Fatalf("cacheWindowsStoreExecutable: %v", err)
	}
	got, err := os.ReadFile(cached)
	if err != nil {
		t.Fatalf("read cached executable: %v", err)
	}
	if string(got) != string(contents) {
		t.Fatalf("cached contents = %q, want %q", got, contents)
	}
	if !validCachedExecutable(cached, int64(len(contents))) {
		t.Fatal("new cached executable did not pass checksum validation")
	}

	again, err := cacheWindowsStoreExecutable(source)
	if err != nil || again != cached {
		t.Fatalf("second cache result = %q, %v; want %q", again, err, cached)
	}
}

func TestCacheWindowsStoreExecutableRepairsCorruptCache(t *testing.T) {
	source := filepath.Join(t.TempDir(), "codex.exe")
	if err := os.WriteFile(source, []byte("good"), 0o700); err != nil {
		t.Fatal(err)
	}
	cacheRoot := t.TempDir()
	t.Setenv("LOCALAPPDATA", cacheRoot)
	t.Setenv("XDG_CACHE_HOME", cacheRoot)
	cached, err := cacheWindowsStoreExecutable(source)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cached, []byte("evil"), 0o700); err != nil {
		t.Fatal(err)
	}
	future := time.Now().Add(time.Second)
	if err := os.Chtimes(cached, future, future); err != nil {
		t.Fatal(err)
	}
	if _, err := cacheWindowsStoreExecutable(source); err != nil {
		t.Fatalf("repair cache: %v", err)
	}
	got, err := os.ReadFile(cached)
	if err != nil || string(got) != "good" {
		t.Fatalf("repaired contents = %q, %v", got, err)
	}
}

func TestPruneWindowsExecutableCacheRetainsCurrentAndPrevious(t *testing.T) {
	root := t.TempDir()
	for index, name := range []string{"current", "previous", "old"} {
		path := filepath.Join(root, name)
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Fatal(err)
		}
		modified := time.Now().Add(time.Duration(index) * time.Minute)
		if err := os.Chtimes(path, modified, modified); err != nil {
			t.Fatal(err)
		}
	}
	pruneWindowsExecutableCache(root, "current", 2)
	if _, err := os.Stat(filepath.Join(root, "current")); err != nil {
		t.Fatal("current cache version was removed")
	}
	if _, err := os.Stat(filepath.Join(root, "old")); err != nil {
		t.Fatal("newest previous cache version was removed")
	}
	if _, err := os.Stat(filepath.Join(root, "previous")); !os.IsNotExist(err) {
		t.Fatal("old cache version was retained")
	}
}

func TestCacheWindowsStoreExecutableSerializesConcurrentWriters(t *testing.T) {
	source := filepath.Join(t.TempDir(), "codex.exe")
	if err := os.WriteFile(source, []byte("concurrent codex"), 0o700); err != nil {
		t.Fatal(err)
	}
	cacheRoot := t.TempDir()
	t.Setenv("LOCALAPPDATA", cacheRoot)
	t.Setenv("XDG_CACHE_HOME", cacheRoot)
	const writers = 8
	paths := make(chan string, writers)
	errs := make(chan error, writers)
	var wait sync.WaitGroup
	for range writers {
		wait.Add(1)
		go func() {
			defer wait.Done()
			path, err := cacheWindowsStoreExecutable(source)
			paths <- path
			errs <- err
		}()
	}
	wait.Wait()
	close(paths)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("cache writer: %v", err)
		}
	}
	want := ""
	for path := range paths {
		if want == "" {
			want = path
		}
		if path != want {
			t.Fatalf("cache path = %q, want %q", path, want)
		}
	}
}

func TestPrepareExecutableCandidatesReturnsWindowsStoreCacheError(t *testing.T) {
	storeDir := filepath.Join(t.TempDir(), "WindowsApps")
	if err := os.MkdirAll(storeDir, 0o700); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(storeDir, "codex.exe")
	if err := os.WriteFile(source, []byte("codex"), 0o700); err != nil {
		t.Fatal(err)
	}
	invalidCacheRoot := filepath.Join(t.TempDir(), "cache-file")
	if err := os.WriteFile(invalidCacheRoot, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("LOCALAPPDATA", invalidCacheRoot)
	t.Setenv("XDG_CACHE_HOME", invalidCacheRoot)
	// os.UserCacheDir uses HOME/Library/Caches on macOS and ignores both
	// Windows and XDG cache environment variables.
	t.Setenv("HOME", invalidCacheRoot)
	candidates, err := prepareExecutableCandidates("windows", []executableCandidate{{Path: source, Source: "test"}})
	if err == nil || len(candidates) != 0 {
		t.Fatalf("candidates = %#v, err = %v; want cache failure", candidates, err)
	}
}
