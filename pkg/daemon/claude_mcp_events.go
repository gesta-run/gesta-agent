package daemon

import (
	"strconv"
	"strings"
	"time"

	"github.com/gesta-run/gesta-agent/pkg/mcpmeta"
	"github.com/gesta-run/gesta-agent/pkg/model"
	"github.com/gesta-run/gesta-agent/pkg/util"
)

func claudeMCPToolCallEvents(cfg Config, session claudeSessionUsage) []model.EventEnvelope {
	if strings.TrimSpace(session.SessionID) == "" {
		return nil
	}
	sessionID := util.ShortHash(session.SessionID)
	events := make([]model.EventEnvelope, 0, len(session.MCPToolCalls))
	for _, call := range session.MCPToolCalls {
		observedAt, ok := parseClaudeTimestamp(call.Timestamp)
		if !ok {
			continue
		}
		serverID, serverName := mcpmeta.ServerIdentity(call.MCPServer)
		tool := strings.TrimSpace(call.MCPTool)
		name := strings.TrimSpace(call.Name)
		if serverID == "" || tool == "" {
			continue
		}
		if name == "" {
			name = "mcp__" + serverID + "__" + tool
		}
		payload := map[string]interface{}{
			"agent_type":           claudeCodeAgentType,
			"metadata_only":        true,
			"tool_name":            name,
			"tool_type":            "mcp",
			"mcp_server_id":        serverID,
			"mcp_tool":             tool,
			"session_id":           sessionID,
			"session_id_hash":      sessionID,
			"session_id_is_hashed": true,
			"observed_at":          observedAt.Format(time.RFC3339Nano),
		}
		if serverName != "" {
			payload["mcp_server_name"] = serverName
		}
		if callID := strings.TrimSpace(call.CallID); callID != "" {
			payload["call_id_hash"] = util.ShortHash(callID)
		}
		event := baseEvent(cfg, "tool.call", claudeCodeUsageSource, claudeCodeAgentType, payload)
		event.CreatedAt = observedAt
		event.EventID = claudeMCPToolCallEventID(sessionID, call)
		events = append(events, event)
	}
	return events
}

func claudeMCPToolCallEventID(sessionID string, call claudeTranscriptToolCall) string {
	parts := []string{"claude_code.tool.call", sessionID}
	if callID := strings.TrimSpace(call.CallID); callID != "" {
		parts = append(parts, "call_id", util.ShortHash(callID))
	} else {
		parts = append(parts,
			"metadata",
			call.Timestamp,
			call.Name,
			call.MCPServer,
			call.MCPTool,
			strconv.Itoa(call.BlockIndex),
		)
	}
	return "evt_" + util.ShortHash(strings.Join(parts, "\x00"))
}
