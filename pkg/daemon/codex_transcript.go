package daemon

import (
	"bufio"
	"encoding/json"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/gesta-run/gesta-agent/pkg/privacy"
	turnusage "github.com/gesta-run/gesta-agent/pkg/turn"
	"github.com/gesta-run/gesta-agent/pkg/util"
)

type codexTranscriptFallbackCandidate struct {
	Session    turnusage.CodexSession
	ModifiedAt time.Time
}

func mergeCodexTranscriptFallbacks(
	existing []map[string]interface{},
	sessions []turnusage.CodexSession,
) ([]map[string]interface{}, int) {
	seen := make(map[string]struct{}, len(existing))
	for _, payload := range existing {
		if sessionID := sessionIDFromPayload(payload); sessionID != "" {
			seen[sessionID] = struct{}{}
		}
	}
	candidates := make([]codexTranscriptFallbackCandidate, 0, len(sessions))
	for _, session := range sessions {
		if strings.TrimSpace(session.RolloutPath) == "" {
			continue
		}
		if _, ok := seen[session.SessionID]; ok {
			continue
		}
		candidates = append(candidates, codexTranscriptFallbackCandidate{
			Session:    session,
			ModifiedAt: codexRolloutModTime(session.RolloutPath),
		})
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].ModifiedAt.Equal(candidates[j].ModifiedAt) {
			return candidates[i].Session.SessionID < candidates[j].Session.SessionID
		}
		return candidates[i].ModifiedAt.After(candidates[j].ModifiedAt)
	})

	merged := append([]map[string]interface{}(nil), existing...)
	added := 0
	for _, candidate := range candidates {
		if added >= codexMaxTranscriptRows {
			break
		}
		payload := codexTranscriptPayloadFromSession(candidate.Session, candidate.ModifiedAt)
		if len(payload) == 0 {
			continue
		}
		payload[internalTranscriptFallbackPayloadKey] = true
		merged = append(merged, payload)
		seen[candidate.Session.SessionID] = struct{}{}
		added++
	}
	return merged, added
}

func codexTranscriptPayloadFromSession(session turnusage.CodexSession, modifiedAt time.Time) map[string]interface{} {
	row := map[string]interface{}{
		"rollout_path":   session.RolloutPath,
		"model":          session.Model,
		"model_provider": session.ModelProvider,
		"title":          session.Title,
	}
	if !modifiedAt.IsZero() {
		row["updated_at"] = modifiedAt.UTC().Format(time.RFC3339Nano)
	}
	usage := map[string]interface{}{
		"session_id":             session.SessionID,
		"session_id_hash":        session.SessionID,
		"session_id_is_hashed":   true,
		"parent_session_id":      session.ParentSessionID,
		"parent_session_id_hash": session.ParentSessionID,
		"repo":                   session.Repo,
		"title":                  session.Title,
	}
	return codexTranscriptPayload(row, usage, nil)
}

func codexRolloutModTime(path string) time.Time {
	info, err := os.Stat(path)
	if err != nil {
		return time.Time{}
	}
	return info.ModTime()
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
		if role == "assistant" {
			message["summary_phase"] = codexTranscriptSummaryPhase(firstString(record.Payload, "phase"))
		}
		if messageID := firstString(record.Payload, "id", "message_id"); messageID != "" {
			message["message_id"] = messageID
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
