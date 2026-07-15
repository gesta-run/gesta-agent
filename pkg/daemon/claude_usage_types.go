package daemon

import (
	"time"
)

const (
	claudeCodeUsageSource     = "claude_code"
	claudeCodeAgentType       = "claude_code"
	claudeSyntheticModel      = "<synthetic>"
	claudeScannerInitialBytes = 64 * 1024
	claudeScannerMaxBytes     = 16 * 1024 * 1024
)

// claudeAssistantUsage is a single assistant (LLM) turn's reported usage. Claude
// Code reports usage per assistant message (not cumulative), so per-session totals
// are computed by summing across the session's assistant messages.
type claudeAssistantUsage struct {
	InputTokens         int64
	OutputTokens        int64
	CacheCreationTokens int64
	CacheReadTokens     int64
}

// TotalTokens defines the Claude Code token total the same way the control plane
// and Codex treat the full billed token count: every input-side token (fresh
// input + cache creation + cache reads) plus output tokens. This keeps the
// console charts consistent across agents.
func (u claudeAssistantUsage) TotalTokens() int64 {
	return u.InputTokens + u.OutputTokens + u.CacheCreationTokens + u.CacheReadTokens
}

func (u claudeAssistantUsage) add(other claudeAssistantUsage) claudeAssistantUsage {
	return claudeAssistantUsage{
		InputTokens:         u.InputTokens + other.InputTokens,
		OutputTokens:        u.OutputTokens + other.OutputTokens,
		CacheCreationTokens: u.CacheCreationTokens + other.CacheCreationTokens,
		CacheReadTokens:     u.CacheReadTokens + other.CacheReadTokens,
	}
}

func (u claudeAssistantUsage) isZero() bool {
	return u.InputTokens == 0 && u.OutputTokens == 0 && u.CacheCreationTokens == 0 && u.CacheReadTokens == 0
}

// claudeSessionUsage is the parsed result of one Claude Code transcript file.
type claudeSessionUsage struct {
	SessionID       string
	CWD             string
	GitBranch       string
	Title           string
	FirstEventAt    time.Time
	LastEventAt     time.Time
	AssistantEvents int
	// Models is the set of model identifiers observed in this session.
	Models []string
	// ByModelDay groups usage by (model, day) so cross-day and per-model
	// breakdowns can be emitted as separate buckets.
	ByModelDay map[claudeModelDayKey]claudeAssistantUsage
	// Total is the session-wide sum across every assistant message.
	Total               claudeAssistantUsage
	Messages            []map[string]interface{}
	TranscriptTruncated bool
}

type claudeModelDayKey struct {
	Model string
	Day   string // YYYY-MM-DD (UTC)
}

type claudeTranscriptCandidate struct {
	Role      string
	Text      string
	Timestamp string
	Model     string
	MessageID string
}

func (s claudeSessionUsage) totalTokens() int64 { return s.Total.TotalTokens() }
