package activitydetail

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/gesta-run/gesta-agent/pkg/model"
	"github.com/gesta-run/gesta-agent/pkg/turnreceipt"
)

func TestStoreCreatesAndReadsMinimalActivityDetail(t *testing.T) {
	store := NewStore(t.TempDir())
	now := time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return now }
	store.newID = func(string) string { return "activity_0123456789abcdef0123456789abcdef" }
	detail, err := store.Create("codex", []ContextRuleMatch{
		{
			RuleID:    "rule-review",
			Name:      "Review Standards",
			MatchType: "keyword_any",
			Priority:  100,
			Content:   "Review the complete diff.",
		},
		{
			RuleID:    "rule-always",
			Name:      "Always",
			MatchType: "always",
		},
	}, turnreceipt.OutputSummary{CodeLines: 12, DocWords: 30})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if detail.ExpiresAt != now.Add(detailTTL) || len(detail.ContextMatches) != 1 {
		t.Fatalf("created detail = %#v", detail)
	}
	got, err := store.Get(detail.ActivityID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.ActivityID != detail.ActivityID ||
		got.AgentType != "codex" ||
		got.ContextMatches[0].Name != "Review Standards" ||
		got.ContextMatches[0].Content != "Review the complete diff." ||
		got.Output.CodeLines != 12 {
		t.Fatalf("read detail = %#v", got)
	}
	data, err := os.ReadFile(filepath.Join(store.rootPath(), detail.ActivityID+".json"))
	if err != nil {
		t.Fatalf("read stored detail: %v", err)
	}
	for _, forbidden := range []string{"prompt", "keywords", "pattern", "session"} {
		if strings.Contains(string(data), forbidden) {
			t.Fatalf("stored detail leaked %q: %s", forbidden, data)
		}
	}
}

func TestStoreRequiresTargetedContextMatches(t *testing.T) {
	store := NewStore(t.TempDir())
	if _, err := store.Create(
		"codex",
		nil,
		turnreceipt.OutputSummary{CodeLines: 1},
	); err == nil {
		t.Fatal("Create output-only detail succeeded")
	}
	if _, err := store.Create(
		"codex",
		[]ContextRuleMatch{{
			RuleID: "always", Name: "Always", MatchType: "always",
		}},
		turnreceipt.OutputSummary{},
	); err == nil {
		t.Fatal("Create always-only detail succeeded")
	}
}

func TestStoreDoesNotReadVersionOneActivityDetails(t *testing.T) {
	dataDir := t.TempDir()
	store := NewStore(dataDir)
	const activityID = "activity_0123456789abcdef0123456789abcdef"
	oldRoot := filepath.Join(dataDir, "activity-details", "v1")
	if err := os.MkdirAll(oldRoot, 0o700); err != nil {
		t.Fatalf("MkdirAll old root: %v", err)
	}
	if err := os.WriteFile(
		filepath.Join(oldRoot, activityID+".json"),
		[]byte(`{"schema_version":1}`),
		0o600,
	); err != nil {
		t.Fatalf("WriteFile old detail: %v", err)
	}

	if _, err := store.Get(activityID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get old detail error = %v, want ErrNotFound", err)
	}
	if filepath.Base(store.rootPath()) != "v3" {
		t.Fatalf("activity detail root = %q, want v3", store.rootPath())
	}
}

func TestCleanupRemovesLegacySchemaRoots(t *testing.T) {
	dataDir := t.TempDir()
	for _, version := range []string{"v1", "v2"} {
		root := filepath.Join(dataDir, "activity-details", version)
		if err := os.MkdirAll(root, 0o700); err != nil {
			t.Fatalf("MkdirAll %s: %v", version, err)
		}
		if err := os.WriteFile(filepath.Join(root, "stale.json"), []byte("{}"), 0o600); err != nil {
			t.Fatalf("WriteFile %s: %v", version, err)
		}
	}
	store := NewStore(dataDir)
	current, err := store.Begin("codex")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Cleanup(); err != nil {
		t.Fatal(err)
	}
	for _, version := range []string{"v1", "v2"} {
		if _, err := os.Stat(filepath.Join(dataDir, "activity-details", version)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("legacy root %s remains: %v", version, err)
		}
	}
	if _, err := store.Get(current.ActivityID); err != nil {
		t.Fatalf("current activity was removed: %v", err)
	}
}

func TestStoreTracksCurrentContextMemoryAndPreviousOutput(t *testing.T) {
	store := NewStore(t.TempDir())
	detail, err := store.Begin("codex")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.RecordContext(detail.ActivityID, testMatches(1)); err != nil {
		t.Fatal(err)
	}
	memories := []model.Memory{
		{FactID: "fact-a", Content: "Use the release checklist."},
		{FactID: "fact-a", Content: "Use the release checklist."},
		{FactID: "fact-b", Content: "Run the health check."},
	}
	if err := store.RecordMemories(detail.ActivityID, memories); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordOutput(detail.ActivityID, turnreceipt.OutputSummary{
		CodeLines: 10, DocWords: 8, OtherWords: 4,
	}); err != nil {
		t.Fatal(err)
	}
	got, err := store.Get(detail.ActivityID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.ContextMatches) != 1 || got.MemoryRecallStatus != MemoryRecallSuccess ||
		got.MemoryCount != 2 || len(got.Memories) != 2 {
		t.Fatalf("activity detail = %#v", got)
	}
	if got.Output.EquivalentLOC() != 11.5 {
		t.Fatalf("equivalent LOC = %v, want 11.5", got.Output.EquivalentLOC())
	}
}

func TestStoreTracksMemoryRecallFailureWithoutReportingAMatch(t *testing.T) {
	store := NewStore(t.TempDir())
	detail, err := store.Begin("codex")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.RecordMemoryRecall(detail.ActivityID, MemoryRecallTimeout, nil); err != nil {
		t.Fatal(err)
	}
	got, err := store.Get(detail.ActivityID)
	if err != nil {
		t.Fatal(err)
	}
	if got.MemoryRecallStatus != MemoryRecallTimeout ||
		got.MemoryRecallFailure != MemoryRecallFailureTimeout ||
		got.MemoryCount != 0 || len(got.Memories) != 0 {
		t.Fatalf("activity detail = %#v", got)
	}
}

func TestStoreRejectsUnboundedMemoryRecallFailure(t *testing.T) {
	store := NewStore(t.TempDir())
	detail, err := store.Begin("codex")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.RecordMemoryRecallResult(
		detail.ActivityID,
		MemoryRecallError,
		MemoryRecallFailure("upstream response included secret text"),
		nil,
	); err == nil {
		t.Fatal("RecordMemoryRecallResult accepted a free-text failure")
	}
}

func TestStoreReadsLegacyMemoryRecallWithoutFailure(t *testing.T) {
	store := NewStore(t.TempDir())
	detail, err := store.Begin("codex")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.update(detail.ActivityID, func(detail *Detail) {
		detail.MemoryRecallStatus = MemoryRecallError
	}); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(store.rootPath(), detail.ActivityID+".json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "memory_recall_failure") {
		t.Fatalf("legacy record unexpectedly contains a failure field: %s", raw)
	}
	got, err := store.Get(detail.ActivityID)
	if err != nil {
		t.Fatal(err)
	}
	if got.MemoryRecallStatus != MemoryRecallError || got.MemoryRecallFailure != "" {
		t.Fatalf("legacy activity detail = %#v", got)
	}
}

func TestStoreExpiresAndCleansDetails(t *testing.T) {
	store := NewStore(t.TempDir())
	now := time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return now.Add(-48 * time.Hour) }
	detail, err := store.Create("claude_code", testMatches(1), turnreceipt.OutputSummary{})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	store.now = func() time.Time { return now }
	if _, err := store.Get(detail.ActivityID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get expired error = %v, want ErrNotFound", err)
	}
	if err := store.Cleanup(); err != nil {
		t.Fatalf("Cleanup: %v", err)
	}
	if _, err := os.Stat(filepath.Join(store.rootPath(), detail.ActivityID+".json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expired detail stat = %v, want not exist", err)
	}
}

func TestStoreKeepsConcurrentCreatesWithinCapacity(t *testing.T) {
	store := NewStore(t.TempDir())
	const writers = 48
	var wait sync.WaitGroup
	errs := make(chan error, writers)
	for index := 0; index < writers; index++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			_, err := store.Create("codex", testMatches(1), turnreceipt.OutputSummary{})
			errs <- err
		}()
	}
	wait.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent Create: %v", err)
		}
	}
	entries, err := os.ReadDir(store.rootPath())
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != writers {
		t.Fatalf("detail count = %d, want %d", len(entries), writers)
	}
}

func TestStoreEnforcesGlobalCapacity(t *testing.T) {
	store := NewStore(t.TempDir())
	for index := 0; index < maxActivityDetails+12; index++ {
		detail, err := store.Create("codex", []ContextRuleMatch{{
			RuleID:    "rule-" + time.Unix(int64(index), 0).UTC().Format("150405"),
			Name:      "Rule",
			MatchType: "keyword_any",
			Content:   "Follow the rule.",
		}}, turnreceipt.OutputSummary{})
		if err != nil {
			t.Fatalf("Create %d: %v", index, err)
		}
		if _, err := store.Get(detail.ActivityID); err != nil {
			t.Fatalf("new detail %d was evicted: %v", index, err)
		}
	}
	entries, err := os.ReadDir(store.rootPath())
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != maxActivityDetails {
		t.Fatalf("detail count = %d, want %d", len(entries), maxActivityDetails)
	}
}

func TestFailedCreateAtCapacityDoesNotEvictExistingDetail(t *testing.T) {
	store := NewStore(t.TempDir())
	for index := 0; index < maxActivityDetails; index++ {
		if _, err := store.Create("codex", []ContextRuleMatch{{
			RuleID:    "rule-" + time.Unix(int64(index), 0).UTC().Format("150405"),
			Name:      "Rule",
			MatchType: "keyword_any",
			Content:   "Follow the rule.",
		}}, turnreceipt.OutputSummary{}); err != nil {
			t.Fatalf("Create %d: %v", index, err)
		}
	}
	before, err := os.ReadDir(store.rootPath())
	if err != nil {
		t.Fatalf("ReadDir before failed create: %v", err)
	}
	store.newID = func(string) string { return "invalid" }
	if _, err := store.Create("codex", testMatches(1), turnreceipt.OutputSummary{}); err == nil {
		t.Fatal("Create with invalid generated ID succeeded")
	}
	after, err := os.ReadDir(store.rootPath())
	if err != nil {
		t.Fatalf("ReadDir after failed create: %v", err)
	}
	if len(after) != len(before) {
		t.Fatalf("detail count after failed create = %d, want %d", len(after), len(before))
	}
	for index := range before {
		if after[index].Name() != before[index].Name() {
			t.Fatalf("detail %d after failed create = %q, want %q", index, after[index].Name(), before[index].Name())
		}
	}
}

func TestReadEntriesSortsNamesForCleanupCursor(t *testing.T) {
	store := NewStore(t.TempDir())
	if err := os.MkdirAll(store.rootPath(), 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	names := []string{"z.json", "a.json", "m.json"}
	for _, name := range names {
		if err := os.WriteFile(filepath.Join(store.rootPath(), name), []byte("{}"), 0o600); err != nil {
			t.Fatalf("WriteFile %s: %v", name, err)
		}
	}
	entries, err := store.readEntries(len(names))
	if err != nil {
		t.Fatalf("readEntries: %v", err)
	}
	want := []string{"a.json", "m.json", "z.json"}
	for index := range want {
		if entries[index].Name() != want[index] {
			t.Fatalf("entry %d = %q, want %q", index, entries[index].Name(), want[index])
		}
	}
}

func TestStoreBoundsAndEscapesRuleMetadata(t *testing.T) {
	store := NewStore(t.TempDir())
	detail, err := store.Create("codex", []ContextRuleMatch{
		{
			RuleID:    strings.Repeat("标", 100),
			Name:      "\n" + strings.Repeat("<名>", 100) + "\t",
			MatchType: "REGEX",
			Content:   "Review <all> changes.\nPreserve this newline.",
		},
	}, turnreceipt.OutputSummary{})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	match := detail.ContextMatches[0]
	if len(match.RuleID) > 128 || len(match.Name) > 160 {
		t.Fatalf("unbounded match = %#v", match)
	}
	if !utf8.ValidString(match.RuleID) || !utf8.ValidString(match.Name) {
		t.Fatalf("invalid UTF-8 match = %#v", match)
	}
	if strings.ContainsAny(match.Name, "\n\t") {
		t.Fatalf("control characters remained in match = %#v", match)
	}
}

func testMatches(count int) []ContextRuleMatch {
	matches := make([]ContextRuleMatch, 0, count)
	for index := 0; index < count; index++ {
		matches = append(matches, ContextRuleMatch{
			RuleID:    "rule",
			Name:      "Rule",
			MatchType: "keyword_any",
			Priority:  100,
			Content:   "Follow the rule.",
		})
	}
	return matches
}
