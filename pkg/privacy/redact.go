package privacy

import (
	"regexp"
	"strings"
)

var secretPatterns = []*regexp.Regexp{
	regexp.MustCompile(`sk-[A-Za-z0-9_-]{20,}`),
	regexp.MustCompile(`AKIA[0-9A-Z]{16}`),
	regexp.MustCompile(`(?i)[A-Z0-9._%+-]+@[A-Z0-9.-]+\.[A-Z]{2,}`),
	regexp.MustCompile(`(?i)(api[_-]?key|token|secret|password)\s*[:=]\s*["']?[^"'\s,;]+`),
	regexp.MustCompile(`(?i)bearer\s+[A-Za-z0-9._~+/=-]{20,}`),
	regexp.MustCompile(`[A-Za-z0-9_-]{48,}`),
}

var (
	emailPattern             = regexp.MustCompile(`(?i)[A-Z0-9._%+-]+@[A-Z0-9.-]+\.[A-Z]{2,}`)
	asciiLetterPattern       = regexp.MustCompile(`[A-Za-z]`)
	digitPattern             = regexp.MustCompile(`[0-9]`)
	credentialContextPattern = regexp.MustCompile(`(?i)(login|signin|sign in|password|passwd|credential|api[_ -]?key|token|secret|email|mail|账号|账户|邮箱|登录|登陆|密码|用这个)`)
)

func RedactString(input string) string {
	out := input
	out = redactCredentialBlocks(out)
	for _, pattern := range secretPatterns {
		out = pattern.ReplaceAllStringFunc(out, func(match string) string {
			if strings.Contains(match, "=") {
				return match[:strings.Index(match, "=")+1] + "[REDACTED]"
			}
			if strings.Contains(match, ":") {
				return match[:strings.Index(match, ":")+1] + "[REDACTED]"
			}
			return "[REDACTED]"
		})
	}
	return out
}

func redactCredentialBlocks(input string) string {
	lines := strings.Split(input, "\n")
	credentialContextLines := 0
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		hasCredentialContext := credentialContextPattern.MatchString(trimmed)
		hasEmail := emailPattern.MatchString(trimmed)
		if credentialContextLines > 0 && looksLikeCredentialLine(trimmed) {
			lines[i] = strings.Replace(line, trimmed, "[REDACTED]", 1)
			credentialContextLines = 0
			continue
		}
		if hasCredentialContext || hasEmail {
			credentialContextLines = 3
			continue
		}
		if credentialContextLines > 0 {
			credentialContextLines--
		}
	}
	return strings.Join(lines, "\n")
}

func looksLikeCredentialLine(value string) bool {
	if value == "" || strings.ContainsAny(value, " \t") {
		return false
	}
	if len(value) < 6 || len(value) > 96 {
		return false
	}
	hasLetter := asciiLetterPattern.MatchString(value)
	hasDigit := digitPattern.MatchString(value)
	if hasLetter && hasDigit {
		return true
	}
	if hasDigit && len(value) >= 8 {
		return true
	}
	return false
}

func RedactAndTruncate(input string, limit int) string {
	if limit <= 0 {
		limit = 4096
	}
	out := RedactString(input)
	if len(out) <= limit {
		return out
	}
	return out[:limit] + "\n[TRUNCATED]"
}
