package options

import (
	"flag"
	"os"
	"time"
)

type RunOptions struct {
	ControlURL  string
	APIKey      string
	Token       string
	Agent       string
	Interval    time.Duration
	UsageWindow time.Duration
}

func NewRunOptions() RunOptions {
	usageWindow := durationEnvOrDefault("GESTA_USAGE_WINDOW", 10*time.Minute)
	return RunOptions{
		ControlURL:  os.Getenv("GESTA_CONTROL_URL"),
		Agent:       "codex",
		Interval:    durationEnvOrDefault("GESTA_COLLECTION_INTERVAL", time.Minute),
		UsageWindow: usageWindow,
	}
}

func (o *RunOptions) AddFlags(fs *flag.FlagSet) {
	fs.StringVar(&o.ControlURL, "control-url", o.ControlURL, "control plane URL")
	fs.StringVar(&o.APIKey, "apikey", o.APIKey, "API key used to send daemon telemetry")
	fs.StringVar(&o.Token, "token", o.Token, "deprecated alias for --apikey")
	fs.StringVar(&o.Agent, "agent", o.Agent, "agent type used when run is given a guarded command")
	fs.DurationVar(&o.Interval, "interval", o.Interval, "collection interval")
	fs.DurationVar(&o.UsageWindow, "usage-window", o.UsageWindow, "token usage accounting window")
}

func durationEnvOrDefault(name string, fallback time.Duration) time.Duration {
	if value := os.Getenv(name); value != "" {
		if parsed, err := time.ParseDuration(value); err == nil && parsed > 0 {
			return parsed
		}
	}
	return fallback
}
