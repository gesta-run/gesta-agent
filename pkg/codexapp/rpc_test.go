package codexapp

import (
	"context"
	"strings"
	"testing"
)

func TestStreamRPCResponsesReportsMalformedJSON(t *testing.T) {
	t.Parallel()

	results := streamRPCResponses(context.Background(), strings.NewReader("{invalid}\n"))
	result, ok := <-results
	if !ok {
		t.Fatal("response stream closed without reporting malformed JSON")
	}
	if result.Err == nil {
		t.Fatal("malformed JSON did not produce an error")
	}
	if !strings.Contains(result.Err.Error(), "decode codex app-server response") {
		t.Fatalf("unexpected error: %v", result.Err)
	}
	if _, ok := <-results; ok {
		t.Fatal("response stream remained open after malformed JSON")
	}
}

func TestStreamRPCResponsesSkipsNotifications(t *testing.T) {
	t.Parallel()

	input := strings.Join([]string{
		`{"method":"thread/started","params":{"threadId":"thread_123"}}`,
		`{"id":2,"result":{"thread":{"turns":[]}}}`,
		"",
	}, "\n")
	results := streamRPCResponses(context.Background(), strings.NewReader(input))

	result, ok := <-results
	if !ok {
		t.Fatal("response stream closed before returning the RPC response")
	}
	if result.Err != nil {
		t.Fatalf("unexpected stream error: %v", result.Err)
	}
	if string(result.Response.ID) != "2" {
		t.Fatalf("response id = %s, want 2", result.Response.ID)
	}
	if _, ok := <-results; ok {
		t.Fatal("response stream returned an unexpected extra result")
	}
}
