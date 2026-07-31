package turnreceipt

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/gesta-run/gesta-agent/pkg/atomicfile"
	"github.com/gesta-run/gesta-agent/pkg/contextmatch"
	"github.com/gesta-run/gesta-agent/pkg/util"
)

type Store struct {
	dataDir string
	now     func() time.Time
}

func NewStore(dataDir string) Store {
	return Store{
		dataDir: strings.TrimSpace(dataDir),
		now:     func() time.Time { return time.Now().UTC() },
	}
}

func (s Store) Begin(agentType, sessionID, turnID string) error {
	path, ok := s.receiptPath(agentType, sessionID, turnID)
	if !ok {
		return nil
	}
	return s.withReceiptLock(path, func() error {
		if err := os.RemoveAll(path); err != nil {
			return fmt.Errorf("reset turn receipt: %w", err)
		}
		return s.writeReceipt(path, s.newReceipt())
	})
}

func (s Store) RecordContextMatches(
	agentType, sessionID, turnID string,
	matches []ContextRuleMatch,
) error {
	path, ok := s.receiptPath(agentType, sessionID, turnID)
	matches = NormalizeContextMatches(matches)
	if !ok || len(matches) == 0 {
		return nil
	}
	return s.withReceiptLock(path, func() error {
		receipt, err := readReceipt(filepath.Join(path, "receipt.json"))
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		receipt.ContextMatches = matches
		return s.writeReceipt(path, receipt)
	})
}

func (s Store) RecordOutput(
	agentType, sessionID, turnID, observationID string,
	output OutputSummary,
) error {
	path, ok := s.receiptPath(agentType, sessionID, turnID)
	observationID = strings.TrimSpace(observationID)
	if !ok || observationID == "" || output.Empty() {
		return nil
	}
	err := s.withReceiptLock(path, func() error {
		if _, err := readReceipt(filepath.Join(path, "receipt.json")); errors.Is(err, os.ErrNotExist) {
			return nil
		} else if err != nil {
			return err
		}
		outputDir := filepath.Join(path, "output")
		if err := os.MkdirAll(outputDir, 0o700); err != nil {
			return fmt.Errorf("create turn receipt output directory: %w", err)
		}
		fragmentPath := filepath.Join(outputDir, util.ShortHash(observationID)+".json")
		if _, err := os.Stat(fragmentPath); errors.Is(err, os.ErrNotExist) {
			count, countErr := countOutputFragments(outputDir)
			if countErr != nil {
				return countErr
			}
			if count >= maxOutputFragments {
				return ErrOutputFragmentLimit
			}
		} else if err != nil {
			return fmt.Errorf("stat turn receipt output fragment: %w", err)
		}
		return atomicWriteJSON(fragmentPath, outputFragment{
			SchemaVersion: schemaVersion,
			Output:        output,
		})
	})
	pruneEmptyParents(filepath.Dir(path), s.rootPath())
	return err
}

func (s Store) Consume(agentType, sessionID, turnID string) (Receipt, bool, error) {
	path, ok := s.receiptPath(agentType, sessionID, turnID)
	if !ok {
		return Receipt{}, false, nil
	}
	claimPath := path + ".consuming-" + util.NewID("receipt")
	found := false
	if err := s.withReceiptLock(path, func() error {
		if err := os.Rename(path, claimPath); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return nil
			}
			return fmt.Errorf("claim turn receipt: %w", err)
		}
		found = true
		return nil
	}); err != nil {
		return Receipt{}, false, err
	}
	if !found {
		pruneEmptyParents(filepath.Dir(path), s.rootPath())
		return Receipt{}, false, nil
	}
	defer func() {
		_ = os.RemoveAll(claimPath)
		pruneEmptyParents(filepath.Dir(path), s.rootPath())
	}()

	receipt, err := readReceipt(filepath.Join(claimPath, "receipt.json"))
	if err != nil {
		return Receipt{}, true, err
	}
	if !receipt.ExpiresAt.After(s.now()) {
		return Receipt{}, false, nil
	}
	outputDir := filepath.Join(claimPath, "output")
	entries, err := os.ReadDir(outputDir)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return Receipt{}, true, fmt.Errorf("read turn receipt fragments: %w", err)
	}
	fragmentsRead := 0
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		if fragmentsRead >= maxOutputFragments {
			break
		}
		fragmentsRead++
		var fragment outputFragment
		if err := readBoundedJSON(filepath.Join(outputDir, entry.Name()), &fragment); err != nil {
			continue
		}
		if fragment.SchemaVersion != schemaVersion {
			continue
		}
		receipt.Output.Add(fragment.Output)
	}
	return receipt, true, nil
}

func (s Store) newReceipt() Receipt {
	return Receipt{
		SchemaVersion: schemaVersion,
		ExpiresAt:     s.now().Add(receiptTTL),
	}
}

func (s Store) writeReceipt(path string, receipt Receipt) error {
	if err := os.MkdirAll(path, 0o700); err != nil {
		return fmt.Errorf("create turn receipt directory: %w", err)
	}
	if receipt.SchemaVersion == 0 {
		receipt.SchemaVersion = schemaVersion
	}
	return atomicWriteBoundedJSON(
		filepath.Join(path, "receipt.json"),
		receipt,
		maxReceiptBytes,
		"turn receipt",
	)
}

func (s Store) receiptPath(agentType, sessionID, turnID string) (string, bool) {
	agent, ok := normalizeAgentType(agentType)
	sessionID = strings.TrimSpace(sessionID)
	if !ok || s.dataDir == "" || sessionID == "" {
		return "", false
	}
	turnKey := "active"
	if turnID = strings.TrimSpace(turnID); turnID != "" {
		turnKey = "turn-" + util.ShortHash(turnID)
	}
	return filepath.Join(s.rootPath(), agent, util.ShortHash(sessionID), turnKey), true
}

func (s Store) rootPath() string {
	return filepath.Join(s.dataDir, "runtime", "turn-receipts")
}

func (s Store) lockPath(path string) string {
	return filepath.Join(
		s.lockRootPath(),
		util.HashString(path)+".lock",
	)
}

func (s Store) lockRootPath() string {
	return filepath.Join(s.dataDir, "runtime", "turn-receipt-locks")
}

func normalizeAgentType(agentType string) (string, bool) {
	switch strings.TrimSpace(strings.ToLower(agentType)) {
	case "codex":
		return "codex", true
	case "claude_code":
		return "claude_code", true
	default:
		return "", false
	}
}

func readReceipt(path string) (Receipt, error) {
	var receipt Receipt
	if err := readBoundedJSON(path, &receipt); err != nil {
		return Receipt{}, err
	}
	if receipt.SchemaVersion != schemaVersion {
		return Receipt{}, fmt.Errorf("unsupported turn receipt schema version %d", receipt.SchemaVersion)
	}
	receipt.ContextMatches = NormalizeContextMatches(receipt.ContextMatches)
	return receipt, nil
}

// NormalizeContextMatches bounds and sanitizes targeted context snapshots that
// may be persisted in turn receipts or local activity details.
func NormalizeContextMatches(matches []ContextRuleMatch) []ContextRuleMatch {
	normalized := make([]ContextRuleMatch, 0, min(len(matches), maxContextMatches))
	seen := make(map[string]struct{}, min(len(matches), maxContextMatches))
	contentRunes := 0
	for _, match := range matches {
		if len(normalized) >= maxContextMatches {
			break
		}
		match.RuleID = truncateUTF8Bytes(strings.TrimSpace(match.RuleID), maxContextRuleIDBytes)
		match.Name = truncateUTF8Bytes(strings.TrimSpace(match.Name), maxContextRuleNameBytes)
		match.MatchType = strings.TrimSpace(strings.ToLower(match.MatchType))
		match.Content = strings.TrimSpace(strings.ToValidUTF8(match.Content, ""))
		if match.RuleID == "" || (match.MatchType != "keyword_any" && match.MatchType != "regex") {
			continue
		}
		if match.Content == "" {
			continue
		}
		if _, exists := seen[match.RuleID]; exists {
			continue
		}
		separatorRunes := 0
		if len(normalized) > 0 {
			separatorRunes = 2
		}
		matchContentRunes := utf8.RuneCountInString(match.Content)
		if contentRunes+separatorRunes+matchContentRunes > contextmatch.MaxContextContent {
			continue
		}
		if match.Name == "" {
			match.Name = "Unnamed context rule"
		}
		seen[match.RuleID] = struct{}{}
		normalized = append(normalized, match)
		contentRunes += separatorRunes + matchContentRunes
	}
	return normalized
}

func truncateUTF8Bytes(value string, maxBytes int) string {
	value = strings.ToValidUTF8(value, "")
	value = strings.Map(func(character rune) rune {
		if unicode.IsControl(character) {
			return -1
		}
		return character
	}, value)
	if len(value) <= maxBytes {
		return value
	}
	for maxBytes > 0 && (value[maxBytes]&0xc0) == 0x80 {
		maxBytes--
	}
	return value[:maxBytes]
}

func readBoundedJSON(path string, target interface{}) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return fmt.Errorf("stat %s: %w", path, err)
	}
	if info.Size() <= 0 {
		return fmt.Errorf("%s is empty", path)
	}
	if info.Size() > maxReceiptBytes {
		return fmt.Errorf("%s exceeds %d bytes", path, maxReceiptBytes)
	}
	decoder := json.NewDecoder(io.LimitReader(file, maxReceiptBytes+1))
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("decode %s: %w", path, err)
	}
	return nil
}

func atomicWriteJSON(path string, value interface{}) error {
	if err := atomicfile.ReplaceJSON(path, value); err != nil {
		return fmt.Errorf("write turn receipt: %w", err)
	}
	return nil
}

func atomicWriteBoundedJSON(
	path string,
	value interface{},
	maxBytes int,
	label string,
) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal %s: %w", label, err)
	}
	data = append(data, '\n')
	if len(data) > maxBytes {
		return fmt.Errorf("%s exceeds %d bytes", label, maxBytes)
	}
	if err := atomicfile.Replace(path, data); err != nil {
		return fmt.Errorf("write %s: %w", label, err)
	}
	return nil
}

func countOutputFragments(outputDir string) (int, error) {
	entries, err := os.ReadDir(outputDir)
	if err != nil {
		return 0, fmt.Errorf("read turn receipt output directory: %w", err)
	}
	count := 0
	for _, entry := range entries {
		if !entry.IsDir() && filepath.Ext(entry.Name()) == ".json" {
			count++
		}
	}
	return count, nil
}

func pruneEmptyParents(path, stop string) {
	for path != stop && strings.HasPrefix(path, stop+string(os.PathSeparator)) {
		entries, err := os.ReadDir(path)
		if err != nil || len(entries) > 0 {
			return
		}
		if err := os.Remove(path); err != nil {
			return
		}
		path = filepath.Dir(path)
	}
}
