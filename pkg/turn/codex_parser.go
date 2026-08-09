package turn

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/gesta-run/gesta-agent/pkg/util"
)

type codexRecord struct {
	Timestamp string                 `json:"timestamp"`
	Type      string                 `json:"type"`
	Payload   map[string]interface{} `json:"payload"`
}

func processCodexRecord(session CodexSession, daemonID string, cursor *Cursor, record codexRecord, observedAt time.Time, emit bool) (Usage, bool) {
	payloadType := strings.ToLower(strings.TrimSpace(stringValue(record.Payload, "type")))
	recordedAt := parseTime(record.Timestamp, observedAt)
	if record.Type == "turn_context" {
		if model := stringValue(record.Payload, "model"); model != "" {
			cursor.Model = model
		}
		startCodexTurn(cursor, stringValue(record.Payload, "turn_id"), recordedAt, session.InheritedTurnIDHashes)
	}
	if record.Type == "event_msg" {
		switch payloadType {
		case "task_started":
			startCodexTurn(cursor, stringValue(record.Payload, "turn_id"), recordedAt, session.InheritedTurnIDHashes)
		case "token_count":
			updateCodexTokenTotals(cursor, record.Payload)
		case "task_complete", "turn_aborted":
			return completeCodexTurn(session, daemonID, cursor, record.Payload, payloadType, recordedAt, emit)
		}
	}
	if cursor.Active != nil {
		scoreCodexRecord(cursor.Active.Scores, record, payloadType)
	}
	return Usage{}, false
}

func startCodexTurn(cursor *Cursor, turnID string, startedAt time.Time, inheritedTurnIDHashes ...map[string]struct{}) {
	if turnID == "" {
		return
	}
	turnIDHash := util.HashString(turnID)
	if cursor.Active != nil && cursor.Active.TurnIDHash == turnIDHash {
		return
	}
	inherited := false
	if len(inheritedTurnIDHashes) > 0 {
		_, inherited = inheritedTurnIDHashes[0][turnIDHash]
	}
	cursor.Active = &activeTurn{
		TurnIDHash: turnIDHash,
		StartedAt:  startedAt,
		Baseline:   cursor.LastTokens,
		Latest:     cursor.LastTokens,
		Scores:     map[string]int{},
		Inherited:  inherited,
	}
}

func updateCodexTokenTotals(cursor *Cursor, payload map[string]interface{}) {
	totals, ok := codexTokenTotals(payload)
	if !ok {
		return
	}
	if cursor.AwaitingBaseline {
		cursor.LastTokens = totals
		cursor.AwaitingBaseline = false
		if cursor.Active != nil {
			cursor.Active.Baseline = totals
			cursor.Active.Latest = totals
		}
		return
	}
	if cursor.Active != nil && totals.decreasedFrom(cursor.LastTokens) {
		cursor.Active.CounterReset = true
		cursor.Active.ResetPrevious = cursor.LastTokens
		cursor.Active.ResetCurrent = totals
	}
	cursor.LastTokens = totals
	if cursor.Active != nil {
		cursor.Active.Latest = totals
	}
}

func completeCodexTurn(session CodexSession, daemonID string, cursor *Cursor, payload map[string]interface{}, payloadType string, endedAt time.Time, emit bool) (Usage, bool) {
	if cursor.Active == nil {
		return Usage{}, false
	}
	turnID := stringValue(payload, "turn_id")
	if turnID != "" && util.HashString(turnID) != cursor.Active.TurnIDHash {
		return Usage{}, false
	}
	active := *cursor.Active
	cursor.Active = nil
	if active.Inherited {
		return Usage{}, false
	}
	if active.CounterReset || active.Latest.decreasedFrom(active.Baseline) {
		previous := active.ResetPrevious
		current := active.ResetCurrent
		if !active.CounterReset {
			previous = active.Baseline
			current = active.Latest
		}
		if session.OnCounterReset != nil {
			session.OnCounterReset(CounterReset{
				SessionIDHash: session.SessionID,
				TurnIDHash:    active.TurnIDHash,
				Previous:      previous,
				Current:       current,
			})
		}
		return Usage{}, false
	}
	delta := active.Latest.Delta(active.Baseline)
	if !emit || delta.Total() <= 0 {
		return Usage{}, false
	}
	status := "completed"
	if payloadType == "turn_aborted" {
		status = "aborted"
	}
	return Usage{
		EventID:       stableEventID(daemonID, session.SessionID, active.TurnIDHash),
		SessionIDHash: session.SessionID,
		TurnIDHash:    active.TurnIDHash,
		Status:        status,
		Title:         session.Title,
		StartedAt:     active.StartedAt,
		EndedAt:       endedAt,
		Model:         firstCodexModel(cursor.Model, session.Model),
		Repo:          session.Repo,
		ModelProvider: session.ModelProvider,
		Tokens:        delta,
		WorkType:      classify(active.Scores),
		TotalEncoding: session.TotalEncoding,
	}, true
}

func firstCodexModel(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}

func codexTokenTotals(payload map[string]interface{}) (TokenTotals, bool) {
	info := mapValue(payload["info"])
	total := mapValue(info["total_token_usage"])
	if len(total) == 0 {
		return TokenTotals{}, false
	}
	input := intValue(total, "input_tokens")
	cacheRead := intValue(total, "cached_input_tokens", "cache_read_tokens")
	return TokenTotals{
		Input:      nonNegative(input - cacheRead),
		Output:     intValue(total, "output_tokens"),
		CacheRead:  cacheRead,
		CacheWrite: intValue(total, "cache_write_input_tokens", "cache_write_tokens"),
	}, true
}

func scoreCodexRecord(scores map[string]int, record codexRecord, payloadType string) {
	if record.Type != "response_item" && record.Type != "event_msg" {
		return
	}
	if record.Type == "event_msg" && payloadType == "user_message" {
		scoreText(scores, stringValue(record.Payload, "message"), 5)
		return
	}
	if payloadType == "message" && strings.EqualFold(stringValue(record.Payload, "role"), "user") {
		scoreText(scores, contentText(record.Payload["content"]), 5)
		return
	}
	if payloadType != "function_call" && payloadType != "tool_call" && payloadType != "custom_tool_call" && payloadType != "mcp_tool_call_end" {
		return
	}
	name := stringValue(record.Payload, "name", "tool_name")
	if invocation := mapValue(record.Payload["invocation"]); len(invocation) > 0 {
		name = strings.Join([]string{name, stringValue(invocation, "server"), stringValue(invocation, "tool")}, " ")
		scoreText(scores, localText(invocation["arguments"]), 7)
	}
	scoreText(scores, name, 3)
	scoreText(scores, localText(record.Payload["arguments"]), 7)
	scoreText(scores, localText(record.Payload["input"]), 7)
}

func contentText(value interface{}) string {
	switch typed := value.(type) {
	case string:
		return typed
	case []interface{}:
		var parts []string
		for _, item := range typed {
			if object := mapValue(item); len(object) > 0 {
				if text := stringValue(object, "text", "content", "message", "input"); text != "" {
					parts = append(parts, text)
				}
			}
		}
		return strings.Join(parts, "\n")
	default:
		return ""
	}
}

func localText(value interface{}) string {
	if text, ok := value.(string); ok {
		return text
	}
	data, err := json.Marshal(value)
	if err != nil {
		return ""
	}
	return string(data)
}

func mapValue(value interface{}) map[string]interface{} {
	object, _ := value.(map[string]interface{})
	return object
}

func stringValue(object map[string]interface{}, keys ...string) string {
	for _, key := range keys {
		if value, ok := object[key].(string); ok && strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func intValue(object map[string]interface{}, keys ...string) int64 {
	for _, key := range keys {
		switch value := object[key].(type) {
		case float64:
			return int64(value)
		case int64:
			return value
		case int:
			return int64(value)
		case json.Number:
			parsed, _ := value.Int64()
			return parsed
		}
	}
	return 0
}

func parseTime(value string, fallback time.Time) time.Time {
	parsed, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(value))
	if err != nil {
		return fallback.UTC()
	}
	return parsed.UTC()
}
