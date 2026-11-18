package update

// THE NODE'S UPDATE STATUS, AS IT STATES IT.
//
// The owner asked "can we see what version all the nodes are on?". The answer
// a node can give about another is only what that node says: its identify
// agent version, and, from 1.2.0, a small status message it publishes on the
// update topic of its channel. The message is authored and signed by the node's
// own peer key (gossipsub's strict signing policy), and a receiving node
// accepts it only when its peer_id is that author. So "failed and rolled back"
// on a fleet view is the node's own statement, read from its own deploy
// ledger, never an inference by the viewer.
//
// A node built before this message existed (1.1.9 and older) rejects it in its
// update topic validator, which accepts only signals: the message is neither
// delivered to it nor forwarded by it, and without peer scoring the rejection
// costs the sender nothing.

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	// StatusSchema identifies the status message.
	StatusSchema = "org.spacedatanetwork.update.status.v1"
	// StatusMaxBytes bounds a status message.
	StatusMaxBytes = 4 << 10

	// Status states.
	StatusCurrent = "current"
	StatusPending = "pending"
	StatusFailed  = "failed"

	statusReasonLimit = 200
)

// StatusPendingUpdate is the update a node has accepted and not yet acted on.
type StatusPendingUpdate struct {
	UpdateID string `json:"update_id"`
	Version  string `json:"version"`
	Sequence int64  `json:"sequence"`
	DueAt    string `json:"due_at"`
}

// StatusFailure is an update a node installed and that failed its health
// gate or trial, from its own deploy ledger.
type StatusFailure struct {
	UpdateID string `json:"update_id"`
	Version  string `json:"version"`
	Sequence int64  `json:"sequence"`
	At       string `json:"at"`
	Reason   string `json:"reason,omitempty"`
	// RolledBack is false when the revert was refused and the failed build
	// keeps running (ledger revert-refused).
	RolledBack bool `json:"rolled_back"`
}

// StatusAck is the newest update signal a node accepted.
type StatusAck struct {
	UpdateID string `json:"update_id"`
	Sequence int64  `json:"sequence"`
}

// Status is the message a node publishes about its own update state.
type Status struct {
	Schema       string               `json:"schema"`
	PeerID       string               `json:"peer_id"`
	Version      string               `json:"version"`
	Sequence     int64                `json:"sequence"`
	UpdateID     string               `json:"update_id,omitempty"`
	State        string               `json:"state"`
	Pending      *StatusPendingUpdate `json:"pending,omitempty"`
	Failed       *StatusFailure       `json:"failed,omitempty"`
	Acknowledged *StatusAck           `json:"acknowledged,omitempty"`
	At           string               `json:"at"`
}

// ParseStatus decodes a status message. It does not authenticate it: the
// receiver checks peer_id against the message's author.
func ParseStatus(raw []byte) (*Status, error) {
	if len(raw) == 0 || len(raw) > StatusMaxBytes {
		return nil, errors.New("update status message is empty or too large")
	}
	if err := assertStrictJSONDocument(raw); err != nil {
		return nil, fmt.Errorf("parse update status: %w", err)
	}
	var status Status
	if err := json.Unmarshal(raw, &status); err != nil {
		return nil, fmt.Errorf("parse update status: %w", err)
	}
	if status.Schema != StatusSchema {
		return nil, fmt.Errorf("not an update status message: %q", status.Schema)
	}
	if strings.TrimSpace(status.PeerID) == "" || strings.TrimSpace(status.Version) == "" {
		return nil, errors.New("update status message names no peer or version")
	}
	switch status.State {
	case StatusCurrent, StatusPending, StatusFailed:
	default:
		return nil, fmt.Errorf("update status state %q is not current, pending or failed", status.State)
	}
	if _, err := time.Parse(time.RFC3339, status.At); err != nil {
		return nil, fmt.Errorf("update status at: %w", err)
	}
	return &status, nil
}

const acknowledgedFileName = "acknowledged.json"

// ReadAcknowledged is the newest update signal this node accepted
// (updates/acknowledged.json), or nil.
func ReadAcknowledged(paths Paths) *StatusAck {
	data, err := os.ReadFile(filepath.Join(paths.Updates, acknowledgedFileName))
	if err != nil {
		return nil
	}
	var ack StatusAck
	if json.Unmarshal(data, &ack) != nil || ack.UpdateID == "" {
		return nil
	}
	return &ack
}

// WriteAcknowledged records ack as the newest update signal this node
// accepted, when it is newer than the one recorded.
func WriteAcknowledged(paths Paths, ack StatusAck) error {
	if held := ReadAcknowledged(paths); held != nil && held.Sequence >= ack.Sequence {
		return nil
	}
	if err := os.MkdirAll(paths.Updates, 0o700); err != nil {
		return err
	}
	return writeJSONAtomic(filepath.Join(paths.Updates, acknowledgedFileName), &ack)
}

// AtTime is At parsed.
func (s *Status) AtTime() time.Time {
	t, _ := time.Parse(time.RFC3339, s.At)
	return t
}

// LocalStatus builds this node's status from its update root: state.json,
// pending.json and the deploy ledger. A failure the node has moved past (it
// runs a sequence at or above the failed one) is not reported.
func LocalStatus(paths Paths, peerID, version string, ack *StatusAck, now time.Time) *Status {
	status := &Status{Schema: StatusSchema, PeerID: peerID, Version: version, State: StatusCurrent,
		Acknowledged: ack, At: now.UTC().Format(time.RFC3339)}
	if state, err := LoadState(paths); err == nil {
		status.Sequence, status.UpdateID = state.Sequence, state.UpdateID
	}
	if pending, err := CurrentPending(paths); err == nil && pending != nil {
		status.State = StatusPending
		status.Pending = &StatusPendingUpdate{UpdateID: pending.UpdateID, Version: pending.Version, Sequence: pending.Sequence, DueAt: pending.DueAt}
	}
	if failure := ActiveUpdateFailure(paths); failure != nil {
		status.Failed = failure
		if status.State == StatusCurrent {
			status.State = StatusFailed
		}
	}
	return status
}

// ActiveUpdateFailure is the newest update failure in the node's deploy
// ledger that still describes it, by the rule the update_failed alert uses:
// a reverted update until the node runs its sequence or a later one, a
// refused revert until the node runs a later sequence, and neither once the
// same version passed its health gate afterwards.
func ActiveUpdateFailure(paths Paths) *StatusFailure {
	file, err := os.Open(filepath.Join(paths.Root, deployLedgerName))
	if err != nil {
		return nil
	}
	defer file.Close()
	state, err := LoadState(paths)
	if err != nil {
		return nil
	}
	failures := map[string]DeployLedgerEntry{}
	var order []string
	reader := bufio.NewReaderSize(file, 64<<10)
	for {
		line, readErr := reader.ReadBytes('\n')
		var entry DeployLedgerEntry
		if len(line) <= 4<<20 && json.Unmarshal(line, &entry) == nil {
			switch entry.Action {
			case "health-reverted", "revert-refused":
				failures[entry.UpdateID] = entry
				order = append(order, entry.UpdateID)
			case "health-passed":
				if failed, ok := failures[entry.UpdateID]; ok && failed.Version == entry.Version && entry.Sequence >= failed.Sequence {
					delete(failures, entry.UpdateID)
				}
			}
		}
		if readErr != nil {
			if !errors.Is(readErr, io.EOF) {
				return nil
			}
			break
		}
	}
	for i := len(order) - 1; i >= 0; i-- {
		failed, ok := failures[order[i]]
		if !ok {
			continue
		}
		refused := failed.Action == "revert-refused"
		if refused && state.Sequence > failed.Sequence || !refused && state.Sequence >= failed.Sequence {
			continue
		}
		reason := failed.Reason
		if len(reason) > statusReasonLimit {
			reason = strings.ToValidUTF8(reason[:statusReasonLimit], "")
		}
		return &StatusFailure{UpdateID: failed.UpdateID, Version: failed.Version, Sequence: failed.Sequence,
			At: failed.RecordedAt, Reason: reason, RolledBack: !refused}
	}
	return nil
}
