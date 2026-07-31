package activitydetail

import (
	"path/filepath"

	"github.com/gesta-run/gesta-agent/pkg/lockfile"
)

var activityLockOptions = lockfile.Options{
	Label:        "activity detail",
	Wait:         lockWait,
	StaleAfter:   lockStaleAfter,
	PollInterval: lockPollInterval,
}

func (s Store) acquireLock(wait bool) (func(), error) {
	path := filepath.Join(s.dataDir, "runtime", "activity-details.lock")
	return lockfile.Acquire(path, wait, activityLockOptions)
}
