package daemon

import (
	"encoding/json"
	"errors"
	"os"
	osuser "os/user"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/gesta-run/gesta-agent/pkg/model"
	"github.com/gesta-run/gesta-agent/pkg/util"
)

type Config struct {
	ServerURL       string `json:"server_url,omitempty"`
	ControlURL      string `json:"control_url,omitempty"`
	CustomerID      string `json:"customer_id"`
	DeploymentID    string `json:"deployment_id"`
	DaemonID        string `json:"daemon_id"`
	APIKey          string `json:"api_key,omitempty"`
	Token           string `json:"token,omitempty"`
	DeviceID        string `json:"device_id"`
	TeamID          string `json:"team_id,omitempty"`
	EnrollmentKeyID string `json:"enrollment_key_id,omitempty"`
	HostType        string `json:"host_type"`
	InstallMode     string `json:"install_mode"`
	PolicyVersion   string `json:"policy_version"`
	DataDir         string `json:"data_dir"`
	UsageWindow     string `json:"usage_window"`
}

func DefaultDataDir() string {
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, ".gesta")
	}
	return ".gesta"
}

func DefaultStatePath() string {
	return filepath.Join(DefaultDataDir(), "state.json")
}

func legacyAgentSecConfigPath() string {
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, ".agentsec", "daemon.json")
	}
	return filepath.Join(".agentsec", "daemon.json")
}

func legacyConfigPaths() []string {
	return []string{
		filepath.Join(DefaultDataDir(), "daemon.json"),
		legacyAgentSecConfigPath(),
	}
}

func LoadConfig(path string) (Config, error) {
	if path == "" {
		path = DefaultStatePath()
		if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
			for _, legacyPath := range legacyConfigPaths() {
				if _, legacyErr := os.Stat(legacyPath); legacyErr == nil {
					path = legacyPath
					break
				}
			}
		}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, err
	}
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return Config{}, err
	}
	normalizeConfig(&cfg, path)
	return cfg, nil
}

func SaveConfig(path string, cfg Config) error {
	if path == "" {
		path = DefaultStatePath()
	}
	normalizeConfig(&cfg, path)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	persisted := cfg
	persisted.ControlURL = ""
	persisted.TeamID = ""
	if persisted.APIKey == "" {
		persisted.APIKey = persisted.Token
	}
	persisted.Token = ""
	data, err := json.MarshalIndent(persisted, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}

func normalizeConfig(cfg *Config, path string) {
	if cfg.APIKey == "" {
		cfg.APIKey = cfg.Token
	}
	if cfg.Token == "" {
		cfg.Token = cfg.APIKey
	}
	if cfg.ServerURL == "" {
		cfg.ServerURL = cfg.ControlURL
	}
	if cfg.ControlURL == "" {
		cfg.ControlURL = cfg.ServerURL
	}
	if cfg.DeploymentID == "" {
		cfg.DeploymentID = "single-node"
	}
	if cfg.HostType == "" {
		cfg.HostType = "laptop"
	}
	if cfg.InstallMode == "" {
		cfg.InstallMode = "daemon"
	}
	if cfg.PolicyVersion == "" {
		cfg.PolicyVersion = model.DefaultPolicyVersion
	}
	if cfg.DataDir == "" {
		cfg.DataDir = DefaultDataDir()
	}
	if cfg.UsageWindow == "" {
		cfg.UsageWindow = "10m"
	}
}

func (c Config) EffectiveServerURL() string {
	if c.ServerURL != "" {
		return c.ServerURL
	}
	return c.ControlURL
}

func (c Config) EffectiveUsageWindow() time.Duration {
	window, err := time.ParseDuration(c.UsageWindow)
	if err != nil || window <= 0 {
		return 10 * time.Minute
	}
	return window
}

func NewRuntimeConfig(serverURL string) Config {
	if serverURL == "" {
		serverURL = "http://localhost:8080"
	}
	return Config{
		ServerURL:     serverURL,
		ControlURL:    serverURL,
		DeploymentID:  "single-node",
		DeviceID:      util.NewID("dev"),
		HostType:      "laptop",
		InstallMode:   "daemon",
		PolicyVersion: model.DefaultPolicyVersion,
		DataDir:       DefaultDataDir(),
		UsageWindow:   "10m",
	}
}

func NewDirectRuntimeConfig(serverURL, apiKey string) Config {
	cfg := NewRuntimeConfig(serverURL)
	apiKey = strings.TrimSpace(apiKey)
	hostname, _ := os.Hostname()
	localUser := localUsername()
	identity := strings.Join([]string{
		strings.ToLower(hostname),
		runtime.GOOS,
		localUser,
		util.HashString(apiKey),
	}, "|")
	cfg.DeviceID = "dev_" + util.ShortHash(identity)
	cfg.DaemonID = "daemon_" + util.ShortHash(identity)
	cfg.APIKey = apiKey
	cfg.Token = apiKey
	return cfg
}

func localUsername() string {
	if current, err := osuser.Current(); err == nil && current.Username != "" {
		return strings.TrimSpace(strings.ToLower(current.Username))
	}
	if value := strings.TrimSpace(os.Getenv("USER")); value != "" {
		return strings.ToLower(value)
	}
	if value := strings.TrimSpace(os.Getenv("USERNAME")); value != "" {
		return strings.ToLower(value)
	}
	return ""
}

func (c Config) ValidateEnrolled() error {
	if c.EffectiveServerURL() == "" || c.DaemonID == "" || c.Token == "" || c.DeviceID == "" {
		return errors.New("daemon runtime config is incomplete; run with --control-url and --apikey")
	}
	return nil
}

func RuntimeOS() string {
	return runtime.GOOS
}

func RuntimeArch() string {
	return runtime.GOARCH
}
