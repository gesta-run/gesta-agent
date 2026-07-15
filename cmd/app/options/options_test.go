package options

import (
	"testing"
	"time"
)

func TestNewRunOptionsDefaultsCollectionIntervalToOneMinute(t *testing.T) {
	t.Setenv("GESTA_USAGE_WINDOW", "15m")
	t.Setenv("GESTA_COLLECTION_INTERVAL", "")

	opts := NewRunOptions()
	if opts.Interval != time.Minute {
		t.Fatalf("Interval = %s, want 1m", opts.Interval)
	}
	if opts.UsageWindow != 15*time.Minute {
		t.Fatalf("UsageWindow = %s, want 15m", opts.UsageWindow)
	}
}

func TestNewRunOptionsAllowsCollectionIntervalOverride(t *testing.T) {
	t.Setenv("GESTA_COLLECTION_INTERVAL", "2m")

	opts := NewRunOptions()
	if opts.Interval != 2*time.Minute {
		t.Fatalf("Interval = %s, want 2m", opts.Interval)
	}
}
