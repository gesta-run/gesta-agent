package daemon

import (
	"strings"
	"time"

	"github.com/gesta-run/gesta-agent/pkg/mcpmeta"
	"github.com/gesta-run/gesta-agent/pkg/model"
	"github.com/gesta-run/gesta-agent/pkg/util"
)

type codexTranscriptToolCall struct {
	Name      string
	CallID    string
	Timestamp string
	MCPServer string
	MCPTool   string
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
			serverID, serverName := mcpmeta.ServerIdentity(server)
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
