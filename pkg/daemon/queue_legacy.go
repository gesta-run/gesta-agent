package daemon

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/gesta-run/gesta-agent/pkg/model"
)

func (q Queue) LegacyStats() (LegacyQueueStats, error) {
	path := q.legacyPath
	if path == "" {
		path = filepath.Join(filepath.Dir(q.path), "queue.jsonl")
	}
	info, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return LegacyQueueStats{}, nil
	}
	if err != nil {
		return LegacyQueueStats{}, err
	}
	stats := LegacyQueueStats{
		Bytes:      info.Size(),
		EventTypes: make(map[string]int),
	}
	file, err := os.Open(path)
	if err != nil {
		return stats, err
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), 10*1024*1024)
	line := 0
	for scanner.Scan() {
		line++
		var event model.EventEnvelope
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			return stats, fmt.Errorf("read legacy queue %s line %d: %w", path, line, err)
		}
		stats.Events++
		stats.EventTypes[event.EventType]++
		if stats.OldestQueuedAt.IsZero() || event.CreatedAt.Before(stats.OldestQueuedAt) {
			stats.OldestQueuedAt = event.CreatedAt
		}
		if stats.NewestQueuedAt.IsZero() || event.CreatedAt.After(stats.NewestQueuedAt) {
			stats.NewestQueuedAt = event.CreatedAt
		}
	}
	return stats, scanner.Err()
}
