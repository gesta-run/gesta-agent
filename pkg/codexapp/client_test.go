package codexapp

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestReadTurnUsesOfficialThreadReadResponse(t *testing.T) {
	script := `#!/bin/sh
read initialize
printf '%s\n' '{"id":1,"result":{"userAgent":"fake"}}'
read initialized
read thread_read
printf '%s\n' '{"id":2,"result":{"thread":{"turns":[{"id":"turn-2","status":"completed","completedAt":1700000000,"items":[{"id":"item-file","type":"fileChange","status":"completed","changes":[{"path":"docs/test.md","kind":{"type":"add"},"diff":"hello docs\\n"}]},{"id":"item-mcp","type":"mcpToolCall","status":"completed","server":"notion","tool":"create_page","arguments":{"title":"Release notes"}}]}]}}}'
`
	bin := writeTestExecutable(t, "codex", script, 0o700)
	t.Setenv("GESTA_CODEX_BIN", bin)

	turn, err := ReadTurn(context.Background(), "thread-1", "turn-2")
	if err != nil {
		t.Fatalf("ReadTurn: %v", err)
	}
	if turn.ID != "turn-2" || len(turn.Items) != 2 {
		t.Fatalf("turn = %#v", turn)
	}
	if got := turn.Items[0].Changes[0].Path; got != "docs/test.md" {
		t.Fatalf("file change path = %q", got)
	}
	if got := turn.Items[0].Changes[0].Kind.Type; got != "add" {
		t.Fatalf("file change kind = %q", got)
	}
	if got := turn.Items[1].Tool; got != "create_page" {
		t.Fatalf("MCP tool = %q", got)
	}
}

func TestReadTurnRejectsMissingTurn(t *testing.T) {
	script := `#!/bin/sh
read initialize
printf '%s\n' '{"id":1,"result":{"userAgent":"fake"}}'
read initialized
read thread_read
printf '%s\n' '{"id":2,"result":{"thread":{"turns":[]}}}'
`
	bin := writeTestExecutable(t, "codex", script, 0o700)
	t.Setenv("GESTA_CODEX_BIN", bin)

	if _, err := ReadTurn(context.Background(), "thread-1", "missing"); err == nil {
		t.Fatal("ReadTurn should reject a missing turn")
	}
}

func TestReadTurnFallsBackAfterAutomaticCandidateCannotStartAppServer(t *testing.T) {
	broken := writeTestExecutable(t, "broken-codex", "#!/bin/sh\necho missing platform package >&2\nexit 1\n", 0o700)
	working := writeTestExecutable(t, "working-codex", `#!/bin/sh
read initialize
printf '%s\n' '{"id":1,"result":{"userAgent":"fake"}}'
read initialized
read thread_read
printf '%s\n' '{"id":2,"result":{"thread":{"turns":[{"id":"turn-2","status":"completed","items":[]}]}}}'
`, 0o700)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	turn, err := readTurnFromCandidates(ctx, []executableCandidate{
		{Path: broken, Source: "broken automatic candidate"},
		{Path: working, Source: "working automatic candidate"},
	}, "thread-1", "turn-2")
	if err != nil {
		t.Fatalf("readTurnFromCandidates: %v", err)
	}
	if turn.ID != "turn-2" {
		t.Fatalf("turn = %#v, want turn-2", turn)
	}
}

func TestReadTurnFallsBackAfterInitializeFailure(t *testing.T) {
	incompatible := writeTestExecutable(t, "incompatible-codex", `#!/bin/sh
read initialize
printf '%s\n' '{"id":1,"error":{"code":-32601,"message":"initialize is unsupported"}}'
`, 0o700)
	working := writeTestExecutable(t, "working-codex", `#!/bin/sh
read initialize
printf '%s\n' '{"id":1,"result":{"userAgent":"fake"}}'
read initialized
read thread_read
printf '%s\n' '{"id":2,"result":{"thread":{"turns":[{"id":"turn-2","status":"completed","items":[]}]}}}'
`, 0o700)

	turn, err := readTurnFromCandidates(context.Background(), []executableCandidate{
		{Path: incompatible, Source: "incompatible automatic candidate"},
		{Path: working, Source: "working automatic candidate"},
	}, "thread-1", "turn-2")
	if err != nil {
		t.Fatalf("readTurnFromCandidates: %v", err)
	}
	if turn.ID != "turn-2" {
		t.Fatalf("turn = %#v, want turn-2", turn)
	}
}

func TestReadTurnFallsBackAfterInvalidInitializeResult(t *testing.T) {
	incompatible := writeTestExecutable(t, "incompatible-codex", `#!/bin/sh
read initialize
printf '%s\n' '{"id":1,"result":null}'
`, 0o700)
	working := writeTestExecutable(t, "working-codex", `#!/bin/sh
read initialize
printf '%s\n' '{"id":1,"result":{"userAgent":"fake"}}'
read initialized
read thread_read
printf '%s\n' '{"id":2,"result":{"thread":{"turns":[{"id":"turn-2","status":"completed","items":[]}]}}}'
`, 0o700)

	turn, err := readTurnFromCandidates(context.Background(), []executableCandidate{
		{Path: incompatible, Source: "incompatible automatic candidate"},
		{Path: working, Source: "working automatic candidate"},
	}, "thread-1", "turn-2")
	if err != nil {
		t.Fatalf("readTurnFromCandidates: %v", err)
	}
	if turn.ID != "turn-2" {
		t.Fatalf("turn = %#v, want turn-2", turn)
	}
}

func TestReadTurnFallsBackWhenAutomaticCandidateDoesNotContainTurn(t *testing.T) {
	missing := writeTestExecutable(t, "missing-turn-codex", `#!/bin/sh
read initialize
printf '%s\n' '{"id":1,"result":{"userAgent":"fake"}}'
read initialized
read thread_read
printf '%s\n' '{"id":2,"result":{"thread":{"turns":[]}}}'
`, 0o700)
	working := writeTestExecutable(t, "working-codex", `#!/bin/sh
read initialize
printf '%s\n' '{"id":1,"result":{"userAgent":"fake"}}'
read initialized
read thread_read
printf '%s\n' '{"id":2,"result":{"thread":{"turns":[{"id":"turn-2","status":"completed","items":[]}]}}}'
`, 0o700)

	turn, err := readTurnFromCandidates(context.Background(), []executableCandidate{
		{Path: missing, Source: "candidate without the turn"},
		{Path: working, Source: "candidate with the turn"},
	}, "thread-1", "turn-2")
	if err != nil {
		t.Fatalf("readTurnFromCandidates: %v", err)
	}
	if turn.ID != "turn-2" {
		t.Fatalf("turn = %#v, want turn-2", turn)
	}
}

func TestReadTurnFallsBackAfterAutomaticCandidateTimeout(t *testing.T) {
	hung := writeTestExecutable(t, "hung-codex", "#!/bin/sh\nwhile :; do :; done\n", 0o700)
	working := writeTestExecutable(t, "working-codex", `#!/bin/sh
read initialize
printf '%s\n' '{"id":1,"result":{"userAgent":"fake"}}'
read initialized
read thread_read
printf '%s\n' '{"id":2,"result":{"thread":{"turns":[{"id":"turn-2","status":"completed","items":[]}]}}}'
`, 0o700)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	turn, err := readTurnFromCandidatesWithTimeout(ctx, []executableCandidate{
		{Path: hung, Source: "hung automatic candidate"},
		{Path: working, Source: "working automatic candidate"},
	}, "thread-1", "turn-2", 500*time.Millisecond)
	if err != nil {
		t.Fatalf("readTurnFromCandidatesWithTimeout: %v", err)
	}
	if turn.ID != "turn-2" {
		t.Fatalf("turn = %#v, want turn-2", turn)
	}
}

func TestReadTurnAllowsSlowThreadReadAfterFastInitialize(t *testing.T) {
	working := writeTestExecutable(t, "slow-thread-read-codex", `#!/bin/sh
read initialize
printf '%s\n' '{"id":1,"result":{"userAgent":"fake"}}'
read initialized
read thread_read
sleep 1
printf '%s\n' '{"id":2,"result":{"thread":{"turns":[{"id":"turn-2","status":"completed","items":[]}]}}}'
`, 0o700)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	turn, err := readTurnFromCandidatesWithTimeout(ctx, []executableCandidate{
		{Path: working, Source: "slow thread/read candidate"},
	}, "thread-1", "turn-2", 500*time.Millisecond)
	if err != nil {
		t.Fatalf("readTurnFromCandidatesWithTimeout: %v", err)
	}
	if turn.ID != "turn-2" {
		t.Fatalf("turn = %#v, want turn-2", turn)
	}
}

func TestExplicitExecutableFailureDoesNotFallBack(t *testing.T) {
	explicit := writeTestExecutable(t, "explicit-codex", "#!/bin/sh\nexit 1\n", 0o700)
	working := writeTestExecutable(t, "working-codex", `#!/bin/sh
read initialize
printf '%s\n' '{"id":1,"result":{"userAgent":"fake"}}'
read initialized
read thread_read
printf '%s\n' '{"id":2,"result":{"thread":{"turns":[{"id":"turn-2","status":"completed","items":[]}]}}}'
`, 0o700)

	_, err := readTurnFromCandidates(context.Background(), []executableCandidate{
		{Path: explicit, Source: "GESTA_CODEX_BIN", Explicit: true},
		{Path: working, Source: "automatic candidate"},
	}, "thread-1", "turn-2")
	if err == nil {
		t.Fatal("readTurnFromCandidates should reject a broken explicit executable")
	}
	if !strings.Contains(err.Error(), "GESTA_CODEX_BIN") {
		t.Fatalf("error = %q, want explicit configuration source", err)
	}
}

func writeTestExecutable(t *testing.T, name, contents string, mode os.FileMode) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(contents), mode); err != nil {
		t.Fatalf("write test executable: %v", err)
	}
	return path
}
