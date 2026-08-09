package codexapp

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/gesta-run/gesta-agent/pkg/atomicfile"
	"github.com/gesta-run/gesta-agent/pkg/lockfile"
)

const windowsExecutableCacheVersions = 2

var windowsExecutableCacheLockOptions = lockfile.Options{
	Label:        "Codex executable cache",
	Wait:         2 * time.Minute,
	StaleAfter:   30 * time.Minute,
	PollInterval: 50 * time.Millisecond,
}

type executableCacheManifest struct {
	SHA256   string `json:"sha256"`
	Size     int64  `json:"size"`
	Modified int64  `json:"modified_at"`
}

func prepareExecutableCandidates(goos string, candidates []executableCandidate) ([]executableCandidate, error) {
	if goos != "windows" {
		return candidates, nil
	}
	prepared := make([]executableCandidate, 0, len(candidates))
	var cacheErrors []error
	for _, candidate := range candidates {
		if !isWindowsStoreExecutable(candidate.Path) {
			prepared = appendUniqueCandidate(prepared, candidate)
			continue
		}
		cachedPath, err := cacheWindowsStoreExecutable(candidate.Path)
		if err != nil {
			cacheErrors = append(cacheErrors, fmt.Errorf("cache %s Codex executable: %w", candidate.Source, err))
			continue
		}
		candidate.Path = cachedPath
		candidate.Source += " cached copy"
		prepared = appendUniqueCandidate(prepared, candidate)
	}
	if len(prepared) == 0 && len(cacheErrors) > 0 {
		return nil, errors.Join(cacheErrors...)
	}
	return prepared, nil
}

func isWindowsStoreExecutable(path string) bool {
	path = strings.ReplaceAll(strings.ToLower(strings.TrimSpace(path)), "\\", "/")
	return strings.Contains(path, "/windowsapps/")
}

func cacheWindowsStoreExecutable(source string) (string, error) {
	cacheRoot, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	cacheDirectory := filepath.Join(cacheRoot, "gesta-agent", "codex")
	unlock, err := lockfile.Acquire(filepath.Join(cacheDirectory, ".cache.lock"), true, windowsExecutableCacheLockOptions)
	if err != nil {
		return "", err
	}
	defer unlock()

	info, err := os.Stat(source)
	if err != nil {
		return "", err
	}
	identity := fmt.Sprintf("%s\x00%d\x00%d", filepath.Clean(source), info.Size(), info.ModTime().UnixNano())
	version := fmt.Sprintf("%x", sha256.Sum256([]byte(identity)))[:16]
	directory := filepath.Join(cacheDirectory, version)
	target := filepath.Join(directory, "codex.exe")
	if validCachedExecutable(target, info.Size()) {
		pruneWindowsExecutableCache(cacheDirectory, version, windowsExecutableCacheVersions)
		return target, nil
	}
	if cached, statErr := os.Stat(target); statErr == nil && cached.Size() == info.Size() {
		sourceDigest, sourceHashErr := executableFileSHA256(source)
		cachedDigest, cachedHashErr := executableFileSHA256(target)
		if sourceHashErr == nil && cachedHashErr == nil && sourceDigest == cachedDigest {
			if err := writeExecutableCacheManifest(target, cachedDigest); err == nil {
				pruneWindowsExecutableCache(cacheDirectory, version, windowsExecutableCacheVersions)
				return target, nil
			}
		}
	}
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return "", err
	}
	removeStaleExecutableTemps(directory, time.Now())
	digest, err := copyExecutableWithChecksum(source, directory)
	if err != nil {
		return "", err
	}
	temporary := digest.path
	defer os.Remove(temporary)
	if err := os.Rename(temporary, target); err != nil {
		if cachedDigest, hashErr := executableFileSHA256(target); hashErr != nil || cachedDigest != digest.sha256 {
			if removeErr := os.Remove(target); removeErr != nil {
				return "", err
			}
			if retryErr := os.Rename(temporary, target); retryErr != nil {
				return "", retryErr
			}
		}
	}
	if err := writeExecutableCacheManifest(target, digest.sha256); err != nil {
		return "", err
	}
	pruneWindowsExecutableCache(cacheDirectory, version, windowsExecutableCacheVersions)
	return target, nil
}

type copiedExecutable struct {
	path   string
	sha256 string
}

func copyExecutableWithChecksum(source, directory string) (copiedExecutable, error) {
	input, err := os.Open(source)
	if err != nil {
		return copiedExecutable{}, err
	}
	defer input.Close()
	output, err := os.CreateTemp(directory, "codex-*.tmp")
	if err != nil {
		return copiedExecutable{}, err
	}
	temporary := output.Name()
	hasher := sha256.New()
	if _, err := io.Copy(io.MultiWriter(output, hasher), input); err != nil {
		_ = output.Close()
		_ = os.Remove(temporary)
		return copiedExecutable{}, err
	}
	if err := output.Close(); err != nil {
		_ = os.Remove(temporary)
		return copiedExecutable{}, err
	}
	digest := hex.EncodeToString(hasher.Sum(nil))
	if copiedDigest, err := executableFileSHA256(temporary); err != nil || copiedDigest != digest {
		_ = os.Remove(temporary)
		if err != nil {
			return copiedExecutable{}, err
		}
		return copiedExecutable{}, errors.New("cached Codex executable failed checksum validation")
	}
	return copiedExecutable{path: temporary, sha256: digest}, nil
}

func validCachedExecutable(path string, sourceSize int64) bool {
	info, err := os.Stat(path)
	if err != nil || info.Size() != sourceSize {
		return false
	}
	data, err := os.ReadFile(path + ".sha256")
	if err != nil {
		return false
	}
	var manifest executableCacheManifest
	if json.Unmarshal(data, &manifest) != nil || manifest.Size != info.Size() || len(manifest.SHA256) != 64 {
		return false
	}
	if manifest.Modified == info.ModTime().UnixNano() {
		return true
	}
	digest, err := executableFileSHA256(path)
	if err != nil || digest != manifest.SHA256 {
		return false
	}
	return writeExecutableCacheManifest(path, digest) == nil
}

func executableFileSHA256(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hasher := sha256.New()
	if _, err := io.Copy(hasher, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hasher.Sum(nil)), nil
}

func writeExecutableCacheManifest(target, digest string) error {
	info, err := os.Stat(target)
	if err != nil {
		return err
	}
	return atomicfile.WriteJSON(target+".sha256", executableCacheManifest{
		SHA256:   digest,
		Size:     info.Size(),
		Modified: info.ModTime().UnixNano(),
	})
}

func pruneWindowsExecutableCache(root, current string, keep int) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return
	}
	type cacheVersion struct {
		name     string
		modified time.Time
	}
	versions := make([]cacheVersion, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		info, err := entry.Info()
		if err == nil {
			versions = append(versions, cacheVersion{name: entry.Name(), modified: info.ModTime()})
		}
	}
	sort.Slice(versions, func(i, j int) bool { return versions[i].modified.After(versions[j].modified) })
	retained := map[string]struct{}{current: {}}
	for _, version := range versions {
		if len(retained) >= keep {
			break
		}
		retained[version.name] = struct{}{}
	}
	for _, version := range versions {
		if _, ok := retained[version.name]; !ok {
			_ = os.RemoveAll(filepath.Join(root, version.name))
		}
	}
}

func removeStaleExecutableTemps(directory string, now time.Time) {
	entries, err := os.ReadDir(directory)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".tmp") {
			continue
		}
		info, err := entry.Info()
		if err == nil && now.Sub(info.ModTime()) > 10*time.Minute {
			_ = os.Remove(filepath.Join(directory, entry.Name()))
		}
	}
}
