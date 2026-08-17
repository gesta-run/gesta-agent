package model

import "time"

type MemorySettings struct {
	Enabled bool `json:"enabled"`
}

type MemoryWorkspace struct {
	CWDName   string   `json:"cwd_name,omitempty"`
	ChildDirs []string `json:"child_dirs,omitempty"`
}

type MemoryContextRequest struct {
	DaemonID  string          `json:"daemon_id"`
	Prompt    string          `json:"prompt"`
	Context   string          `json:"context,omitempty"`
	Workspace MemoryWorkspace `json:"workspace,omitempty"`
}

type MemorySearchRequest struct {
	DaemonID  string          `json:"daemon_id"`
	Query     string          `json:"query"`
	Limit     int             `json:"limit,omitempty"`
	Workspace MemoryWorkspace `json:"workspace,omitempty"`
}

type MemoryRememberRequest struct {
	RequestID  string          `json:"request_id"`
	DaemonID   string          `json:"daemon_id"`
	Content    string          `json:"content"`
	OccurredAt time.Time       `json:"occurred_at"`
	Workspace  MemoryWorkspace `json:"workspace,omitempty"`
}

type Memory struct {
	FactID         string     `json:"fact_id"`
	Content        string     `json:"content"`
	RelevanceScore float64    `json:"relevance_score"`
	WorkspaceBoost float64    `json:"workspace_boost"`
	Score          float64    `json:"score"`
	ValidAt        *time.Time `json:"valid_at,omitempty"`
	InvalidAt      *time.Time `json:"invalid_at,omitempty"`
}

type MemorySearchResponse struct {
	Memories  []Memory `json:"memories"`
	Truncated bool     `json:"truncated"`
}

type MemoryRememberResponse struct {
	Status    string `json:"status"`
	EpisodeID string `json:"episode_id"`
	Duplicate bool   `json:"duplicate,omitempty"`
}
