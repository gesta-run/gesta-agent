package turnreceipt

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/gesta-run/gesta-agent/pkg/util"
)

func (s Store) SavePending(
	agentType, sessionID string,
	output OutputSummary,
) error {
	path, ok := s.pendingPath(agentType, sessionID)
	if !ok || output.Empty() {
		return nil
	}
	return s.withReceiptLock(path, func() error {
		if err := os.RemoveAll(path); err != nil {
			return fmt.Errorf("replace pending turn notice: %w", err)
		}
		pending := PendingNotice{
			SchemaVersion: pendingSchemaVersion,
			ExpiresAt:     s.now().Add(receiptTTL),
			Output:        output,
		}
		return s.writePendingNotice(path, pending)
	})
}

func (s Store) ConsumePending(
	agentType, sessionID string,
) (PendingNotice, bool, error) {
	path, ok := s.pendingPath(agentType, sessionID)
	if !ok {
		return PendingNotice{}, false, nil
	}
	claimPath := path + ".consuming-" + util.NewID("pending")
	found := false
	if err := s.withReceiptLock(path, func() error {
		if err := os.Rename(path, claimPath); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return nil
			}
			return fmt.Errorf("claim pending turn notice: %w", err)
		}
		found = true
		return nil
	}); err != nil {
		return PendingNotice{}, false, err
	}
	if !found {
		pruneEmptyParents(filepath.Dir(path), s.rootPath())
		return PendingNotice{}, false, nil
	}
	defer func() {
		_ = os.RemoveAll(claimPath)
		pruneEmptyParents(filepath.Dir(path), s.rootPath())
	}()

	pending, err := readPendingNotice(filepath.Join(claimPath, "receipt.json"))
	if err != nil {
		return PendingNotice{}, true, err
	}
	if !pending.ExpiresAt.After(s.now()) {
		return PendingNotice{}, false, nil
	}
	return pending, true, nil
}

func (s Store) writePendingNotice(path string, pending PendingNotice) error {
	if err := os.MkdirAll(path, 0o700); err != nil {
		return fmt.Errorf("create pending turn notice directory: %w", err)
	}
	return atomicWriteBoundedJSON(
		filepath.Join(path, "receipt.json"),
		pending,
		maxPendingRecordBytes,
		"pending turn notice record",
	)
}

func (s Store) pendingPath(agentType, sessionID string) (string, bool) {
	agent, ok := normalizeAgentType(agentType)
	sessionID = strings.TrimSpace(sessionID)
	if !ok || s.dataDir == "" || sessionID == "" {
		return "", false
	}
	return filepath.Join(s.rootPath(), agent, util.ShortHash(sessionID), "pending"), true
}

func readPendingNotice(path string) (PendingNotice, error) {
	info, err := os.Stat(path)
	if err != nil {
		return PendingNotice{}, err
	}
	if info.Size() > maxPendingRecordBytes {
		return PendingNotice{}, fmt.Errorf(
			"pending turn notice record exceeds %d bytes",
			maxPendingRecordBytes,
		)
	}
	var pending PendingNotice
	if err := readBoundedJSON(path, &pending); err != nil {
		return PendingNotice{}, err
	}
	if pending.SchemaVersion != pendingSchemaVersion {
		return PendingNotice{}, fmt.Errorf(
			"unsupported pending turn notice schema version %d",
			pending.SchemaVersion,
		)
	}
	if pending.Output.Empty() {
		return PendingNotice{}, errors.New("pending turn notice has no output")
	}
	return pending, nil
}
