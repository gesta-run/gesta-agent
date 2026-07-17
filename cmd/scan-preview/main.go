// Command scan-preview is a local verification harness for the merged-commit
// scanner (CLO-1733). It scans the repos passed as arguments exactly the way
// the daemon's GitCommitsAdapter does — same code path, a throwaway state dir,
// no daemon — and prints the commits it would report plus the per-author
// totals the efficiency card would be built from.
//
// With --post it additionally registers a preview daemon via heartbeat and
// ships the repo.commits events to a control plane, so a local
// stack can be fed real data end to end.
//
// Usage:
//
//	go run ./cmd/scan-preview /path/to/repo [more repos...]
//	go run ./cmd/scan-preview --post http://localhost:8080 --apikey sk-... /path/to/repo
//	go run ./cmd/scan-preview --post ... --apikey ... --heartbeat-only
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/gesta-run/gesta-agent/pkg/daemon"
	"github.com/gesta-run/gesta-agent/pkg/model"
	"github.com/gesta-run/gesta-agent/pkg/util"
)

func main() {
	post := flag.String("post", "", "control plane URL to ship the events to (default: print only)")
	apiKey := flag.String("apikey", "", "daemon API key for --post")
	daemonID := flag.String("daemon-id", "", "daemon id to report as (default: derived from the API key)")
	heartbeatOnly := flag.Bool("heartbeat-only", false, "only register the preview daemon; scan nothing")
	collectUsage := flag.Bool("collect-usage", false, "run ALL adapters (Claude/Codex usage + commits), not just the commit scanner")
	flag.Parse()

	identitySeed := strings.TrimSpace(*apiKey)
	if identitySeed == "" {
		identitySeed = "local-preview"
	}
	if hostname, err := os.Hostname(); err == nil {
		identitySeed = strings.ToLower(strings.TrimSpace(hostname)) + "|" + identitySeed
	}
	if *daemonID == "" {
		*daemonID = "d-preview-" + util.ShortHash(identitySeed)
	}
	cfg := daemon.Config{
		CustomerID:   "local-preview",
		DeploymentID: "local-preview",
		DaemonID:     *daemonID,
		DeviceID:     "dev-" + util.ShortHash(identitySeed),
	}

	var client *daemon.Client
	if *post != "" {
		if *apiKey == "" {
			fmt.Fprintln(os.Stderr, "--post requires --apikey")
			os.Exit(2)
		}
		client = daemon.NewClient(*post, *apiKey)
		if _, err := client.Heartbeat(model.HeartbeatRequest{
			DaemonID:      cfg.DaemonID,
			DeviceID:      cfg.DeviceID,
			Hostname:      "scan-preview",
			DaemonVersion: model.DaemonVersion,
			PolicyVersion: "bootstrap-v0",
			HealthStatus:  "ok",
		}); err != nil {
			fmt.Fprintln(os.Stderr, "heartbeat:", err)
			os.Exit(1)
		}
		fmt.Printf("registered %s at %s\n", cfg.DaemonID, *post)
	}
	if *heartbeatOnly {
		return
	}
	if flag.NArg() == 0 {
		fmt.Fprintln(os.Stderr, "usage: scan-preview [--post URL --apikey KEY] <repo path> [more repos...]")
		os.Exit(2)
	}

	// Commit scanning is stateless enough for a throwaway dir, but usage
	// accounting is cursor-based: the first collection seeds per-session
	// cumulative cursors and only subsequent collections emit usage.delta
	// events. --collect-usage therefore needs state that survives between runs.
	var dataDir string
	if *collectUsage {
		dataDir = filepath.Join(os.TempDir(), "gesta-scan-preview-state")
		if err := os.MkdirAll(dataDir, 0o700); err != nil {
			fmt.Fprintln(os.Stderr, "state dir:", err)
			os.Exit(1)
		}
	} else {
		tmp, err := os.MkdirTemp("", "gesta-scan-preview-*")
		if err != nil {
			fmt.Fprintln(os.Stderr, "temp dir:", err)
			os.Exit(1)
		}
		defer os.RemoveAll(tmp)
		dataDir = tmp
	}
	cfg.DataDir = dataDir

	repoDir := filepath.Join(dataDir, "commit-repos.d")
	if err := os.MkdirAll(repoDir, 0o700); err != nil {
		fmt.Fprintln(os.Stderr, "registry dir:", err)
		os.Exit(1)
	}
	for _, root := range flag.Args() {
		abs, err := filepath.Abs(root)
		if err != nil {
			fmt.Fprintf(os.Stderr, "skip %s: %v\n", root, err)
			continue
		}
		record, _ := json.Marshal(map[string]string{"root": abs})
		if err := os.WriteFile(filepath.Join(repoDir, util.ShortHash(abs)+".json"), record, 0o600); err != nil {
			fmt.Fprintf(os.Stderr, "register %s: %v\n", abs, err)
		}
	}

	var events []model.EventEnvelope
	var commitUsageCursors func() error
	var commitOutputCursors func() error
	if *collectUsage {
		// Full collection: real Claude/Codex usage from this machine's transcripts
		// plus the commit scan — followed by the SAME transforms the daemon runner
		// applies before queueing (delta building, internal-event filtering,
		// output-summary dedupe), so a stack seeded by this tool matches what a
		// real fleet produces. Cursor commits run only after a successful ship,
		// mirroring the runner's queue-then-commit ordering.
		all, statuses := daemon.Collect(context.Background(), cfg)
		deltas, commitDeltas, err := daemon.BuildUsageDeltaEvents(cfg, all, time.Now().UTC())
		if err != nil {
			fmt.Fprintln(os.Stderr, "usage deltas:", err)
		} else {
			commitUsageCursors = commitDeltas
			fmt.Printf("usage.delta events prepared: %d\n", len(deltas))
		}
		queueEvents := make([]model.EventEnvelope, 0, len(all)+len(deltas))
		for _, event := range all {
			if cursorOnly, _ := event.Payload["_gesta_internal_cursor_only"].(bool); cursorOnly {
				continue
			}
			queueEvents = append(queueEvents, event)
		}
		queueEvents = append(queueEvents, deltas...)
		queueEvents, commitOutputs, err := daemon.FilterOutputSummaryEvents(cfg, queueEvents)
		if err != nil {
			fmt.Fprintln(os.Stderr, "filter output summaries:", err)
			os.Exit(1)
		}
		commitOutputCursors = commitOutputs
		events = queueEvents
		for _, status := range statuses {
			fmt.Printf("adapter %s: %s (detected: %v)\n", status.Name, status.Status, status.Detected)
		}
		fmt.Println()
	} else {
		result, commitEvents := daemon.GitCommitsAdapter{}.Collect(context.Background(), cfg)
		events = commitEvents
		fmt.Printf("adapter status: %s (repos detected: %v)\n\n", result.Status.Status, result.Status.Detected)
	}

	type totals struct {
		commits, aiCommits              int
		code, test, doc, lines, aiLines int64
	}
	byAuthor := map[string]*totals{}

	num := func(commit map[string]interface{}, key string) int64 {
		if v, ok := commit[key].(int64); ok {
			return v
		}
		return 0
	}

	for _, event := range events {
		if event.EventType != "repo.commits" {
			continue
		}
		commits, _ := event.Payload["commits"].([]map[string]interface{})
		fmt.Printf("repo %v (default branch %v): %d commits in this batch\n",
			event.Payload["repo_remote"], event.Payload["default_branch"], len(commits))
		for _, commit := range commits {
			sha, _ := commit["sha"].(string)
			if len(sha) > 8 {
				sha = sha[:8]
			}
			author, _ := commit["author_email"].(string)
			when, _ := commit["committed_at"].(string)
			code, test, doc := num(commit, "code_lines_added"), num(commit, "test_lines_added"), num(commit, "doc_lines_added")
			ai := ""
			if commit["ai_assisted"] == true {
				tool, _ := commit["ai_tool"].(string)
				ai = "  [AI:" + tool + "]"
			}
			fmt.Printf("  %s  %s  %-32s  code +%-5d test +%-4d doc +%-4d%s\n", sha, when, author, code, test, doc, ai)

			t := byAuthor[author]
			if t == nil {
				t = &totals{}
				byAuthor[author] = t
			}
			t.commits++
			added := code + test + doc
			t.code += code
			t.test += test
			t.doc += doc
			t.lines += added
			if commit["ai_assisted"] == true {
				t.aiCommits++
				t.aiLines += added
			}
		}
		fmt.Println()
	}

	authors := make([]string, 0, len(byAuthor))
	for author := range byAuthor {
		authors = append(authors, author)
	}
	sort.Slice(authors, func(i, j int) bool { return byAuthor[authors[i]].lines > byAuthor[authors[j]].lines })

	fmt.Println("=== per-author totals (what the efficiency denominator is built from) ===")
	for _, author := range authors {
		t := byAuthor[author]
		share := 0.0
		if t.lines > 0 {
			share = float64(t.aiLines) / float64(t.lines) * 100
		}
		fmt.Printf("%-36s commits %-4d code +%-6d test +%-5d doc +%-5d AI share %5.1f%%\n",
			author, t.commits, t.code, t.test, t.doc, share)
	}

	if client != nil && len(events) > 0 {
		if err := client.SendEvents(events); err != nil {
			fmt.Fprintln(os.Stderr, "send events:", err)
			os.Exit(1)
		}
		if commitUsageCursors != nil {
			if err := commitUsageCursors(); err != nil {
				fmt.Fprintln(os.Stderr, "commit usage cursors:", err)
			}
		}
		if commitOutputCursors != nil {
			if err := commitOutputCursors(); err != nil {
				fmt.Fprintln(os.Stderr, "commit output cursors:", err)
			}
		}
		// Same contract as the runner: commit-scan cursors advance only after
		// the events were durably shipped.
		if commitScanCursors := daemon.CommitScanCursorCommit(cfg, events); commitScanCursors != nil {
			if err := commitScanCursors(); err != nil {
				fmt.Fprintln(os.Stderr, "commit scan cursors:", err)
			}
		}
		fmt.Printf("\nshipped %d events to %s\n", len(events), *post)
	}
}
