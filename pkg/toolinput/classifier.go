package toolinput

import (
	pathpkg "path"
	"strings"
)

type Classifier struct {
	codeSuffixes  []string
	codeFilenames map[string]struct{}
}

func NewClassifier(codeSuffixes, codeFilenames []string) Classifier {
	classifier := Classifier{
		codeSuffixes:  make([]string, 0, len(codeSuffixes)),
		codeFilenames: make(map[string]struct{}, len(codeFilenames)),
	}
	seenSuffixes := make(map[string]struct{}, len(codeSuffixes))
	for _, value := range codeSuffixes {
		suffix := strings.ToLower(strings.TrimSpace(value))
		if suffix == "" || suffix == "." || !strings.HasPrefix(suffix, ".") || strings.ContainsAny(suffix, `/\`) {
			continue
		}
		if _, ok := seenSuffixes[suffix]; ok {
			continue
		}
		seenSuffixes[suffix] = struct{}{}
		classifier.codeSuffixes = append(classifier.codeSuffixes, suffix)
	}
	for _, value := range codeFilenames {
		filename := strings.TrimSpace(value)
		if filename == "" || filename == "." || filename == ".." || strings.ContainsAny(filename, `/\`) {
			continue
		}
		classifier.codeFilenames[strings.ToLower(filename)] = struct{}{}
	}
	return classifier
}

func (c Classifier) classifyPath(path string) string {
	lower := strings.ToLower(strings.TrimSpace(path))
	if lower == "" {
		return CategoryOther
	}
	normalizedPath := strings.ReplaceAll(lower, `\`, "/")
	if strings.Contains(normalizedPath, "/tests/") || strings.Contains(normalizedPath, "/__tests__/") || strings.HasPrefix(normalizedPath, "tests/") || strings.HasPrefix(normalizedPath, "__tests__/") || strings.Contains(normalizedPath, ".test.") || strings.Contains(normalizedPath, ".spec.") || strings.HasSuffix(normalizedPath, "_test.go") {
		return CategoryTests
	}
	if _, ok := c.codeFilenames[pathpkg.Base(normalizedPath)]; ok {
		return CategoryCode
	}
	for _, suffix := range c.codeSuffixes {
		if strings.HasSuffix(normalizedPath, suffix) {
			return CategoryCode
		}
	}
	for _, suffix := range []string{".md", ".mdx", ".txt", ".rst", ".adoc"} {
		if strings.HasSuffix(normalizedPath, suffix) {
			return CategoryDocs
		}
	}
	for _, suffix := range []string{".json", ".yaml", ".yml", ".toml", ".ini", ".conf", ".env"} {
		if strings.HasSuffix(normalizedPath, suffix) {
			return CategoryConfig
		}
	}
	return CategoryOther
}
