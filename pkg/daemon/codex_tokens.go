package daemon

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"os"
)

const codexTokenUsageMaxLineBytes = 4 * 1024 * 1024

type codexTokenUsage struct {
	InputTokens       int64
	CachedInputTokens int64
	OutputTokens      int64
	TotalTokens       int64
}

func (u codexTokenUsage) EffectiveTokens() int64 {
	input := u.InputTokens - u.CachedInputTokens
	if input < 0 {
		input = 0
	}
	total := input + u.OutputTokens
	if total > 0 {
		return total
	}
	return u.TotalTokens
}

func codexEffectiveTokenUsage(path string) (codexTokenUsage, bool) {
	file, err := os.Open(path)
	if err != nil {
		return codexTokenUsage{}, false
	}
	defer file.Close()

	reader := bufio.NewReaderSize(file, 64*1024)
	pattern := []byte(`"token_count"`)
	line := make([]byte, 0, 64*1024)
	lineTooLarge := false
	var latest codexTokenUsage
	found := false

	processLine := func() {
		if lineTooLarge || !bytes.Contains(line, pattern) {
			return
		}
		usage, ok := codexTokenUsageFromJSONLine(string(line))
		if ok {
			latest = usage
			found = true
		}
	}

	for {
		chunk, err := reader.ReadSlice('\n')
		if len(chunk) > 0 && !lineTooLarge {
			if len(line)+len(chunk) <= codexTokenUsageMaxLineBytes {
				line = append(line, chunk...)
			} else {
				lineTooLarge = true
				line = line[:0]
			}
		}
		if err == nil {
			processLine()
			line = line[:0]
			lineTooLarge = false
			continue
		}
		if err == bufio.ErrBufferFull {
			continue
		}
		if err == io.EOF {
			processLine()
			break
		}
		break
	}
	if !found || latest.TotalTokens <= 0 {
		return codexTokenUsage{}, false
	}
	return latest, true
}

func codexForkParentFromRollout(path string) string {
	file, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer file.Close()

	reader := bufio.NewReaderSize(file, 64*1024)
	forkPattern := []byte(`"forked_from`)
	parentPattern := []byte(`"parent_thread_id"`)
	line := make([]byte, 0, 64*1024)
	lineTooLarge := false
	parentID := ""

	processLine := func() {
		if parentID != "" || lineTooLarge || (!bytes.Contains(line, forkPattern) && !bytes.Contains(line, parentPattern)) {
			return
		}
		parentID = codexForkParentFromJSONLine(line)
	}

	for {
		chunk, err := reader.ReadSlice('\n')
		if len(chunk) > 0 && !lineTooLarge {
			if len(line)+len(chunk) <= codexTokenUsageMaxLineBytes {
				line = append(line, chunk...)
			} else {
				lineTooLarge = true
				line = line[:0]
			}
		}
		if err == nil {
			processLine()
			if parentID != "" {
				break
			}
			line = line[:0]
			lineTooLarge = false
			continue
		}
		if err == bufio.ErrBufferFull {
			continue
		}
		if err == io.EOF {
			processLine()
		}
		break
	}
	return parentID
}

func codexTokenUsageFromJSONLine(line string) (codexTokenUsage, bool) {
	var entry struct {
		Type    string `json:"type"`
		Payload struct {
			Type string `json:"type"`
			Info struct {
				TotalTokenUsage struct {
					InputTokens       int64 `json:"input_tokens"`
					CachedInputTokens int64 `json:"cached_input_tokens"`
					OutputTokens      int64 `json:"output_tokens"`
					TotalTokens       int64 `json:"total_tokens"`
				} `json:"total_token_usage"`
			} `json:"info"`
		} `json:"payload"`
	}
	if err := json.Unmarshal([]byte(line), &entry); err != nil {
		return codexTokenUsage{}, false
	}
	if entry.Type != "event_msg" || entry.Payload.Type != "token_count" {
		return codexTokenUsage{}, false
	}
	usage := entry.Payload.Info.TotalTokenUsage
	if usage.TotalTokens <= 0 {
		return codexTokenUsage{}, false
	}
	return codexTokenUsage{
		InputTokens:       usage.InputTokens,
		CachedInputTokens: usage.CachedInputTokens,
		OutputTokens:      usage.OutputTokens,
		TotalTokens:       usage.TotalTokens,
	}, true
}

func codexForkParentFromJSONLine(line []byte) string {
	var entry struct {
		Type    string `json:"type"`
		Payload struct {
			ForkedFromID string `json:"forked_from_id"`
			ForkedFrom   string `json:"forked_from"`
			Source       struct {
				Subagent struct {
					ThreadSpawn struct {
						ParentThreadID string `json:"parent_thread_id"`
					} `json:"thread_spawn"`
				} `json:"subagent"`
			} `json:"source"`
		} `json:"payload"`
	}
	if err := json.Unmarshal(line, &entry); err != nil {
		return ""
	}
	if entry.Type != "session_meta" {
		return ""
	}
	return firstNonEmptyString(
		entry.Payload.ForkedFromID,
		entry.Payload.ForkedFrom,
		entry.Payload.Source.Subagent.ThreadSpawn.ParentThreadID,
	)
}
