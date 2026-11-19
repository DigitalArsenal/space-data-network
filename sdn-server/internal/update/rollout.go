package update

// STAGGERED ROLLOUT.
//
// The first production push (1.1.9, 2026-11-18) worked, and both production
// hosts were down for the same five minutes: every node acts on a signal the
// moment it arrives. So a node waits before it acts on an accepted update, by a
// delay of its own within a bounded window:
//
//	delay = window x (first 8 bytes of sha256("SDN-UPDATE-ROLLOUT-V1" 0 peer 0 update)) / 2^64
//	due   = the time this node first accepted the update + delay
//
// The delay depends only on the node's peer id and the update id, so two nodes
// almost never share one, and it is the same after a restart. The accepted
// time and the due time are kept in updates/pending.json, so a restart during
// the wait keeps the due time. A typed `update install` never waits.

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	// DefaultRolloutWindowSeconds is update.rollout_window_seconds when the
	// config does not set it.
	DefaultRolloutWindowSeconds = 600
	// MaxRolloutWindowSeconds bounds a window from config or from a signal.
	MaxRolloutWindowSeconds = 24 * 60 * 60

	// PendingSchema is the schema of updates/pending.json.
	PendingSchema = "org.spacedatanetwork.update.pending.v1"

	pendingFileName = "pending.json"
	rolloutDomain   = "SDN-UPDATE-ROLLOUT-V1"
)

// RolloutDelay is this node's delay within window for updateID.
func RolloutDelay(peerID, updateID string, window time.Duration) time.Duration {
	if window <= 0 {
		return 0
	}
	sum := sha256.Sum256([]byte(rolloutDomain + "\x00" + peerID + "\x00" + updateID))
	fraction := new(big.Int).SetUint64(binary.BigEndian.Uint64(sum[:8]))
	product := new(big.Int).Mul(fraction, big.NewInt(int64(window/time.Second)))
	seconds := product.Rsh(product, 64).Int64()
	return time.Duration(seconds) * time.Second
}

// Pending is updates/pending.json: an update this node accepted and acts on
// at DueAt.
type Pending struct {
	Schema   string `json:"schema"`
	UpdateID string `json:"update_id"`
	Version  string `json:"version"`
	Sequence int64  `json:"sequence"`
	// Trigger is "signal" or "feed-check".
	Trigger       string `json:"trigger"`
	AcceptedAt    string `json:"accepted_at"`
	WindowSeconds int64  `json:"window_seconds"`
	// WindowFrom is "config" or "signal": where the window came from.
	WindowFrom   string `json:"window_from"`
	DelaySeconds int64  `json:"delay_seconds"`
	DueAt        string `json:"due_at"`
	// Signal is the accepted signal's bytes as published (signal trigger
	// only), verified again when a restarted node resumes the wait.
	Signal json.RawMessage `json:"signal,omitempty"`
}

// Due is DueAt parsed.
func (p *Pending) Due() time.Time {
	t, _ := time.Parse(time.RFC3339, p.DueAt)
	return t
}

func pendingPath(paths Paths) string { return filepath.Join(paths.Updates, pendingFileName) }

// ReadPending returns the pending update, or nil.
func ReadPending(paths Paths) (*Pending, error) {
	if strings.TrimSpace(paths.Updates) == "" {
		return nil, nil
	}
	data, err := os.ReadFile(pendingPath(paths))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var pending Pending
	if err := json.Unmarshal(data, &pending); err != nil || pending.Schema != PendingSchema || pending.UpdateID == "" {
		return nil, fmt.Errorf("%s is not a pending update record", pendingPath(paths))
	}
	if _, err := time.Parse(time.RFC3339, pending.DueAt); err != nil {
		return nil, fmt.Errorf("%s has no due time: %w", pendingPath(paths), err)
	}
	return &pending, nil
}

// CurrentPending is ReadPending without a record that names an update at or
// below the installed sequence (installed since, by any path), which it
// removes.
func CurrentPending(paths Paths) (*Pending, error) {
	pending, err := ReadPending(paths)
	if err != nil || pending == nil {
		return pending, err
	}
	if state, serr := LoadState(paths); serr == nil && pending.Sequence <= state.Sequence {
		_ = ClearPending(paths, pending.UpdateID)
		return nil, nil
	}
	return pending, nil
}

// SchedulePending returns the pending record of an update this node accepts
// now: the kept record when it names the same update (a restart keeps the due
// time), else a new one, written to disk before it is returned.
func SchedulePending(paths Paths, p Pending, peerID string, window time.Duration, now time.Time) (*Pending, error) {
	if kept, err := ReadPending(paths); err == nil && kept != nil && kept.UpdateID == p.UpdateID {
		if len(kept.Signal) == 0 && len(p.Signal) > 0 {
			kept.Signal = p.Signal
			kept.Trigger = p.Trigger
			if err := writeJSONAtomic(pendingPath(paths), kept); err != nil {
				return nil, err
			}
		}
		return kept, nil
	}
	if window < 0 {
		window = 0
	}
	delay := RolloutDelay(peerID, p.UpdateID, window)
	now = now.UTC().Truncate(time.Second)
	p.Schema = PendingSchema
	p.AcceptedAt = now.Format(time.RFC3339)
	p.WindowSeconds = int64(window / time.Second)
	p.DelaySeconds = int64(delay / time.Second)
	p.DueAt = now.Add(delay).Format(time.RFC3339)
	if err := os.MkdirAll(paths.Updates, 0o700); err != nil {
		return nil, err
	}
	if err := writeJSONAtomic(pendingPath(paths), &p); err != nil {
		return nil, err
	}
	return &p, nil
}

// CapPendingDue bounds the remaining wait of p at its window: a due time more
// than WindowSeconds after now (a clock that was ahead when the update was
// accepted, corrected before a restart) becomes now plus the window, written
// back to disk. It reports whether the due time changed.
func CapPendingDue(paths Paths, p *Pending, now time.Time) (bool, error) {
	if p == nil {
		return false, nil
	}
	window := time.Duration(max(p.WindowSeconds, 0)) * time.Second
	limit := now.UTC().Truncate(time.Second).Add(window)
	if !p.Due().After(limit) {
		return false, nil
	}
	p.DueAt = limit.Format(time.RFC3339)
	return true, writeJSONAtomic(pendingPath(paths), p)
}

// ClearPending removes the pending record when it names updateID (any record
// when updateID is empty).
func ClearPending(paths Paths, updateID string) error {
	if updateID != "" {
		if kept, err := ReadPending(paths); err == nil && kept != nil && kept.UpdateID != updateID {
			return nil
		}
	}
	if err := os.Remove(pendingPath(paths)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}
