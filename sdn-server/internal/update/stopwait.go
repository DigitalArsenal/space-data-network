package update

// WHAT A DAEMON WAITS FOR BEFORE IT STOPS FOR AN UPDATE (stack issue 1044).
//
// An update's restart once stopped the node while it published a dataset,
// and the publication was lost. The daemon now finishes its running dataset
// publications before it stops for an update, for at most a bound, and
// accepts no new one meanwhile. While it waits it says so in
// updates/stop-wait.json, which `spacedatanetwork status` prints as
// update_stop_wait; the daemon removes the file when it begins its shutdown,
// and at every start.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

const stopWaitFileName = "stop-wait.json"

// StopWaitPublications is the StopWait.For of a wait for dataset
// publications.
const StopWaitPublications = "dataset-publications"

// StopWait is a daemon's wait, before it stops for an update, for work that
// was running when the update asked it to stop.
type StopWait struct {
	// For names the work: StopWaitPublications.
	For     string `json:"for"`
	Running int    `json:"running"`
	PID     int    `json:"pid"`
	Since   string `json:"since"`
	// Until is when the daemon stops whatever still runs.
	Until string `json:"until"`
}

// WriteStopWait records that the daemon waits.
func WriteStopWait(paths Paths, wait StopWait) error {
	if err := os.MkdirAll(paths.Updates, 0o700); err != nil {
		return err
	}
	return writeJSONAtomic(filepath.Join(paths.Updates, stopWaitFileName), &wait)
}

// ReadStopWait is the daemon's recorded wait, or nil.
func ReadStopWait(paths Paths) *StopWait {
	data, err := os.ReadFile(filepath.Join(paths.Updates, stopWaitFileName))
	if err != nil {
		return nil
	}
	var wait StopWait
	if json.Unmarshal(data, &wait) != nil || wait.For == "" {
		return nil
	}
	return &wait
}

// ClearStopWait removes the recorded wait.
func ClearStopWait(paths Paths) error {
	err := os.Remove(filepath.Join(paths.Updates, stopWaitFileName))
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

// StopWaitTimes formats the times of a wait that begins at since and lasts
// at most bound.
func StopWaitTimes(since time.Time, bound time.Duration) (string, string) {
	return since.UTC().Format(time.RFC3339), since.Add(bound).UTC().Format(time.RFC3339)
}
