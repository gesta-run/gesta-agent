package daemon

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"github.com/gesta-run/gesta-agent/pkg/util"
)

func codexSessionIndexPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".codex", "session_index.jsonl")
}

func codexSessionIndexTitles(path string) map[string]string {
	titles := map[string]string{}
	if path == "" {
		return titles
	}
	file, err := os.Open(path)
	if err != nil {
		return titles
	}
	defer file.Close()

	reader := transcriptReader(file)
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 16*1024), 1024*1024)
	for scanner.Scan() {
		var record struct {
			ID         string `json:"id"`
			ThreadName string `json:"thread_name"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &record); err != nil {
			continue
		}
		id := strings.TrimSpace(record.ID)
		title := titleFromTranscriptText(record.ThreadName)
		if id == "" || title == "" {
			continue
		}
		titles[id] = title
		titles[util.ShortHash(id)] = title
	}
	return titles
}

func codexSessionIndexTitle(row map[string]interface{}, sessionTitles map[string]string) string {
	if len(sessionTitles) == 0 {
		return ""
	}
	sessionID := firstString(row, "session_id", "id")
	if sessionID == "" {
		return ""
	}
	if title := sessionTitles[sessionID]; title != "" {
		return title
	}
	return sessionTitles[util.ShortHash(sessionID)]
}

func transcriptTitle(messages []map[string]interface{}) string {
	for _, message := range messages {
		role := strings.ToLower(strings.TrimSpace(firstString(message, "role")))
		if !strings.Contains(role, "user") && role != "human" {
			continue
		}
		if title := titleFromTranscriptText(firstString(message, "text", "content", "message")); title != "" {
			return title
		}
	}
	for _, message := range messages {
		if title := titleFromTranscriptText(firstString(message, "text", "content", "message")); title != "" {
			return title
		}
	}
	return ""
}

func titleFromTranscriptText(value string) string {
	normalized := strings.Join(strings.Fields(value), " ")
	lower := strings.ToLower(normalized)
	if normalized == "" || strings.HasPrefix(lower, "<environment_context>") || strings.HasPrefix(lower, "<system_context>") {
		return ""
	}
	const maxTitleLength = 120
	runes := []rune(normalized)
	if len(runes) <= maxTitleLength {
		return normalized
	}
	return strings.TrimSpace(string(runes[:maxTitleLength-1])) + "..."
}
