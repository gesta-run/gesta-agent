package turnreceipt

import (
	"errors"
	"math"
	"time"
)

const (
	schemaVersion           = 4
	pendingSchemaVersion    = 6
	maxOutputFragments      = 256
	maxReceiptBytes         = 64 * 1024
	maxPendingRecordBytes   = 64 * 1024
	maxCleanupVisits        = 512
	maxCleanupRemovals      = 128
	receiptTTL              = 24 * time.Hour
	receiptLockWait         = 2 * time.Second
	receiptLockStaleAfter   = time.Minute
	receiptLockPollInterval = 10 * time.Millisecond
)

var ErrOutputFragmentLimit = errors.New("turn receipt output fragment limit reached")

type OutputSummary struct {
	CodeLines   int64 `json:"code_lines,omitempty"`
	TestLines   int64 `json:"test_lines,omitempty"`
	DocWords    int64 `json:"doc_words,omitempty"`
	ConfigLines int64 `json:"config_lines,omitempty"`
	OtherWords  int64 `json:"other_words,omitempty"`
}

func (s OutputSummary) Empty() bool {
	return s.CodeLines <= 0 &&
		s.TestLines <= 0 &&
		s.DocWords <= 0 &&
		s.ConfigLines <= 0 &&
		s.OtherWords <= 0
}

func (s *OutputSummary) Add(other OutputSummary) {
	s.CodeLines = saturatingAdd(s.CodeLines, other.CodeLines)
	s.TestLines = saturatingAdd(s.TestLines, other.TestLines)
	s.DocWords = saturatingAdd(s.DocWords, other.DocWords)
	s.ConfigLines = saturatingAdd(s.ConfigLines, other.ConfigLines)
	s.OtherWords = saturatingAdd(s.OtherWords, other.OtherWords)
}

func (s OutputSummary) EquivalentLOC() float64 {
	lineEquivalent := float64(nonNegative(s.CodeLines) + nonNegative(s.ConfigLines) + nonNegative(s.TestLines))
	proseEquivalent := float64(nonNegative(s.DocWords)+nonNegative(s.OtherWords)) / 8
	return lineEquivalent + proseEquivalent
}

func nonNegative(value int64) int64 {
	if value < 0 {
		return 0
	}
	return value
}

type Receipt struct {
	SchemaVersion int           `json:"schema_version"`
	ExpiresAt     time.Time     `json:"expires_at"`
	Output        OutputSummary `json:"-"`
}

type PendingNotice struct {
	SchemaVersion int           `json:"schema_version"`
	ExpiresAt     time.Time     `json:"expires_at"`
	Output        OutputSummary `json:"output,omitempty"`
}

type outputFragment struct {
	SchemaVersion int           `json:"schema_version"`
	Output        OutputSummary `json:"output"`
}

func saturatingAdd(current, delta int64) int64 {
	if delta <= 0 {
		return current
	}
	if current > math.MaxInt64-delta {
		return math.MaxInt64
	}
	return current + delta
}
