package agent

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/gesta-run/gesta-agent/pkg/promptscope"
)

const codexPromptProvenanceTailBytes int64 = 8 * 1024 * 1024

var (
	errCodexPromptMismatch = errors.New("codex user prompt does not match the active turn")
	errCodexTurnNotActive  = errors.New("codex turn is not active")
)

// verifyUserPromptSubmission ensures a Codex hook belongs to the active rollout
// turn. Codex runs UserPromptSubmit before it persists the current user message,
// so an active turn without a persisted prompt is valid. Once the canonical
// user_message exists, it must match the hook payload.
func verifyUserPromptSubmission(_ context.Context, event agentHookEvent, agentType, userPrompt string) error {
	_, err := verifyUserPromptSubmissionWithHistory(event, agentType, userPrompt, 0)
	return err
}

func verifyUserPromptSubmissionWithHistory(
	event agentHookEvent,
	agentType, userPrompt string,
	historyLimit int,
) ([]string, error) {
	if !strings.EqualFold(strings.TrimSpace(agentType), "codex") {
		return nil, nil
	}
	if strings.TrimSpace(event.TranscriptPath) == "" {
		return nil, errors.New("codex UserPromptSubmit is missing transcript_path")
	}
	if strings.TrimSpace(event.TurnID) == "" {
		return nil, errors.New("codex UserPromptSubmit is missing turn_id")
	}

	state, err := codexTranscriptPromptStateWithHistory(
		event.TranscriptPath,
		event.TurnID,
		userPrompt,
		historyLimit,
	)
	if err != nil {
		return nil, err
	}
	if !state.turnFound || state.turnComplete || state.turnSuperseded {
		return nil, errCodexTurnNotActive
	}
	if state.canonicalPromptSeen && !state.canonicalPromptMatched {
		return nil, errCodexPromptMismatch
	}
	return state.recentUserPrompts, nil
}

func codexTranscriptPromptState(path, turnID, userPrompt string) (codexPromptState, error) {
	return codexTranscriptPromptStateWithHistory(path, turnID, userPrompt, 0)
}

func codexTranscriptPromptStateWithHistory(
	path, turnID, userPrompt string,
	historyLimit int,
) (codexPromptState, error) {
	data, err := readRecentTranscript(path, codexPromptProvenanceTailBytes)
	if err != nil {
		return codexPromptState{}, fmt.Errorf("read Codex transcript: %w", err)
	}
	return scanCodexUserPromptWithHistory(data, turnID, userPrompt, historyLimit)
}

func readRecentTranscript(path string, limit int64) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	start := int64(0)
	if info.Size() > limit {
		start = info.Size() - limit
	}
	data := make([]byte, info.Size()-start)
	if len(data) > 0 {
		if _, err := file.ReadAt(data, start); err != nil {
			return nil, err
		}
	}
	if start > 0 {
		if newline := bytes.IndexByte(data, '\n'); newline >= 0 {
			data = data[newline+1:]
		} else {
			return nil, nil
		}
	}
	return data, nil
}

type codexPromptState struct {
	turnFound              bool
	turnComplete           bool
	turnSuperseded         bool
	canonicalPromptSeen    bool
	canonicalPromptMatched bool
	recentUserPrompts      []string
}

func scanCodexUserPromptWithHistory(
	data []byte,
	turnID, userPrompt string,
	historyLimit int,
) (codexPromptState, error) {
	wanted := normalizePromptForComparison(userPrompt)
	state := codexPromptState{}

	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 64*1024), int(codexPromptProvenanceTailBytes))
	for scanner.Scan() {
		var record codexTranscriptRecord
		if err := json.Unmarshal(scanner.Bytes(), &record); err != nil {
			continue
		}

		if codexTranscriptTurnStart(record) {
			if record.Payload.TurnID == turnID {
				state.turnFound = true
				continue
			}
			if state.turnFound {
				state.turnSuperseded = true
			}
		}
		if !state.turnFound {
			if historyLimit > 0 && record.Type == "event_msg" && record.Payload.Type == "user_message" {
				prompt := strings.TrimSpace(promptscope.Extract("codex", record.Payload.Message))
				if prompt != "" {
					state.recentUserPrompts = append(state.recentUserPrompts, prompt)
					if len(state.recentUserPrompts) > historyLimit {
						state.recentUserPrompts = state.recentUserPrompts[1:]
					}
				}
			}
			continue
		}
		if state.turnSuperseded {
			continue
		}

		if record.Type == "event_msg" && record.Payload.Type == "task_complete" &&
			record.Payload.TurnID == turnID {
			state.turnComplete = true
			continue
		}
		if record.Type == "event_msg" && record.Payload.Type == "user_message" {
			state.canonicalPromptSeen = true
			if normalizePromptForComparison(record.Payload.Message) == wanted {
				state.canonicalPromptMatched = true
			}
			continue
		}
	}
	if err := scanner.Err(); err != nil {
		return codexPromptState{}, fmt.Errorf("scan Codex transcript: %w", err)
	}
	return state, nil
}

func codexTranscriptTurnStart(record codexTranscriptRecord) bool {
	return record.Type == "turn_context" ||
		(record.Type == "event_msg" && record.Payload.Type == "task_started")
}

type codexTranscriptRecord struct {
	Type    string `json:"type"`
	Payload struct {
		Type    string `json:"type"`
		TurnID  string `json:"turn_id"`
		Message string `json:"message"`
	} `json:"payload"`
}

func normalizePromptForComparison(value string) string {
	return strings.TrimSpace(strings.ReplaceAll(value, "\r\n", "\n"))
}
