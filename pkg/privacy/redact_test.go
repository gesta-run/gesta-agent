package privacy

import "testing"

func TestRedactString(t *testing.T) {
	got := RedactString("token=abc123 password: hunter2 sk-abcdefghijklmnopqrstuvwxyz")
	if got == "token=abc123 password: hunter2 sk-abcdefghijklmnopqrstuvwxyz" {
		t.Fatalf("expected secrets to be redacted")
	}
	if got != "token=[REDACTED] password:[REDACTED] [REDACTED]" {
		t.Fatalf("unexpected redaction: %q", got)
	}
}

func TestRedactStringRedactsCredentialBlockAfterEmail(t *testing.T) {
	input := "user@example.com\nCjjcjj123\n如果需要登陆，用这个"

	got := RedactString(input)

	if got != "[REDACTED]\n[REDACTED]\n如果需要登陆，用这个" {
		t.Fatalf("unexpected credential block redaction: %q", got)
	}
}

func TestRedactStringRedactsCredentialBlockAfterHint(t *testing.T) {
	input := "login with this account\nuser@example.com\n12345678"

	got := RedactString(input)

	if got != "login with this account\n[REDACTED]\n[REDACTED]" {
		t.Fatalf("unexpected credential hint redaction: %q", got)
	}
}
