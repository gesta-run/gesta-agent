package daemon

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/gesta-run/gesta-agent/pkg/util"
)

func TestCodexTurnSessionFromRollout(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rollout.jsonl")
	contents := "" +
		`{"type":"session_meta","payload":{"session_id":"raw-session","cwd":"C:\\repo","model":"gpt-5.6-sol","model_provider":"openai"}}` + "\n" +
		`{"type":"event_msg","payload":{"type":"user_message","message":"Fix the Windows collector"}}` + "\n"
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("write rollout: %v", err)
	}

	session, ok, _, err := readCodexTurnSession(path)
	if err != nil || !ok {
		t.Fatal("rollout session was not discovered")
	}
	if session.SessionID != util.ShortHash("raw-session") || session.Model != "gpt-5.6-sol" {
		t.Fatalf("session = %#v", session)
	}
	if session.ModelProvider != "openai" || session.Repo != util.ShortHash(`C:\repo`) {
		t.Fatalf("session metadata = %#v", session)
	}
	if session.Title != "Fix the Windows collector" {
		t.Fatalf("fallback title = %q", session.Title)
	}
}

func TestDiscoverCodexTurnSessionsPrefersNewerDuplicate(t *testing.T) {
	root := t.TempDir()
	oldPath := filepath.Join(root, "old.jsonl")
	newPath := filepath.Join(root, "new.jsonl")
	meta := `{"type":"session_meta","payload":{"session_id":"shared-session"}}` + "\n"
	if err := os.WriteFile(oldPath, []byte(meta), 0o600); err != nil {
		t.Fatalf("write old rollout: %v", err)
	}
	if err := os.WriteFile(newPath, []byte(meta+`{"type":"event_msg","payload":{"type":"user_message","message":"fallback title"}}`+"\n"), 0o600); err != nil {
		t.Fatalf("write new rollout: %v", err)
	}
	oldTime := time.Now().Add(-time.Hour)
	if err := os.Chtimes(oldPath, oldTime, oldTime); err != nil {
		t.Fatalf("age old rollout: %v", err)
	}

	title := "Friendly session title"
	sessions, err := discoverCodexTurnSessionsWithTitles(root, map[string]string{util.ShortHash("shared-session"): title})
	if err != nil {
		t.Fatalf("discover sessions: %v", err)
	}
	if len(sessions) != 1 || sessions[0].RolloutPath != newPath || sessions[0].Title != title {
		t.Fatalf("sessions = %#v", sessions)
	}
}

func TestCodexTurnSessionOmitsRepoForEmptyCWD(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rollout.jsonl")
	if err := os.WriteFile(path, []byte(`{"type":"session_meta","payload":{"session_id":"raw-session","cwd":""}}`+"\n"), 0o600); err != nil {
		t.Fatalf("write rollout: %v", err)
	}
	session, ok, _, err := readCodexTurnSession(path)
	if err != nil || !ok || session.Repo != "" {
		t.Fatalf("session = %#v, want empty repo", session)
	}
}

func TestCodexTurnSessionDiscoversHashedParent(t *testing.T) {
	tests := map[string]string{
		"legacy fork":  `{"type":"session_meta","payload":{"id":"child","forked_from_id":"parent"}}`,
		"thread spawn": `{"type":"session_meta","payload":{"id":"child","source":{"subagent":{"thread_spawn":{"parent_thread_id":"parent"}}}}}`,
	}
	for name, contents := range tests {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "rollout.jsonl")
			if err := os.WriteFile(path, []byte(contents+"\n"), 0o600); err != nil {
				t.Fatalf("write rollout: %v", err)
			}
			session, ok, _, err := readCodexTurnSession(path)
			if err != nil || !ok || session.ParentSessionID != util.ShortHash("parent") {
				t.Fatalf("session = %#v, ok=%v, err=%v", session, ok, err)
			}
		})
	}
}

func TestDiscoverCodexTurnSessionsFindsFileAddedAfterInitialScan(t *testing.T) {
	root := t.TempDir()
	firstPath := filepath.Join(root, "first.jsonl")
	if err := os.WriteFile(firstPath, []byte(`{"type":"session_meta","payload":{"id":"first"}}`+"\n"), 0o600); err != nil {
		t.Fatalf("write first rollout: %v", err)
	}
	first, err := discoverCodexTurnSessionsWithTitles(root, nil)
	if err != nil || len(first) != 1 {
		t.Fatalf("first discovery = %#v, err=%v", first, err)
	}

	secondPath := filepath.Join(root, "second.jsonl")
	if err := os.WriteFile(secondPath, []byte(`{"type":"session_meta","payload":{"id":"second"}}`+"\n"), 0o600); err != nil {
		t.Fatalf("write second rollout: %v", err)
	}
	second, err := discoverCodexTurnSessionsWithTitles(root, nil)
	if err != nil || len(second) != 2 {
		t.Fatalf("second discovery = %#v, err=%v", second, err)
	}
}

func TestDiscoverCodexTurnSessionsRefreshesLateTitle(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "rollout.jsonl")
	meta := `{"type":"session_meta","payload":{"id":"late-title"}}` + "\n"
	if err := os.WriteFile(path, []byte(meta), 0o600); err != nil {
		t.Fatalf("write rollout: %v", err)
	}
	first, err := discoverCodexTurnSessionsWithTitles(root, nil)
	if err != nil || len(first) != 1 || first[0].Title != "" {
		t.Fatalf("first discovery = %#v, err=%v", first, err)
	}
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatalf("open rollout: %v", err)
	}
	_, writeErr := file.WriteString(`{"type":"event_msg","payload":{"type":"user_message","message":"Late title"}}` + "\n")
	closeErr := file.Close()
	if writeErr != nil || closeErr != nil {
		t.Fatalf("append rollout: write=%v close=%v", writeErr, closeErr)
	}
	second, err := discoverCodexTurnSessionsWithTitles(root, nil)
	if err != nil || len(second) != 1 || second[0].Title != "Late title" {
		t.Fatalf("second discovery = %#v, err=%v", second, err)
	}
}

func TestDiscoverCodexTurnSessionsRetriesOnlyMalformedRollout(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "rollout.jsonl")
	if err := os.WriteFile(path, []byte("{"), 0o600); err != nil {
		t.Fatalf("write malformed rollout: %v", err)
	}
	if sessions, err := discoverCodexTurnSessionsWithTitles(root, nil); err == nil || len(sessions) != 0 {
		t.Fatalf("malformed discovery = %#v, err=%v", sessions, err)
	}
	if changed := changedCodexDiscoveryDirectories(root); len(changed) != 0 {
		t.Fatalf("malformed rollout left directories pending: %#v", changed)
	}

	contents := `{"type":"session_meta","payload":{"id":"recovered"}}` + "\n"
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("repair rollout: %v", err)
	}
	sessions, err := discoverCodexTurnSessionsWithTitles(root, nil)
	if err != nil || len(sessions) != 1 || sessions[0].SessionID != util.ShortHash("recovered") {
		t.Fatalf("recovered discovery = %#v, err=%v", sessions, err)
	}
}

func TestCodexFallbackTitleUsesExplicitRequest(t *testing.T) {
	line := `{"type":"event_msg","payload":{"type":"user_message","message":"<in-app-browser-context>ignored</in-app-browser-context>\n\n## My request:\nReal request title"}}`
	if got := codexUserTitleFromRecord(line); got != "Real request title" {
		t.Fatalf("title = %q", got)
	}
}
