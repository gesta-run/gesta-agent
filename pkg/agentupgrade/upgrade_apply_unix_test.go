//go:build !windows

package agentupgrade

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gesta-run/gesta-agent/pkg/model"
)

func TestApplyAgentUpgradeToPathDownloadsVerifiesAndReplaces(t *testing.T) {
	const targetVersion = "9.9.9"
	newAgent := "#!/bin/sh\nif [ \"$1\" = \"version\" ]; then echo " + targetVersion + "; else echo upgraded; fi\n"
	sum := sha256.Sum256([]byte(newAgent))
	sha := hex.EncodeToString(sum[:])

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/bin/gesta-agent-darwin-arm64":
			_, _ = w.Write([]byte(newAgent))
		case "/SHA256SUMS":
			_, _ = fmt.Fprintf(w, "%s  bin/gesta-agent-darwin-arm64\n", sha)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	dir := t.TempDir()
	targetPath := filepath.Join(dir, "gesta-agent")
	if err := os.WriteFile(targetPath, []byte("#!/bin/sh\necho old\n"), 0o755); err != nil {
		t.Fatalf("write target: %v", err)
	}

	err := ApplyAgentUpgradeToPath(context.Background(), model.AgentUpgradePolicy{
		TargetVersion: targetVersion,
		URL:           server.URL + "/bin/gesta-agent-darwin-arm64",
		ChecksumURL:   server.URL + "/SHA256SUMS",
	}, targetPath)
	if err != nil {
		t.Fatalf("ApplyAgentUpgradeToPath: %v", err)
	}

	out, err := exec.Command(targetPath, "version").CombinedOutput()
	if err != nil {
		t.Fatalf("run upgraded agent: %v: %s", err, out)
	}
	if got := strings.TrimSpace(string(out)); got != targetVersion {
		t.Fatalf("upgraded version = %q, want %q", got, targetVersion)
	}
	if _, err := os.Stat(targetPath + ".prev"); err != nil {
		t.Fatalf("expected backup binary: %v", err)
	}
}
