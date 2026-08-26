package turn

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
	"time"

	"github.com/gesta-run/gesta-agent/pkg/util"
)

const codexSeedTailBytes int64 = 8 * 1024 * 1024

func CollectCodex(cfg Config, sessions []CodexSession, observedAt time.Time) ([]Usage, func() error, error) {
	if observedAt.IsZero() {
		observedAt = time.Now().UTC()
	}
	store, err := loadCursorStore(cfg.DataDir)
	if err != nil {
		return nil, nil, err
	}
	next := cloneCursorStore(store)
	firstCollection := strings.TrimSpace(store.InitializedAt) == ""
	initializedAt, _ := time.Parse(time.RFC3339Nano, store.InitializedAt)
	identityCutoverAt, _ := time.Parse(time.RFC3339Nano, store.CodexIdentityCutoverAt)
	if identityCutoverAt.IsZero() {
		identityCutoverAt = observedAt
		next.CodexIdentityCutoverAt = observedAt.UTC().Format(time.RFC3339Nano)
	}
	sessionsByID := make(map[string]CodexSession, len(sessions))
	for _, session := range sessions {
		if session.SessionID != "" && session.ParentSessionID == session.SessionID {
			continue
		}
		sessionsByID[session.SessionID] = session
	}
	inheritedByParent := map[string]map[string]struct{}{}
	var events []Usage
	for _, session := range sessions {
		session.TotalEncoding = cfg.TotalEncoding
		session.OnCounterReset = cfg.OnCounterReset
		if strings.TrimSpace(session.SessionID) == "" || strings.TrimSpace(session.RolloutPath) == "" || session.ParentSessionID == session.SessionID {
			continue
		}
		if _, statErr := os.Stat(session.RolloutPath); statErr != nil {
			continue
		}
		pathHash := util.HashString(session.RolloutPath)
		cursor, exists := next.Sessions[session.SessionID]
		if !firstCollection && !exists {
			cursor, exists, err = migrateCodexIdentityCursor(next, session, pathHash, identityCutoverAt, observedAt)
		}
		if firstCollection || !exists {
			if firstCollection {
				cursor, err = seedCodexCursor(session.RolloutPath, pathHash, observedAt)
			} else if !codexRolloutStartedAtOrAfter(session.RolloutPath, initializedAt) {
				cursor, err = seedCodexCursor(session.RolloutPath, pathHash, observedAt)
			} else {
				if session.ParentSessionID != "" {
					var parentFound bool
					session.InheritedTurnIDHashes, parentFound, err = inheritedCodexTurnIDHashes(
						session.ParentSessionID,
						sessionsByID,
						inheritedByParent,
					)
					if err == nil && !parentFound {
						cursor, err = seedCodexCursor(session.RolloutPath, pathHash, observedAt)
					}
				}
				if err == nil && cursor.RolloutPathHash == "" {
					cursor = Cursor{RolloutPathHash: pathHash}
					var collected []Usage
					cursor, collected, err = scanCodex(session, cfg.DaemonID, cursor, observedAt, true)
					events = append(events, collected...)
				}
			}
		} else {
			info, statErr := os.Stat(session.RolloutPath)
			if statErr != nil {
				continue
			}
			if cursor.RolloutPathHash != pathHash || info.Size() < cursor.Offset {
				cursor, err = seedCodexCursor(session.RolloutPath, pathHash, observedAt)
			} else {
				var collected []Usage
				cursor, collected, err = scanCodex(session, cfg.DaemonID, cursor, observedAt, true)
				events = append(events, collected...)
			}
		}
		if err != nil {
			return nil, nil, err
		}
		next.Sessions[session.SessionID] = cursor
		if legacyCursor, ok := next.Sessions[session.LegacySessionID]; session.LegacySessionID != "" && ok && legacyCursor.RolloutPathHash == pathHash {
			next.Sessions[session.LegacySessionID] = cursor
		}
	}
	if firstCollection {
		next.InitializedAt = observedAt.UTC().Format(time.RFC3339Nano)
	}
	commit := func() error { return saveCursorStore(cfg.DataDir, next) }
	return events, commit, nil
}

func migrateCodexIdentityCursor(
	store cursorStore,
	session CodexSession,
	pathHash string,
	cutoverAt, observedAt time.Time,
) (Cursor, bool, error) {
	if session.LegacySessionID == "" || session.LegacySessionID == session.SessionID {
		return Cursor{}, false, nil
	}
	if legacy, ok := store.Sessions[session.LegacySessionID]; ok && legacy.RolloutPathHash == pathHash {
		info, err := os.Stat(session.RolloutPath)
		if err != nil {
			return Cursor{}, false, err
		}
		if info.Size() >= legacy.Offset {
			return legacy, true, nil
		}
	}
	if codexRolloutStartedAtOrAfter(session.RolloutPath, cutoverAt) {
		return Cursor{}, false, nil
	}
	// Multiple old forks may share one legacy key, so only an exact path match
	// is safe to migrate. Seed every other pre-cutover rollout without emission.
	cursor, err := seedCodexCursor(session.RolloutPath, pathHash, observedAt)
	return cursor, err == nil, err
}

func inheritedCodexTurnIDHashes(
	parentSessionID string,
	sessionsByID map[string]CodexSession,
	cache map[string]map[string]struct{},
) (map[string]struct{}, bool, error) {
	if inherited, ok := cache[parentSessionID]; ok {
		return inherited, true, nil
	}
	parent, ok := sessionsByID[parentSessionID]
	if !ok || strings.TrimSpace(parent.RolloutPath) == "" {
		return nil, false, nil
	}
	inherited, err := completedCodexTurnIDHashes(parent.RolloutPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, false, nil
		}
		return nil, false, err
	}
	cache[parentSessionID] = inherited
	return inherited, true, nil
}

func completedCodexTurnIDHashes(path string) (map[string]struct{}, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	completed := map[string]struct{}{}
	reader := bufio.NewReaderSize(file, 64*1024)
	activeTurnID := ""
	for {
		line, readErr := reader.ReadString('\n')
		if readErr != nil && !errors.Is(readErr, io.EOF) {
			return nil, readErr
		}
		if strings.TrimSpace(line) != "" {
			var record codexRecord
			if json.Unmarshal([]byte(line), &record) == nil && record.Type == "event_msg" {
				switch strings.ToLower(strings.TrimSpace(stringValue(record.Payload, "type"))) {
				case "task_started":
					activeTurnID = stringValue(record.Payload, "turn_id")
				case "task_complete", "turn_aborted":
					turnID := stringValue(record.Payload, "turn_id")
					if turnID == "" {
						turnID = activeTurnID
					}
					if turnID != "" && (activeTurnID == "" || turnID == activeTurnID) {
						completed[util.HashString(turnID)] = struct{}{}
					}
					activeTurnID = ""
				}
			}
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
	}
	return completed, nil
}

func codexRolloutStartedAtOrAfter(path string, cutover time.Time) bool {
	if cutover.IsZero() {
		return false
	}
	file, err := os.Open(path)
	if err != nil {
		return false
	}
	defer file.Close()
	line, err := bufio.NewReaderSize(file, 64*1024).ReadString('\n')
	if err != nil {
		return false
	}
	var record codexRecord
	if json.Unmarshal([]byte(line), &record) != nil {
		return false
	}
	startedAt, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(record.Timestamp))
	return err == nil && !startedAt.Before(cutover)
}

func seedCodexCursor(path, pathHash string, observedAt time.Time) (Cursor, error) {
	session := CodexSession{SessionID: "seed", RolloutPath: path}
	info, err := os.Stat(path)
	if err != nil {
		return Cursor{}, err
	}
	offset := info.Size() - codexSeedTailBytes
	if offset < 0 {
		offset = 0
	}
	cursor, _, err := scanCodex(session, "", Cursor{
		RolloutPathHash:  pathHash,
		Offset:           offset,
		AwaitingBaseline: true,
	}, observedAt, false)
	if err != nil {
		return Cursor{}, err
	}
	if cursor.Active != nil {
		cursor.Active.Baseline = cursor.LastTokens
		cursor.Active.Latest = cursor.LastTokens
	}
	return cursor, nil
}

func scanCodex(session CodexSession, daemonID string, cursor Cursor, observedAt time.Time, emit bool) (Cursor, []Usage, error) {
	file, err := os.Open(session.RolloutPath)
	if err != nil {
		return cursor, nil, err
	}
	defer file.Close()
	if _, err := file.Seek(cursor.Offset, io.SeekStart); err != nil {
		return cursor, nil, err
	}
	reader := bufio.NewReaderSize(file, 64*1024)
	var events []Usage
	for {
		line, readErr := reader.ReadString('\n')
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				break
			}
			return cursor, events, readErr
		}
		cursor.Offset += int64(len(line))
		var record codexRecord
		if json.Unmarshal([]byte(line), &record) != nil || record.Payload == nil {
			continue
		}
		usage, ok := processCodexRecord(session, daemonID, &cursor, record, observedAt, emit)
		if ok {
			events = append(events, usage)
		}
	}
	return cursor, events, nil
}
