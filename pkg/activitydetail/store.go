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
	"unicode/utf8"

	"github.com/gesta-run/gesta-agent/pkg/atomicfile"
	"github.com/gesta-run/gesta-agent/pkg/model"
	"github.com/gesta-run/gesta-agent/pkg/turnreceipt"
	"github.com/gesta-run/gesta-agent/pkg/util"
)

const (
	schemaVersion          = 3
	maxActivityDetails     = 256
	maxActivityRecordBytes = 64 * 1024
	maxMemoryKeys          = 256
	maxMemorySamples       = 24
	maxMemoryContentBytes  = 2048
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
	SchemaVersion  int                       `json:"schema_version"`
	ActivityID     string                    `json:"activity_id"`
	CreatedAt      time.Time                 `json:"created_at"`
	ExpiresAt      time.Time                 `json:"expires_at"`
	AgentType      string                    `json:"agent_type"`
	ContextMatches []ContextRuleMatch        `json:"context_matches"`
	MemoryCount    int                       `json:"memory_count"`
	MemoryKeys     []string                  `json:"memory_keys,omitempty"`
	Memories       []RecalledMemory          `json:"memories,omitempty"`
	Output         turnreceipt.OutputSummary `json:"output"`
}

type RecalledMemory struct {
	Content string `json:"content"`
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

func (s Store) Begin(agentType string) (Detail, error) {
	return s.create(agentType, nil, turnreceipt.OutputSummary{})
}

func (s Store) Create(
	agentType string,
	matches []ContextRuleMatch,
	output turnreceipt.OutputSummary,
) (Detail, error) {
	agentType = normalizeAgentType(agentType)
	matches = normalizeContextMatches(matches)
	if s.dataDir == "" || agentType == "" || len(matches) == 0 {
		return Detail{}, errors.New("activity detail requires a data directory, agent type, and context matches")
	}
	return s.create(agentType, matches, output)
}

func (s Store) create(
	agentType string,
	matches []ContextRuleMatch,
	output turnreceipt.OutputSummary,
) (Detail, error) {
	agentType = normalizeAgentType(agentType)
	if s.dataDir == "" || agentType == "" {
		return Detail{}, errors.New("activity detail requires a data directory and agent type")
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
	if err := s.writeNew(detail); err != nil {
		return Detail{}, err
	}
	if err := s.ensureCapacityLocked(maxActivityDetails, detail.ActivityID+".json"); err != nil {
		_ = os.Remove(filepath.Join(s.rootPath(), detail.ActivityID+".json"))
		return Detail{}, err
	}
	return detail, nil
}

func (s Store) RecordContext(activityID string, matches []ContextRuleMatch) error {
	matches = normalizeContextMatches(matches)
	return s.update(activityID, func(detail *Detail) {
		detail.ContextMatches = matches
	})
}

func (s Store) RecordMemories(activityID string, memories []model.Memory) error {
	if len(memories) == 0 {
		return nil
	}
	return s.update(activityID, func(detail *Detail) {
		seen := make(map[string]struct{}, len(detail.MemoryKeys))
		for _, key := range detail.MemoryKeys {
			seen[key] = struct{}{}
		}
		for _, memory := range memories {
			content := truncateMemoryContent(memory.Content)
			if content == "" {
				continue
			}
			identity := strings.TrimSpace(memory.FactID)
			if identity == "" {
				identity = content
			}
			key := util.ShortHash(identity)
			if _, exists := seen[key]; exists {
				continue
			}
			if len(detail.MemoryKeys) >= maxMemoryKeys {
				break
			}
			seen[key] = struct{}{}
			detail.MemoryKeys = append(detail.MemoryKeys, key)
			detail.MemoryCount++
			if len(detail.Memories) < maxMemorySamples {
				detail.Memories = append(detail.Memories, RecalledMemory{Content: content})
			}
		}
	})
}

func (s Store) RecordOutput(activityID string, output turnreceipt.OutputSummary) error {
	return s.update(activityID, func(detail *Detail) {
		detail.Output = output
	})
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
	detail.ContextMatches = normalizeContextMatches(detail.ContextMatches)
	if detail.SchemaVersion != schemaVersion ||
		detail.ActivityID != activityID ||
		normalizeAgentType(detail.AgentType) == "" ||
		!detail.ExpiresAt.After(s.now()) {
		return Detail{}, ErrNotFound
	}
	detail.AgentType = normalizeAgentType(detail.AgentType)
	return detail, nil
}

func (s Store) update(activityID string, mutate func(*Detail)) error {
	activityID = strings.TrimSpace(activityID)
	if s.dataDir == "" || !validActivityID.MatchString(activityID) {
		return ErrNotFound
	}
	unlock, err := s.acquireLock(true)
	if err != nil {
		return err
	}
	defer unlock()
	detail, err := s.Get(activityID)
	if err != nil {
		return err
	}
	mutate(&detail)
	detail.ContextMatches = normalizeContextMatches(detail.ContextMatches)
	return s.writeReplacing(detail)
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
	if err := s.cleanupLegacyRootsLocked(); err != nil {
		return err
	}
	if err := s.cleanupExpiredLocked(); err != nil {
		return err
	}
	return s.ensureCapacityLocked(maxActivityDetails, "")
}

func (s Store) cleanupLegacyRootsLocked() error {
	for _, version := range []string{"v1", "v2"} {
		path := filepath.Join(s.dataDir, "activity-details", version)
		if err := os.RemoveAll(path); err != nil {
			return fmt.Errorf("remove legacy activity details %s: %w", version, err)
		}
	}
	return nil
}

func (s Store) writeNew(detail Detail) error {
	path := filepath.Join(s.rootPath(), detail.ActivityID+".json")
	if _, err := os.Stat(path); err == nil {
		return errors.New("activity detail already exists")
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("stat activity detail destination: %w", err)
	}
	return s.writeReplacing(detail)
}

func (s Store) writeReplacing(detail Detail) error {
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
	return filepath.Join(s.dataDir, "activity-details", "v3")
}

func truncateMemoryContent(value string) string {
	value = strings.TrimSpace(strings.ToValidUTF8(value, ""))
	if len(value) <= maxMemoryContentBytes {
		return value
	}
	end := maxMemoryContentBytes
	for end > 0 && !utf8.RuneStart(value[end]) {
		end--
	}
	return strings.TrimSpace(value[:end])
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
