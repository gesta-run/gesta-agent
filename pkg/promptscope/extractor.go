package promptscope

import "strings"

const codexAgentType = "codex"

// Extract returns the user-authored portion of a hook prompt. Generated
// attachment and ambient-state envelopes are excluded from sensitive-data and
// organization-context matching.
func Extract(agentType, rawPrompt string) string {
	if strings.EqualFold(strings.TrimSpace(agentType), codexAgentType) {
		return extractCodexPrompt(rawPrompt)
	}
	return rawPrompt
}
