package activitydetail

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/gesta-run/gesta-agent/pkg/atomicfile"
	"github.com/gesta-run/gesta-agent/pkg/turnreceipt"
	"github.com/gesta-run/gesta-agent/pkg/util"
)

const (
	schemaVersion          = 2
	maxActivityDetails     = 256
	maxActivityRecordBytes = 64 * 1024
	maxCleanupVisits       = 128
	maxCleanupRemovals     = 32
	detailTTL              = 24 * time.Hour
	lockWait               = 2 * time.Second
	lockStaleAfter         = time.Minute
	lockPollInterval       = 10 * time.Millisecond
)

var (
	ErrNotFound     = errors.New("activity detail not found")
	validActivityID = regexp.MustCompile(`^activity_[0-9a-f]{32}$`)
)

type Detail struct {
	SchemaVersion  int                            `json:"schema_version"`
	ActivityID     string                         `json:"activity_id"`
	CreatedAt      time.Time                      `json:"created_at"`
	ExpiresAt      time.Time                      `json:"expires_at"`
	AgentType      string                         `json:"agent_type"`
	ContextMatches []turnreceipt.ContextRuleMatch `json:"context_matches"`
	Output         turnreceipt.OutputSummary      `json:"output"`
}

type Store struct {
	dataDir string
	now     func() time.Time
	newID   func(string) string
}

func NewStore(dataDir string) Store {
	return Store{
		dataDir: strings.TrimSpace(dataDir),
		now:     func() time.Time { return time.Now().UTC() },
		newID:   util.NewID,
	}
}

func (s Store) Create(
	agentType string,
	matches []turnreceipt.ContextRuleMatch,
	output turnreceipt.OutputSummary,
) (Detail, error) {
	agentType = normalizeAgentType(agentType)
	matches = turnreceipt.NormalizeContextMatches(matches)
	if s.dataDir == "" || agentType == "" || len(matches) == 0 {
		return Detail{}, errors.New("activity detail requires a data directory, agent type, and context matches")
	}
	now := s.now()
	detail := Detail{
		SchemaVersion:  schemaVersion,
		ActivityID:     s.newID("activity"),
		CreatedAt:      now,
		ExpiresAt:      now.Add(detailTTL),
		AgentType:      agentType,
		ContextMatches: matches,
		Output:         output,
	}
	if !validActivityID.MatchString(detail.ActivityID) {
		return Detail{}, errors.New("generated activity ID is invalid")
	}
	unlock, err := s.acquireLock(true)
	if err != nil {
		return Detail{}, err
	}
	defer unlock()
	if err := s.write(detail); err != nil {
		return Detail{}, err
	}
	if err := s.ensureCapacityLocked(maxActivityDetails, detail.ActivityID+".json"); err != nil {
		_ = os.Remove(filepath.Join(s.rootPath(), detail.ActivityID+".json"))
		return Detail{}, err
	}
	return detail, nil
}

func (s Store) Get(activityID string) (Detail, error) {
	activityID = strings.TrimSpace(activityID)
	if s.dataDir == "" || !validActivityID.MatchString(activityID) {
		return Detail{}, ErrNotFound
	}
	path := filepath.Join(s.rootPath(), activityID+".json")
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return Detail{}, ErrNotFound
	}
	if err != nil {
		return Detail{}, fmt.Errorf("open activity detail: %w", err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return Detail{}, fmt.Errorf("stat activity detail: %w", err)
	}
	if info.Size() <= 0 || info.Size() > maxActivityRecordBytes {
		return Detail{}, ErrNotFound
	}
	var detail Detail
	decoder := json.NewDecoder(io.LimitReader(file, maxActivityRecordBytes+1))
	if err := decoder.Decode(&detail); err != nil {
		return Detail{}, ErrNotFound
	}
	detail.ContextMatches = turnreceipt.NormalizeContextMatches(detail.ContextMatches)
	if detail.SchemaVersion != schemaVersion ||
		detail.ActivityID != activityID ||
		normalizeAgentType(detail.AgentType) == "" ||
		len(detail.ContextMatches) == 0 ||
		!detail.ExpiresAt.After(s.now()) {
		return Detail{}, ErrNotFound
	}
	detail.AgentType = normalizeAgentType(detail.AgentType)
	return detail, nil
}

func (s Store) Cleanup() error {
	if s.dataDir == "" {
		return nil
	}
	unlock, err := s.acquireLock(false)
	if errors.Is(err, os.ErrExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer unlock()
	if err := s.cleanupExpiredLocked(); err != nil {
		return err
	}
	return s.ensureCapacityLocked(maxActivityDetails, "")
}

func (s Store) write(detail Detail) error {
	if err := os.MkdirAll(s.rootPath(), 0o700); err != nil {
		return fmt.Errorf("create activity detail directory: %w", err)
	}
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(detail); err != nil {
		return fmt.Errorf("marshal activity detail: %w", err)
	}
	data := buffer.Bytes()
	if len(data) > maxActivityRecordBytes {
		return fmt.Errorf("activity detail exceeds %d bytes", maxActivityRecordBytes)
	}
	path := filepath.Join(s.rootPath(), detail.ActivityID+".json")
	if _, err := os.Stat(path); err == nil {
		return errors.New("activity detail already exists")
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("stat activity detail destination: %w", err)
	}
	if err := atomicfile.Write(path, data); err != nil {
		return fmt.Errorf("write activity detail: %w", err)
	}
	return nil
}

func (s Store) cleanupExpiredLocked() error {
	entries, err := s.readEntries(maxActivityDetails + 1)
	if errors.Is(err, os.ErrNotExist) {
		return s.clearCursor()
	}
	if err != nil {
		return err
	}
	after := s.readCursor()
	visits := 0
	removals := 0
	nextCursor := ""
	complete := true
	for _, entry := range entries {
		if entry.Name() <= after || entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		if entry.Name() == filepath.Base(s.cursorPath()) {
			continue
		}
		visits++
		nextCursor = entry.Name()
		path := filepath.Join(s.rootPath(), entry.Name())
		if s.entryExpiredOrInvalid(path, entry) {
			if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
				return fmt.Errorf("remove expired activity detail: %w", err)
			}
			removals++
		}
		if visits >= maxCleanupVisits || removals >= maxCleanupRemovals {
			complete = false
			break
		}
	}
	if complete {
		return s.clearCursor()
	}
	return s.writeCursor(nextCursor)
}

func (s Store) ensureCapacityLocked(limit int, protectedName string) error {
	entries, err := s.readEntries(maxActivityDetails + maxCleanupRemovals + 1)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	type candidate struct {
		name    string
		modTime time.Time
	}
	detailEntries := make([]os.DirEntry, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		detailEntries = append(detailEntries, entry)
	}
	if len(detailEntries) <= limit {
		return nil
	}

	candidates := make([]candidate, 0, len(detailEntries))
	for _, entry := range detailEntries {
		info, err := entry.Info()
		if err != nil {
			continue
		}
		candidates = append(candidates, candidate{name: entry.Name(), modTime: info.ModTime()})
	}
	if len(candidates) <= limit {
		return nil
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].name == protectedName {
			return false
		}
		if candidates[j].name == protectedName {
			return true
		}
		if candidates[i].modTime.Equal(candidates[j].modTime) {
			return candidates[i].name < candidates[j].name
		}
		return candidates[i].modTime.Before(candidates[j].modTime)
	})
	removeCount := len(candidates) - limit
	if removeCount > maxCleanupRemovals {
		removeCount = maxCleanupRemovals
	}
	for _, candidate := range candidates[:removeCount] {
		if err := os.Remove(filepath.Join(s.rootPath(), candidate.name)); err != nil &&
			!errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("remove activity detail over capacity: %w", err)
		}
	}
	return nil
}

func (s Store) entryExpiredOrInvalid(path string, entry os.DirEntry) bool {
	file, err := os.Open(path)
	if err == nil {
		defer file.Close()
		info, statErr := file.Stat()
		if statErr != nil || info.Size() <= 0 || info.Size() > maxActivityRecordBytes {
			return true
		}
		var detail Detail
		decoder := json.NewDecoder(io.LimitReader(file, maxActivityRecordBytes+1))
		if decodeErr := decoder.Decode(&detail); decodeErr == nil &&
			detail.SchemaVersion == schemaVersion &&
			!detail.ExpiresAt.IsZero() {
			return !detail.ExpiresAt.After(s.now())
		}
		return true
	}
	info, infoErr := entry.Info()
	return infoErr == nil && info.ModTime().Before(s.now().Add(-detailTTL))
}

func (s Store) readEntries(limit int) ([]os.DirEntry, error) {
	directory, err := os.Open(s.rootPath())
	if err != nil {
		return nil, err
	}
	defer directory.Close()
	entries, err := directory.ReadDir(limit)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("read activity detail directory: %w", err)
	}
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Name() < entries[j].Name()
	})
	return entries, nil
}

func (s Store) rootPath() string {
	return filepath.Join(s.dataDir, "activity-details", "v2")
}

func normalizeAgentType(agentType string) string {
	switch strings.TrimSpace(strings.ToLower(agentType)) {
	case "codex":
		return "codex"
	case "claude_code":
		return "claude_code"
	default:
		return ""
	}
}
