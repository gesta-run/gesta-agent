package agent

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gesta-run/gesta-agent/pkg/atomicfile"
	"github.com/gesta-run/gesta-agent/pkg/daemon"
	"github.com/gesta-run/gesta-agent/pkg/lockfile"
)

const (
	dailyRecapNoticeHour          = 17
	dailyRecapNoticeSchemaVersion = 1
	maxDailyRecapNoticeStateBytes = 4 * 1024
	productionAPIHost             = "api.gesta.run"
	productionRecapURL            = "https://console.gesta.run/#my-recap"
	preproductionRecapURL         = "https://pre-console.gesta.run/#my-recap"
)

var dailyRecapNoticeNow = time.Now

var dailyRecapNoticeLockOptions = lockfile.Options{
	Label:        "daily recap notice",
	Wait:         2 * time.Second,
	StaleAfter:   time.Minute,
	PollInterval: 10 * time.Millisecond,
}

type dailyRecapNoticeState struct {
	SchemaVersion int    `json:"schema_version"`
	LastShownDate string `json:"last_shown_date"`
}

func dailyRecapNoticeBestEffort(cfg daemon.Config) string {
	now := dailyRecapNoticeNow()
	if now.Hour() < dailyRecapNoticeHour {
		return ""
	}

	claimed, err := claimDailyRecapNotice(cfg.DataDir, now)
	if err != nil {
		fmt.Fprintf(os.Stderr, "gesta-agent hook: daily recap notice was not recorded: %v\n", err)
	}
	if !claimed {
		return ""
	}

	recapURL, environment := consoleRecapURL(cfg.EffectiveServerURL())
	return "Gesta recap" + environment +
		" · Your work recap is ready · [Review your day →](" + recapURL + ")"
}

func consoleRecapURL(controlURL string) (string, string) {
	parsed, err := url.Parse(strings.TrimSpace(controlURL))
	if err == nil && strings.EqualFold(parsed.Hostname(), productionAPIHost) {
		return productionRecapURL, ""
	}
	return preproductionRecapURL, " (Pre)"
}

func claimDailyRecapNotice(dataDir string, now time.Time) (bool, error) {
	dataDir = strings.TrimSpace(dataDir)
	if dataDir == "" {
		return false, nil
	}
	statePath := filepath.Join(dataDir, "runtime", "daily-recap-notice.json")
	lockPath := filepath.Join(dataDir, "runtime", "daily-recap-notice.lock")
	unlock, err := lockfile.Acquire(lockPath, true, dailyRecapNoticeLockOptions)
	if err != nil {
		return false, fmt.Errorf("acquire daily recap notice lock: %w", err)
	}
	defer unlock()

	state, err := loadDailyRecapNoticeState(statePath)
	if err != nil {
		return false, err
	}
	today := now.Format("2006-01-02")
	if state.LastShownDate == today {
		return false, nil
	}
	state = dailyRecapNoticeState{
		SchemaVersion: dailyRecapNoticeSchemaVersion,
		LastShownDate: today,
	}
	if err := atomicfile.WriteJSON(statePath, state); err != nil {
		return false, fmt.Errorf("write daily recap notice state: %w", err)
	}
	return true, nil
}

func loadDailyRecapNoticeState(path string) (dailyRecapNoticeState, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return dailyRecapNoticeState{}, nil
	}
	if err != nil {
		return dailyRecapNoticeState{}, fmt.Errorf("read daily recap notice state: %w", err)
	}
	if len(data) == 0 || len(data) > maxDailyRecapNoticeStateBytes {
		return dailyRecapNoticeState{}, fmt.Errorf(
			"daily recap notice state size %d is outside the supported range",
			len(data),
		)
	}
	var state dailyRecapNoticeState
	if err := json.Unmarshal(data, &state); err != nil {
		return dailyRecapNoticeState{}, fmt.Errorf("decode daily recap notice state: %w", err)
	}
	if state.SchemaVersion != dailyRecapNoticeSchemaVersion {
		return dailyRecapNoticeState{}, fmt.Errorf(
			"unsupported daily recap notice state schema version %d",
			state.SchemaVersion,
		)
	}
	return state, nil
}
