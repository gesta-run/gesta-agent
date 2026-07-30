package daemon

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/gesta-run/gesta-agent/pkg/util"
)

const (
	transcriptChunkEventType    = "session.transcript.chunk"
	transcriptChunkMaxMessages  = 64
	transcriptChunkMaxBytes     = 128 * 1024
	transcriptCursorMaxVersions = 4096
)

var transcriptMetadataFields = []string{
	"title",
	"model",
	"model_provider",
	"repo",
	"git_branch",
	"git_sha",
	"parent_session_id",
	"parent_session_id_hash",
}

func prepareTranscriptChunks(
	payload map[string]interface{},
	existing baselineSession,
	observedAt time.Time,
	location *time.Location,
) ([]map[string]interface{}, baselineSession) {
	sessionID := sessionIDFromPayload(payload)
	if sessionID == "" {
		return nil, existing
	}
	messages := normalizedTranscriptMessages(sessionID, payload)
	seen := make(map[string]struct{}, len(existing.TranscriptMessageVersions)+len(messages))
	for _, version := range existing.TranscriptMessageVersions {
		if version = strings.TrimSpace(version); version != "" {
			seen[version] = struct{}{}
		}
	}

	unseen := make([]map[string]interface{}, 0, len(messages))
	for _, message := range messages {
		version := transcriptMessageVersion(message)
		if _, ok := seen[version]; ok {
			continue
		}
		seen[version] = struct{}{}
		existing.TranscriptMessageVersions = append(existing.TranscriptMessageVersions, version)
		unseen = append(unseen, message)
	}
	existing.TranscriptMessageVersions = boundedTranscriptVersions(existing.TranscriptMessageVersions)
	existing.TranscriptCursorInitialized = true

	var chunks []map[string]interface{}
	var current []map[string]interface{}
	currentBytes := 0
	currentDate := ""
	flush := func() {
		if len(current) == 0 {
			return
		}
		existing.TranscriptSequence++
		chunk := transcriptChunkPayload(payload, current, existing.TranscriptSequence, currentDate)
		chunks = append(chunks, chunk)
		current = nil
		currentBytes = 0
		currentDate = ""
	}
	for _, message := range unseen {
		messageBytes := transcriptMessageBytes(message)
		localDate := transcriptMessageDate(message, payload, observedAt, location)
		if len(current) > 0 &&
			(len(current) >= transcriptChunkMaxMessages ||
				currentBytes+messageBytes > transcriptChunkMaxBytes ||
				localDate != currentDate) {
			flush()
		}
		current = append(current, message)
		currentBytes += messageBytes
		currentDate = localDate
	}
	flush()
	return chunks, existing
}

func seedTranscriptCursor(payload map[string]interface{}, existing baselineSession) baselineSession {
	sessionID := sessionIDFromPayload(payload)
	if sessionID == "" {
		return existing
	}
	seen := make(map[string]struct{}, len(existing.TranscriptMessageVersions))
	for _, version := range existing.TranscriptMessageVersions {
		if version = strings.TrimSpace(version); version != "" {
			seen[version] = struct{}{}
		}
	}
	for _, message := range normalizedTranscriptMessages(sessionID, payload) {
		version := transcriptMessageVersion(message)
		if _, ok := seen[version]; ok {
			continue
		}
		seen[version] = struct{}{}
		existing.TranscriptMessageVersions = append(existing.TranscriptMessageVersions, version)
	}
	existing.TranscriptMessageVersions = boundedTranscriptVersions(existing.TranscriptMessageVersions)
	existing.TranscriptCursorInitialized = true
	return existing
}

func boundedTranscriptVersions(versions []string) []string {
	if len(versions) <= transcriptCursorMaxVersions {
		return versions
	}
	return append([]string(nil), versions[len(versions)-transcriptCursorMaxVersions:]...)
}

func normalizedTranscriptMessages(
	sessionID string,
	payload map[string]interface{},
) []map[string]interface{} {
	raw, ok := payload["messages"]
	if !ok {
		return nil
	}
	var values []interface{}
	switch typed := raw.(type) {
	case []interface{}:
		values = typed
	case []map[string]interface{}:
		values = make([]interface{}, 0, len(typed))
		for _, value := range typed {
			values = append(values, value)
		}
	default:
		return nil
	}
	messages := make([]map[string]interface{}, 0, len(values))
	for _, value := range values {
		record, ok := value.(map[string]interface{})
		if !ok {
			continue
		}
		role := strings.ToLower(strings.TrimSpace(firstString(record, "role", "type")))
		if role == "human" {
			role = "user"
		}
		if role != "user" && role != "assistant" && role != "system" {
			continue
		}
		text := strings.TrimSpace(firstString(record, "text", "content", "message"))
		if text == "" {
			continue
		}
		sourceTimestamp := firstString(record, "timestamp", "created_at", "observed_at")
		sourceID := firstString(record, "message_id", "id")
		identity := []string{sessionID, role, sourceTimestamp, text}
		if sourceID != "" {
			identity = []string{sessionID, role, sourceID}
		}
		messageID := "msg_" + util.HashString(strings.Join(identity, "\x00"))
		message := map[string]interface{}{
			"message_id": messageID,
			"role":       role,
			"text":       text,
		}
		if sourceTimestamp != "" {
			message["timestamp"] = sourceTimestamp
		}
		if model := firstString(record, "model"); model != "" {
			message["model"] = model
		}
		if role == "assistant" {
			message["summary_phase"] = normalizeTranscriptSummaryPhase(firstString(record, "summary_phase"))
		}
		messages = append(messages, message)
	}
	return messages
}

func transcriptChunkPayload(
	source map[string]interface{},
	messages []map[string]interface{},
	sequence int64,
	localDate string,
) map[string]interface{} {
	sessionID := sessionIDFromPayload(source)
	metadata := map[string]interface{}{}
	for _, field := range transcriptMetadataFields {
		if value := firstString(source, field); value != "" {
			metadata[field] = value
		}
	}
	chunk := map[string]interface{}{
		"schema_version":   1,
		"session_id":       sessionID,
		"session_id_hash":  sessionID,
		"sequence":         sequence,
		"local_date":       localDate,
		"agent_type":       firstString(source, "agent_type"),
		"repo":             firstString(source, "repo"),
		"messages":         messages,
		"message_count":    len(messages),
		"session_metadata": metadata,
	}
	for _, field := range transcriptMetadataFields {
		if value := firstString(source, field); value != "" {
			chunk[field] = value
		}
	}
	if rolloutPath := firstString(source, "_rollout_path"); rolloutPath != "" {
		chunk["_rollout_path"] = rolloutPath
	}
	encoded, _ := json.Marshal(struct {
		SessionID string                   `json:"session_id"`
		Sequence  int64                    `json:"sequence"`
		Messages  []map[string]interface{} `json:"messages"`
	}{
		SessionID: sessionID,
		Sequence:  sequence,
		Messages:  messages,
	})
	chunk["chunk_id"] = "tchunk_" + util.HashString(string(encoded))
	return chunk
}

func transcriptChunkEventID(payload map[string]interface{}) string {
	return "evt_" + firstString(payload, "chunk_id")
}

func transcriptMessageBytes(message map[string]interface{}) int {
	encoded, err := json.Marshal(message)
	if err != nil {
		return len(firstString(message, "text"))
	}
	return len(encoded)
}

func transcriptMessageVersion(message map[string]interface{}) string {
	messageID := firstString(message, "message_id")
	content := strings.Join([]string{
		firstString(message, "role"),
		firstString(message, "text"),
		firstString(message, "timestamp"),
		firstString(message, "model"),
		firstString(message, "summary_phase"),
	}, "\x00")
	return messageID + ":" + util.HashString(content)
}

func transcriptMessageDate(
	message map[string]interface{},
	payload map[string]interface{},
	observedAt time.Time,
	location *time.Location,
) string {
	if location == nil {
		location = time.UTC
	}
	for _, value := range []string{
		firstString(message, "timestamp"),
		firstString(payload, "updated_at", "last_event_at"),
	} {
		if parsed, err := time.Parse(time.RFC3339Nano, value); err == nil {
			return parsed.In(location).Format("2006-01-02")
		}
	}
	if observedAt.IsZero() {
		observedAt = time.Now().UTC()
	}
	return observedAt.In(location).Format("2006-01-02")
}

func dailyWorkLocation(name string) *time.Location {
	location, err := time.LoadLocation(strings.TrimSpace(name))
	if err != nil {
		return time.UTC
	}
	return location
}

func transcriptCursorPayload(payload map[string]interface{}, cursor baselineSession) map[string]interface{} {
	out := make(map[string]interface{}, len(payload)+3)
	for key, value := range payload {
		out[key] = value
	}
	out["_gesta_transcript_message_versions"] = append([]string(nil), cursor.TranscriptMessageVersions...)
	out["_gesta_transcript_sequence"] = cursor.TranscriptSequence
	out["_gesta_transcript_cursor_initialized"] = cursor.TranscriptCursorInitialized
	return out
}

func transcriptCursorFromPayload(payload map[string]interface{}, existing baselineSession) baselineSession {
	if raw, ok := payload["_gesta_transcript_message_versions"]; ok {
		switch typed := raw.(type) {
		case []string:
			existing.TranscriptMessageVersions = append([]string(nil), typed...)
		case []interface{}:
			var ids []string
			for _, value := range typed {
				if id := strings.TrimSpace(fmt.Sprint(value)); id != "" {
					ids = append(ids, id)
				}
			}
			existing.TranscriptMessageVersions = ids
		}
	}
	if sequence, ok := payloadInt64Value(payload, "_gesta_transcript_sequence"); ok {
		existing.TranscriptSequence = sequence
	}
	if initialized, ok := payload["_gesta_transcript_cursor_initialized"].(bool); ok {
		existing.TranscriptCursorInitialized = initialized
	}
	return existing
}

func payloadInt64Value(payload map[string]interface{}, key string) (int64, bool) {
	value, ok := payload[key]
	if !ok {
		return 0, false
	}
	switch typed := value.(type) {
	case int64:
		return typed, true
	case int:
		return int64(typed), true
	case float64:
		return int64(typed), true
	case json.Number:
		value, err := typed.Int64()
		return value, err == nil
	default:
		return 0, false
	}
}
