package model

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestMemoryScoreJSONContract(t *testing.T) {
	encoded, err := json.Marshal(Memory{RelevanceScore: 0.7, WorkspaceBoost: 0.2, Score: 0.9})
	if err != nil {
		t.Fatal(err)
	}
	body := string(encoded)
	for _, field := range []string{`"relevance_score":0.7`, `"workspace_boost":0.2`, `"score":0.9`} {
		if !strings.Contains(body, field) {
			t.Fatalf("memory contract missing %s: %s", field, body)
		}
	}
	if strings.Contains(body, `"rank_score"`) {
		t.Fatalf("memory contract contains obsolete rank_score: %s", body)
	}
}

func TestMemorySearchWireContract(t *testing.T) {
	request, err := json.Marshal(MemorySearchRequest{
		DaemonID: "daemon", Query: "project", Limit: 5,
		Workspace: MemoryWorkspace{CWDName: "gesta", ChildDirs: []string{"pkg"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	const expectedRequest = `{"daemon_id":"daemon","query":"project","limit":5,"workspace":{"cwd_name":"gesta","child_dirs":["pkg"]}}`
	if string(request) != expectedRequest {
		t.Fatalf("search request contract = %s", request)
	}

	response, err := json.Marshal(MemorySearchResponse{Memories: []Memory{{
		FactID: "fact", Content: "stable fact", RelevanceScore: 0.7, WorkspaceBoost: 0.2, Score: 0.9,
	}}})
	if err != nil {
		t.Fatal(err)
	}
	const expectedResponse = `{"memories":[{"fact_id":"fact","content":"stable fact","relevance_score":0.7,"workspace_boost":0.2,"score":0.9}],"truncated":false}`
	if string(response) != expectedResponse {
		t.Fatalf("search response contract = %s", response)
	}
}
