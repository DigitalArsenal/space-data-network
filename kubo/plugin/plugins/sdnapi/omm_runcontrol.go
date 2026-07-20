package sdnapi

// omm_runcontrol.go — START / STOP / RESET admin controls for the
// supplemental-OMM OD flow's own fire lifecycle, mounted on the same
// loopback listener as the rest of the compat surface (see omm_compat.go).
//
// # Built against a stub, bound automatically — never a hard flowrt dependency
//
// The underlying lifecycle primitives (FireNow / AbortFire / ClearBatch) were
// developed on *flowrt.ServiceFlow by a SEPARATE, concurrent workstream and
// landed in kubo commit 898c073c ("flowrt OD lane: provider attribution +
// pulled_at, retention trampolines, operator run-control", guardian PASS).
// This file was written and reviewed BEFORE that commit landed, against a
// LOCAL, duck-typed runControl interface (never a direct flowrt import of
// those methods) resolved via a plain Go interface type assertion — so it
// never had a hard compile-time dependency on that exact commit existing,
// and would have honestly reported 503 ("run control is not available on
// this build yet") on any node whose flowrt predates it. Now that the real
// methods exist with matching signatures, the assertion succeeds and every
// route is live; the compile-time proof just below (`var _ runControl =
// (*flowrt.ServiceFlow)(nil)`) pins that down permanently — if a future
// flowrt change ever reshapes any of the three methods, THAT breaks the
// build here, loudly, rather than these routes silently reverting to
// "unavailable."
//
// # Endpoints (loopback, same-origin, under the existing /sdn/v1/modules
// namespace the compat shim already owns)
//
//	POST /sdn/v1/modules/supplemental-omm/run/start   409 if a run is already in flight
//	POST /sdn/v1/modules/supplemental-omm/run/stop    cooperative cancel (may take a
//	                                                    moment to actually end — the
//	                                                    real granularity is the flow's
//	                                                    next hostcall boundary)
//	POST /sdn/v1/modules/supplemental-omm/run/reset   {"batch_id": "..."} (optional;
//	                                                    defaults to the most recent
//	                                                    real batch_id the store holds)
//	                                                    — clears that batch's stored
//	                                                    rows + its fire-history entry
//
// # Zero orchestration
//
// This is administrative control over the ONE composed flow's OWN existing
// fire/cancel/clear primitives — the SAME fire the cron scheduler already
// drives on its timer (FireNow is FireTrigger's non-blocking sibling, per
// its own doc: "the host still decides nothing about IF/WHEN autonomously —
// the owner's button did"). Never a second run mechanism, no batching, no
// provider knowledge, no record handling — this file only routes an HTTP
// verb to an already-existing method call and reports the result.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/ipfs/kubo/sdn/flowrt"
)

// runControlWriteJSON/runControlWriteErr are local (this package has no
// shared HTTP-response helpers — the generic ModuleAdmin routes in
// sdn/sdnapi own their own private writeJSON/writeErr in a DIFFERENT
// package). Same bare-JSON contract every other /sdn/v1/* route uses.
func runControlWriteJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func runControlWriteErr(w http.ResponseWriter, status int, msg string) {
	runControlWriteJSON(w, status, map[string]string{"error": msg})
}

// Compile-time proof the duck-typed binding actually holds against the REAL
// ServiceFlow (as of kubo commit 898c073c, "flowrt OD lane: provider
// attribution + pulled_at, retention trampolines, operator run-control" —
// FireNow/AbortFire/ClearBatch landed there with exactly these signatures).
// If a future flowrt change ever renames/reshapes any of the three methods,
// this line breaks the build HERE, loudly, instead of the routes silently
// reverting to "not available yet."
var (
	_ runControl  = (*flowrt.ServiceFlow)(nil)
	_ ommFlowLike = (*flowrt.ServiceFlow)(nil)
)

// runControl is the local, duck-typed lifecycle-control seam — see the file
// doc for why this is not an import of flowrt's own types.
type runControl interface {
	// FireNow fires triggerID immediately, blocking until it completes;
	// rejects (never blocks or corrupts) with a non-nil error when a fire is
	// already in flight.
	FireNow(ctx context.Context, triggerID string) ([]byte, error)
	// AbortFire cooperatively cancels an in-flight fire; returns whether one
	// was in flight (false when idle — a no-op).
	AbortFire() bool
	// ClearBatch clears a batch's stored rows + fire-history entry, returning
	// the SURVIVING row count; rejects while a fire is in flight.
	ClearBatch(batchID string) (int64, error)
}

// ommFlowLike is the (much narrower, already-stable) seam this file needs
// beyond runControl: whether a fire is currently in flight, and the linked
// store to resolve RESET's default batch_id from. Both methods already exist
// on *flowrt.ServiceFlow today (OngoingFire in firehistory.go, Store in
// cronmount.go — committed, stable, NOT part of the concurrent
// runcontrol-primitives workstream), so this costs nothing in production; it
// exists here purely so tests can inject a fake instead of a real wasm-backed
// ServiceFlow.
type ommFlowLike interface {
	OngoingFire() (flowrt.FireRecord, bool)
	Store() *flowrt.LinkedStore
}

// ommRunControlHandler serves the START/STOP/RESET routes. resolve is
// swappable in tests (fakes); in production it resolves the real mounted
// ServiceFlow via ommFlow() and a type assertion (see resolveOmmRunControl).
type ommRunControlHandler struct {
	resolve func() (runControl, ommFlowLike)
}

// resolveOmmRunControl is the production resolver: nil, nil when the OD flow
// isn't mounted; a non-nil flow with a nil runControl when it IS mounted but
// doesn't (yet) implement the lifecycle primitives; both non-nil once it does.
func resolveOmmRunControl() (runControl, ommFlowLike) {
	sf := ommFlow()
	if sf == nil {
		return nil, nil
	}
	rc, ok := interface{}(sf).(runControl)
	if !ok {
		return nil, sf
	}
	return rc, sf
}

// newOmmRunControlHandler builds the START/STOP/RESET admin surface.
func newOmmRunControlHandler(resolve func() (runControl, ommFlowLike)) http.Handler {
	h := &ommRunControlHandler{resolve: resolve}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /sdn/v1/modules/supplemental-omm/run/start", h.start)
	mux.HandleFunc("POST /sdn/v1/modules/supplemental-omm/run/stop", h.stop)
	mux.HandleFunc("POST /sdn/v1/modules/supplemental-omm/run/reset", h.reset)
	return mux
}

func (h *ommRunControlHandler) start(w http.ResponseWriter, r *http.Request) {
	rc, sf := h.resolve()
	if sf == nil {
		runControlWriteErr(w, http.StatusServiceUnavailable, "the OD flow is not mounted on this node")
		return
	}
	if _, ongoing := sf.OngoingFire(); ongoing {
		runControlWriteErr(w, http.StatusConflict, "a run is already in flight")
		return
	}
	if rc == nil {
		runControlWriteErr(w, http.StatusServiceUnavailable, "run control is not available on this build yet")
		return
	}
	// FireNow blocks until the fire completes (a full-catalog fit can take
	// minutes) — run it in the background so this request returns promptly,
	// exactly like the cron scheduler's own tick already does; this is the
	// SAME fire, manually invoked instead of timer-invoked.
	go func() {
		if _, err := rc.FireNow(context.Background(), "t0"); err != nil {
			log.Warnf("supplemental-omm run/start: FireNow: %v", err)
		}
	}()
	runControlWriteJSON(w, http.StatusOK, map[string]interface{}{"status": "started"})
}

func (h *ommRunControlHandler) stop(w http.ResponseWriter, r *http.Request) {
	rc, sf := h.resolve()
	if sf == nil {
		runControlWriteErr(w, http.StatusServiceUnavailable, "the OD flow is not mounted on this node")
		return
	}
	if rc == nil {
		runControlWriteErr(w, http.StatusServiceUnavailable, "run control is not available on this build yet")
		return
	}
	aborted := rc.AbortFire()
	status := "idle"
	note := ""
	if aborted {
		// Cooperative cancel only (AbortFire's own doc): it cancels at the
		// flow's NEXT hostcall boundary (aborting an in-flight http fetch),
		// but a fit wave already inside one guest Execute (the threaded OD
		// fit) runs to completion first — never a hard mid-instruction
		// interrupt. The board must show "STOPPING…" (never "IDLE") until it
		// polls the run log and OngoingFire() actually clears.
		status = "stopping"
		note = "cooperative cancel: the run stops at its next hostcall boundary, not instantly — an in-flight fit wave finishes first"
	}
	runControlWriteJSON(w, http.StatusOK, map[string]interface{}{"status": status, "aborted": aborted, "note": note})
}

// resetRequest is POST run/reset's optional body.
type resetRequest struct {
	BatchID string `json:"batch_id"`
}

func (h *ommRunControlHandler) reset(w http.ResponseWriter, r *http.Request) {
	rc, sf := h.resolve()
	if sf == nil {
		runControlWriteErr(w, http.StatusServiceUnavailable, "the OD flow is not mounted on this node")
		return
	}
	if _, ongoing := sf.OngoingFire(); ongoing {
		runControlWriteErr(w, http.StatusConflict, "cannot reset while a run is in flight; stop it first")
		return
	}
	if rc == nil {
		runControlWriteErr(w, http.StatusServiceUnavailable, "run control is not available on this build yet")
		return
	}

	var req resetRequest
	if r.ContentLength != 0 {
		dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16))
		if err := dec.Decode(&req); err != nil {
			runControlWriteErr(w, http.StatusBadRequest, "malformed request body")
			return
		}
	}
	batchID := strings.TrimSpace(req.BatchID)
	if batchID == "" {
		batchID = latestStoreBatchID(sf)
		if batchID == "" {
			runControlWriteErr(w, http.StatusNotFound, "no run to reset")
			return
		}
	}

	survivors, err := rc.ClearBatch(batchID)
	if err != nil {
		// ClearBatch's own guard (flowrt.ErrFireInFlight) can still fire here
		// even after the OngoingFire() pre-check above — a fire may have
		// started in the narrow window between the two calls. Give the same
		// actionable "stop first" guidance either way, using the error's own
		// text as a fallback for anything else ClearBatch might reject.
		msg := err.Error()
		if errors.Is(err, flowrt.ErrFireInFlight) {
			msg = "cannot reset while a run is in flight; stop it first"
		}
		runControlWriteErr(w, http.StatusConflict, msg)
		return
	}
	runControlWriteJSON(w, http.StatusOK, map[string]interface{}{"status": "reset", "batch_id": batchID, "survivors": survivors})
}

// latestStoreBatchID resolves RESET's default target: the most recent REAL
// batch_id the store actually holds (a read-only SELECT over the existing
// LinkedStore.Query surface — the store's own per-fire-unique batch_id
// column, not this package's board-level "backfill"/"fire-<timestamp>" ids,
// which are a SEPARATE, host-observed identifier scheme — see flowrt/
// firehistory.go's doc). Empty when the flow has no linked store or holds no
// rows yet.
func latestStoreBatchID(sf ommFlowLike) string {
	store := sf.Store()
	if store == nil {
		return ""
	}
	res, err := store.Query("SELECT batch_id FROM sds_omm ORDER BY rowid DESC LIMIT 1")
	if err != nil || len(res.Rows) != 1 || len(res.Rows[0]) != 1 {
		return ""
	}
	id, _ := res.Rows[0][0].(string)
	return id
}
