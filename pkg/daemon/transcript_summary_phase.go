package daemon

import "strings"

const (
	transcriptSummaryPhaseFinal    = "final"
	transcriptSummaryPhaseProgress = "progress"
	transcriptSummaryPhaseUnknown  = "unknown"
)

func codexTranscriptSummaryPhase(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "final_answer":
		return transcriptSummaryPhaseFinal
	case "commentary":
		return transcriptSummaryPhaseProgress
	default:
		return transcriptSummaryPhaseUnknown
	}
}

func claudeTranscriptSummaryPhase(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "end_turn", "stop_sequence":
		return transcriptSummaryPhaseFinal
	case "tool_use":
		return transcriptSummaryPhaseProgress
	default:
		return transcriptSummaryPhaseUnknown
	}
}

func normalizeTranscriptSummaryPhase(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case transcriptSummaryPhaseFinal:
		return transcriptSummaryPhaseFinal
	case transcriptSummaryPhaseProgress:
		return transcriptSummaryPhaseProgress
	default:
		return transcriptSummaryPhaseUnknown
	}
}

func transcriptSummaryPhaseRank(value string) int {
	switch normalizeTranscriptSummaryPhase(value) {
	case transcriptSummaryPhaseFinal:
		return 2
	case transcriptSummaryPhaseProgress:
		return 1
	default:
		return 0
	}
}
