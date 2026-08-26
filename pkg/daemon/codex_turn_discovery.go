package daemon

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"

	turnusage "github.com/gesta-run/gesta-agent/pkg/turn"
	"github.com/gesta-run/gesta-agent/pkg/util"
)

const codexFallbackTitleScanBytes = 1024 * 1024

var errCodexSelfParent = errors.New("codex fork parent matches canonical session id")

func defaultCodexSessionsRoot() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".codex", "sessions")
}

func readCodexTurnSession(path string) (turnusage.CodexSession, bool, bool, error) {
	file, err := os.Open(path)
	if err != nil {
		return turnusage.CodexSession{}, false, false, err
	}
	defer file.Close()
	reader := bufio.NewReaderSize(file, 64*1024)
	line, err := reader.ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return turnusage.CodexSession{}, false, false, err
	}
	var record struct {
		Type    string `json:"type"`
		Payload struct {
			SessionID     string `json:"session_id"`
			ID            string `json:"id"`
			CWD           string `json:"cwd"`
			Model         string `json:"model"`
			ModelProvider string `json:"model_provider"`
		} `json:"payload"`
	}
	if err := json.Unmarshal([]byte(line), &record); err != nil {
		return turnusage.CodexSession{}, false, false, err
	}
	if record.Type != "session_meta" {
		return turnusage.CodexSession{}, false, false, nil
	}
	rawID, legacyID := codexSessionIdentity(record.Payload.ID, record.Payload.SessionID)
	if rawID == "" {
		return turnusage.CodexSession{}, false, false, nil
	}
	title, titlePending := firstCodexUserTitle(reader)
	session := turnusage.CodexSession{
		SessionID:       util.ShortHash(rawID),
		LegacySessionID: hashOptionalCodexSessionID(legacyID),
		RolloutPath:     path,
		Title:           title,
		Model:           strings.TrimSpace(record.Payload.Model),
		ModelProvider:   strings.TrimSpace(record.Payload.ModelProvider),
	}
	if parentID := codexForkParentFromJSONLine([]byte(line)); parentID != "" {
		if strings.TrimSpace(parentID) == rawID {
			return turnusage.CodexSession{}, false, false, errCodexSelfParent
		}
		session.ParentSessionID = util.ShortHash(parentID)
	}
	if cwd := strings.TrimSpace(record.Payload.CWD); cwd != "" {
		session.Repo = util.ShortHash(cwd)
	}
	return session, true, titlePending, nil
}

func firstCodexUserTitle(reader *bufio.Reader) (string, bool) {
	bytesRead := 0
	for bytesRead < codexFallbackTitleScanBytes {
		line, err := reader.ReadString('\n')
		bytesRead += len(line)
		title, turnStarted := codexUserTitleStateFromRecord(line)
		if title != "" {
			return title, false
		}
		if turnStarted {
			return "", false
		}
		if err != nil {
			return "", errors.Is(err, io.EOF)
		}
	}
	return "", false
}

func codexUserTitleFromRecord(line string) string {
	title, _ := codexUserTitleStateFromRecord(line)
	return title
}

func codexUserTitleStateFromRecord(line string) (string, bool) {
	var record struct {
		Type    string                 `json:"type"`
		Payload map[string]interface{} `json:"payload"`
	}
	if json.Unmarshal([]byte(line), &record) != nil || record.Payload == nil {
		return "", false
	}
	payloadType := strings.ToLower(strings.TrimSpace(firstString(record.Payload, "type")))
	turnStarted := record.Type == "turn_context" || (record.Type == "event_msg" && payloadType == "task_started")
	text := ""
	if record.Type == "event_msg" && payloadType == "user_message" {
		text = firstString(record.Payload, "message")
	}
	if record.Type == "response_item" && payloadType == "message" && strings.EqualFold(firstString(record.Payload, "role"), "user") {
		text = codexContentText(record.Payload["content"])
	}
	return cleanCodexFallbackTitle(text), turnStarted
}

func cleanCodexFallbackTitle(value string) string {
	if marker := strings.LastIndex(value, "## My request:"); marker >= 0 {
		value = value[marker+len("## My request:"):]
	}
	value = strings.Join(strings.Fields(value), " ")
	runes := []rune(value)
	if len(runes) > 120 {
		value = string(runes[:120])
	}
	return strings.TrimSpace(value)
}
