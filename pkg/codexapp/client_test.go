package codexapp

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestReadTurnUsesOfficialThreadReadResponse(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "codex")
	script := `#!/bin/sh
read initialize
read initialized
read thread_read
printf '%s\n' '{"id":1,"result":{"userAgent":"fake"}}'
printf '%s\n' '{"id":2,"result":{"thread":{"turns":[{"id":"turn-2","status":"completed","completedAt":1700000000,"items":[{"id":"item-file","type":"fileChange","status":"completed","changes":[{"path":"docs/test.md","kind":{"type":"add"},"diff":"hello docs\\n"}]},{"id":"item-mcp","type":"mcpToolCall","status":"completed","server":"notion","tool":"create_page","arguments":{"title":"Release notes"}}]}]}}}'
`
	if err := os.WriteFile(bin, []byte(script), 0o700); err != nil {
		t.Fatalf("write fake Codex: %v", err)
	}
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
	bin := filepath.Join(t.TempDir(), "codex")
	script := `#!/bin/sh
read initialize
read initialized
read thread_read
printf '%s\n' '{"id":2,"result":{"thread":{"turns":[]}}}'
`
	if err := os.WriteFile(bin, []byte(script), 0o700); err != nil {
		t.Fatalf("write fake Codex: %v", err)
	}
	t.Setenv("GESTA_CODEX_BIN", bin)

	if _, err := ReadTurn(context.Background(), "thread-1", "missing"); err == nil {
		t.Fatal("ReadTurn should reject a missing turn")
	}
}
