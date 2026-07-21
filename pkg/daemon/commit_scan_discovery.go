package daemon

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"strings"
	"sync"
)

// Repo discovery for the merged-commit scan (CLO-1747). The scanner only reads
// repos listed in commit-repos.d, and that list used to be fed exclusively by
// the agent-hook output pipeline — which codex installs cannot be relied on to
// keep running (the hook file is owned by the external tool and has been
// observed to stop firing after a codex upgrade). A codex-only user's repos
// were therefore never scanned and their merged output read as zero. This file
// feeds the same registry from the daemon's own codex collection pass: every
// transcript that carries a working directory registers its git repo root, and
// when the codex state DB schema has no cwd column the session's rollout file
// header is consulted instead.

// codexRolloutCwdMaxRecords bounds how much of a rollout file the cwd probe
// reads. The session_meta / turn_context records that carry cwd sit at the top
// of the file, so a short prefix is enough and a corrupt file costs nothing.
const codexRolloutCwdMaxRecords = 40

// repoDiscovery debounces discovery for one daemon process: each distinct cwd
// resolves `git rev-parse` once and each rollout file is probed once. The
// registry file itself is write-once, so a restart only re-pays cheap probes.
type repoDiscovery struct {
	mu       sync.Mutex
	cwds     map[string]bool
	rollouts map[string]bool
}

func newRepoDiscovery() *repoDiscovery {
	return &repoDiscovery{cwds: map[string]bool{}, rollouts: map[string]bool{}}
}

var codexRepoDiscovery = newRepoDiscovery()

// observeCwd registers the git repo containing cwd for commit scanning.
// Non-git directories are remembered as misses so they are not probed again.
func (d *repoDiscovery) observeCwd(ctx context.Context, dataDir, cwd string) {
	cwd = strings.TrimSpace(cwd)
	if cwd == "" {
		return
	}
	d.mu.Lock()
	seen := d.cwds[cwd]
	d.cwds[cwd] = true
	d.mu.Unlock()
	if seen {
		return
	}
	if root, ok := gitRepoRoot(ctx, cwd); ok {
		registerCommitScanRepo(dataDir, root)
	}
}

// observeRollout extracts the session working directory from a codex rollout
// file's leading records and registers its repo. This is the fallback for
// codex state DB schemas that carry no cwd column.
func (d *repoDiscovery) observeRollout(ctx context.Context, dataDir, rolloutPath string) {
	rolloutPath = strings.TrimSpace(rolloutPath)
	if rolloutPath == "" {
		return
	}
	d.mu.Lock()
	seen := d.rollouts[rolloutPath]
	d.rollouts[rolloutPath] = true
	d.mu.Unlock()
	if seen {
		return
	}
	d.observeCwd(ctx, dataDir, codexRolloutCwd(rolloutPath))
}

// codexRolloutCwd returns the first cwd-shaped field in the leading records of
// a codex rollout file, or "" when none is present.
func codexRolloutCwd(path string) string {
	file, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer file.Close()
	scanner := bufio.NewScanner(transcriptReader(file))
	scanner.Buffer(make([]byte, 64*1024), 10*1024*1024)
	for i := 0; i < codexRolloutCwdMaxRecords && scanner.Scan(); i++ {
		var record struct {
			Payload map[string]interface{} `json:"payload"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &record); err != nil {
			continue
		}
		if record.Payload == nil {
			continue
		}
		if cwd := firstString(record.Payload, "cwd", "repo_root", "worktree"); cwd != "" {
			return cwd
		}
	}
	return ""
}
