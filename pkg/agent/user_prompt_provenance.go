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
	"time"
)

const (
	codexPromptProvenanceTailBytes int64 = 8 * 1024 * 1024
	codexPromptPersistenceWait           = 3 * time.Second
	codexPromptPersistencePoll           = 50 * time.Millisecond
)

var errCodexPromptNotPersisted = errors.New("matching user prompt is not persisted for this turn")

// verifyUserPromptSubmission ensures a Codex hook is backed by the same
// user-authored message in the rollout transcript. This rejects internal or
// synthetic UserPromptSubmit payloads that happen to reuse a real turn ID.
func verifyUserPromptSubmission(ctx context.Context, event agentHookEvent, agentType, userPrompt string) error {
	if !strings.EqualFold(strings.TrimSpace(agentType), "codex") {
		return nil
	}
	if strings.TrimSpace(event.TranscriptPath) == "" {
		return errors.New("Codex UserPromptSubmit is missing transcript_path")
	}
	if strings.TrimSpace(event.TurnID) == "" {
		return errors.New("Codex UserPromptSubmit is missing turn_id")
	}

	deadline := time.NewTimer(codexPromptPersistenceWait)
	defer deadline.Stop()
	ticker := time.NewTicker(codexPromptPersistencePoll)
	defer ticker.Stop()
	var lastReadErr error

	for {
		matched, err := codexTranscriptHasUserPrompt(event.TranscriptPath, event.TurnID, userPrompt)
		if matched {
			return nil
		}
		lastReadErr = err

		select {
		case <-ctx.Done():
			if lastReadErr != nil {
				return lastReadErr
			}
			return fmt.Errorf("%w: %v", errCodexPromptNotPersisted, ctx.Err())
		case <-deadline.C:
			if lastReadErr != nil {
				return lastReadErr
			}
			return errCodexPromptNotPersisted
		case <-ticker.C:
		}
	}
}

func codexTranscriptHasUserPrompt(path, turnID, userPrompt string) (bool, error) {
	data, err := readRecentTranscript(path, codexPromptProvenanceTailBytes)
	if err != nil {
		return false, fmt.Errorf("read Codex transcript: %w", err)
	}
	return scanCodexUserPrompt(data, turnID, userPrompt)
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

func scanCodexUserPrompt(data []byte, turnID, userPrompt string) (bool, error) {
	wanted := normalizePromptForComparison(userPrompt)
	foundTargetTurn := false

	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 64*1024), int(codexPromptProvenanceTailBytes))
	for scanner.Scan() {
		var record codexTranscriptRecord
		if err := json.Unmarshal(scanner.Bytes(), &record); err != nil {
			continue
		}

		if record.Type == "event_msg" && record.Payload.Type == "task_started" {
			if record.Payload.TurnID == turnID {
				foundTargetTurn = true
				continue
			}
			if foundTargetTurn {
				return false, nil
			}
		}
		if !foundTargetTurn {
			continue
		}

		if record.Type == "event_msg" && record.Payload.Type == "user_message" &&
			normalizePromptForComparison(record.Payload.Message) == wanted {
			return true, nil
		}
		if record.Type == "response_item" && record.Payload.Type == "message" && record.Payload.Role == "user" {
			metadataTurnID := record.Payload.Metadata.TurnID
			if metadataTurnID != "" && metadataTurnID != turnID {
				continue
			}
			if normalizePromptForComparison(codexMessageText(record.Payload.Content)) == wanted {
				return true, nil
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return false, fmt.Errorf("scan Codex transcript: %w", err)
	}
	return false, nil
}

type codexTranscriptRecord struct {
	Type    string `json:"type"`
	Payload struct {
		Type    string `json:"type"`
		TurnID  string `json:"turn_id"`
		Role    string `json:"role"`
		Message string `json:"message"`
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		Metadata struct {
			TurnID string `json:"turn_id"`
		} `json:"internal_chat_message_metadata_passthrough"`
	} `json:"payload"`
}

func codexMessageText(content []struct {
	Type string `json:"type"`
	Text string `json:"text"`
}) string {
	parts := make([]string, 0, len(content))
	for _, item := range content {
		if item.Type == "input_text" || item.Type == "text" {
			parts = append(parts, item.Text)
		}
	}
	return strings.Join(parts, "\n")
}

func normalizePromptForComparison(value string) string {
	return strings.TrimSpace(strings.ReplaceAll(value, "\r\n", "\n"))
}
