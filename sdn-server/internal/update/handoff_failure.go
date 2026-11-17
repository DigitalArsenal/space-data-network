package update

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// HandoffFailureSchema identifies updates/handoff-failure.json.
const HandoffFailureSchema = "org.spacedatanetwork.update.handoff-failure.v1"

const handoffFailureFileName = "handoff-failure.json"

// HandoffFailure is the durable record of consecutive self-upgrade hand-offs
// that swapped nothing. The helper writes it when it stopped the daemon and
// then failed before the swap, before it restarts the build that was running;
// the daemon writes it when its hand-off bound passes. A daemon reads it at
// start, so a restart does not reset the retry backoff. A completed swap
// removes it.
type HandoffFailure struct {
	Schema   string `json:"schema"`
	UpdateID string `json:"update_id"`
	// Failures counts consecutive failed hand-offs, of any update.
	Failures int       `json:"failures"`
	FailedAt time.Time `json:"failed_at"`
	Reason   string    `json:"reason,omitempty"`
}

func handoffFailurePath(paths Paths) string {
	return filepath.Join(paths.Updates, handoffFailureFileName)
}

// LoadHandoffFailure returns the recorded hand-off failure, or nil when none
// is recorded.
func LoadHandoffFailure(paths Paths) (*HandoffFailure, error) {
	data, err := os.ReadFile(handoffFailurePath(paths))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read hand-off failure record: %w", err)
	}
	var record HandoffFailure
	if err := json.Unmarshal(data, &record); err != nil {
		return nil, fmt.Errorf("parse hand-off failure record: %w", err)
	}
	if record.Schema != HandoffFailureSchema || strings.TrimSpace(record.UpdateID) == "" || record.Failures < 1 || record.FailedAt.IsZero() {
		return nil, fmt.Errorf("invalid hand-off failure record %s", handoffFailurePath(paths))
	}
	return &record, nil
}

// RecordHandoffFailure adds one failed hand-off of updateID to the record
// and returns it. An unreadable record counts as none.
func RecordHandoffFailure(paths Paths, updateID, reason string, now time.Time) (*HandoffFailure, error) {
	failures := 1
	if previous, err := LoadHandoffFailure(paths); err == nil && previous != nil {
		failures = previous.Failures + 1
	}
	record := &HandoffFailure{
		Schema:   HandoffFailureSchema,
		UpdateID: updateID,
		Failures: failures,
		FailedAt: now.UTC(),
		Reason:   reason,
	}
	data, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(paths.Updates, 0o755); err != nil {
		return nil, err
	}
	path := handoffFailurePath(paths)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o644); err != nil {
		return nil, err
	}
	if err := os.Rename(tmp, path); err != nil {
		return nil, err
	}
	return record, nil
}

// ClearHandoffFailure removes the record after a completed swap.
func ClearHandoffFailure(paths Paths) error {
	err := os.Remove(handoffFailurePath(paths))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}
