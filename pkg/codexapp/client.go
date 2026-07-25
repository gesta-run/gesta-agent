package codexapp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

const readTimeout = 10 * time.Second

type FileChange struct {
	Path string     `json:"path"`
	Kind ChangeKind `json:"kind"`
	Diff string     `json:"diff"`
}

type ChangeKind struct {
	Type string `json:"type"`
}

type Item struct {
	ID        string       `json:"id"`
	Type      string       `json:"type"`
	Status    string       `json:"status"`
	Changes   []FileChange `json:"changes"`
	Server    string       `json:"server"`
	Tool      string       `json:"tool"`
	Arguments interface{}  `json:"arguments"`
}

type Turn struct {
	ID          string `json:"id"`
	Status      string `json:"status"`
	CompletedAt *int64 `json:"completedAt"`
	Items       []Item `json:"items"`
}

type threadReadResponse struct {
	Thread struct {
		Turns []Turn `json:"turns"`
	} `json:"thread"`
}

type rpcResponse struct {
	ID     json.RawMessage `json:"id"`
	Result json.RawMessage `json:"result"`
	Error  *rpcError       `json:"error"`
}

type rpcError struct {
	Code    int         `json:"code"`
	Message string      `json:"message"`
	Data    interface{} `json:"data,omitempty"`
}

func ReadTurn(ctx context.Context, threadID, turnID string) (Turn, error) {
	threadID = strings.TrimSpace(threadID)
	turnID = strings.TrimSpace(turnID)
	if threadID == "" || turnID == "" {
		return Turn{}, errors.New("thread id and turn id are required")
	}
	executable, err := resolveExecutable()
	if err != nil {
		return Turn{}, err
	}
	readCtx, cancel := context.WithTimeout(ctx, readTimeout)
	defer cancel()
	turns, err := readThread(readCtx, executable, threadID)
	if err != nil {
		return Turn{}, err
	}
	for _, turn := range turns {
		if turn.ID == turnID {
			return turn, nil
		}
	}
	return Turn{}, fmt.Errorf("turn %q was not found in thread %q", turnID, threadID)
}

func resolveExecutable() (string, error) {
	for _, key := range []string{"GESTA_CODEX_BIN", "CODEX_BIN"} {
		if candidate := strings.TrimSpace(os.Getenv(key)); candidate != "" {
			if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
				return candidate, nil
			}
			return "", fmt.Errorf("%s does not point to a Codex executable: %s", key, candidate)
		}
	}
	if candidate, err := exec.LookPath("codex"); err == nil {
		return candidate, nil
	}
	if runtime.GOOS == "darwin" {
		for _, candidate := range []string{
			"/Applications/ChatGPT.app/Contents/Resources/codex",
			"/Applications/Codex.app/Contents/Resources/codex",
		} {
			if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
				return candidate, nil
			}
		}
	}
	return "", errors.New("codex executable was not found; set GESTA_CODEX_BIN")
}

func readThread(ctx context.Context, executable, threadID string) ([]Turn, error) {
	cmd := exec.CommandContext(ctx, executable, "app-server", "--stdio")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("open Codex app-server stdin: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("open Codex app-server stdout: %w", err)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start Codex app-server: %w", err)
	}
	defer func() {
		_ = stdin.Close()
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		_ = cmd.Wait()
	}()

	requests := []interface{}{
		map[string]interface{}{
			"id":     1,
			"method": "initialize",
			"params": map[string]interface{}{
				"clientInfo": map[string]interface{}{
					"name":    "gesta_agent",
					"title":   "Gesta Agent",
					"version": "1",
				},
			},
		},
		map[string]interface{}{"method": "initialized", "params": map[string]interface{}{}},
		map[string]interface{}{
			"id":     2,
			"method": "thread/read",
			"params": map[string]interface{}{"threadId": threadID, "includeTurns": true},
		},
	}
	encoder := json.NewEncoder(stdin)
	for _, request := range requests {
		if err := encoder.Encode(request); err != nil {
			return nil, fmt.Errorf("write Codex app-server request: %w", err)
		}
	}

	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 64*1024), 64*1024*1024)
	for scanner.Scan() {
		var response rpcResponse
		if err := json.Unmarshal(scanner.Bytes(), &response); err != nil {
			continue
		}
		if string(response.ID) != "2" {
			continue
		}
		if response.Error != nil {
			return nil, fmt.Errorf("codex thread/read failed (%d): %s", response.Error.Code, response.Error.Message)
		}
		var result threadReadResponse
		if err := json.Unmarshal(response.Result, &result); err != nil {
			return nil, fmt.Errorf("decode Codex thread/read response: %w", err)
		}
		return result.Thread.Turns, nil
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read Codex app-server response: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("codex thread/read timed out: %w", err)
	}
	detail := strings.TrimSpace(stderr.String())
	if detail != "" {
		return nil, fmt.Errorf("codex app-server exited without a thread/read response: %s", detail)
	}
	return nil, errors.New("codex app-server exited without a thread/read response")
}
