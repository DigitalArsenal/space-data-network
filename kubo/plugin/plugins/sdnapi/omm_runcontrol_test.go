package sdnapi

// omm_runcontrol_test.go — HTTP-level proof of the START/STOP/RESET admin
// surface's request/response/status-code logic, using fakes (fakeOmmFlow/
// fakeRunControl) for both seams — NOT a live flowrt mount (the underlying
// FireNow/AbortFire/ClearBatch primitives are a separate, concurrent, still-
// in-flight workstream; see omm_runcontrol.go's doc for why this file's
// production code binds to them via a duck-typed interface rather than an
// import). What these tests prove: the honest-unavailable contract (nil
// flow / flow-without-runcontrol), the 409 guards, RESET's default-batch-id
// resolution against a REAL LinkedStore (flowrt.OpenLinkedStore + the
// existing IngestTestRow fixture helper), and that a successful call reaches
// the fake's method with the right arguments.

import (
	"context"
	"errors"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ipfs/kubo/sdn/flowrt"
)

type fakeOmmFlow struct {
	ongoing bool
	store   *flowrt.LinkedStore
}

func (f *fakeOmmFlow) OngoingFire() (flowrt.FireRecord, bool) {
	if f.ongoing {
		return flowrt.FireRecord{ID: "fire-x"}, true
	}
	return flowrt.FireRecord{}, false
}
func (f *fakeOmmFlow) Store() *flowrt.LinkedStore { return f.store }

type fakeRunControl struct {
	fireNowCalls   int
	fireNowErr     error
	fireNowDone    chan struct{} // closed once FireNow is called, for the async /start test
	abortResult    bool
	clearBatchArg  string
	clearSurvivors int64
	clearErr       error
}

func (f *fakeRunControl) FireNow(ctx context.Context, triggerID string) ([]byte, error) {
	f.fireNowCalls++
	if f.fireNowDone != nil {
		close(f.fireNowDone)
	}
	return nil, f.fireNowErr
}
func (f *fakeRunControl) AbortFire() bool { return f.abortResult }
func (f *fakeRunControl) ClearBatch(batchID string) (int64, error) {
	f.clearBatchArg = batchID
	return f.clearSurvivors, f.clearErr
}

// --- honest-unavailable contract ---

func TestRunControlNotMounted(t *testing.T) {
	h := newOmmRunControlHandler(func() (runControl, ommFlowLike) { return nil, nil })
	for _, path := range []string{"start", "stop", "reset"} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("POST", "/sdn/v1/modules/supplemental-omm/run/"+path, nil)
		h.ServeHTTP(rec, req)
		if rec.Code != 503 {
			t.Errorf("POST run/%s with no flow mounted = %d, want 503", path, rec.Code)
		}
	}
}

func TestRunControlNotYetAvailable(t *testing.T) {
	flow := &fakeOmmFlow{}
	h := newOmmRunControlHandler(func() (runControl, ommFlowLike) { return nil, flow })
	for _, path := range []string{"start", "stop", "reset"} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("POST", "/sdn/v1/modules/supplemental-omm/run/"+path, nil)
		h.ServeHTTP(rec, req)
		if rec.Code != 503 {
			t.Errorf("POST run/%s with a mounted flow but no runControl = %d, want 503 (honest, never fabricated)", path, rec.Code)
		}
	}
}

// --- START ---

func TestRunControlStartSuccess(t *testing.T) {
	flow := &fakeOmmFlow{ongoing: false}
	rc := &fakeRunControl{fireNowDone: make(chan struct{})}
	h := newOmmRunControlHandler(func() (runControl, ommFlowLike) { return rc, flow })

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/sdn/v1/modules/supplemental-omm/run/start", nil)
	h.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("POST run/start = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "started") {
		t.Fatalf("POST run/start body = %q, want status=started", rec.Body.String())
	}
	select {
	case <-rc.fireNowDone:
	case <-time.After(2 * time.Second):
		t.Fatal("FireNow was never called (start must fire in the background)")
	}
	if rc.fireNowCalls != 1 {
		t.Fatalf("FireNow called %d times, want 1", rc.fireNowCalls)
	}
}

func TestRunControlStartConflictWhileOngoing(t *testing.T) {
	flow := &fakeOmmFlow{ongoing: true}
	rc := &fakeRunControl{}
	h := newOmmRunControlHandler(func() (runControl, ommFlowLike) { return rc, flow })

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/sdn/v1/modules/supplemental-omm/run/start", nil)
	h.ServeHTTP(rec, req)
	if rec.Code != 409 {
		t.Fatalf("POST run/start while ongoing = %d, want 409", rec.Code)
	}
	if rc.fireNowCalls != 0 {
		t.Fatalf("FireNow must not be called when a run is already in flight (called %d times)", rc.fireNowCalls)
	}
}

// --- STOP ---

func TestRunControlStop(t *testing.T) {
	cases := []struct {
		aborted      bool
		wantStatus   string
		wantHTTPCode int
	}{
		{aborted: true, wantStatus: "stopping", wantHTTPCode: 200},
		{aborted: false, wantStatus: "idle", wantHTTPCode: 200},
	}
	for _, c := range cases {
		flow := &fakeOmmFlow{}
		rc := &fakeRunControl{abortResult: c.aborted}
		h := newOmmRunControlHandler(func() (runControl, ommFlowLike) { return rc, flow })

		rec := httptest.NewRecorder()
		req := httptest.NewRequest("POST", "/sdn/v1/modules/supplemental-omm/run/stop", nil)
		h.ServeHTTP(rec, req)
		if rec.Code != c.wantHTTPCode {
			t.Fatalf("POST run/stop (aborted=%v) = %d, want %d", c.aborted, rec.Code, c.wantHTTPCode)
		}
		if !strings.Contains(rec.Body.String(), c.wantStatus) {
			t.Fatalf("POST run/stop (aborted=%v) body = %q, want status=%q", c.aborted, rec.Body.String(), c.wantStatus)
		}
	}
}

// --- RESET ---

func TestRunControlResetExplicitBatchID(t *testing.T) {
	flow := &fakeOmmFlow{}
	rc := &fakeRunControl{clearSurvivors: 3}
	h := newOmmRunControlHandler(func() (runControl, ommFlowLike) { return rc, flow })

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/sdn/v1/modules/supplemental-omm/run/reset", strings.NewReader(`{"batch_id":"explicit-batch"}`))
	h.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("POST run/reset(explicit) = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	if rc.clearBatchArg != "explicit-batch" {
		t.Fatalf("ClearBatch called with %q, want %q", rc.clearBatchArg, "explicit-batch")
	}
	if !strings.Contains(rec.Body.String(), `"survivors":3`) {
		t.Fatalf("POST run/reset body = %q, want survivors=3", rec.Body.String())
	}
}

func TestRunControlResetConflictWhileOngoing(t *testing.T) {
	flow := &fakeOmmFlow{ongoing: true}
	rc := &fakeRunControl{}
	h := newOmmRunControlHandler(func() (runControl, ommFlowLike) { return rc, flow })

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/sdn/v1/modules/supplemental-omm/run/reset", strings.NewReader(`{"batch_id":"x"}`))
	h.ServeHTTP(rec, req)
	if rec.Code != 409 {
		t.Fatalf("POST run/reset while ongoing = %d, want 409", rec.Code)
	}
	if rc.clearBatchArg != "" {
		t.Fatalf("ClearBatch must not be called while a run is in flight (got arg %q)", rc.clearBatchArg)
	}
}

func TestRunControlResetNoBatchNoStore(t *testing.T) {
	flow := &fakeOmmFlow{} // no store attached
	rc := &fakeRunControl{}
	h := newOmmRunControlHandler(func() (runControl, ommFlowLike) { return rc, flow })

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/sdn/v1/modules/supplemental-omm/run/reset", nil)
	h.ServeHTTP(rec, req)
	if rec.Code != 404 {
		t.Fatalf("POST run/reset with no batch_id and no store = %d, want 404 (nothing to reset)", rec.Code)
	}
}

// TestRunControlResetDefaultBatchIDFromRealStore proves latestStoreBatchID
// against a REAL LinkedStore (flatsqlrt/WasmEdge — proven to run on darwin
// by flowrt's own test suite): RESET with no batch_id in the request
// resolves the most recently-ingested row's REAL batch_id column.
func TestRunControlResetDefaultBatchIDFromRealStore(t *testing.T) {
	dir := t.TempDir()
	store, err := flowrt.OpenLinkedStore(filepath.Join(dir, "aot"), filepath.Join(dir, "store.snapshot"))
	if err != nil {
		t.Fatalf("OpenLinkedStore: %v", err)
	}
	defer store.Close()
	if err := store.IngestTestRow("SOMM", "cid-1", "iss", "ISS-E", "batch-older", []byte{0, 0, 0, 0, '$', 'O', 'M', 'M', 1}); err != nil {
		t.Fatalf("ingest: %v", err)
	}
	if err := store.IngestTestRow("SOMM", "cid-2", "iss", "ISS-E", "batch-newest", []byte{0, 0, 0, 0, '$', 'O', 'M', 'M', 2}); err != nil {
		t.Fatalf("ingest: %v", err)
	}

	flow := &fakeOmmFlow{store: store}
	rc := &fakeRunControl{clearSurvivors: 0}
	h := newOmmRunControlHandler(func() (runControl, ommFlowLike) { return rc, flow })

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/sdn/v1/modules/supplemental-omm/run/reset", nil)
	h.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("POST run/reset(default) = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	if rc.clearBatchArg != "batch-newest" {
		t.Fatalf("ClearBatch called with %q, want the most recently-ingested row's batch_id %q", rc.clearBatchArg, "batch-newest")
	}
}

func TestRunControlResetClearBatchError(t *testing.T) {
	flow := &fakeOmmFlow{}
	rc := &fakeRunControl{clearErr: errors.New("boom")}
	h := newOmmRunControlHandler(func() (runControl, ommFlowLike) { return rc, flow })

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/sdn/v1/modules/supplemental-omm/run/reset", strings.NewReader(`{"batch_id":"x"}`))
	h.ServeHTTP(rec, req)
	if rec.Code != 409 {
		t.Fatalf("POST run/reset with a ClearBatch error = %d, want 409", rec.Code)
	}
}
