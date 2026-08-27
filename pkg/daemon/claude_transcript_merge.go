package daemon

import (
	"sort"
	"strings"

	"github.com/gesta-run/gesta-agent/pkg/privacy"
)

// mergeClaudeSessionsByID groups per-file parse results into one logical session
// per SessionID. Claude Code splits a single session across multiple transcript
// files (e.g. on resume), so usage must be summed across every file before
// building payloads — otherwise same-key buckets collide on the MAX-keeping
// baseline and tokens are silently undercounted, and the session index thrashes
// between conflicting per-file event counts / transcript hashes.
//
// Within a merged session: ByModelDay / Total / AssistantEvents are summed,
// FirstEventAt is the min and LastEventAt the max across files, and Models is the
// union. Scalar identity fields (CWD, GitBranch, Title) take the first non-empty
// value, preferring the file whose first event is earliest so the title reflects
// the session's opening prompt. The returned slice is sorted by SessionID for
// deterministic downstream ordering.
func mergeClaudeSessionsByID(sessions []claudeSessionUsage) []claudeSessionUsage {
	if len(sessions) == 0 {
		return nil
	}
	merged := make(map[string]*claudeSessionUsage)
	order := make([]string, 0, len(sessions))
	// Process files in FirstEventAt order so the earliest file seeds the title /
	// cwd / branch for each session, regardless of filesystem walk order.
	ordered := make([]claudeSessionUsage, len(sessions))
	copy(ordered, sessions)
	sort.SliceStable(ordered, func(i, j int) bool {
		if !ordered[i].FirstEventAt.Equal(ordered[j].FirstEventAt) {
			return ordered[i].FirstEventAt.Before(ordered[j].FirstEventAt)
		}
		return ordered[i].SessionID < ordered[j].SessionID
	})

	for i := range ordered {
		file := ordered[i]
		existing, ok := merged[file.SessionID]
		if !ok {
			combined := claudeSessionUsage{
				SessionID:           file.SessionID,
				CWD:                 file.CWD,
				GitBranch:           file.GitBranch,
				Title:               file.Title,
				FirstEventAt:        file.FirstEventAt,
				LastEventAt:         file.LastEventAt,
				AssistantEvents:     file.AssistantEvents,
				Messages:            cloneClaudeTranscriptMessages(file.Messages),
				TranscriptTruncated: file.TranscriptTruncated,
				MCPToolCalls:        mergeClaudeMCPToolCalls(nil, file.MCPToolCalls),
				Turns:               mergeClaudeTurns(nil, file.Turns),
				Invocations:         mergeClaudeInvocations(nil, file.Invocations),
				ByModelDay:          map[claudeModelDayKey]claudeAssistantUsage{},
			}
			modelsSeen := map[string]struct{}{}
			for _, name := range file.Models {
				if _, dup := modelsSeen[name]; dup {
					continue
				}
				modelsSeen[name] = struct{}{}
				combined.Models = append(combined.Models, name)
			}
			sort.Strings(combined.Models)
			merged[file.SessionID] = &combined
			order = append(order, file.SessionID)
			continue
		}

		if existing.CWD == "" {
			existing.CWD = file.CWD
		}
		if existing.GitBranch == "" {
			existing.GitBranch = file.GitBranch
		}
		if existing.Title == "" {
			existing.Title = file.Title
		}
		if !file.FirstEventAt.IsZero() && (existing.FirstEventAt.IsZero() || file.FirstEventAt.Before(existing.FirstEventAt)) {
			existing.FirstEventAt = file.FirstEventAt
		}
		if file.LastEventAt.After(existing.LastEventAt) {
			existing.LastEventAt = file.LastEventAt
		}
		existing.AssistantEvents += file.AssistantEvents
		existing.Messages, existing.TranscriptTruncated = mergeClaudeTranscriptMessages(existing.Messages, file.Messages, existing.TranscriptTruncated || file.TranscriptTruncated)
		existing.MCPToolCalls = mergeClaudeMCPToolCalls(existing.MCPToolCalls, file.MCPToolCalls)
		existing.Turns = mergeClaudeTurns(existing.Turns, file.Turns)
		existing.Invocations = mergeClaudeInvocations(existing.Invocations, file.Invocations)
		modelsSeen := map[string]struct{}{}
		for _, name := range existing.Models {
			modelsSeen[name] = struct{}{}
		}
		for _, name := range file.Models {
			if _, dup := modelsSeen[name]; dup {
				continue
			}
			modelsSeen[name] = struct{}{}
			existing.Models = append(existing.Models, name)
		}
		sort.Strings(existing.Models)
	}

	result := make([]claudeSessionUsage, 0, len(order))
	for _, id := range order {
		if len(merged[id].Invocations) > 0 {
			rebuildClaudeInvocationTotals(merged[id])
		}
		result = append(result, *merged[id])
	}
	sort.Slice(result, func(i, j int) bool { return result[i].SessionID < result[j].SessionID })
	return result
}

func mergeClaudeInvocations(existing, next []claudeInvocationUsage) []claudeInvocationUsage {
	merged := make([]claudeInvocationUsage, 0, len(existing)+len(next))
	indexes := map[string]int{}
	add := func(invocation claudeInvocationUsage) {
		key := strings.TrimSpace(invocation.InvocationID)
		if key == "" {
			return
		}
		if index, ok := indexes[key]; ok {
			if invocation.Usage.TotalTokens() > merged[index].Usage.TotalTokens() {
				merged[index] = invocation
			}
			return
		}
		indexes[key] = len(merged)
		merged = append(merged, invocation)
	}
	for _, invocation := range existing {
		add(invocation)
	}
	for _, invocation := range next {
		add(invocation)
	}
	sort.SliceStable(merged, func(i, j int) bool {
		if !merged[i].ObservedAt.Equal(merged[j].ObservedAt) {
			return merged[i].ObservedAt.Before(merged[j].ObservedAt)
		}
		return merged[i].InvocationID < merged[j].InvocationID
	})
	return merged
}

func rebuildClaudeInvocationTotals(session *claudeSessionUsage) {
	session.ByModelDay = map[claudeModelDayKey]claudeAssistantUsage{}
	session.Total = claudeAssistantUsage{}
	for _, invocation := range session.Invocations {
		day := ""
		if !invocation.ObservedAt.IsZero() {
			day = invocation.ObservedAt.UTC().Format("2006-01-02")
		}
		key := claudeModelDayKey{Model: invocation.Model, Day: day}
		session.ByModelDay[key] = session.ByModelDay[key].add(invocation.Usage)
		session.Total = session.Total.add(invocation.Usage)
	}
}

func mergeClaudeTurns(existing, next []claudeTurnUsage) []claudeTurnUsage {
	merged := make([]claudeTurnUsage, 0, len(existing)+len(next))
	indexes := map[string]int{}
	add := func(turn claudeTurnUsage) {
		key := strings.TrimSpace(turn.TurnID)
		if key == "" {
			return
		}
		if index, ok := indexes[key]; ok {
			if turn.Usage.TotalTokens() > merged[index].Usage.TotalTokens() ||
				(turn.Usage.TotalTokens() == merged[index].Usage.TotalTokens() && turn.Status == "completed") {
				merged[index] = turn
			}
			return
		}
		indexes[key] = len(merged)
		merged = append(merged, turn)
	}
	for _, turn := range existing {
		add(turn)
	}
	for _, turn := range next {
		add(turn)
	}
	sort.SliceStable(merged, func(i, j int) bool {
		if !merged[i].EndedAt.Equal(merged[j].EndedAt) {
			return merged[i].EndedAt.Before(merged[j].EndedAt)
		}
		return merged[i].TurnID < merged[j].TurnID
	})
	return merged
}

func mergeClaudeMCPToolCalls(existing, next []claudeTranscriptToolCall) []claudeTranscriptToolCall {
	if len(existing) == 0 && len(next) == 0 {
		return nil
	}
	merged := make([]claudeTranscriptToolCall, 0, len(existing)+len(next))
	indexes := map[string]int{}
	add := func(call claudeTranscriptToolCall) {
		key := claudeMCPToolCallKey(call)
		if index, duplicate := indexes[key]; duplicate {
			current := merged[index]
			if current.Timestamp == "" && call.Timestamp != "" {
				merged[index] = call
			}
			return
		}
		indexes[key] = len(merged)
		merged = append(merged, call)
	}
	for _, call := range existing {
		add(call)
	}
	for _, call := range next {
		add(call)
	}
	sort.SliceStable(merged, func(i, j int) bool {
		if merged[i].Timestamp != merged[j].Timestamp {
			return merged[i].Timestamp < merged[j].Timestamp
		}
		if merged[i].CallID != merged[j].CallID {
			return merged[i].CallID < merged[j].CallID
		}
		if merged[i].Name != merged[j].Name {
			return merged[i].Name < merged[j].Name
		}
		return merged[i].BlockIndex < merged[j].BlockIndex
	})
	return merged
}

func addClaudeTranscriptCandidate(
	candidates *[]claudeTranscriptCandidate,
	indexes map[string]int,
	role, text, timestamp, modelName, messageID, summaryPhase string,
) {
	role = strings.ToLower(strings.TrimSpace(role))
	text = strings.TrimSpace(text)
	if (role != "user" && role != "assistant") || text == "" || isCodexNonChatText(text) {
		return
	}
	if role == "assistant" {
		summaryPhase = normalizeTranscriptSummaryPhase(summaryPhase)
	} else {
		summaryPhase = ""
	}
	candidate := claudeTranscriptCandidate{
		Role:         role,
		Text:         text,
		Timestamp:    strings.TrimSpace(timestamp),
		Model:        strings.TrimSpace(modelName),
		MessageID:    strings.TrimSpace(messageID),
		SummaryPhase: summaryPhase,
	}
	key := strings.Join([]string{
		candidate.Role,
		candidate.MessageID,
		candidate.Timestamp,
		candidate.Text,
	}, "\x00")
	if candidate.MessageID != "" {
		key = candidate.Role + "\x00" + candidate.MessageID
	}
	if index, ok := indexes[key]; ok {
		existing := (*candidates)[index]
		candidateRank := transcriptSummaryPhaseRank(candidate.SummaryPhase)
		existingRank := transcriptSummaryPhaseRank(existing.SummaryPhase)
		if candidateRank > existingRank ||
			(candidateRank == existingRank && len(candidate.Text) >= len(existing.Text)) {
			(*candidates)[index] = candidate
		}
		return
	}
	indexes[key] = len(*candidates)
	*candidates = append(*candidates, candidate)
}

func claudeTranscriptMessagesFromCandidates(candidates []claudeTranscriptCandidate) ([]map[string]interface{}, bool) {
	var messages []map[string]interface{}
	totalBytes := 0
	truncated := false
	for _, candidate := range candidates {
		text := privacy.RedactAndTruncate(candidate.Text, codexMaxTranscriptMessageBytes)
		text = strings.TrimSpace(text)
		if text == "" || isCodexNonChatText(text) {
			continue
		}
		message := map[string]interface{}{
			"role": candidate.Role,
			"text": text,
		}
		if candidate.MessageID != "" {
			message["message_id"] = candidate.MessageID
		}
		if candidate.Timestamp != "" {
			message["timestamp"] = candidate.Timestamp
		}
		if candidate.Role == "assistant" && candidate.Model != "" {
			message["model"] = candidate.Model
		}
		if candidate.Role == "assistant" {
			message["summary_phase"] = normalizeTranscriptSummaryPhase(candidate.SummaryPhase)
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
	return messages, truncated
}

func cloneClaudeTranscriptMessages(messages []map[string]interface{}) []map[string]interface{} {
	if len(messages) == 0 {
		return nil
	}
	out := make([]map[string]interface{}, 0, len(messages))
	for _, message := range messages {
		clone := make(map[string]interface{}, len(message))
		for key, value := range message {
			clone[key] = value
		}
		out = append(out, clone)
	}
	return out
}

func mergeClaudeTranscriptMessages(existing, next []map[string]interface{}, alreadyTruncated bool) ([]map[string]interface{}, bool) {
	combined := append(cloneClaudeTranscriptMessages(existing), cloneClaudeTranscriptMessages(next)...)
	sort.SliceStable(combined, func(i, j int) bool {
		left, leftOK := parseClaudeTimestamp(firstString(combined[i], "timestamp"))
		right, rightOK := parseClaudeTimestamp(firstString(combined[j], "timestamp"))
		if leftOK && rightOK && !left.Equal(right) {
			return left.Before(right)
		}
		if leftOK != rightOK {
			return leftOK
		}
		return i < j
	})
	var candidates []claudeTranscriptCandidate
	indexes := map[string]int{}
	for _, message := range combined {
		addClaudeTranscriptCandidate(
			&candidates,
			indexes,
			firstString(message, "role"),
			firstString(message, "text"),
			firstString(message, "timestamp"),
			firstString(message, "model"),
			firstString(message, "message_id"),
			firstString(message, "summary_phase"),
		)
	}
	messages, truncated := claudeTranscriptMessagesFromCandidates(candidates)
	return messages, alreadyTruncated || truncated
}
