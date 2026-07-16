package sdnbackup

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/ipfs/kubo/sdn/sdnstore"
)

// NamedAdapter is one configured backup destination: an adapter plus its tier
// (spec C.1). PRIMARY must all succeed for a run to be complete; a SECONDARY
// failure degrades the run to partial. Both tiers get identical puts.
type NamedAdapter struct {
	ID      string
	Tier    string // "primary" | "secondary"
	Adapter Adapter
}

func (n NamedAdapter) isPrimary() bool { return n.Tier != "secondary" }

// VerifyMode selects the post-put verification cadence (spec C.4/F-4).
type VerifyMode string

const (
	// VerifyStored re-fetches and hash-checks each NEWLY stored blob (default).
	VerifyStored VerifyMode = "stored"
	// VerifyAll re-fetches every blob, including ones already present.
	VerifyAll VerifyMode = "all"
	// VerifyNone skips verification.
	VerifyNone VerifyMode = "none"
)

// Runner is the BACKUP flow and its inverse RESTORE (spec C). It is the Go
// orchestrator a cron-scheduled backup flow drives; the same logic a WASM
// backup flow would express as a graph.
type Runner struct {
	Source   *BackupSource
	Adapters []NamedAdapter
	// Store persists the run receipt via StoreManifest(Node, "BKR", ...) when
	// non-nil (spec C.5/D.2). Optional.
	Store *sdnstore.Store
	Node  string
	// Verify selects the verification cadence. Zero value => VerifyStored.
	Verify VerifyMode
	// Now/Log are injectable for tests. Now defaults to time.Now.
	Now func() time.Time
	Log func(format string, args ...interface{})
}

// BackupResult summarizes one run.
type BackupResult struct {
	RunID       string
	Status      RunStatus
	UnitCount   int
	StoredCount int
	SkipCount   int
	Landings    []Landing
	Units       []ReceiptUnit
	ReceiptMBL  []byte
	ReceiptCID  string
}

// RestoreTargets lists the (contentHash, kind) pairs to restore, derived from
// the run's units — the kind is the fast-path key hint for get().
func (r *BackupResult) RestoreTargets() []RestoreTarget {
	out := make([]RestoreTarget, 0, len(r.Units))
	for _, u := range r.Units {
		out = append(out, RestoreTarget{ContentHash: u.ContentHash, Kind: u.Kind})
	}
	return out
}

func (r *Runner) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

func (r *Runner) logf(format string, args ...interface{}) {
	if r.Log != nil {
		r.Log(format, args...)
	}
}

func (r *Runner) verifyMode() VerifyMode {
	if r.Verify == "" {
		return VerifyStored
	}
	return r.Verify
}

// Backup enumerates the node's units, incrementally skips content hashes each
// adapter already has, fans the misses out to every adapter, verifies per the
// policy, and emits a receipt (spec C.1). It returns even on partial/failed
// status; the error return is reserved for enumeration failures that prevent a
// run at all.
func (r *Runner) Backup(ctx context.Context) (*BackupResult, error) {
	if r.Source == nil {
		return nil, fmt.Errorf("sdnbackup: runner has no BackupSource")
	}
	if len(r.Adapters) == 0 {
		return nil, fmt.Errorf("sdnbackup: runner has no adapters configured")
	}
	units, err := r.Source.Units(ctx)
	if err != nil {
		return nil, fmt.Errorf("sdnbackup: enumerate units: %w", err)
	}

	started := r.now().UTC()
	runID := fmt.Sprintf("bkr-%s", started.Format("20060102T150405.000000000Z"))
	res := &BackupResult{RunID: runID}

	recUnits := make([]ReceiptUnit, 0, len(units))
	var bytesTotal int64
	for _, u := range units {
		recUnits = append(recUnits, ReceiptUnit{ContentHash: u.ContentHash, Kind: u.Kind, Size: len(u.Bytes), Meta: u.Meta})
		bytesTotal += int64(len(u.Bytes))
	}
	res.Units = recUnits
	res.UnitCount = len(units)

	mode := r.verifyMode()
	var landings []Landing
	for _, na := range r.Adapters {
		for _, u := range units {
			landing := Landing{AdapterID: na.ID, Tier: na.Tier, ContentHash: u.ContentHash, Kind: u.Kind, Size: len(u.Bytes)}
			ref := u.Ref()

			pres, err := na.Adapter.Has(ctx, ref)
			if err != nil {
				landing.ErrorCode = CodeOf(err)
				landing.Error = err.Error()
				landings = append(landings, landing)
				continue
			}
			if pres.Present {
				// Incremental: adapter already has this content hash — skip the put.
				landing.Present = true
				landing.ProviderVersionID = pres.ProviderVersionID
				res.SkipCount++
				if mode == VerifyAll {
					r.verifyLanding(ctx, na.Adapter, u, &landing)
				}
				landings = append(landings, landing)
				r.logf("skip %s on %s (already present)", short(u.ContentHash), na.ID)
				continue
			}

			ack, err := na.Adapter.Put(ctx, u)
			if err != nil {
				landing.ErrorCode = CodeOf(err)
				landing.Error = err.Error()
				landings = append(landings, landing)
				r.logf("put %s on %s FAILED: %v", short(u.ContentHash), na.ID, err)
				continue
			}
			landing.ProviderKey = ack.ProviderKey
			landing.ProviderVersionID = ack.ProviderVersionID
			landing.Encrypted = ack.Encrypted
			landing.Stored = !ack.AlreadyPresent
			landing.Present = ack.AlreadyPresent
			if landing.Stored {
				res.StoredCount++
			} else {
				res.SkipCount++
			}
			if mode == VerifyAll || (mode == VerifyStored && landing.Stored) {
				r.verifyLanding(ctx, na.Adapter, u, &landing)
			}
			landings = append(landings, landing)
			r.logf("put %s on %s ok (verified=%v)", short(u.ContentHash), na.ID, landing.Verified)
		}
	}
	res.Landings = landings
	res.Status = runStatus(landings)

	// Emit + optionally persist the receipt.
	completed := r.now().UTC()
	receipt := RunReceipt{
		RunID:       runID,
		Node:        r.Node,
		StartedAt:   started.Format(time.RFC3339Nano),
		CompletedAt: completed.Format(time.RFC3339Nano),
		Status:      res.Status,
		UnitCount:   len(units),
		BytesTotal:  bytesTotal,
		Units:       recUnits,
		Landings:    landings,
	}
	mbl, err := BuildReceiptMBL(receipt)
	if err != nil {
		return res, fmt.Errorf("sdnbackup: build receipt: %w", err)
	}
	res.ReceiptMBL = mbl
	if r.Store != nil {
		src := r.Node
		if src == "" {
			src = "node"
		}
		c, err := r.Store.StoreManifest(ctx, src, ReceiptType, mbl)
		if err != nil {
			return res, fmt.Errorf("sdnbackup: store receipt: %w", err)
		}
		res.ReceiptCID = c.String()
	}
	return res, nil
}

// verifyLanding re-fetches a stored blob and asserts sha256(bytes) == hash — the
// same guard ResolveModuleByContentHash applies (spec C.4). A miss marks the
// landing unverified (with the error) rather than silently passing.
func (r *Runner) verifyLanding(ctx context.Context, a Adapter, u BackupBlob, landing *Landing) {
	got, err := a.Get(ctx, u.Ref())
	if err != nil {
		landing.Verified = false
		landing.ErrorCode = CodeOf(err)
		landing.Error = "verify fetch: " + err.Error()
		return
	}
	if h := HashBytes(got.Bytes); h != u.ContentHash {
		landing.Verified = false
		landing.ErrorCode = ErrProvider
		landing.Error = fmt.Sprintf("verify hash mismatch: got %s want %s", h, u.ContentHash)
		return
	}
	landing.Verified = true
}

// runStatus resolves the overall status from the landings (spec C.1).
func runStatus(landings []Landing) RunStatus {
	primaryFail, secondaryFail := false, false
	for _, l := range landings {
		ok := (l.Present || l.Stored) && l.Error == ""
		if ok {
			continue
		}
		if l.Tier == "secondary" {
			secondaryFail = true
		} else {
			primaryFail = true
		}
	}
	switch {
	case primaryFail:
		return StatusFailed
	case secondaryFail:
		return StatusPartial
	default:
		return StatusComplete
	}
}

// RestoreTarget names one unit to restore. Kind is the get() key hint.
type RestoreTarget struct {
	ContentHash string
	Kind        Kind
}

// RestoreUnitResult is one unit's restore outcome.
type RestoreUnitResult struct {
	ContentHash string
	Kind        Kind
	AdapterID   string
	OK          bool
	Error       string
}

// RestoreResult summarizes a restore.
type RestoreResult struct {
	Restored int
	Failed   int
	Units    []RestoreUnitResult
}

// Restage re-installs a fetched, hash-verified blob into the node by kind. The
// module path additionally passes a fail-closed capability precheck (spec C.7).
type Restager interface {
	Restage(ctx context.Context, blob BackupBlob) error
}

// Restore fetches each target by content hash with multi-provider failover
// (PRIMARY adapters first, then SECONDARY), verifies sha256 == hash on fetch
// (so a corrupt/substituted blob is rejected and the next adapter is tried),
// and re-stages it via the Restager (spec C.7).
func (r *Runner) Restore(ctx context.Context, restager Restager, targets []RestoreTarget) (*RestoreResult, error) {
	if restager == nil {
		return nil, fmt.Errorf("sdnbackup: restore needs a Restager")
	}
	ordered := r.restoreOrder()
	out := &RestoreResult{}
	for _, t := range targets {
		ur := RestoreUnitResult{ContentHash: t.ContentHash, Kind: t.Kind}
		served := false
		var lastErr string
		for _, na := range ordered {
			blob, err := na.Adapter.Get(ctx, BlobRef{ContentHash: t.ContentHash, Kind: t.Kind})
			if err != nil {
				lastErr = err.Error()
				continue // failover to the next adapter
			}
			if h := HashBytes(blob.Bytes); h != t.ContentHash {
				lastErr = fmt.Sprintf("hash mismatch from %s: got %s", na.ID, h)
				continue // reject substituted blob, try next adapter
			}
			if err := restager.Restage(ctx, blob); err != nil {
				// A restage failure is terminal for this unit (not a provider
				// failover): the bytes were good but re-staging refused.
				ur.AdapterID = na.ID
				ur.Error = "restage: " + err.Error()
				break
			}
			ur.AdapterID = na.ID
			ur.OK = true
			served = true
			break
		}
		if !served && ur.Error == "" {
			ur.Error = "no adapter served the blob: " + lastErr
		}
		if ur.OK {
			out.Restored++
		} else {
			out.Failed++
		}
		out.Units = append(out.Units, ur)
		r.logf("restore %s (%s): ok=%v via=%s", short(t.ContentHash), t.Kind, ur.OK, ur.AdapterID)
	}
	return out, nil
}

// restoreOrder returns adapters primary-first (stable within tier).
func (r *Runner) restoreOrder() []NamedAdapter {
	ordered := make([]NamedAdapter, len(r.Adapters))
	copy(ordered, r.Adapters)
	sort.SliceStable(ordered, func(i, j int) bool {
		return ordered[i].isPrimary() && !ordered[j].isPrimary()
	})
	return ordered
}

func short(h string) string {
	if len(h) > 12 {
		return h[:12]
	}
	return h
}
