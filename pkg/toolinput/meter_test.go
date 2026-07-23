package toolinput

import "testing"

func TestMeasureCodexFileChangesByTargetCategory(t *testing.T) {
	measurements := MeasureCodexFileChanges([]FileChange{
		{Path: "src/app.go", Kind: "update", Diff: "@@\n-old\n+new code\n+second line\n"},
		{Path: "docs/design.md", Kind: "add", Diff: "hello docs\n"},
	})
	if len(measurements) != 2 {
		t.Fatalf("measurements = %#v, want two categories", measurements)
	}
	if got := measurements[0]; got.Category != CategoryCode || got.Counts.Lines != 2 || got.Counts.Characters != 19 {
		t.Fatalf("code measurement = %#v", got)
	}
	if got := measurements[1]; got.Category != CategoryDocs || got.Counts.Lines != 1 || got.Counts.Characters != 10 {
		t.Fatalf("docs measurement = %#v", got)
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
			}})
			if len(measurements) != 1 || measurements[0].Category != CategoryCode {
				t.Fatalf("measurements = %#v, want code", measurements)
			}
		})
	}
}

func TestMeasureClaudeWriteAndEditUseOnlyModelText(t *testing.T) {
	write := MeasureClaudeToolUse("Write", map[string]interface{}{"file_path": "/tmp/readme.md", "content": "hello\nworld\n"})
	if len(write) != 1 || write[0].Category != CategoryDocs || write[0].Counts.Characters != 10 || write[0].Counts.Lines != 2 {
		t.Fatalf("write = %#v", write)
	}
	edit := MeasureClaudeToolUse("Edit", map[string]interface{}{"file_path": "main.go", "old_string": "old text", "new_string": "new"})
	if len(edit) != 1 || edit[0].Counts.Characters != 3 {
		t.Fatalf("edit = %#v", edit)
	}
}

func TestMeasureCodexFileChangesCountContentStartingWithPlus(t *testing.T) {
	measurements := MeasureCodexFileChanges([]FileChange{{Path: "src/app.go", Kind: "update", Diff: "@@\n-value\n+++value\n"}})
	if len(measurements) != 1 {
		t.Fatalf("measurements = %#v, want one", measurements)
	}
	if got := measurements[0].Counts; got.Characters != 7 || got.Lines != 1 {
		t.Fatalf("counts = %#v, want 7 characters and one line", got)
	}
}

func TestMeasureCodexFileChangesSkipsDeletes(t *testing.T) {
	measurements := MeasureCodexFileChanges([]FileChange{{Path: "src/old.go", Kind: "delete", Diff: "removed code\n"}})
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
	})
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
	})
	if len(code) != 1 || code[0].Category != CategoryCode {
		t.Fatalf("code measurement = %#v", code)
	}

	other := MeasureCodexMCP("mcp__custom__submit", map[string]interface{}{
		"content": "unclassified external output",
	})
	if len(other) != 1 || other[0].Category != CategoryOther {
		t.Fatalf("other measurement = %#v", other)
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
			measurements := MeasureCodexMCP(test.toolName, map[string]interface{}{"content": "measured text"})
			if len(measurements) != 1 || measurements[0].Category != test.want {
				t.Fatalf("measurement = %#v, want category %q", measurements, test.want)
			}
		})
	}
}

func TestMeasureIgnoresShellReplAndUnknownTools(t *testing.T) {
	for _, tool := range []string{"functions.exec_command", "Bash", "Node REPL", "update_plan"} {
		if got := MeasureCodexMCP(tool, map[string]interface{}{"cmd": "go test ./...", "text": "not product output"}); len(got) != 0 {
			t.Fatalf("%s measurement = %#v, want none", tool, got)
		}
	}
}

func TestCodexPreToolUseDoesNotMeasureFileWrites(t *testing.T) {
	for _, tool := range []string{"apply_patch", "Write", "Edit", "write_file"} {
		if got := MeasureCodexMCP(tool, map[string]interface{}{"file_path": "main.go", "content": "new code"}); len(got) != 0 {
			t.Fatalf("%s measurement = %#v, want file changes to come from thread/read", tool, got)
		}
	}
}
