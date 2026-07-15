package daemon

import (
	"bufio"
	"encoding/json"
	"os"
	"sort"
	"strings"
	"time"
)

type claudeTranscriptRecord struct {
	Type      string `json:"type"`
	SessionID string `json:"sessionId"`
	CWD       string `json:"cwd"`
	GitBranch string `json:"gitBranch"`
	Timestamp string `json:"timestamp"`
	Message   struct {
		ID    string `json:"id"`
		Model string `json:"model"`
		Usage struct {
			InputTokens              int64 `json:"input_tokens"`
			OutputTokens             int64 `json:"output_tokens"`
			CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
			CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
		} `json:"usage"`
		Content interface{} `json:"content"`
	} `json:"message"`
}

// parseClaudeTranscript reads a single Claude Code transcript file and aggregates
// real assistant usage while preserving readable chat transcripts. Synthetic /
// non-LLM assistant messages (model "<synthetic>") are kept as transcript text
// when readable, but they do not create usage buckets. Some providers currently
// emit real assistant messages with zero usage; those messages are also indexed
// as transcript content without producing usage buckets.
func parseClaudeTranscript(path string) (claudeSessionUsage, bool) {
	file, err := os.Open(path)
	if err != nil {
		return claudeSessionUsage{}, false
	}
	defer file.Close()

	session := claudeSessionUsage{
		ByModelDay: map[claudeModelDayKey]claudeAssistantUsage{},
	}
	modelsSeen := map[string]struct{}{}
	// Claude Code emits one LLM turn as multiple JSONL records that share the
	// same message.id, each carrying the SAME cumulative usage for that turn.
	// Count each distinct message.id exactly once so per-session totals (and
	// AssistantEvents / event_count) reflect real turns, not streaming chunks.
	seenMessageIDs := map[string]struct{}{}
	transcriptCandidateIndexes := map[string]int{}
	var transcriptCandidates []claudeTranscriptCandidate
	var firstUserText string

	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, claudeScannerInitialBytes), claudeScannerMaxBytes)
	for scanner.Scan() {
		line := scanner.Bytes()
		var record claudeTranscriptRecord
		if err := json.Unmarshal(line, &record); err != nil {
			continue
		}
		if session.SessionID == "" && record.SessionID != "" {
			session.SessionID = record.SessionID
		}
		if session.CWD == "" && record.CWD != "" {
			session.CWD = record.CWD
		}
		if session.GitBranch == "" && record.GitBranch != "" {
			session.GitBranch = record.GitBranch
		}
		role := strings.ToLower(strings.TrimSpace(record.Type))
		if role == "user" || role == "assistant" {
			modelName := strings.TrimSpace(record.Message.Model)
			text := claudeTranscriptContentText(record.Message.Content)
			addClaudeTranscriptCandidate(&transcriptCandidates, transcriptCandidateIndexes, role, text, record.Timestamp, modelName, record.Message.ID)
			if role == "user" && firstUserText == "" && !isCodexNonChatText(text) {
				if title := titleFromTranscriptText(text); title != "" {
					firstUserText = title
				}
			}
		}
		if record.Type == "user" && firstUserText == "" {
			if text := claudeTitleFromContent(record.Message.Content); text != "" && !isCodexNonChatText(text) {
				firstUserText = text
			}
		}
		if record.Type != "assistant" {
			continue
		}
		modelName := strings.TrimSpace(record.Message.Model)
		if modelName == "" || modelName == claudeSyntheticModel {
			continue
		}
		// De-duplicate by message.id: every record sharing one id repeats the
		// same cumulative turn usage, so counting them all overcounts (~2.5x on
		// real transcripts). Records without an id fall back to per-record
		// counting (each is treated as its own turn).
		if messageID := strings.TrimSpace(record.Message.ID); messageID != "" {
			if _, dup := seenMessageIDs[messageID]; dup {
				continue
			}
			seenMessageIDs[messageID] = struct{}{}
		}
		observedAt, hasTime := parseClaudeTimestamp(record.Timestamp)
		if hasTime {
			if session.FirstEventAt.IsZero() || observedAt.Before(session.FirstEventAt) {
				session.FirstEventAt = observedAt
			}
			if observedAt.After(session.LastEventAt) {
				session.LastEventAt = observedAt
			}
		}
		session.AssistantEvents++
		modelsSeen[modelName] = struct{}{}

		usage := claudeAssistantUsage{
			InputTokens:         record.Message.Usage.InputTokens,
			OutputTokens:        record.Message.Usage.OutputTokens,
			CacheCreationTokens: record.Message.Usage.CacheCreationInputTokens,
			CacheReadTokens:     record.Message.Usage.CacheReadInputTokens,
		}
		if usage.isZero() {
			continue
		}
		// Real Claude transcripts always carry an RFC3339 timestamp. If one is
		// missing/unparseable we deliberately leave the day empty: the usage-delta
		// machinery then attributes the tokens to the daemon's observation (work)
		// day rather than dropping them, so no usage is silently lost.
		day := ""
		if hasTime {
			day = observedAt.Format("2006-01-02")
		}
		key := claudeModelDayKey{Model: modelName, Day: day}
		session.ByModelDay[key] = session.ByModelDay[key].add(usage)
		session.Total = session.Total.add(usage)
	}
	if err := scanner.Err(); err != nil {
		return claudeSessionUsage{}, false
	}
	session.Title = firstUserText
	session.Messages, session.TranscriptTruncated = claudeTranscriptMessagesFromCandidates(transcriptCandidates)
	if session.SessionID == "" || (session.AssistantEvents == 0 && len(session.Messages) == 0) {
		return claudeSessionUsage{}, false
	}
	for name := range modelsSeen {
		session.Models = append(session.Models, name)
	}
	sort.Strings(session.Models)
	return session, true
}

func parseClaudeTimestamp(value string) (time.Time, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}, false
	}
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}, false
	}
	return parsed.UTC(), true
}

func claudeTitleFromContent(value interface{}) string {
	switch typed := value.(type) {
	case string:
		return titleFromTranscriptText(typed)
	case []interface{}:
		for _, item := range typed {
			block, ok := item.(map[string]interface{})
			if !ok {
				continue
			}
			if blockType, _ := block["type"].(string); blockType != "" && blockType != "text" {
				continue
			}
			if text, ok := block["text"].(string); ok {
				if title := titleFromTranscriptText(text); title != "" {
					return title
				}
			}
		}
	}
	return ""
}

func claudeTranscriptContentText(value interface{}) string {
	switch typed := value.(type) {
	case string:
		return typed
	case []interface{}:
		var parts []string
		for _, item := range typed {
			switch block := item.(type) {
			case string:
				if strings.TrimSpace(block) != "" {
					parts = append(parts, block)
				}
			case map[string]interface{}:
				blockType := strings.ToLower(strings.TrimSpace(firstString(block, "type")))
				if blockType != "" && blockType != "text" {
					continue
				}
				if text := claudeTranscriptContentText(block["text"]); text != "" {
					parts = append(parts, text)
					continue
				}
				if text := claudeTranscriptContentText(block["content"]); text != "" {
					parts = append(parts, text)
					continue
				}
				if text := claudeTranscriptContentText(block["message"]); text != "" {
					parts = append(parts, text)
				}
			}
		}
		return strings.Join(parts, "\n")
	case map[string]interface{}:
		blockType := strings.ToLower(strings.TrimSpace(firstString(typed, "type")))
		if blockType != "" && blockType != "text" {
			return ""
		}
		for _, key := range []string{"text", "content", "message", "input", "output"} {
			if text := claudeTranscriptContentText(typed[key]); text != "" {
				return text
			}
		}
	}
	return ""
}
