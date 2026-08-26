package daemon

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	turnusage "github.com/gesta-run/gesta-agent/pkg/turn"
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

func TestCodexTurnSessionPrefersCanonicalIDAndKeepsLegacyID(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rollout.jsonl")
	contents := `{"type":"session_meta","payload":{"id":"child","session_id":"root","forked_from_id":"parent"}}` + "\n"
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("write rollout: %v", err)
	}

	session, ok, _, err := readCodexTurnSession(path)
	if err != nil || !ok {
		t.Fatalf("read session: ok=%v err=%v", ok, err)
	}
	if session.SessionID != util.ShortHash("child") || session.LegacySessionID != util.ShortHash("root") {
		t.Fatalf("session identity = %#v", session)
	}
	if session.ParentSessionID != util.ShortHash("parent") {
		t.Fatalf("parent session = %q", session.ParentSessionID)
	}
}

func TestCodexTurnSessionRejectsSelfParent(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "rollout.jsonl")
	contents := `{"type":"session_meta","payload":{"id":"child","session_id":"root","forked_from_id":"child"}}` + "\n"
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("write rollout: %v", err)
	}
	if _, ok, _, err := readCodexTurnSession(path); err == nil || ok {
		t.Fatalf("self-parent session: ok=%v err=%v", ok, err)
	}
	if sessions, err := discoverCodexTurnSessionsWithTitles(root, nil); err == nil || len(sessions) != 0 {
		t.Fatalf("first discovery: sessions=%#v err=%v", sessions, err)
	}
	if sessions, err := discoverCodexTurnSessionsWithTitles(root, nil); err != nil || len(sessions) != 0 {
		t.Fatalf("unchanged discovery retried permanent error: sessions=%#v err=%v", sessions, err)
	}
	contents = `{"type":"session_meta","payload":{"id":"child","session_id":"root","forked_from_id":"repaired-parent"}}` + "\n"
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("repair rollout: %v", err)
	}
	if sessions, err := discoverCodexTurnSessionsWithTitles(root, nil); err != nil || len(sessions) != 1 {
		t.Fatalf("changed rollout was not revalidated: sessions=%#v err=%v", sessions, err)
	}
}

func TestCodexDualIdentitySiblingForksEmitOnlyUniqueTurns(t *testing.T) {
	root := t.TempDir()
	dataDir := t.TempDir()
	cutover := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	parentPath := filepath.Join(root, "parent.jsonl")
	writeCodexDiscoveryRollout(t, parentPath, []string{
		`{"timestamp":"2026-08-20T11:59:00Z","type":"session_meta","payload":{"id":"parent","session_id":"root"}}`,
		`{"timestamp":"2026-08-20T11:59:01Z","type":"event_msg","payload":{"type":"task_started","turn_id":"parent-turn"}}`,
		`{"timestamp":"2026-08-20T11:59:02Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":100}}}}`,
		`{"timestamp":"2026-08-20T11:59:03Z","type":"event_msg","payload":{"type":"task_complete","turn_id":"parent-turn"}}`,
	})
	parentSessions, err := discoverCodexTurnSessionsWithTitles(root, nil)
	if err != nil || len(parentSessions) != 1 {
		t.Fatalf("parent discovery = %#v, err=%v", parentSessions, err)
	}
	_, commit, err := turnusage.CollectCodex(turnusage.Config{DataDir: dataDir, DaemonID: "daemon"}, parentSessions, cutover)
	if err != nil || commit == nil {
		t.Fatalf("initialize collector: commit=%v err=%v", commit != nil, err)
	}
	if err := commit(); err != nil {
		t.Fatalf("commit initialization: %v", err)
	}

	writeCodexFork := func(path, childID, childTurn, startedAt string, finalInput int) {
		writeCodexDiscoveryRollout(t, path, []string{
			`{"timestamp":"` + startedAt + `","type":"session_meta","payload":{"id":"` + childID + `","session_id":"root","forked_from_id":"parent"}}`,
			`{"timestamp":"2026-08-20T12:01:01Z","type":"event_msg","payload":{"type":"task_started","turn_id":"parent-turn"}}`,
			`{"timestamp":"2026-08-20T12:01:02Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":100}}}}`,
			`{"timestamp":"2026-08-20T12:01:03Z","type":"event_msg","payload":{"type":"task_complete","turn_id":"parent-turn"}}`,
			`{"timestamp":"2026-08-20T12:02:00Z","type":"event_msg","payload":{"type":"task_started","turn_id":"` + childTurn + `"}}`,
			`{"timestamp":"2026-08-20T12:02:01Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":` + stringInt(int64(finalInput)) + `}}}}`,
			`{"timestamp":"2026-08-20T12:02:02Z","type":"event_msg","payload":{"type":"task_complete","turn_id":"` + childTurn + `"}}`,
		})
	}
	writeCodexFork(filepath.Join(root, "child-a.jsonl"), "child-a", "child-a-turn", "2026-08-20T12:01:00Z", 150)
	writeCodexFork(filepath.Join(root, "child-b.jsonl"), "child-b", "child-b-turn", "2026-08-20T12:01:10Z", 170)

	sessions, err := discoverCodexTurnSessionsWithTitles(root, nil)
	if err != nil || len(sessions) != 3 {
		t.Fatalf("fork discovery = %#v, err=%v", sessions, err)
	}
	events, _, err := turnusage.CollectCodex(
		turnusage.Config{DataDir: dataDir, DaemonID: "daemon"},
		mergeCodexTurnSessions(nil, sessions),
		cutover.Add(3*time.Minute),
	)
	if err != nil || len(events) != 2 {
		t.Fatalf("fork events = %#v, err=%v", events, err)
	}
	want := map[string]int64{util.ShortHash("child-a"): 50, util.ShortHash("child-b"): 70}
	for _, event := range events {
		if event.Tokens.Total() != want[event.SessionIDHash] {
			t.Fatalf("unexpected fork event = %#v", event)
		}
		delete(want, event.SessionIDHash)
	}
	if len(want) != 0 {
		t.Fatalf("missing child events = %#v", want)
	}
}

func writeCodexDiscoveryRollout(t *testing.T, path string, lines []string) {
	t.Helper()
	contents := ""
	for _, line := range lines {
		contents += line + "\n"
	}
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("write rollout: %v", err)
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
