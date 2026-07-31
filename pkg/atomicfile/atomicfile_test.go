package atomicfile

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestJSONWritersLeaveCompleteFileUnderConcurrency(t *testing.T) {
	tests := []struct {
		name  string
		write func(string, interface{}) error
	}{
		{name: "durable", write: WriteJSON},
		{name: "replace", write: ReplaceJSON},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "state.json")
			const writers = 16
			var wait sync.WaitGroup
			errors := make(chan error, writers)
			for index := 0; index < writers; index++ {
				wait.Add(1)
				go func(value int) {
					defer wait.Done()
					errors <- test.write(path, map[string]int{"value": value})
				}(index)
			}
			wait.Wait()
			close(errors)
			for err := range errors {
				if err != nil {
					t.Fatalf("write JSON: %v", err)
				}
			}

			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read result: %v", err)
			}
			var result map[string]int
			if err := json.Unmarshal(data, &result); err != nil {
				t.Fatalf("result is incomplete JSON: %v", err)
			}
			if result["value"] < 0 || result["value"] >= writers {
				t.Fatalf("result value = %d, want a writer value", result["value"])
			}
			info, err := os.Stat(path)
			if err != nil {
				t.Fatalf("stat result: %v", err)
			}
			if info.Mode().Perm() != 0o600 {
				t.Fatalf("result permissions = %o, want 600", info.Mode().Perm())
			}
		})
	}
}
