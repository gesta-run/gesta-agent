package codexapp

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

const (
	readTimeout                = 10 * time.Second
	candidateInitializeTimeout = 3 * time.Second
)

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

func ReadTurn(ctx context.Context, threadID, turnID string) (Turn, error) {
	threadID = strings.TrimSpace(threadID)
	turnID = strings.TrimSpace(turnID)
	if threadID == "" || turnID == "" {
		return Turn{}, errors.New("thread id and turn id are required")
	}
	candidates, err := resolveExecutableCandidates()
	if err != nil {
		return Turn{}, err
	}
	readCtx, cancel := context.WithTimeout(ctx, readTimeout)
	defer cancel()
	return readTurnFromCandidates(readCtx, candidates, threadID, turnID)
}

func readTurnFromCandidates(
	ctx context.Context,
	candidates []executableCandidate,
	threadID, turnID string,
) (Turn, error) {
	return readTurnFromCandidatesWithTimeout(
		ctx,
		candidates,
		threadID,
		turnID,
		candidateInitializeTimeout,
	)
}

func readTurnFromCandidatesWithTimeout(
	ctx context.Context,
	candidates []executableCandidate,
	threadID, turnID string,
	automaticCandidateInitializeTimeout time.Duration,
) (Turn, error) {
	var candidateErrors []error
	for _, candidate := range candidates {
		initializeTimeout := time.Duration(0)
		if !candidate.Explicit {
			initializeTimeout = automaticCandidateInitializeTimeout
		}
		turns, err := readThread(ctx, candidate.Path, threadID, initializeTimeout)
		if err != nil {
			candidateErr := fmt.Errorf("%s Codex executable: %w", candidate.Source, err)
			if candidate.Explicit {
				return Turn{}, candidateErr
			}
			candidateErrors = append(candidateErrors, candidateErr)
			if ctx.Err() != nil {
				break
			}
			continue
		}

		for _, turn := range turns {
			if turn.ID == turnID {
				return turn, nil
			}
		}
		candidateErr := fmt.Errorf(
			"%s Codex executable: turn %q was not found in thread %q",
			candidate.Source,
			turnID,
			threadID,
		)
		if candidate.Explicit {
			return Turn{}, candidateErr
		}
		candidateErrors = append(candidateErrors, candidateErr)
	}
	if len(candidateErrors) == 0 {
		return Turn{}, errors.New("no Codex executable candidates were provided")
	}
	return Turn{}, fmt.Errorf("no discovered Codex executable could read the turn: %w", errors.Join(candidateErrors...))
}
