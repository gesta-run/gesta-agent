package model

import "time"

type AdapterStatus struct {
	Name         string              `json:"name"`
	Detected     bool                `json:"detected"`
	Version      string              `json:"version,omitempty"`
	Status       string              `json:"status"`
	UpdatedAt    string              `json:"updated_at"`
	MCPInventory *MCPInventoryStatus `json:"mcp_inventory,omitempty"`
}

type MCPServerConfiguration struct {
	Name    string `json:"name"`
	Enabled bool   `json:"enabled"`
}

type MCPInventoryStatus struct {
	ScanStatus string                   `json:"scan_status"`
	ObservedAt string                   `json:"observed_at"`
	Hash       string                   `json:"hash,omitempty"`
	ErrorCode  string                   `json:"error_code,omitempty"`
	Servers    []MCPServerConfiguration `json:"servers,omitempty"`
}

type HeartbeatRequest struct {
	DaemonID      string          `json:"daemon_id,omitempty"`
	DeviceID      string          `json:"device_id,omitempty"`
	TeamID        string          `json:"team_id,omitempty"`
	Hostname      string          `json:"hostname,omitempty"`
	InstallMode   string          `json:"install_mode,omitempty"`
	OS            string          `json:"os,omitempty"`
	Arch          string          `json:"arch,omitempty"`
	DaemonVersion string          `json:"daemon_version"`
	PolicyVersion string          `json:"policy_version"`
	HostType      string          `json:"host_type,omitempty"`
	HealthStatus  string          `json:"health_status"`
	Adapters      []AdapterStatus `json:"adapters"`
}

type AgentUpgradeArtifact struct {
	URL    string `json:"url"`
	SHA256 string `json:"sha256"`
}

type AgentUpgradePolicy struct {
	Mode          string                `json:"mode"`
	Channel       string                `json:"channel,omitempty"`
	TargetVersion string                `json:"target_version,omitempty"`
	URL           string                `json:"url,omitempty"`
	SHA256        string                `json:"sha256,omitempty"`
	ChecksumURL   string                `json:"checksum_url,omitempty"`
	HookLauncher  *AgentUpgradeArtifact `json:"hook_launcher,omitempty"`
	Required      bool                  `json:"required,omitempty"`
	RolloutID     string                `json:"rollout_id,omitempty"`
}

type HeartbeatResponse struct {
	Daemon               map[string]interface{}        `json:"daemon,omitempty"`
	Upgrade              *AgentUpgradePolicy           `json:"upgrade,omitempty"`
	OutputClassification *OutputClassificationSettings `json:"output_classification,omitempty"`
	DailyWorkTimezone    string                        `json:"daily_work_timezone,omitempty"`
	TurnUsageTotal       string                        `json:"turn_usage_total,omitempty"`
}

type OutputClassificationSettings struct {
	Revision      int64    `json:"revision"`
	CodeSuffixes  []string `json:"code_suffixes"`
	CodeFilenames []string `json:"code_filenames"`
}

type EventEnvelope struct {
	EventID      string                 `json:"event_id"`
	CustomerID   string                 `json:"customer_id"`
	DeploymentID string                 `json:"deployment_id"`
	DaemonID     string                 `json:"daemon_id"`
	DeviceID     string                 `json:"device_id"`
	UserID       string                 `json:"user_id,omitempty"`
	UserName     string                 `json:"user_name,omitempty"`
	TeamID       string                 `json:"team_id"`
	EventType    string                 `json:"event_type"`
	Source       string                 `json:"source"`
	AgentType    string                 `json:"agent_type,omitempty"`
	CreatedAt    time.Time              `json:"created_at"`
	Payload      map[string]interface{} `json:"payload"`
}

type EventBatch struct {
	Events []EventEnvelope `json:"events"`
}

type EventRecord struct {
	EventEnvelope
	Model       string    `json:"model,omitempty"`
	Repo        string    `json:"repo,omitempty"`
	SessionID   string    `json:"session_id,omitempty"`
	TotalTokens int64     `json:"total_tokens"`
	ReceivedAt  time.Time `json:"received_at"`
}

type APIError struct {
	Error string `json:"error"`
}

type PolicyRule struct {
	RuleID        string `json:"rule_id"`
	Name          string `json:"name"`
	Description   string `json:"description"`
	Status        string `json:"status"`
	AgentType     string `json:"agent_type"`
	Scope         string `json:"scope"`
	MatchType     string `json:"match_type"`
	MatchValue    string `json:"match_value"`
	Action        string `json:"action"`
	Priority      int    `json:"priority"`
	RiskLevel     string `json:"risk_level"`
	Owner         string `json:"owner"`
	PolicyVersion string `json:"policy_version"`
	HitCount      int    `json:"hit_count"`
}

type PolicyRulesResponse struct {
	Rules []PolicyRule `json:"rules"`
}

type SensitiveRule struct {
	RuleID       string  `json:"rule_id"`
	Name         string  `json:"name"`
	Description  string  `json:"description"`
	Status       string  `json:"status"`
	Source       string  `json:"source"`
	DetectorType string  `json:"detector_type"`
	Pattern      string  `json:"pattern"`
	Category     string  `json:"category"`
	Severity     string  `json:"severity"`
	Action       string  `json:"action"`
	SampleMode   string  `json:"sample_mode"`
	Confidence   float64 `json:"confidence"`
	Priority     int     `json:"priority"`
	Owner        string  `json:"owner"`
	HitCount     int     `json:"hit_count"`
}

type SensitiveRulesResponse struct {
	Rules []SensitiveRule `json:"rules"`
}

type ContextRule struct {
	RuleID         string   `json:"rule_id"`
	Name           string   `json:"name"`
	Description    string   `json:"description"`
	Status         string   `json:"status"`
	MatchType      string   `json:"match_type"`
	Keywords       []string `json:"keywords"`
	Pattern        string   `json:"pattern"`
	ContextContent string   `json:"context_content"`
	Priority       int      `json:"priority"`
	AgentType      string   `json:"agent_type"`
	HitCount       int      `json:"hit_count"`
}

type ContextRuleBundle struct {
	Version     string        `json:"version"`
	GeneratedAt time.Time     `json:"generated_at"`
	Rules       []ContextRule `json:"rules"`
}

type PolicyApproval struct {
	ApprovalID     string     `json:"approval_id"`
	OrgID          string     `json:"org_id"`
	Status         string     `json:"status"`
	RuleIDs        []string   `json:"rule_ids"`
	AgentType      string     `json:"agent_type"`
	CommandHash    string     `json:"command_hash"`
	CommandPreview string     `json:"command_preview"`
	Reason         string     `json:"reason"`
	DaemonID       string     `json:"daemon_id"`
	DeviceID       string     `json:"device_id"`
	UserID         string     `json:"user_id"`
	UserName       string     `json:"user_name"`
	TeamID         string     `json:"team_id"`
	CreatedAt      time.Time  `json:"created_at"`
	ExpiresAt      time.Time  `json:"expires_at"`
	DecidedBy      string     `json:"decided_by,omitempty"`
	DecidedAt      *time.Time `json:"decided_at,omitempty"`
	ConsumedAt     *time.Time `json:"consumed_at,omitempty"`
}

type CreatePolicyApprovalRequest struct {
	DaemonID       string   `json:"daemon_id"`
	DeviceID       string   `json:"device_id"`
	TeamID         string   `json:"team_id"`
	AgentType      string   `json:"agent_type"`
	CommandHash    string   `json:"command_hash"`
	CommandPreview string   `json:"command_preview"`
	RuleIDs        []string `json:"rule_ids"`
	Reason         string   `json:"reason"`
}

type ConsumePolicyApprovalRequest struct {
	DaemonID    string `json:"daemon_id"`
	DeviceID    string `json:"device_id"`
	TeamID      string `json:"team_id"`
	AgentType   string `json:"agent_type"`
	CommandHash string `json:"command_hash"`
}

type PolicyApprovalResolveResponse struct {
	Approved bool            `json:"approved"`
	Approval *PolicyApproval `json:"approval,omitempty"`
}
