package daemon

import (
	"bufio"
	"encoding/json"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gesta-run/gesta-agent/pkg/mcpmeta"
)

type claudeTranscriptRecord struct {
	Type      string `json:"type"`
	SessionID string `json:"sessionId"`
	CWD       string `json:"cwd"`
	GitBranch string `json:"gitBranch"`
	Timestamp string `json:"timestamp"`
	Message   struct {
		ID         string `json:"id"`
		Model      string `json:"model"`
		StopReason string `json:"stop_reason"`
		Usage      struct {
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

	accumulator := newClaudeTranscriptAccumulator()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, claudeScannerInitialBytes), claudeScannerMaxBytes)
	for scanner.Scan() {
		var record claudeTranscriptRecord
		if err := json.Unmarshal(scanner.Bytes(), &record); err != nil {
			continue
		}
		accumulator.add(record)
	}
	if err := scanner.Err(); err != nil {
		return claudeSessionUsage{}, false
	}
	return accumulator.result()
}

type claudeTranscriptAccumulator struct {
	session                    claudeSessionUsage
	modelsSeen                 map[string]struct{}
	seenMessageIDs             map[string]struct{}
	seenMCPToolCalls           map[string]struct{}
	transcriptCandidateIndexes map[string]int
	transcriptCandidates       []claudeTranscriptCandidate
	firstUserText              string
}

func newClaudeTranscriptAccumulator() *claudeTranscriptAccumulator {
	return &claudeTranscriptAccumulator{
		session:                    claudeSessionUsage{ByModelDay: map[claudeModelDayKey]claudeAssistantUsage{}},
		modelsSeen:                 map[string]struct{}{},
		seenMessageIDs:             map[string]struct{}{},
		seenMCPToolCalls:           map[string]struct{}{},
		transcriptCandidateIndexes: map[string]int{},
	}
}

func (a *claudeTranscriptAccumulator) add(record claudeTranscriptRecord) {
	if a.session.SessionID == "" {
		a.session.SessionID = record.SessionID
	}
	if a.session.CWD == "" {
		a.session.CWD = record.CWD
	}
	if a.session.GitBranch == "" {
		a.session.GitBranch = record.GitBranch
	}
	role := strings.ToLower(strings.TrimSpace(record.Type))
	if role == "user" || role == "assistant" {
		a.addContent(record, role)
	}
	if role == "user" && a.firstUserText == "" {
		if text := claudeTitleFromContent(record.Message.Content); text != "" && !isCodexNonChatText(text) {
			a.firstUserText = text
		}
	}
	if role == "assistant" {
		a.addAssistantUsage(record)
	}
}

func (a *claudeTranscriptAccumulator) addContent(record claudeTranscriptRecord, role string) {
	modelName := strings.TrimSpace(record.Message.Model)
	text := claudeTranscriptContentText(record.Message.Content)
	addClaudeTranscriptCandidate(
		&a.transcriptCandidates,
		a.transcriptCandidateIndexes,
		role,
		text,
		record.Timestamp,
		modelName,
		record.Message.ID,
		claudeTranscriptSummaryPhase(record.Message.StopReason),
	)
	if role == "user" && a.firstUserText == "" && !isCodexNonChatText(text) {
		a.firstUserText = titleFromTranscriptText(text)
	}
	if role != "assistant" {
		return
	}
	for _, call := range claudeMCPToolCallsFromContent(record.Message.Content, record.Timestamp) {
		key := claudeMCPToolCallKey(call)
		if _, duplicate := a.seenMCPToolCalls[key]; duplicate {
			continue
		}
		a.seenMCPToolCalls[key] = struct{}{}
		a.session.MCPToolCalls = append(a.session.MCPToolCalls, call)
	}
}

func (a *claudeTranscriptAccumulator) addAssistantUsage(record claudeTranscriptRecord) {
	modelName := strings.TrimSpace(record.Message.Model)
	if modelName == "" || modelName == claudeSyntheticModel {
		return
	}
	if messageID := strings.TrimSpace(record.Message.ID); messageID != "" {
		if _, duplicate := a.seenMessageIDs[messageID]; duplicate {
			return
		}
		a.seenMessageIDs[messageID] = struct{}{}
	}
	observedAt, hasTime := parseClaudeTimestamp(record.Timestamp)
	if hasTime {
		if a.session.FirstEventAt.IsZero() || observedAt.Before(a.session.FirstEventAt) {
			a.session.FirstEventAt = observedAt
		}
		if observedAt.After(a.session.LastEventAt) {
			a.session.LastEventAt = observedAt
		}
	}
	a.session.AssistantEvents++
	a.modelsSeen[modelName] = struct{}{}
	a.addUsage(record, modelName, observedAt, hasTime)
}

func (a *claudeTranscriptAccumulator) addUsage(
	record claudeTranscriptRecord,
	modelName string,
	observedAt time.Time,
	hasTime bool,
) {
	usage := claudeAssistantUsage{
		InputTokens:         record.Message.Usage.InputTokens,
		OutputTokens:        record.Message.Usage.OutputTokens,
		CacheCreationTokens: record.Message.Usage.CacheCreationInputTokens,
		CacheReadTokens:     record.Message.Usage.CacheReadInputTokens,
	}
	if usage.isZero() {
		return
	}
	day := ""
	if hasTime {
		day = observedAt.Format("2006-01-02")
	}
	key := claudeModelDayKey{Model: modelName, Day: day}
	a.session.ByModelDay[key] = a.session.ByModelDay[key].add(usage)
	a.session.Total = a.session.Total.add(usage)
}

func (a *claudeTranscriptAccumulator) result() (claudeSessionUsage, bool) {
	a.session.Title = a.firstUserText
	a.session.Messages, a.session.TranscriptTruncated = claudeTranscriptMessagesFromCandidates(a.transcriptCandidates)
	session := a.session
	if session.SessionID == "" || (session.AssistantEvents == 0 && len(session.Messages) == 0) {
		return claudeSessionUsage{}, false
	}
	for name := range a.modelsSeen {
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

func claudeMCPToolCallsFromContent(value interface{}, timestamp string) []claudeTranscriptToolCall {
	var calls []claudeTranscriptToolCall
	blockIndex := 0
	var visit func(interface{})
	visit = func(current interface{}) {
		switch typed := current.(type) {
		case []interface{}:
			for _, item := range typed {
				visit(item)
			}
		case map[string]interface{}:
			currentIndex := blockIndex
			blockIndex++
			blockType := strings.ToLower(strings.TrimSpace(firstString(typed, "type")))
			if blockType == "tool_use" {
				name := strings.TrimSpace(firstString(typed, "name"))
				server, tool := mcpmeta.ToolParts(name)
				if server == "" || tool == "" {
					return
				}
				calls = append(calls, claudeTranscriptToolCall{
					Name:       name,
					CallID:     strings.TrimSpace(firstString(typed, "id", "call_id")),
					Timestamp:  strings.TrimSpace(timestamp),
					MCPServer:  server,
					MCPTool:    tool,
					BlockIndex: currentIndex,
				})
				return
			}
			if content, ok := typed["content"]; ok {
				visit(content)
			}
		}
	}
	visit(value)
	return calls
}

func claudeMCPToolCallKey(call claudeTranscriptToolCall) string {
	if callID := strings.TrimSpace(call.CallID); callID != "" {
		return "id\x00" + callID
	}
	return strings.Join([]string{
		"metadata",
		call.Timestamp,
		call.Name,
		strconv.Itoa(call.BlockIndex),
	}, "\x00")
}
