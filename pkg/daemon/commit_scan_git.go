package daemon

import (
	"bytes"
	"context"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"
)

// normalizeGitRemoteURL reduces a remote URL to host/path identity so SSH and
// HTTPS clones of the same repository produce the same repository id.
func normalizeGitRemoteURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	hadScheme := false
	for _, prefix := range []string{"ssh://", "git://", "https://", "http://", "file://"} {
		if strings.HasPrefix(raw, prefix) {
			raw = strings.TrimPrefix(raw, prefix)
			hadScheme = true
			break
		}
	}
	if at := strings.Index(raw, "@"); at >= 0 {
		raw = raw[at+1:]
	}
	if hadScheme {
		slash := strings.Index(raw, "/")
		if slash <= 0 {
			return ""
		}
		host := raw[:slash]
		if colon := strings.Index(host, ":"); colon >= 0 {
			host = host[:colon]
		}
		raw = host + raw[slash:]
	} else {
		raw = strings.Replace(raw, ":", "/", 1)
	}
	raw = strings.TrimSuffix(strings.TrimSuffix(raw, "/"), ".git")
	slash := strings.Index(raw, "/")
	if slash <= 0 {
		return ""
	}
	host := strings.ToLower(raw[:slash])
	path := strings.Trim(raw[slash:], "/")
	if path == "" {
		return ""
	}
	return host + "/" + path
}

func clampCommitTime(committed, now time.Time) time.Time {
	if committed.IsZero() || committed.After(now) {
		return now
	}
	return committed
}

func resolveDefaultBranchRef(ctx context.Context, root string) (string, bool) {
	out, err := gitStdout(ctx, root, "", "symbolic-ref", "--short", "refs/remotes/origin/HEAD")
	if err == nil {
		if ref := strings.TrimSpace(out); ref != "" {
			return ref, true
		}
	}
	for _, candidate := range []string{"origin/main", "origin/master"} {
		if gitRevParse(ctx, root, candidate) != "" {
			return candidate, true
		}
	}
	out, err = gitStdout(ctx, root, "", "for-each-ref", "--format=%(refname:short)", "refs/remotes/origin")
	if err != nil {
		return "", false
	}
	var heads []string
	for _, line := range strings.Split(out, "\n") {
		ref := strings.TrimSpace(line)
		if ref != "" && !strings.HasSuffix(ref, "/HEAD") {
			heads = append(heads, ref)
		}
	}
	if len(heads) == 1 {
		return heads[0], true
	}
	return "", false
}

// gitIsShallowClone reports whether the clone at root is shallow. Shallow
// history ends at the clone boundary, so the commit scan silently undercounts
// there; the adapter surfaces it as a one-time warning per daemon run.
func gitIsShallowClone(ctx context.Context, root string) bool {
	out, err := gitStdout(ctx, root, "", "rev-parse", "--is-shallow-repository")
	return err == nil && strings.TrimSpace(out) == "true"
}

func gitOriginURL(ctx context.Context, root string) string {
	out, err := gitStdout(ctx, root, "", "config", "--get", "remote.origin.url")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

func gitRevParse(ctx context.Context, root, ref string) string {
	out, err := gitStdout(ctx, root, "", "rev-parse", "--verify", "--quiet", ref)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

func gitStdout(ctx context.Context, root, stdin string, args ...string) (string, error) {
	cmdCtx, cancel := context.WithTimeout(ctx, commitScanGitTimeout)
	defer cancel()
	cmd := exec.CommandContext(cmdCtx, "git", append([]string{"-C", root}, args...)...)
	cmd.Env = os.Environ()
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = io.Discard
	err := cmd.Run()
	return stdout.String(), err
}
