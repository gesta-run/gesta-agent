package turnreceipt

import (
	"errors"
	"math"
	"time"
)

const (
	schemaVersion           = 1
	pendingSchemaVersion    = 2
	maxPolicyMatches        = 10
	maxOutputFragments      = 256
	maxReceiptBytes         = 64 * 1024
	maxPendingNoticeBytes   = 1024
	maxPendingRecordBytes   = 1024
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
	OtherLines  int64 `json:"other_lines,omitempty"`
}

func (s OutputSummary) Empty() bool {
	return s.CodeLines <= 0 &&
		s.TestLines <= 0 &&
		s.DocWords <= 0 &&
		s.ConfigLines <= 0 &&
		s.OtherLines <= 0
}

func (s *OutputSummary) Add(other OutputSummary) {
	s.CodeLines = saturatingAdd(s.CodeLines, other.CodeLines)
	s.TestLines = saturatingAdd(s.TestLines, other.TestLines)
	s.DocWords = saturatingAdd(s.DocWords, other.DocWords)
	s.ConfigLines = saturatingAdd(s.ConfigLines, other.ConfigLines)
	s.OtherLines = saturatingAdd(s.OtherLines, other.OtherLines)
}

type Receipt struct {
	SchemaVersion    int           `json:"schema_version"`
	ExpiresAt        time.Time     `json:"expires_at"`
	PolicyMatchCount int           `json:"policy_match_count,omitempty"`
	Output           OutputSummary `json:"-"`
}

type PendingNotice struct {
	SchemaVersion int       `json:"schema_version"`
	ExpiresAt     time.Time `json:"expires_at"`
	Notice        string    `json:"notice"`
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
