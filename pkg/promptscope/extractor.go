package promptscope

import "strings"

const codexAgentType = "codex"

// Extract returns the portion of a hook prompt that is eligible for
// organization-context matching.
func Extract(agentType, rawPrompt string) string {
	if strings.EqualFold(strings.TrimSpace(agentType), codexAgentType) {
		return extractCodexPrompt(rawPrompt)
	}
	return rawPrompt
}
