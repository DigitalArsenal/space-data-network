package update

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// The deploy ledger: an apply that cannot be recorded does not happen.
//
// WHY THIS EXISTS. Until now the deploy lock (/run/sdn-deploy.lock) and the
// deploy ledger were pure operator convention — no code anywhere in this repo
// read or wrote either one. Taking the lock and writing the line were two
// separate voluntary acts, so agents routinely did one and not the other, and
// nothing noticed. The measured result on host-01 for 2026-08-09 alone: eleven
// distinct binaries went live across twenty-two daemon starts; SEVEN of those
// rolls were recorded in only one of the box's two ledgers and TWO were
// recorded in neither, including one performed while correctly holding the
// lock. A binary that reached a host with no line is unattributable after the
// fact — you cannot tell what is running, what it superseded, or who put it
// there — and on a box where every agent authenticates with the same key from
// the same IP, the ledger line is the ONLY accountability mechanism that
// exists. See graph task ops-host01-unledgered-rolls-and-fleet-skew.
//
// So the ledger write is now a PRECONDITION of the mutation, not a courtesy
// after it: Apply refuses to swap a single byte until the line is on disk.
//
// The design turn that makes this unfalsifiable is WHERE the line goes. It
// goes inside the bundle root — the very tree the apply is about to modify.
// Writability of the ledger is therefore implied by writability of the thing
// being changed: any process that can perform the apply can record it, and any
// process that cannot record it could not have performed the apply either.
// That removes the "the ledger path wasn't writable here, so we skipped it"
// escape hatch that would otherwise turn this back into a convention. The
// system-wide /var/log/sdn-deploy-lock.ledger is written too when it is
// available, but it is a MIRROR: never the thing that can veto, and never the
// thing whose absence excuses a missing record.

const (
	// deployLedgerName lives under the bundle root, beside the bundle the
	// apply mutates. Not under updates/: this is a record of what happened to
	// the bundle, and it should travel with the bundle.
	deployLedgerName = "deploy-ledger.jsonl"

	// systemDeployLedger is the fleet-wide operator ledger. Best-effort mirror.
	systemDeployLedger = "/var/log/sdn-deploy-lock.ledger"

	// deployLockPath is the fleet's advisory cutover lock. Read (never taken)
	// so the ledger line records who held the box at apply time.
	deployLockPath = "/run/sdn-deploy.lock"

	deployLedgerSchema = "org.spacedatanetwork.deploy.ledger.v1"
)

// DeployLedgerEntry is one durable record of a binary/bundle change.
type DeployLedgerEntry struct {
	Schema       string `json:"schema"`
	RecordedAt   string `json:"recorded_at"`
	Action       string `json:"action"`
	UpdateID     string `json:"update_id"`
	Version      string `json:"version"`
	Sequence     int64  `json:"sequence"`
	Channel      string `json:"channel"`
	BundleRoot   string `json:"bundle_root"`
	FromVersion  string `json:"from_version,omitempty"`
	FromSequence int64  `json:"from_sequence,omitempty"`
	Rollback     bool   `json:"rollback,omitempty"`
	// LockHolder is whoever held /run/sdn-deploy.lock when this ran, verbatim
	// from the lock file, or a marker saying the apply ran with NO lock held.
	// An unlocked apply is not refused here — refusing would brick recovery on
	// a box whose lock file is unreachable — but it is never silent again.
	LockHolder string `json:"lock_holder"`
	PID        int    `json:"pid"`
}

// DeployLedgerRefusal is returned when the apply cannot be recorded. It is a
// hard failure: the caller must not proceed to mutate the bundle.
type DeployLedgerRefusal struct{ Err error }

func (e *DeployLedgerRefusal) Error() string {
	return fmt.Sprintf("REFUSING TO APPLY: the deploy ledger could not be written, so this change "+
		"would be unattributable. An apply that cannot be recorded does not happen. (%v)", e.Err)
}
func (e *DeployLedgerRefusal) Unwrap() error { return e.Err }

// readDeployLockHolder reports who holds the cutover lock. It never takes,
// creates or removes the lock — this only describes the world for the record.
func readDeployLockHolder() string {
	raw, err := os.ReadFile(deployLockPath)
	if err != nil {
		if os.IsNotExist(err) {
			return "NONE (no /run/sdn-deploy.lock at apply time — this apply was UNLOCKED)"
		}
		return fmt.Sprintf("UNREADABLE (%v)", err)
	}
	body := strings.TrimSpace(string(raw))
	if body == "" {
		// A zero-byte lock carries no holder and no token, so it cannot be
		// told apart from a stale leftover. Say so rather than imply a holder.
		return "PRESENT BUT EMPTY (0-byte lock: no holder, no token — unattributable by construction)"
	}
	for _, line := range strings.Split(body, "\n") {
		if h, ok := strings.CutPrefix(strings.TrimSpace(line), "holder="); ok {
			return h
		}
	}
	return "PRESENT, NO holder= FIELD: " + strings.Join(strings.Fields(body), " ")
}

// RecordDeployLedgerEntry appends the entry and returns an error only if the
// authoritative in-bundle ledger could not be written. Callers MUST treat that
// error as fatal and abandon the change.
func RecordDeployLedgerEntry(paths Paths, entry DeployLedgerEntry) error {
	entry.Schema = deployLedgerSchema
	if entry.RecordedAt == "" {
		entry.RecordedAt = time.Now().UTC().Format(time.RFC3339)
	}
	entry.BundleRoot = paths.Root
	entry.LockHolder = readDeployLockHolder()
	entry.PID = os.Getpid()

	line, err := json.Marshal(entry)
	if err != nil {
		return &DeployLedgerRefusal{Err: fmt.Errorf("encode ledger entry: %w", err)}
	}
	line = append(line, '\n')

	if paths.Root == "" {
		return &DeployLedgerRefusal{Err: fmt.Errorf("no bundle root to record against")}
	}
	// Authoritative write, and the one that can veto the apply.
	if err := appendFileSync(filepath.Join(paths.Root, deployLedgerName), line); err != nil {
		return &DeployLedgerRefusal{Err: err}
	}
	// Mirror. Best effort by design: on a box where /var/log is not writable
	// (an unprivileged or containerised install) the in-bundle record above is
	// still complete, and a mirror must never be able to block a legitimate
	// apply. It must also never let a missing record pass unnoticed, which is
	// why the authoritative copy is the one inside the tree being changed.
	_ = appendFileSync(systemDeployLedger, mirrorLine(entry))
	return nil
}

// mirrorLine renders the entry in the operator ledger's human-readable shape
// so the fleet-wide file stays greppable by eye alongside hand-written entries.
func mirrorLine(e DeployLedgerEntry) []byte {
	var b strings.Builder
	fmt.Fprintf(&b, "%s %s update_id=%s version=%s sequence=%d channel=%s",
		e.RecordedAt, strings.ToUpper(e.Action), e.UpdateID, e.Version, e.Sequence, e.Channel)
	if e.FromVersion != "" {
		fmt.Fprintf(&b, " from_version=%s from_sequence=%d", e.FromVersion, e.FromSequence)
	}
	if e.Rollback {
		b.WriteString(" DECLARED_ROLLBACK=yes")
	}
	fmt.Fprintf(&b, " bundle_root=%s lock_holder=%q pid=%d", e.BundleRoot, e.LockHolder, e.PID)
	b.WriteString(" source=update-lane-automatic (written by update.Apply; the apply refuses without it)\n")
	return []byte(b.String())
}

func appendFileSync(path string, line []byte) error {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()
	if _, err := f.Write(line); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	// fsync: the whole point is that the record survives whatever the apply
	// does next, including a crash or a SIGKILL mid-swap.
	if err := f.Sync(); err != nil {
		return fmt.Errorf("sync %s: %w", path, err)
	}
	return nil
}
