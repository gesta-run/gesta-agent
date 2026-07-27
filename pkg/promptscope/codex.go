package promptscope

import "strings"

const (
	codexFilesHeading   = "Files mentioned by the user:"
	codexRequestHeading = "My request for Codex:"
	maxAttachmentRefs   = 128
)

type promptLine struct {
	next int
	text string
}

func extractCodexPrompt(rawPrompt string) string {
	lines := splitPromptLines(rawPrompt)
	filesIndex, requestIndex := -1, -1
	filesCount, requestCount := 0, 0
	for index, line := range lines {
		heading, ok := markdownHeading(line.text)
		if !ok {
			continue
		}
		switch heading {
		case codexFilesHeading:
			filesIndex = index
			filesCount++
		case codexRequestHeading:
			requestIndex = index
			requestCount++
		}
	}

	if filesCount == 0 && requestCount == 0 {
		return rawPrompt
	}
	if filesCount != 1 || requestCount != 1 || filesIndex >= requestIndex {
		return ""
	}

	attachments, ok := attachmentPaths(lines[filesIndex+1 : requestIndex])
	if !ok {
		return ""
	}
	bodyStart := lines[requestIndex].next
	if bodyStart > len(rawPrompt) {
		return ""
	}
	return stripGeneratedImageBlocks(rawPrompt[bodyStart:], attachments)
}

func splitPromptLines(value string) []promptLine {
	lines := make([]promptLine, 0, strings.Count(value, "\n")+1)
	for start := 0; start <= len(value); {
		lineEnd := strings.IndexByte(value[start:], '\n')
		if lineEnd < 0 {
			lines = append(lines, promptLine{next: len(value), text: strings.TrimSuffix(value[start:], "\r")})
			break
		}
		lineEnd += start
		lines = append(lines, promptLine{
			next: lineEnd + 1,
			text: strings.TrimSuffix(value[start:lineEnd], "\r"),
		})
		start = lineEnd + 1
	}
	return lines
}

func markdownHeading(line string) (string, bool) {
	line = strings.TrimSpace(line)
	if line == "" || line[0] != '#' {
		return "", false
	}
	index := 0
	for index < len(line) && line[index] == '#' {
		index++
	}
	if index == len(line) || (line[index] != ' ' && line[index] != '\t') {
		return "", false
	}
	return strings.TrimSpace(line[index:]), true
}

func attachmentPaths(lines []promptLine) (map[string]struct{}, bool) {
	paths := make(map[string]struct{})
	for _, line := range lines {
		heading, ok := markdownHeading(line.text)
		if !ok {
			continue
		}
		separator := strings.LastIndex(heading, ": ")
		if separator <= 0 {
			continue
		}
		path := strings.TrimSpace(heading[separator+2:])
		if path == "" {
			continue
		}
		paths[path] = struct{}{}
		if len(paths) > maxAttachmentRefs {
			return nil, false
		}
	}
	return paths, len(paths) > 0
}

func stripGeneratedImageBlocks(body string, attachments map[string]struct{}) string {
	body = strings.TrimSpace(body)
	for strings.HasSuffix(body, "</image>") {
		closeStart := len(body) - len("</image>")
		openStart := strings.LastIndex(body[:closeStart], "<image")
		if openStart < 0 || (openStart > 0 && body[openStart-1] != '\n' && body[openStart-1] != '\r') {
			break
		}
		tagEndOffset := strings.IndexByte(body[openStart:closeStart], '>')
		if tagEndOffset < 0 {
			break
		}
		startTag := body[openStart : openStart+tagEndOffset+1]
		path, ok := quotedAttribute(startTag, "path")
		if !ok {
			break
		}
		if _, ok := attachments[path]; !ok {
			break
		}
		body = strings.TrimSpace(body[:openStart])
	}
	return body
}

func quotedAttribute(tag, name string) (string, bool) {
	for _, quote := range []byte{'"', '\''} {
		token := " " + name + "=" + string(quote)
		start := strings.Index(tag, token)
		if start < 0 {
			continue
		}
		start += len(token)
		end := strings.IndexByte(tag[start:], quote)
		if end >= 0 {
			return tag[start : start+end], true
		}
	}
	return "", false
}
