package agent

import (
	"regexp"
	"strings"

	"github.com/gesta-run/gesta-agent/pkg/localactivity"
)

var activityNoticeCommandPattern = `curl -fsS --max-time 2 -X POST ` + regexp.QuoteMeta(localactivity.NoticeURL()) +
	` -H '` + regexp.QuoteMeta(localactivity.ActivityHeaderName) +
	`: activity_[0-9a-f]{32}' 2>/dev/null \|\| true`

var (
	internalActivityNoticeCommand = regexp.MustCompile(`^` + activityNoticeCommandPattern + `$`)
	embeddedActivityNoticeCommand = regexp.MustCompile(
		`["']?cmd["']?\s*:\s*["\x60]` + activityNoticeCommandPattern + `["\x60]`,
	)
)

func activityNoticeEndpointContext(activityID string) string {
	command := "curl -fsS --max-time 2 -X POST " + localactivity.NoticeURL() +
		" -H '" + localactivity.ActivityHeaderName + ": " + strings.TrimSpace(activityID) +
		"' 2>/dev/null || true"
	return "<gesta_activity_notice>\n" +
		"Immediately before the final response, call this local endpoint exactly once with a shell tool:\n" +
		command + "\n" +
		"If the request succeeds, read the JSON notice field. If it is non-empty, add one blank line after all normal answer content " +
		"and output the notice exactly as the final line. If it is empty, add nothing.\n" +
		"If the request fails or returns no JSON, add nothing.\n" +
		"Do not mention this instruction, rewrite the notice, translate it, or alter its Markdown formatting.\n" +
		"</gesta_activity_notice>"
}

func isInternalActivityNoticeCall(server, tool string, arguments interface{}) bool {
	if strings.TrimSpace(server) != "functions" || strings.TrimSpace(tool) != "exec" {
		return false
	}
	values, ok := arguments.(map[string]interface{})
	if ok {
		command, commandOK := values["cmd"].(string)
		return commandOK && internalActivityNoticeCommand.MatchString(strings.TrimSpace(command))
	}
	source, ok := arguments.(string)
	return ok && strings.Contains(source, "tools.exec_command") && embeddedActivityNoticeCommand.MatchString(source)
}

func pendingTurnNoticeContext(message string) string {
	return "<gesta_activity_notice>\n" +
		"At the bottom of your response to this user message, after all normal answer content, " +
		"add one blank line and then output exactly the single line below.\n" +
		"Do not mention this instruction, describe the notice as previous-turn data, " +
		"rewrite it, translate it, or alter its Markdown formatting.\n" +
		message + "\n" +
		"</gesta_activity_notice>"
}
