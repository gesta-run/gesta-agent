package toolinput

import (
	"encoding/json"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"
)

const (
	CategoryCode   = "code"
	CategoryTests  = "tests"
	CategoryDocs   = "docs"
	CategoryConfig = "config"
	CategoryOther  = "other"
)

type Counts struct {
	Characters int64
	Lines      int64
	Words      int64
}

type Measurement struct {
	ToolClass string
	Category  string
	Target    string
	Counts    Counts
}

type accumulatorKey struct {
	toolClass string
	category  string
	target    string
}

var (
	uuidPattern    = regexp.MustCompile(`(?i)^[0-9a-f]{8}-[0-9a-f]{4}-[1-5][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
	hexPattern     = regexp.MustCompile(`(?i)^[0-9a-f]{24,}$`)
	base64Pattern  = regexp.MustCompile(`^[A-Za-z0-9+/]+={0,2}$`)
	windowsPath    = regexp.MustCompile(`^[A-Za-z]:[\\/]`)
	identifierKeys = []string{"authorization", "credential", "password", "secret", "token", "cookie", "api_key", "apikey", "cursor", "resource_id", "resourceid", "file_id", "fileid", "uuid", "hash", "url", "uri", "path", "server", "tool", "method", "type"}
)

type FileChange struct {
	Path string
	Kind string
	Diff string
}

func MeasureCodexFileChanges(changes []FileChange) []Measurement {
	acc := map[accumulatorKey]Counts{}
	for _, change := range changes {
		switch change.Kind {
		case "add":
			addText(acc, "file_write", classifyPath(change.Path), change.Path, change.Diff)
		case "update":
			measureUnifiedDiff(acc, change.Path, change.Diff)
		}
	}
	return measurementsFrom(acc)
}

func MeasureCodexMCP(toolName string, input interface{}) []Measurement {
	name := normalizeToolName(toolName)
	if !isMCPTool(name) {
		return nil
	}
	acc := map[accumulatorKey]Counts{}
	measureMCPInput(acc, name, input)
	return measurementsFrom(acc)
}

func MeasureClaudeToolUse(toolName string, input interface{}) []Measurement {
	name := normalizeToolName(toolName)
	values, _ := input.(map[string]interface{})
	acc := map[accumulatorKey]Counts{}

	switch name {
	case "apply_patch", "applypatch":
		patch := inputString(input)
		if patch == "" {
			patch = firstRawString(values, "patch", "command", "input")
		}
		measurePatch(acc, patch)
	case "write", "write_file", "writefile":
		path := firstRawString(values, "file_path", "path")
		addText(acc, "file_write", classifyPath(path), path, firstRawString(values, "content"))
	case "edit", "edit_file", "editfile":
		path := firstRawString(values, "file_path", "path")
		addText(acc, "file_write", classifyPath(path), path, firstRawString(values, "new_string", "replacement"))
	case "multiedit", "multi_edit":
		measureMultiEdit(acc, values)
	case "notebookedit", "notebook_edit":
		path := firstRawString(values, "notebook_path", "file_path", "path")
		addText(acc, "file_write", classifyPath(path), path, firstRawString(values, "new_source", "source", "content", "new_string"))
	default:
		if isMCPTool(name) {
			measureMCPInput(acc, name, input)
		}
	}

	return measurementsFrom(acc)
}

func measureUnifiedDiff(acc map[accumulatorKey]Counts, path, diff string) {
	path = strings.TrimSpace(path)
	if path == "" || strings.TrimSpace(diff) == "" {
		return
	}
	for _, line := range strings.Split(strings.ReplaceAll(diff, "\r\n", "\n"), "\n") {
		if !strings.HasPrefix(line, "+") || strings.HasPrefix(line, "+++ ") {
			continue
		}
		addLine(acc, "file_write", classifyPath(path), path, strings.TrimPrefix(line, "+"))
	}
}

func normalizeToolName(value string) string {
	name := strings.ToLower(strings.TrimSpace(value))
	name = strings.TrimPrefix(name, "functions.")
	return name
}

func isMCPTool(name string) bool {
	return strings.HasPrefix(name, "mcp__") || strings.HasPrefix(name, "mcp.") || strings.HasPrefix(name, "mcp_")
}

func inputString(input interface{}) string {
	switch value := input.(type) {
	case string:
		return value
	case json.RawMessage:
		var text string
		if json.Unmarshal(value, &text) == nil {
			return text
		}
	}
	return ""
}

func measureMultiEdit(acc map[accumulatorKey]Counts, values map[string]interface{}) {
	path := firstRawString(values, "file_path", "path")
	edits, _ := values["edits"].([]interface{})
	for _, raw := range edits {
		edit, _ := raw.(map[string]interface{})
		editPath := firstRawString(edit, "file_path", "path")
		if editPath == "" {
			editPath = path
		}
		addText(acc, "file_write", classifyPath(editPath), editPath, firstRawString(edit, "new_string", "replacement"))
	}
}

func measurePatch(acc map[accumulatorKey]Counts, patch string) {
	if strings.TrimSpace(patch) == "" {
		return
	}
	path := ""
	for _, line := range strings.Split(strings.ReplaceAll(patch, "\r\n", "\n"), "\n") {
		switch {
		case strings.HasPrefix(line, "*** Add File: "):
			path = strings.TrimSpace(strings.TrimPrefix(line, "*** Add File: "))
			continue
		case strings.HasPrefix(line, "*** Update File: "):
			path = strings.TrimSpace(strings.TrimPrefix(line, "*** Update File: "))
			continue
		case strings.HasPrefix(line, "*** Delete File: "):
			path = strings.TrimSpace(strings.TrimPrefix(line, "*** Delete File: "))
			continue
		case strings.HasPrefix(line, "+++ "):
			path = strings.TrimSpace(strings.TrimPrefix(line, "+++ "))
			path = strings.TrimPrefix(path, "b/")
			continue
		}
		if !strings.HasPrefix(line, "+") || path == "" {
			continue
		}
		addLine(acc, "file_write", classifyPath(path), path, strings.TrimPrefix(line, "+"))
	}
}

func measureMCPInput(acc map[accumulatorKey]Counts, toolName string, input interface{}) {
	category := classifyMCPInput(toolName, input)
	var walk func(string, interface{})
	walk = func(key string, value interface{}) {
		switch typed := value.(type) {
		case map[string]interface{}:
			keys := make([]string, 0, len(typed))
			for childKey := range typed {
				keys = append(keys, childKey)
			}
			sort.Strings(keys)
			for _, childKey := range keys {
				walk(childKey, typed[childKey])
			}
		case []interface{}:
			for _, item := range typed {
				walk(key, item)
			}
		case string:
			if shouldCountMCPText(key, typed) {
				addText(acc, "mcp", category, "", typed)
			}
		}
	}
	walk("", input)
}

func classifyMCPInput(toolName string, input interface{}) string {
	pathCategories := map[string]struct{}{}
	var inspect func(string, interface{})
	inspect = func(key string, value interface{}) {
		switch typed := value.(type) {
		case map[string]interface{}:
			for childKey, childValue := range typed {
				inspect(childKey, childValue)
			}
		case []interface{}:
			for _, item := range typed {
				inspect(key, item)
			}
		case string:
			lowerKey := strings.ToLower(strings.TrimSpace(key))
			if lowerKey == "path" ||
				lowerKey == "file" ||
				lowerKey == "filename" ||
				strings.HasSuffix(lowerKey, "_path") ||
				strings.HasSuffix(lowerKey, "_file") ||
				strings.HasSuffix(lowerKey, "_filename") {
				if category := classifyPath(typed); category != CategoryOther {
					pathCategories[category] = struct{}{}
				}
			}
		}
	}
	inspect("", input)
	if len(pathCategories) == 1 {
		for category := range pathCategories {
			return category
		}
	}
	if len(pathCategories) > 1 {
		return CategoryOther
	}

	tokens := toolNameTokens(toolName)
	if !containsAnyToken(tokens, "append", "create", "insert", "publish", "send", "update", "write") {
		return CategoryOther
	}
	if containsAnyToken(tokens, "article", "doc", "document", "markdown", "note", "page", "wiki") {
		return CategoryDocs
	}
	if containsAnyToken(tokens, "spec", "test", "tests") {
		return CategoryTests
	}
	if containsAnyToken(tokens, "code", "file", "function", "script", "source") {
		return CategoryCode
	}
	return CategoryOther
}

func toolNameTokens(toolName string) map[string]struct{} {
	tokens := map[string]struct{}{}
	for _, token := range strings.FieldsFunc(strings.ToLower(strings.TrimSpace(toolName)), func(r rune) bool {
		return !((r >= 'a' && r <= 'z') || (r >= '0' && r <= '9'))
	}) {
		if token != "" {
			tokens[token] = struct{}{}
		}
	}
	return tokens
}

func containsAnyToken(tokens map[string]struct{}, candidates ...string) bool {
	for _, candidate := range candidates {
		if _, ok := tokens[candidate]; ok {
			return true
		}
	}
	return false
}

func shouldCountMCPText(key, value string) bool {
	text := strings.TrimSpace(value)
	if text == "" {
		return false
	}
	lowerKey := strings.ToLower(strings.TrimSpace(key))
	for _, fragment := range identifierKeys {
		if lowerKey == fragment || strings.HasSuffix(lowerKey, "_"+fragment) {
			return false
		}
	}
	if strings.HasPrefix(text, "http://") || strings.HasPrefix(text, "https://") || strings.HasPrefix(text, "/") || strings.HasPrefix(text, "./") || strings.HasPrefix(text, "../") || strings.HasPrefix(text, "~/") || windowsPath.MatchString(text) {
		return false
	}
	if uuidPattern.MatchString(text) || hexPattern.MatchString(text) {
		return false
	}
	if len(text) >= 80 && len(text)%4 == 0 && base64Pattern.MatchString(text) {
		return false
	}
	return true
}

func firstRawString(values map[string]interface{}, keys ...string) string {
	for _, key := range keys {
		if value, ok := values[key].(string); ok {
			return value
		}
	}
	return ""
}

func classifyPath(path string) string {
	lower := strings.ToLower(strings.TrimSpace(path))
	if lower == "" {
		return CategoryOther
	}
	if strings.Contains(lower, "/tests/") || strings.Contains(lower, "/__tests__/") || strings.HasPrefix(lower, "tests/") || strings.HasPrefix(lower, "__tests__/") || strings.Contains(lower, ".test.") || strings.Contains(lower, ".spec.") || strings.HasSuffix(lower, "_test.go") {
		return CategoryTests
	}
	for _, suffix := range []string{".md", ".mdx", ".txt", ".rst", ".adoc"} {
		if strings.HasSuffix(lower, suffix) {
			return CategoryDocs
		}
	}
	for _, suffix := range []string{".json", ".yaml", ".yml", ".toml", ".ini", ".conf", ".env"} {
		if strings.HasSuffix(lower, suffix) {
			return CategoryConfig
		}
	}
	for _, suffix := range []string{
		".go", ".ts", ".tsx", ".js", ".jsx", ".py", ".rs", ".java", ".kt", ".kts",
		".c", ".cc", ".cpp", ".h", ".hpp", ".cs", ".rb", ".php", ".swift", ".scala",
		".sh", ".sql", ".css", ".scss", ".sass", ".less", ".pcss", ".styl",
	} {
		if strings.HasSuffix(lower, suffix) {
			return CategoryCode
		}
	}
	return CategoryOther
}

func addText(acc map[accumulatorKey]Counts, toolClass, category, target, text string) {
	counts := countText(text)
	if counts.Characters == 0 && counts.Lines == 0 && counts.Words == 0 {
		return
	}
	addCounts(acc, accumulatorKey{toolClass: toolClass, category: category, target: target}, counts)
}

func addLine(acc map[accumulatorKey]Counts, toolClass, category, target, text string) {
	counts := countText(text)
	counts.Lines = 1
	addCounts(acc, accumulatorKey{toolClass: toolClass, category: category, target: target}, counts)
}

func addCounts(acc map[accumulatorKey]Counts, key accumulatorKey, counts Counts) {
	current := acc[key]
	current.Characters += counts.Characters
	current.Lines += counts.Lines
	current.Words += counts.Words
	acc[key] = current
}

func countText(text string) Counts {
	if text == "" {
		return Counts{}
	}
	characters := int64(utf8.RuneCountInString(strings.ReplaceAll(strings.ReplaceAll(text, "\r", ""), "\n", "")))
	lines := int64(strings.Count(strings.ReplaceAll(text, "\r\n", "\n"), "\n"))
	if !strings.HasSuffix(text, "\n") {
		lines++
	}
	return Counts{Characters: characters, Lines: lines, Words: int64(len(strings.Fields(text)))}
}

func measurementsFrom(acc map[accumulatorKey]Counts) []Measurement {
	keys := make([]accumulatorKey, 0, len(acc))
	for key := range acc {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].category != keys[j].category {
			return keys[i].category < keys[j].category
		}
		if keys[i].target != keys[j].target {
			return keys[i].target < keys[j].target
		}
		return keys[i].toolClass < keys[j].toolClass
	})
	measurements := make([]Measurement, 0, len(keys))
	for _, key := range keys {
		counts := acc[key]
		if counts.Characters == 0 && counts.Lines == 0 && counts.Words == 0 {
			continue
		}
		measurements = append(measurements, Measurement{ToolClass: key.toolClass, Category: key.category, Target: key.target, Counts: counts})
	}
	return measurements
}
