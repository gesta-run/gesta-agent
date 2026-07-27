package promptscope

import "testing"

func TestExtractDirectPrompt(t *testing.T) {
	const prompt = "hello"
	if got := Extract("codex", prompt); got != prompt {
		t.Fatalf("Extract() = %q, want %q", got, prompt)
	}
}

func TestExtractClaudePromptWithoutCodexParsing(t *testing.T) {
	const prompt = "# Files mentioned by the user:\n\n## file.go: /tmp/file.go"
	if got := Extract("claude_code", prompt); got != prompt {
		t.Fatalf("Extract() = %q, want direct Claude prompt", got)
	}
}

func TestExtractCodexEnvelope(t *testing.T) {
	const prompt = `# Files mentioned by the user:

## refactor-notes.png: /tmp/refactor-notes.png

## My request for Codex:

Please review this layout.

<image name=[Image #1] path="/tmp/refactor-notes.png">
</image>`
	if got := Extract("codex", prompt); got != "Please review this layout." {
		t.Fatalf("Extract() = %q", got)
	}
}

func TestExtractCodexEnvelopeWithCRLFAndMultipleImages(t *testing.T) {
	const prompt = "# Files mentioned by the user:\r\n\r\n" +
		"## first.png: /tmp/first.png\r\n" +
		"## second.png: /tmp/second.png\r\n\r\n" +
		"## My request for Codex:\r\n\r\n" +
		"修复这个布局。\r\n\r\n" +
		"<image name=[Image #1] path=\"/tmp/first.png\">\r\n</image>\r\n" +
		"<image name=[Image #2] path=\"/tmp/second.png\">\r\n</image>"
	if got := Extract("codex", prompt); got != "修复这个布局。" {
		t.Fatalf("Extract() = %q", got)
	}
}

func TestExtractKeepsUserImageMarkupForDifferentPath(t *testing.T) {
	const prompt = `# Files mentioned by the user:

## screenshot.png: /tmp/screenshot.png

## My request for Codex:

Keep this example:
<image path="/tmp/user-example.png">
</image>`
	const want = `Keep this example:
<image path="/tmp/user-example.png">
</image>`
	if got := Extract("codex", prompt); got != want {
		t.Fatalf("Extract() = %q, want %q", got, want)
	}
}

func TestExtractRejectsMalformedCodexEnvelopes(t *testing.T) {
	tests := map[string]string{
		"request heading only": "## My request for Codex:\n\nhello",
		"files heading only":   "# Files mentioned by the user:\n\n## file.go: /tmp/file.go",
		"wrong order": `## My request for Codex:
hello
# Files mentioned by the user:
## file.go: /tmp/file.go`,
		"missing attachment": `# Files mentioned by the user:

## My request for Codex:

hello`,
		"duplicate request heading": `# Files mentioned by the user:
## file.go: /tmp/file.go
## My request for Codex:
hello
## My request for Codex:
again`,
	}
	for name, prompt := range tests {
		t.Run(name, func(t *testing.T) {
			if got := Extract("codex", prompt); got != "" {
				t.Fatalf("Extract() = %q, want empty scope", got)
			}
		})
	}
}

func TestExtractValidEnvelopeWithEmptyRequest(t *testing.T) {
	const prompt = `# Files mentioned by the user:

## screenshot.png: /tmp/screenshot.png

## My request for Codex:`
	if got := Extract("codex", prompt); got != "" {
		t.Fatalf("Extract() = %q, want empty scope", got)
	}
}

func TestExtractDoesNotTreatPartialHeadingAsEnvelope(t *testing.T) {
	const prompt = "Discuss the My request for Codex: label."
	if got := Extract("codex", prompt); got != prompt {
		t.Fatalf("Extract() = %q, want direct prompt", got)
	}
}
