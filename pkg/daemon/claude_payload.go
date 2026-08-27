package daemon

import (
	"encoding/json"
	"sort"
	"strings"
	"time"

	"github.com/gesta-run/gesta-agent/pkg/util"
)

// claudeRepoHash derives a stable, privacy-preserving repo identifier from the
// session cwd, mirroring how Codex hashes its repo/cwd identifiers. The console
// reads "repo_path_hash" into its repo column.
func claudeRepoHash(cwd string) string {
	cwd = strings.TrimSpace(cwd)
	if cwd == "" {
		return ""
	}
	return util.ShortHash(cwd)
}

// claudeUsageSummaryPayload builds the per-(session, model, day) usage.summary
// payload. Each bucket is treated as its own logical usage cursor so the generic
// usage-delta machinery splits tokens by model and by day. The session id is
// hashed (matching Codex) and salted with the model+day so each bucket advances
// its own cursor without double counting other buckets.
func claudeUsageSummaryPayload(session claudeSessionUsage, key claudeModelDayKey, usage claudeAssistantUsage) map[string]interface{} {
	sessionHash := util.ShortHash(session.SessionID)
	bucketSessionID := util.ShortHash(strings.Join([]string{session.SessionID, key.Model, key.Day}, "\x00"))
	payload := map[string]interface{}{
		"agent_type":           claudeCodeAgentType,
		"metadata_only":        true,
		"session_id":           bucketSessionID,
		"session_id_hash":      bucketSessionID,
		"session_id_is_hashed": true,
		"parent_session_id":    sessionHash,
		// index_session_id is the real (un-salted) session hash, matching the
		// agent_sessions session-index id. The usage-delta machinery emits it as the
		// delta's session_id so the control plane reconciles agent_sessions.total_tokens
		// across every (model, day) bucket of this session. session_id above stays the
		// salted per-bucket cursor identity.
		"index_session_id":      sessionHash,
		"model":                 key.Model,
		"usage_day":             key.Day,
		"total_tokens":          usage.TotalTokens(),
		"tokens_used":           usage.TotalTokens(),
		"input_tokens":          usage.InputTokens,
		"output_tokens":         usage.OutputTokens,
		"cached_input_tokens":   usage.CacheReadTokens,
		"cache_creation_tokens": usage.CacheCreationTokens,
		"token_accounting":      claudeTokenAccounting,
	}
	if session.AccountingSeedOnly {
		payload[internalAccountingCutoverPayloadKey] = true
	}
	if repoHash := claudeRepoHash(session.CWD); repoHash != "" {
		payload["repo_path_hash"] = repoHash
	}
	if session.GitBranch != "" {
		payload["git_branch"] = session.GitBranch
	}
	if session.Title != "" {
		payload["title"] = session.Title
	}
	return payload
}

// claudeSessionIndexPayload builds the agent_sessions session-index payload for a
// whole Claude Code session (one transcript file). It carries the readable title
// (as a single-message transcript) so the control plane content-indexes it.
func claudeSessionIndexPayload(session claudeSessionUsage) map[string]interface{} {
	sessionHash := util.ShortHash(session.SessionID)
	primaryModel := ""
	if len(session.Models) > 0 {
		primaryModel = session.Models[0]
	}
	payload := map[string]interface{}{
		"agent_type":            claudeCodeAgentType,
		"metadata_only":         false,
		"session_id":            sessionHash,
		"session_id_hash":       sessionHash,
		"session_id_is_hashed":  true,
		"model":                 primaryModel,
		"models":                session.Models,
		"event_count":           session.AssistantEvents,
		"total_tokens":          session.totalTokens(),
		"tokens_used":           session.totalTokens(),
		"input_tokens":          session.Total.InputTokens,
		"output_tokens":         session.Total.OutputTokens,
		"cached_input_tokens":   session.Total.CacheReadTokens,
		"cache_creation_tokens": session.Total.CacheCreationTokens,
		"transcript_source":     "claude_code_transcript_jsonl",
	}
	if repoHash := claudeRepoHash(session.CWD); repoHash != "" {
		payload["repo_path_hash"] = repoHash
	}
	if session.GitBranch != "" {
		payload["git_branch"] = session.GitBranch
	}
	if !session.FirstEventAt.IsZero() {
		payload["first_event_at"] = session.FirstEventAt.Format(time.RFC3339Nano)
	}
	if !session.LastEventAt.IsZero() {
		payload["last_event_at"] = session.LastEventAt.Format(time.RFC3339Nano)
		payload["updated_at"] = session.LastEventAt.Format(time.RFC3339Nano)
	}
	if session.Title != "" {
		payload["title"] = session.Title
	}
	if len(session.Messages) > 0 {
		payload["messages"] = session.Messages
		payload["message_count"] = len(session.Messages)
		payload["transcript_truncated"] = session.TranscriptTruncated
		payload["transcript_max_messages"] = codexMaxTranscriptMessages
		payload["transcript_max_message_bytes"] = codexMaxTranscriptMessageBytes
		payload["transcript_max_total_text_bytes"] = codexMaxTranscriptTotalBytes
	} else if session.Title != "" {
		// Fallback for unusual Claude records with a title but no parseable chat
		// blocks. Normal transcripts carry the complete user/assistant messages.
		payload["messages"] = []map[string]interface{}{{"role": "user", "text": session.Title}}
		payload["message_count"] = 1
		payload["transcript_truncated"] = false
	}
	// transcript_hash lets the baseline detect whether the session changed since
	// the last collection cycle.
	payload["transcript_hash"] = util.HashString(claudeSessionFingerprint(session))
	return payload
}

func claudeSessionFingerprint(session claudeSessionUsage) string {
	parts := []string{
		session.SessionID,
		session.LastEventAt.Format(time.RFC3339Nano),
	}
	keys := make([]claudeModelDayKey, 0, len(session.ByModelDay))
	for key := range session.ByModelDay {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].Day != keys[j].Day {
			return keys[i].Day < keys[j].Day
		}
		return keys[i].Model < keys[j].Model
	})
	for _, key := range keys {
		usage := session.ByModelDay[key]
		parts = append(parts, key.Day+"|"+key.Model+"|"+stringInt(usage.TotalTokens()))
	}
	for _, call := range session.MCPToolCalls {
		parts = append(parts, strings.Join([]string{
			"mcp",
			call.Timestamp,
			call.Name,
			util.ShortHash(call.CallID),
		}, "|"))
	}
	if len(session.Messages) > 0 {
		data, _ := json.Marshal(session.Messages)
		parts = append(parts, string(data))
	}
	return strings.Join(parts, "\x00")
}
