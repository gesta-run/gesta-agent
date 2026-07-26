package daemon

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gesta-run/gesta-agent/pkg/model"
	"github.com/gesta-run/gesta-agent/pkg/privacy"
	"github.com/gesta-run/gesta-agent/pkg/util"
)

type codexSensitiveTranscriptMessage struct {
	Text      string
	Timestamp string
}

type codexTranscriptToolCall struct {
	Name      string
	CallID    string
	Timestamp string
	MCPServer string
	MCPTool   string
}

func codexTranscriptPayload(row map[string]interface{}, usagePayload map[string]interface{}, sessionTitles map[string]string) map[string]interface{} {
	rolloutPath := firstString(row, "rollout_path")
	sessionID := firstString(usagePayload, "session_id", "session_id_hash")
	if rolloutPath == "" || sessionID == "" {
		return nil
	}
	messages, truncated, err := readCodexTranscript(rolloutPath)
	if err != nil || len(messages) == 0 {
		return nil
	}
	payload := map[string]interface{}{
		"agent_type":                      "codex",
		"metadata_only":                   false,
		"session_id":                      sessionID,
		"session_id_hash":                 sessionID,
		"session_id_is_hashed":            true,
		"messages":                        messages,
		"message_count":                   len(messages),
		"transcript_source":               "codex_rollout_jsonl",
		"transcript_path_hash":            util.ShortHash(rolloutPath),
		"transcript_truncated":            truncated,
		"transcript_max_messages":         codexMaxTranscriptMessages,
		"transcript_max_message_bytes":    codexMaxTranscriptMessageBytes,
		"transcript_max_total_text_bytes": codexMaxTranscriptTotalBytes,
		"_rollout_path":                   rolloutPath,
	}
	copyStringField(payload, usagePayload, "parent_session_id")
	copyStringField(payload, usagePayload, "parent_session_id_hash")
	if hashed, ok := usagePayload["parent_session_id_is_hashed"].(bool); ok {
		payload["parent_session_id_is_hashed"] = hashed
	}
	copyStringField(payload, row, "model_provider")
	copyStringField(payload, row, "model")
	copyStringField(payload, row, "title")
	copyStringField(payload, row, "session_title")
	copyStringField(payload, row, "conversation_title")
	copyStringField(payload, row, "thread_title")
	if title := codexSessionIndexTitle(row, sessionTitles); title != "" {
		payload["title"] = title
		payload["title_source"] = "codex_session_index"
	}
	if cwd := firstString(row, "cwd", "repo_root", "worktree"); cwd != "" {
		payload["_cwd"] = cwd
		payload["repo_path_hash"] = util.ShortHash(cwd)
	}
	copyStringField(payload, row, "git_branch")
	copyStringField(payload, row, "git_sha")
	copyCodexTimeField(payload, row, "created_at")
	copyCodexTimeField(payload, row, "updated_at")
	copyStringField(payload, usagePayload, "repo")
	if title := firstString(payload, "title", "session_title", "conversation_title", "thread_title"); title == "" {
		if title := transcriptTitle(messages); title != "" {
			payload["title"] = title
		}
	}
	data, _ := json.Marshal(messages)
	payload["transcript_hash"] = util.HashString(string(data))
	return payload
}

func codexPublicTranscriptPayload(payload map[string]interface{}) map[string]interface{} {
	if payload == nil {
		return nil
	}
	out := make(map[string]interface{}, len(payload))
	for key, value := range payload {
		if strings.HasPrefix(key, "_") {
			continue
		}
		out[key] = value
	}
	return out
}

func codexToolCallEventsFromTranscript(cfg Config, transcript map[string]interface{}) []model.EventEnvelope {
	rolloutPath := firstString(transcript, "_rollout_path")
	sessionID := firstString(transcript, "session_id", "session_id_hash")
	if rolloutPath == "" || sessionID == "" {
		return nil
	}
	calls, err := readCodexToolCalls(rolloutPath)
	if err != nil || len(calls) == 0 {
		return nil
	}
	var events []model.EventEnvelope
	for _, call := range calls {
		name := strings.TrimSpace(call.Name)
		if name == "" {
			continue
		}
		payload := map[string]interface{}{
			"agent_type":           "codex",
			"metadata_only":        true,
			"tool_name":            name,
			"tool_type":            "agent_tool",
			"session_id":           sessionID,
			"session_id_hash":      sessionID,
			"session_id_is_hashed": true,
			"transcript_path_hash": util.ShortHash(rolloutPath),
		}
		if callID := strings.TrimSpace(call.CallID); callID != "" {
			payload["call_id_hash"] = util.ShortHash(callID)
		}
		if call.Timestamp != "" {
			payload["observed_at"] = call.Timestamp
		}
		server := call.MCPServer
		tool := call.MCPTool
		if codexIsAppMCPServer(server) {
			server = ""
			tool = ""
		}
		if server == "" {
			server, tool = codexMCPToolParts(name)
		}
		if codexIsAppToolName(name) || codexIsAppMCPServer(server) {
			server = ""
			tool = ""
		}
		if server != "" {
			serverID, serverName := mcpServerIdentity(server)
			if serverID == "" {
				continue
			}
			payload["tool_type"] = "mcp"
			payload["mcp_server_id"] = serverID
			if serverName != "" {
				payload["mcp_server_name"] = serverName
			}
			payload["mcp_tool"] = tool
		}
		event := baseEvent(cfg, "tool.call", "codex", "codex", payload)
		if observedAt, ok := parseCodexToolCallTime(call.Timestamp); ok {
			event.CreatedAt = observedAt
		}
		event.EventID = codexToolCallEventID(sessionID, call)
		events = append(events, event)
	}
	return events
}

func codexMCPToolParts(name string) (string, string) {
	trimmed := strings.TrimSpace(name)
	if !strings.HasPrefix(trimmed, "mcp__") {
		return "", ""
	}
	rest := strings.TrimPrefix(trimmed, "mcp__")
	var parts []string
	if strings.Contains(rest, "__") {
		parts = strings.Split(rest, "__")
	} else if strings.Contains(rest, ".") {
		parts = strings.SplitN(rest, ".", 2)
	} else {
		return "", ""
	}
	server := strings.TrimSpace(parts[0])
	tool := strings.TrimSpace(strings.Join(parts[1:], "__"))
	if server == "" || tool == "" || codexIsAppMCPServer(server) {
		return "", ""
	}
	return server, tool
}

func codexMCPServerFromNamespace(namespace string) string {
	trimmed := strings.TrimSpace(namespace)
	if !strings.HasPrefix(trimmed, "mcp__") {
		return ""
	}
	server := strings.Trim(strings.TrimPrefix(trimmed, "mcp__"), "_")
	if i := strings.Index(server, "__"); i >= 0 {
		server = server[:i]
	}
	if codexIsAppMCPServer(server) {
		return ""
	}
	return strings.TrimSpace(server)
}

func codexIsAppToolName(value string) bool {
	normalized := strings.ToLower(strings.TrimSpace(value))
	normalized = strings.Trim(normalized, "`\"'")
	return normalized == "codex_apps" ||
		strings.HasPrefix(normalized, "codex_apps.") ||
		strings.HasPrefix(normalized, "codex_apps__") ||
		strings.HasPrefix(normalized, "mcp__codex_apps__")
}

func codexIsAppMCPServer(value string) bool {
	normalized := strings.ToLower(strings.TrimSpace(value))
	normalized = strings.Trim(normalized, "`\"'")
	normalized = strings.TrimRight(normalized, ":")
	normalized = strings.TrimSpace(normalized)
	return normalized == "codex_apps" || normalized == "codex-apps"
}

func codexToolCallEventID(sessionID string, call codexTranscriptToolCall) string {
	parts := []string{
		"codex.tool.call",
		sessionID,
		call.Timestamp,
		call.Name,
		util.ShortHash(call.CallID),
	}
	return "evt_" + util.ShortHash(strings.Join(parts, "\x00"))
}

func parseCodexToolCallTime(value string) (time.Time, bool) {
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

func codexTranscriptEventID(payload map[string]interface{}) string {
	parts := []string{
		"codex.transcript",
		firstString(payload, "session_id", "session_id_hash"),
		firstString(payload, "updated_at"),
		firstString(payload, "transcript_hash"),
	}
	return "evt_" + util.ShortHash(strings.Join(parts, "\x00"))
}

func readCodexTranscript(path string) ([]map[string]interface{}, bool, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, false, err
	}
	defer file.Close()

	scanner := bufio.NewScanner(transcriptReader(file))
	scanner.Buffer(make([]byte, 64*1024), 10*1024*1024)
	var messages []map[string]interface{}
	totalBytes := 0
	truncated := false
	for scanner.Scan() {
		var record struct {
			Timestamp string                 `json:"timestamp"`
			Type      string                 `json:"type"`
			Payload   map[string]interface{} `json:"payload"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &record); err != nil {
			continue
		}
		if record.Type != "response_item" || record.Payload == nil {
			continue
		}
		itemType := firstString(record.Payload, "type")
		if itemType == "function_call" || itemType == "function_call_output" || itemType == "tool_call" {
			continue
		}
		role := strings.ToLower(strings.TrimSpace(firstString(record.Payload, "role")))
		if !isCodexTranscriptRole(role) {
			continue
		}
		text := codexContentText(record.Payload["content"])
		if isCodexNonChatText(text) {
			continue
		}
		text = privacy.RedactAndTruncate(text, codexMaxTranscriptMessageBytes)
		if totalBytes+len(text) > codexMaxTranscriptTotalBytes {
			truncated = true
			break
		}
		message := map[string]interface{}{
			"role": role,
			"text": text,
		}
		if itemType := firstString(record.Payload, "type"); itemType != "" {
			message["type"] = itemType
		}
		if record.Timestamp != "" {
			message["timestamp"] = record.Timestamp
		}
		messages = append(messages, message)
		totalBytes += len(text)
		for len(messages) > codexMaxTranscriptMessages || totalBytes > codexMaxTranscriptTotalBytes {
			oldest := messages[0]
			if oldText := firstString(oldest, "text"); oldText != "" {
				totalBytes -= len(oldText)
			}
			messages = messages[1:]
			truncated = true
		}
	}
	if err := scanner.Err(); err != nil {
		return messages, truncated, err
	}
	return messages, truncated, nil
}

func readCodexToolCalls(path string) ([]codexTranscriptToolCall, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	scanner := bufio.NewScanner(transcriptReader(file))
	scanner.Buffer(make([]byte, 64*1024), 10*1024*1024)
	var calls []codexTranscriptToolCall
	callIndexes := map[string]int{}
	addCall := func(call codexTranscriptToolCall, prefer bool) {
		key := strings.TrimSpace(call.CallID)
		if key == "" {
			key = strings.Join([]string{call.Timestamp, call.Name, call.MCPServer, call.MCPTool}, "\x00")
		}
		if idx, ok := callIndexes[key]; ok {
			existing := calls[idx]
			if prefer || (existing.MCPServer == "" && call.MCPServer != "") {
				calls[idx] = call
			}
			return
		}
		callIndexes[key] = len(calls)
		calls = append(calls, call)
	}
	for scanner.Scan() {
		var record struct {
			Timestamp string                 `json:"timestamp"`
			Type      string                 `json:"type"`
			Payload   map[string]interface{} `json:"payload"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &record); err != nil {
			continue
		}
		if record.Payload == nil {
			continue
		}
		if record.Type == "event_msg" {
			itemType := strings.ToLower(strings.TrimSpace(firstString(record.Payload, "type")))
			if itemType != "mcp_tool_call_end" {
				continue
			}
			invocation := mapFromInterface(record.Payload["invocation"])
			server := firstString(invocation, "server")
			tool := firstString(invocation, "tool")
			if server == "" || tool == "" {
				continue
			}
			if codexIsAppMCPServer(server) {
				addCall(codexTranscriptToolCall{
					Name:      "codex_apps." + tool,
					CallID:    firstString(record.Payload, "call_id", "id"),
					Timestamp: record.Timestamp,
				}, true)
				continue
			}
			addCall(codexTranscriptToolCall{
				Name:      tool,
				CallID:    firstString(record.Payload, "call_id", "id"),
				Timestamp: record.Timestamp,
				MCPServer: server,
				MCPTool:   tool,
			}, true)
			continue
		}
		if record.Type != "response_item" {
			continue
		}
		itemType := strings.ToLower(strings.TrimSpace(firstString(record.Payload, "type")))
		if itemType != "function_call" && itemType != "tool_call" {
			continue
		}
		name := firstString(record.Payload, "name", "tool_name")
		if name == "" {
			continue
		}
		server, tool := codexMCPToolParts(name)
		if server == "" {
			server = codexMCPServerFromNamespace(firstString(record.Payload, "namespace"))
			if server != "" {
				tool = name
			}
		}
		if codexIsAppToolName(name) || codexIsAppMCPServer(server) {
			server = ""
			tool = ""
		}
		addCall(codexTranscriptToolCall{
			Name:      name,
			CallID:    firstString(record.Payload, "call_id", "id"),
			Timestamp: record.Timestamp,
			MCPServer: server,
			MCPTool:   tool,
		}, false)
	}
	return calls, scanner.Err()
}

func mapFromInterface(value interface{}) map[string]interface{} {
	if typed, ok := value.(map[string]interface{}); ok {
		return typed
	}
	return nil
}

func readCodexSensitiveTranscriptMessages(path string) ([]codexSensitiveTranscriptMessage, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	scanner := bufio.NewScanner(transcriptReader(file))
	scanner.Buffer(make([]byte, 64*1024), 10*1024*1024)
	var messages []codexSensitiveTranscriptMessage
	for scanner.Scan() {
		var record struct {
			Timestamp string                 `json:"timestamp"`
			Type      string                 `json:"type"`
			Payload   map[string]interface{} `json:"payload"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &record); err != nil {
			continue
		}
		if record.Type != "response_item" || record.Payload == nil {
			continue
		}
		itemType := firstString(record.Payload, "type")
		if itemType == "function_call" || itemType == "function_call_output" || itemType == "tool_call" {
			continue
		}
		role := strings.ToLower(strings.TrimSpace(firstString(record.Payload, "role")))
		if role != "user" {
			continue
		}
		text := codexContentText(record.Payload["content"])
		if isCodexNonChatText(text) {
			continue
		}
		text = truncateCodexSensitiveScanText(text)
		messages = append(messages, codexSensitiveTranscriptMessage{
			Text:      text,
			Timestamp: record.Timestamp,
		})
		for len(messages) > codexMaxTranscriptMessages {
			messages = messages[1:]
		}
	}
	return messages, scanner.Err()
}

func transcriptReader(file *os.File) *bufio.Reader {
	info, err := file.Stat()
	if err != nil || info.Size() <= codexTranscriptTailBytes {
		_, _ = file.Seek(0, 0)
		return bufio.NewReader(file)
	}
	offset := info.Size() - codexTranscriptTailBytes
	if _, err := file.Seek(offset, 0); err != nil {
		_, _ = file.Seek(0, 0)
		return bufio.NewReader(file)
	}
	reader := bufio.NewReader(file)
	_, _ = reader.ReadString('\n')
	return reader
}

func isCodexTranscriptRole(role string) bool {
	switch role {
	case "user", "assistant":
		return true
	default:
		return false
	}
}

func isCodexNonChatText(value string) bool {
	normalized := strings.TrimSpace(value)
	if normalized == "" {
		return true
	}
	lower := strings.ToLower(normalized)
	return strings.HasPrefix(lower, "<environment_context>") ||
		strings.HasPrefix(lower, "<system_context>") ||
		strings.HasPrefix(lower, "<developer_context>") ||
		strings.HasPrefix(lower, "<tools>") ||
		strings.HasPrefix(lower, "<local-command-caveat>") ||
		strings.HasPrefix(lower, "<local-command-stdout>") ||
		strings.HasPrefix(lower, "<local-command-stderr>") ||
		strings.HasPrefix(lower, "<command-name>") ||
		strings.HasPrefix(lower, "<command-message>") ||
		strings.HasPrefix(lower, "<command-args>") ||
		strings.HasPrefix(lower, "<command-stdout>") ||
		strings.HasPrefix(lower, "<command-stderr>") ||
		strings.HasPrefix(lower, "<command-error>") ||
		(strings.Contains(lower, "<cwd>") && strings.Contains(lower, "<current_date>"))
}

func codexContentText(value interface{}) string {
	switch typed := value.(type) {
	case string:
		return typed
	case []interface{}:
		var parts []string
		for _, item := range typed {
			switch content := item.(type) {
			case string:
				if strings.TrimSpace(content) != "" {
					parts = append(parts, content)
				}
			case map[string]interface{}:
				for _, key := range []string{"text", "content", "message", "input", "output"} {
					if text := firstString(content, key); text != "" {
						parts = append(parts, text)
						break
					}
				}
			}
		}
		return strings.Join(parts, "\n")
	case map[string]interface{}:
		for _, key := range []string{"text", "content", "message", "input", "output"} {
			if text := firstString(typed, key); text != "" {
				return text
			}
		}
	}
	return ""
}

func truncateCodexSensitiveScanText(value string) string {
	value = strings.ToValidUTF8(value, "")
	if len(value) <= codexMaxTranscriptMessageBytes {
		return value
	}
	return strings.ToValidUTF8(value[:codexMaxTranscriptMessageBytes], "")
}

func parseCodexTimestamp(value string) (time.Time, bool) {
	if strings.TrimSpace(value) == "" {
		return time.Time{}, false
	}
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}, false
	}
	return parsed.UTC(), true
}
