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

func (s Store) RecordPolicyMatches(agentType, sessionID, turnID string, count int) error {
	path, ok := s.receiptPath(agentType, sessionID, turnID)
	if !ok || count <= 0 {
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
		receipt.PolicyMatchCount = count
		if receipt.PolicyMatchCount > maxPolicyMatches {
			receipt.PolicyMatchCount = maxPolicyMatches
		}
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
	return atomicWriteJSON(filepath.Join(path, "receipt.json"), receipt)
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
	if receipt.PolicyMatchCount > maxPolicyMatches {
		receipt.PolicyMatchCount = maxPolicyMatches
	}
	return receipt, nil
}

func readBoundedJSON(path string, target interface{}) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	decoder := json.NewDecoder(io.LimitReader(file, maxReceiptBytes))
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("decode %s: %w", path, err)
	}
	return nil
}

func atomicWriteJSON(path string, value interface{}) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal turn receipt: %w", err)
	}
	data = append(data, '\n')
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create turn receipt parent: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".turn-receipt-*.tmp")
	if err != nil {
		return fmt.Errorf("create temporary turn receipt: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("set turn receipt permissions: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write turn receipt: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close turn receipt: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("replace turn receipt: %w", err)
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
