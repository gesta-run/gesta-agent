package privacy

import (
	"math"
	"regexp"
	"sort"
	"strings"
	"unicode"

	"github.com/gesta-run/gesta-agent/pkg/model"
)

var (
	smartSecretAssignmentPattern = regexp.MustCompile(`(?i)(?:auth0[_-]?)?(?:client[_-]?secret|api[_-]?key|access[_-]?token|auth[_-]?token|refresh[_-]?token|id[_-]?token|secret|password|passwd|credential|bearer|token)\s*[:=]\s*["']?([A-Za-z0-9._~+/=-]{16,})`)
	smartSecretCandidatePattern  = regexp.MustCompile(`[A-Za-z0-9][A-Za-z0-9._~+/=-]{15,}`)
	smartSecretContextPattern    = regexp.MustCompile(`(?i)(auth0|client[_-]?secret|api[_-]?key|access[_-]?token|auth[_-]?token|refresh[_-]?token|id[_-]?token|secret|password|passwd|credential|bearer|token)`)
	smartSecretJWTPattern        = regexp.MustCompile(`^[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}$`)
	smartSecretUUIDPattern       = regexp.MustCompile(`(?i)^[0-9a-f]{8}-[0-9a-f]{4}-[1-5][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
	smartSecretHexPattern        = regexp.MustCompile(`(?i)^[0-9a-f]+$`)
)

type smartSecretLoc struct {
	start int
	end   int
}

func smartSecretMatchIndexes(input string, rule model.SensitiveRule) [][2]int {
	contextPattern := smartSecretOptionalPattern(rule.Pattern)
	seen := map[string]bool{}
	matches := make([]smartSecretLoc, 0, 2)

	add := func(start, end int, hasAssignmentContext bool) {
		start, end = trimSmartSecretCandidate(input, start, end)
		if start < 0 || end <= start || end > len(input) {
			return
		}
		if contextPattern != nil && !smartSecretPatternMatchesContext(contextPattern, input, start, end) {
			return
		}
		if !smartSecretCandidateAccepted(input, start, end, hasAssignmentContext) {
			return
		}
		key := input[start:end]
		if seen[key] {
			return
		}
		seen[key] = true
		matches = append(matches, smartSecretLoc{start: start, end: end})
	}

	for _, loc := range smartSecretAssignmentPattern.FindAllStringSubmatchIndex(input, -1) {
		if len(loc) >= 4 && loc[2] >= 0 && loc[3] > loc[2] {
			add(loc[2], loc[3], true)
		}
	}
	for _, loc := range smartSecretCandidatePattern.FindAllStringIndex(input, -1) {
		if len(loc) == 2 {
			add(loc[0], loc[1], false)
		}
	}

	sort.SliceStable(matches, func(i, j int) bool {
		if matches[i].start != matches[j].start {
			return matches[i].start < matches[j].start
		}
		return matches[i].end < matches[j].end
	})
	out := make([][2]int, 0, len(matches))
	for _, match := range matches {
		out = append(out, [2]int{match.start, match.end})
	}
	return out
}

func smartSecretOptionalPattern(pattern string) *regexp.Regexp {
	pattern = strings.TrimSpace(pattern)
	if pattern == "" {
		return nil
	}
	compiled, err := regexp.Compile(pattern)
	if err != nil {
		return nil
	}
	return compiled
}

func smartSecretPatternMatchesContext(pattern *regexp.Regexp, input string, start, end int) bool {
	if pattern.MatchString(input[start:end]) {
		return true
	}
	windowStart := start - 96
	if windowStart < 0 {
		windowStart = 0
	}
	windowEnd := end + 96
	if windowEnd > len(input) {
		windowEnd = len(input)
	}
	return pattern.MatchString(input[windowStart:windowEnd])
}

func trimSmartSecretCandidate(input string, start, end int) (int, int) {
	if start < 0 || end > len(input) || end <= start {
		return -1, -1
	}
	value := input[start:end]
	if idx := strings.LastIndex(value, "="); idx >= 0 && idx+1 < len(value) {
		start += idx + 1
	}
	for start < end {
		r := rune(input[start])
		if r == '"' || r == '\'' || r == '`' || r == '<' || r == '(' || r == '[' || r == '{' {
			start++
			continue
		}
		break
	}
	for end > start {
		r := rune(input[end-1])
		if strings.ContainsRune("\"'`.,;:>)]}", r) {
			end--
			continue
		}
		break
	}
	return start, end
}

func smartSecretCandidateAccepted(input string, start, end int, hasAssignmentContext bool) bool {
	value := input[start:end]
	if len(value) < 16 || len(value) > 512 {
		return false
	}
	if !hasAssignmentContext && smartSecretLooksLikePublicURLCandidate(input, start, end) {
		return false
	}
	if smartSecretUUIDPattern.MatchString(value) {
		return false
	}
	score := smartSecretScore(input, start, end, hasAssignmentContext)
	if hasAssignmentContext {
		return score >= 0.62
	}
	return score >= 0.72
}

func smartSecretScore(input string, start, end int, hasAssignmentContext bool) float64 {
	value := input[start:end]
	length := len(value)
	score := 0.0

	switch {
	case length >= 64:
		score += 0.30
	case length >= 48:
		score += 0.24
	case length >= 32:
		score += 0.18
	case length >= 24:
		score += 0.10
	case length >= 20:
		score += 0.05
	}

	entropy := shannonEntropy(value)
	switch {
	case entropy >= 4.7:
		score += 0.35
	case entropy >= 4.2:
		score += 0.28
	case entropy >= 3.8:
		score += 0.18
	case entropy >= 3.4:
		score += 0.08
	}

	classes := smartSecretCharacterClasses(value)
	switch {
	case classes >= 4:
		score += 0.18
	case classes == 3:
		score += 0.12
	case classes == 2:
		score += 0.06
	}

	context := hasAssignmentContext || smartSecretContextNearby(input, start, end)
	if context {
		score += 0.28
	}
	if smartSecretJWTPattern.MatchString(value) {
		score += 0.25
	}
	if strings.ContainsAny(value, "_-") && strings.IndexFunc(value, unicode.IsUpper) >= 0 && strings.IndexFunc(value, unicode.IsLower) >= 0 {
		score += 0.08
	}
	if smartSecretHexPattern.MatchString(value) && !context {
		score -= 0.18
	}
	if smartSecretLooksLikeNaturalText(value) {
		score -= 0.25
	}
	if score < 0 {
		return 0
	}
	if score > 1 {
		return 1
	}
	return score
}

func smartSecretContextNearby(input string, start, end int) bool {
	windowStart := start - 96
	if windowStart < 0 {
		windowStart = 0
	}
	windowEnd := end + 96
	if windowEnd > len(input) {
		windowEnd = len(input)
	}
	return smartSecretContextPattern.MatchString(input[windowStart:windowEnd])
}

func smartSecretCharacterClasses(value string) int {
	hasLower := false
	hasUpper := false
	hasDigit := false
	hasSymbol := false
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z':
			hasLower = true
		case r >= 'A' && r <= 'Z':
			hasUpper = true
		case r >= '0' && r <= '9':
			hasDigit = true
		case strings.ContainsRune("._~+/=-", r):
			hasSymbol = true
		}
	}
	classes := 0
	for _, present := range []bool{hasLower, hasUpper, hasDigit, hasSymbol} {
		if present {
			classes++
		}
	}
	return classes
}

func shannonEntropy(value string) float64 {
	if value == "" {
		return 0
	}
	counts := map[rune]int{}
	for _, r := range value {
		counts[r]++
	}
	total := float64(len([]rune(value)))
	entropy := 0.0
	for _, count := range counts {
		p := float64(count) / total
		entropy -= p * math.Log2(p)
	}
	return entropy
}

func smartSecretLooksLikeNaturalText(value string) bool {
	if len(value) < 24 || strings.ContainsAny(value, "._~+/=-0123456789") {
		return false
	}
	vowels := 0
	letters := 0
	for _, r := range strings.ToLower(value) {
		if r >= 'a' && r <= 'z' {
			letters++
			if strings.ContainsRune("aeiou", r) {
				vowels++
			}
		}
	}
	if letters == 0 {
		return false
	}
	ratio := float64(vowels) / float64(letters)
	return ratio > 0.28 && ratio < 0.55
}

func smartSecretLooksLikePublicURLCandidate(input string, start, end int) bool {
	if start < 0 || end <= start || end > len(input) {
		return false
	}
	value := input[start:end]
	if !strings.Contains(value, ".") || !strings.Contains(value, "/") {
		return false
	}
	firstSlash := strings.Index(value, "/")
	firstDot := strings.Index(value, ".")
	if firstSlash <= 0 || firstDot <= 0 || firstDot > firstSlash {
		return false
	}
	host := value[:firstSlash]
	return smartSecretLooksLikeHostname(host)
}

func smartSecretLooksLikeHostname(value string) bool {
	if value == "" || len(value) > 253 || !strings.Contains(value, ".") {
		return false
	}
	trimmed := strings.Trim(value, ".")
	if trimmed != value || trimmed == "" {
		return false
	}
	for _, part := range strings.Split(value, ".") {
		if part == "" || len(part) > 63 {
			return false
		}
		for _, r := range part {
			if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' {
				continue
			}
			return false
		}
	}
	return true
}
