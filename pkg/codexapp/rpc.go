package codexapp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"
	"time"
)

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

type rpcStreamResult struct {
	Response rpcResponse
	Err      error
}

type synchronizedBuffer struct {
	mu     sync.Mutex
	buffer bytes.Buffer
}

func (b *synchronizedBuffer) Write(data []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buffer.Write(data)
}

func (b *synchronizedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buffer.String()
}

func readThread(
	ctx context.Context,
	executable, threadID string,
	initializeTimeout time.Duration,
) ([]Turn, error) {
	cmd := exec.CommandContext(ctx, executable, "app-server", "--stdio")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("open Codex app-server stdin: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("open Codex app-server stdout: %w", err)
	}
	var stderr synchronizedBuffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start Codex app-server: %w", err)
	}
	streamCtx, stopStream := context.WithCancel(ctx)
	responses := streamRPCResponses(streamCtx, stdout)
	defer func() {
		stopStream()
		_ = stdin.Close()
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		_ = cmd.Wait()
	}()

	encoder := json.NewEncoder(stdin)
	if err := encoder.Encode(map[string]interface{}{
		"id":     1,
		"method": "initialize",
		"params": map[string]interface{}{
			"clientInfo": map[string]interface{}{
				"name":    "gesta_agent",
				"title":   "Gesta Agent",
				"version": "1",
			},
		},
	}); err != nil {
		return nil, fmt.Errorf("write Codex initialize request: %w", err)
	}

	initializeCtx := ctx
	cancelInitialize := func() {}
	if initializeTimeout > 0 {
		initializeCtx, cancelInitialize = context.WithTimeout(ctx, initializeTimeout)
	}
	initializeResponse, err := readRPCResponse(initializeCtx, responses, "1", &stderr)
	cancelInitialize()
	if err != nil {
		return nil, fmt.Errorf("initialize Codex app-server: %w", err)
	}
	if initializeResponse.Error != nil {
		return nil, fmt.Errorf(
			"initialize Codex app-server failed (%d): %s",
			initializeResponse.Error.Code,
			initializeResponse.Error.Message,
		)
	}
	var initializeResult map[string]interface{}
	if err := json.Unmarshal(initializeResponse.Result, &initializeResult); err != nil || initializeResult == nil {
		return nil, errors.New("initialize Codex app-server returned an invalid result")
	}

	requests := []interface{}{
		map[string]interface{}{"method": "initialized", "params": map[string]interface{}{}},
		map[string]interface{}{
			"id":     2,
			"method": "thread/read",
			"params": map[string]interface{}{"threadId": threadID, "includeTurns": true},
		},
	}
	for _, request := range requests {
		if err := encoder.Encode(request); err != nil {
			return nil, fmt.Errorf("write Codex app-server request: %w", err)
		}
	}

	response, err := readRPCResponse(ctx, responses, "2", &stderr)
	if err != nil {
		return nil, fmt.Errorf("read Codex thread: %w", err)
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

func streamRPCResponses(ctx context.Context, stdout io.Reader) <-chan rpcStreamResult {
	results := make(chan rpcStreamResult)
	go func() {
		defer close(results)
		scanner := bufio.NewScanner(stdout)
		scanner.Buffer(make([]byte, 64*1024), 64*1024*1024)
		for scanner.Scan() {
			var response rpcResponse
			if err := json.Unmarshal(scanner.Bytes(), &response); err != nil || len(response.ID) == 0 {
				continue
			}
			select {
			case results <- rpcStreamResult{Response: response}:
			case <-ctx.Done():
				return
			}
		}
		if err := scanner.Err(); err != nil {
			select {
			case results <- rpcStreamResult{Err: fmt.Errorf("read Codex app-server response: %w", err)}:
			case <-ctx.Done():
			}
		}
	}()
	return results
}

func readRPCResponse(
	ctx context.Context,
	responses <-chan rpcStreamResult,
	responseID string,
	stderr *synchronizedBuffer,
) (rpcResponse, error) {
	for {
		select {
		case <-ctx.Done():
			return rpcResponse{}, fmt.Errorf("Codex app-server response timed out: %w", ctx.Err())
		case result, ok := <-responses:
			if !ok {
				detail := strings.TrimSpace(stderr.String())
				if detail != "" {
					return rpcResponse{}, fmt.Errorf(
						"Codex app-server exited without response %s: %s",
						responseID,
						detail,
					)
				}
				return rpcResponse{}, fmt.Errorf("Codex app-server exited without response %s", responseID)
			}
			if result.Err != nil {
				return rpcResponse{}, result.Err
			}
			if string(result.Response.ID) == responseID {
				return result.Response, nil
			}
		}
	}
}
