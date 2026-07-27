package toolinput

import "testing"

func configuredClassifier() Classifier {
	return NewClassifier([]string{
		".go", ".css", ".scss", ".sass", ".less", ".pcss", ".styl", ".html", ".ts",
	}, []string{"Dockerfile", "Makefile"})
}

func TestMeasureCodexFileChangesByTargetCategory(t *testing.T) {
	measurements := MeasureCodexFileChanges([]FileChange{
		{Path: "src/app.go", Kind: "update", Diff: "@@\n-old\n+new code\n+second line\n"},
		{Path: "docs/design.md", Kind: "add", Diff: "hello docs\n"},
	}, configuredClassifier())
	if len(measurements) != 2 {
		t.Fatalf("measurements = %#v, want two categories", measurements)
	}
	if got := measurements[0]; got.Category != CategoryCode || got.Counts.Lines != 2 || got.Counts.Characters != 19 {
		t.Fatalf("code measurement = %#v", got)
	}
	if !measurements[0].EfficiencyEligible {
		t.Fatalf("ordinary code measurement must be efficiency eligible: %#v", measurements[0])
	}
	if got := measurements[1]; got.Category != CategoryDocs || got.Counts.Lines != 1 || got.Counts.Characters != 10 {
		t.Fatalf("docs measurement = %#v", got)
	}
}

func TestEfficiencyEligibilityExcludesBulkObservationsWithoutDroppingGrossInk(t *testing.T) {
	tests := []struct {
		name     string
		category string
		counts   Counts
		want     bool
		reason   string
	}{
		{
			name:     "line boundary is eligible",
			category: CategoryCode,
			counts:   Counts{Lines: maxEfficiencyLinesPerObservation},
			want:     true,
		},
		{
			name:     "bulk code is excluded",
			category: CategoryCode,
			counts:   Counts{Lines: maxEfficiencyLinesPerObservation + 1},
			reason:   "observation_line_limit_exceeded",
		},
		{
			name:     "document word boundary is eligible",
			category: CategoryDocs,
			counts:   Counts{Lines: 1, Words: maxEfficiencyDocumentWordsPerObservation},
			want:     true,
		},
		{
			name:     "bulk document is excluded",
			category: CategoryDocs,
			counts:   Counts{Lines: 1, Words: maxEfficiencyDocumentWordsPerObservation + 1},
			reason:   "observation_document_word_limit_exceeded",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			eligible, reason := efficiencyEligibility(test.category, test.counts)
			if eligible != test.want || reason != test.reason {
				t.Fatalf("efficiencyEligibility() = (%t, %q), want (%t, %q)", eligible, reason, test.want, test.reason)
			}
		})
	}
}

func TestMeasureCodexFileChangesClassifiesStylesAsCode(t *testing.T) {
	for _, path := range []string{
		"src/app.css",
		"src/app.scss",
		"src/app.sass",
		"src/app.less",
		"src/app.pcss",
		"src/app.styl",
	} {
		t.Run(path, func(t *testing.T) {
			measurements := MeasureCodexFileChanges([]FileChange{{
				Path: path,
				Kind: "add",
				Diff: ".card { color: red; }\n",
			}}, configuredClassifier())
			if len(measurements) != 1 || measurements[0].Category != CategoryCode {
				t.Fatalf("measurements = %#v, want code", measurements)
			}
		})
	}
}

func TestMeasureClaudeWriteAndEditUseOnlyModelText(t *testing.T) {
	write := MeasureClaudeToolUse("Write", map[string]interface{}{"file_path": "/tmp/readme.md", "content": "hello\nworld\n"}, configuredClassifier())
	if len(write) != 1 || write[0].Category != CategoryDocs || write[0].Counts.Characters != 10 || write[0].Counts.Lines != 2 {
		t.Fatalf("write = %#v", write)
	}
	edit := MeasureClaudeToolUse("Edit", map[string]interface{}{"file_path": "main.go", "old_string": "old text", "new_string": "new"}, configuredClassifier())
	if len(edit) != 1 || edit[0].Counts.Characters != 3 {
		t.Fatalf("edit = %#v", edit)
	}
}

func TestMeasureCodexFileChangesCountContentStartingWithPlus(t *testing.T) {
	measurements := MeasureCodexFileChanges([]FileChange{{Path: "src/app.go", Kind: "update", Diff: "@@\n-value\n+++value\n"}}, configuredClassifier())
	if len(measurements) != 1 {
		t.Fatalf("measurements = %#v, want one", measurements)
	}
	if got := measurements[0].Counts; got.Characters != 7 || got.Lines != 1 {
		t.Fatalf("counts = %#v, want 7 characters and one line", got)
	}
}

func TestMeasureCodexFileChangesSkipsDeletes(t *testing.T) {
	measurements := MeasureCodexFileChanges([]FileChange{{Path: "src/old.go", Kind: "delete", Diff: "removed code\n"}}, configuredClassifier())
	if len(measurements) != 0 {
		t.Fatalf("delete measurements = %#v, want none", measurements)
	}
}

func TestMeasureMCPClassifiesDocumentToolsAndFiltersIdentifiers(t *testing.T) {
	measurements := MeasureCodexMCP("mcp__notion__create_page", map[string]interface{}{
		"parent_id": "123e4567-e89b-12d3-a456-426614174000",
		"url":       "https://example.com/page",
		"server":    "notion",
		"method":    "create_page",
		"title":     "Release plan",
		"content":   "Ship the Gross Ink meter",
		"count":     10,
	}, configuredClassifier())
	if len(measurements) != 1 {
		t.Fatalf("measurements = %#v", measurements)
	}
	got := measurements[0]
	if got.Category != CategoryDocs || got.ToolClass != "mcp" || got.Counts.Words != 7 {
		t.Fatalf("measurement = %#v", got)
	}
}

func TestMeasureMCPClassifiesPathsAndFallsBackToOther(t *testing.T) {
	code := MeasureCodexMCP("mcp__github__create_or_update_file", map[string]interface{}{
		"path":    "src/main.go",
		"content": "package main",
	}, configuredClassifier())
	if len(code) != 1 || code[0].Category != CategoryCode {
		t.Fatalf("code measurement = %#v", code)
	}

	other := MeasureCodexMCP("mcp__custom__submit", map[string]interface{}{
		"content": "unclassified external output",
	}, configuredClassifier())
	if len(other) != 1 || other[0].Category != CategoryOther {
		t.Fatalf("other measurement = %#v", other)
	}

	unconfiguredPath := MeasureCodexMCP("mcp__github__create_or_update_file", map[string]interface{}{
		"path":    "src/main.go",
		"content": "package main",
	}, NewClassifier(nil, nil))
	if len(unconfiguredPath) != 1 || unconfiguredPath[0].Category != CategoryOther {
		t.Fatalf("unconfigured path measurement = %#v", unconfiguredPath)
	}
}

func TestMeasureMCPUsesExactOperationAndArtifactTokens(t *testing.T) {
	tests := []struct {
		name     string
		toolName string
		want     string
	}{
		{name: "document write", toolName: "mcp__notion__create_page", want: CategoryDocs},
		{name: "document read", toolName: "mcp__notion__search", want: CategoryOther},
		{name: "docker is not doc", toolName: "mcp__docker__create_container", want: CategoryOther},
		{name: "code write", toolName: "mcp__custom__write_source", want: CategoryCode},
		{name: "test write", toolName: "mcp__custom__create_test", want: CategoryTests},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			measurements := MeasureCodexMCP(test.toolName, map[string]interface{}{"content": "measured text"}, configuredClassifier())
			if len(measurements) != 1 || measurements[0].Category != test.want {
				t.Fatalf("measurement = %#v, want category %q", measurements, test.want)
			}
		})
	}
}

func TestMeasureIgnoresShellReplAndUnknownTools(t *testing.T) {
	for _, tool := range []string{"functions.exec_command", "Bash", "Node REPL", "update_plan"} {
		if got := MeasureCodexMCP(tool, map[string]interface{}{"cmd": "go test ./...", "text": "not product output"}, configuredClassifier()); len(got) != 0 {
			t.Fatalf("%s measurement = %#v, want none", tool, got)
		}
	}
}

func TestCodexPreToolUseDoesNotMeasureFileWrites(t *testing.T) {
	for _, tool := range []string{"apply_patch", "Write", "Edit", "write_file"} {
		if got := MeasureCodexMCP(tool, map[string]interface{}{"file_path": "main.go", "content": "new code"}, configuredClassifier()); len(got) != 0 {
			t.Fatalf("%s measurement = %#v, want file changes to come from thread/read", tool, got)
		}
	}
}

func TestClassifierUsesOnlyConfiguredCodeRules(t *testing.T) {
	classifier := NewClassifier([]string{".ts", ".html"}, []string{"Dockerfile"})
	for _, path := range []string{"src/app.ts", "public/index.HTML", "Dockerfile", `src\Dockerfile`} {
		if got := classifier.classifyPath(path); got != CategoryCode {
			t.Fatalf("classifyPath(%q) = %q, want code", path, got)
		}
	}
	for _, path := range []string{"src/main.go", "Makefile", "assets/data.csv"} {
		if got := classifier.classifyPath(path); got != CategoryOther {
			t.Fatalf("classifyPath(%q) = %q, want other", path, got)
		}
	}
}

func TestClassifierNormalizesWindowsPathsBeforeTestDetection(t *testing.T) {
	classifier := NewClassifier([]string{".ts"}, nil)

	if got := classifier.classifyPath(`C:\repo\tests\fixture.ts`); got != CategoryTests {
		t.Fatalf("Windows test path category = %q, want %q", got, CategoryTests)
	}
	if got := classifier.classifyPath(`C:\repo\src\fixture.ts`); got != CategoryCode {
		t.Fatalf("Windows source path category = %q, want %q", got, CategoryCode)
	}
}

func TestClassifierPreservesNonCodeCategories(t *testing.T) {
	classifier := NewClassifier([]string{".ts"}, nil)
	tests := map[string]string{
		"docs/readme.md":       CategoryDocs,
		"config/settings.json": CategoryConfig,
		"src/app.test.ts":      CategoryTests,
	}
	for path, want := range tests {
		if got := classifier.classifyPath(path); got != want {
			t.Fatalf("classifyPath(%q) = %q, want %q", path, got, want)
		}
	}
}

func TestClassifierLetsConfiguredCodeOverrideDocumentAndConfigSuffixes(t *testing.T) {
	classifier := NewClassifier([]string{".md", ".json"}, nil)
	for _, path := range []string{"docs/readme.md", "config/settings.json"} {
		if got := classifier.classifyPath(path); got != CategoryCode {
			t.Fatalf("classifyPath(%q) = %q, want code", path, got)
		}
	}
}
