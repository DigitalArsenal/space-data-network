package main

// Locks for GET /api/node/runtime (node_runtime_api.go): the authority it is
// mounted behind, the methods it answers, the exact JSON keys the dashboard
// reads, and — the point of the whole file — that no lifecycle verb ever
// appears on it.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/modulert/caps"
	"github.com/spacedatanetwork/sdn-server/internal/peers"
)

// TestNodeRuntimePathIsAdminClassifiedAndNotAnonymous is the authority lock.
// The endpoint discloses the storage path, disk capacity and this host's
// bandwidth totals: it must be Admin-classified, and it must NOT have slipped
// onto either widened surface (the anonymous read list, or the any-tier
// authenticated list that /api/auth/me lives on).
func TestNodeRuntimePathIsAdminClassifiedAndNotAnonymous(t *testing.T) {
	t.Parallel()

	const path = "/api/node/runtime"

	if !isAdminOnlyAPIPath(path) {
		t.Fatalf("%s must be admin-classified", path)
	}
	if isPublicReadAPIPath(path) {
		t.Fatalf("%s must not be on the anonymous read surface", path)
	}
	if isAnyTierAuthenticatedAPIPath(path) {
		t.Fatalf("%s must not be readable at any authenticated tier — it is Admin", path)
	}
	for _, method := range []string{http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut, http.MethodDelete} {
		if isPublicAPIRequest(method, path) {
			t.Fatalf("%s %s is treated as anonymous", method, path)
		}
	}
}

// TestNodeRuntimeRefusesAnonymousAndBelowAdmin drives the real auth wall the
// route is mounted behind. Anonymous is 401; every tier below Admin is 403; and
// in neither case does the request reach the handler.
func TestNodeRuntimeRefusesAnonymousAndBelowAdmin(t *testing.T) {
	t.Parallel()

	const path = "/api/node/runtime"

	newMux := func() (http.Handler, *int) {
		calls := 0
		mux := http.NewServeMux()
		mux.HandleFunc(path, func(w http.ResponseWriter, _ *http.Request) {
			calls++
			w.WriteHeader(http.StatusOK)
		})
		return mux, &calls
	}

	t.Run("anonymous is refused", func(t *testing.T) {
		t.Parallel()
		handler, _ := newAdminSession(t, peers.Admin)
		mux, calls := newMux()
		rec := httptest.NewRecorder()
		serveAdminMuxRequest(rec, httptest.NewRequest(http.MethodGet, path, nil), mux, true, false, handler, isPublicAPIRequest)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("anonymous GET status = %d, want %d", rec.Code, http.StatusUnauthorized)
		}
		if *calls != 0 {
			t.Fatalf("anonymous GET reached the runtime handler (%d calls)", *calls)
		}
	})

	for _, trust := range []peers.TrustLevel{peers.Unknown, peers.Marginal, peers.Standard, peers.Trusted} {
		trust := trust
		t.Run("below admin is refused/"+trust.String(), func(t *testing.T) {
			t.Parallel()
			handler, token := newAdminSession(t, trust)
			mux, calls := newMux()
			req := httptest.NewRequest(http.MethodGet, path, nil)
			req.AddCookie(&http.Cookie{Name: "sdn_wallet_session", Value: token})
			rec := httptest.NewRecorder()
			serveAdminMuxRequest(rec, req, mux, true, false, handler, isPublicAPIRequest)
			if rec.Code != http.StatusForbidden {
				t.Fatalf("%s GET status = %d, want %d", trust, rec.Code, http.StatusForbidden)
			}
			if *calls != 0 {
				t.Fatalf("%s GET reached the runtime handler (%d calls)", trust, *calls)
			}
		})
	}

	t.Run("admin is served", func(t *testing.T) {
		t.Parallel()
		handler, token := newAdminSession(t, peers.Admin)
		mux, calls := newMux()
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.AddCookie(&http.Cookie{Name: "sdn_wallet_session", Value: token})
		rec := httptest.NewRecorder()
		serveAdminMuxRequest(rec, req, mux, true, false, handler, isPublicAPIRequest)
		if rec.Code != http.StatusOK || *calls != 1 {
			t.Fatalf("admin GET status = %d, calls = %d; want 200, 1: %s", rec.Code, *calls, rec.Body.String())
		}
	})
}

// TestHandleNodeRuntimeIsReadOnly: the handler answers GET and HEAD and refuses
// every mutating verb. There is no lifecycle control on this path and this test
// is what keeps it that way.
func TestHandleNodeRuntimeIsReadOnly(t *testing.T) {
	t.Parallel()

	// A nil *node.Node exercises the method gate before the snapshot: the
	// refusal must not depend on having a live node.
	handler := handleNodeRuntime(nil)

	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(method, "/api/node/runtime", strings.NewReader("{}"))
		req.Header.Set("X-Requested-With", "XMLHttpRequest")
		handler(rec, req)
		if rec.Code != http.StatusMethodNotAllowed {
			t.Fatalf("%s status = %d, want %d", method, rec.Code, http.StatusMethodNotAllowed)
		}
	}

	rec := httptest.NewRecorder()
	handler(rec, httptest.NewRequest(http.MethodGet, "/api/node/runtime", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("GET with no node status = %d, want %d", rec.Code, http.StatusServiceUnavailable)
	}
}

// TestNodeRuntimeSourceCarriesNoLifecycleVerb is a source-level lock on the
// OWNER-STOPPED decision: RESTART/STOP/CHECK is remote process control, a new
// host capability, and it is not in this file. A future agent adding one has to
// delete this test, which is the whole point.
func TestNodeRuntimeSourceCarriesNoLifecycleVerb(t *testing.T) {
	t.Parallel()

	raw, err := os.ReadFile("node_runtime_api.go")
	if err != nil {
		t.Fatalf("read node_runtime_api.go: %v", err)
	}
	src := string(raw)
	// Case-sensitive on purpose: the comments explain WHY these are absent and
	// say the words in upper case.
	for _, forbidden := range []string{
		"os.Exit", "syscall.Kill", "exec.Command", "systemctl", "Shutdown(", "Restart(",
	} {
		if strings.Contains(src, forbidden) {
			t.Fatalf("node_runtime_api.go must stay a read surface; found %q", forbidden)
		}
	}
}

// TestNodeStatusSnapshotIsTheOneAssembler proves the HTTP body and the
// node_status_read.status result are the SAME object, key for key — a
// divergence here is a dashboard rendering a shape no module ever sees.
func TestNodeStatusSnapshotIsTheOneAssembler(t *testing.T) {
	t.Parallel()

	started := time.Date(2026, 7, 30, 0, 0, 0, 0, time.UTC)
	materials := caps.NodeStatusMaterials{
		StartedAt:   started,
		Now:         func() time.Time { return started.Add(90 * time.Second) },
		Mode:        "full",
		StoragePath: "/var/lib/sdn",
		StorageSummary: func() (int64, int64, error) {
			return 1240876, 332, nil
		},
		DiskStat: func(string) (caps.DiskStat, error) {
			return caps.DiskStat{CapacityBytes: 84 << 30, FreeBytes: 42 << 30, AvailableBytes: 40 << 30}, nil
		},
		BandwidthTotals: func() (int64, int64, float64, float64, bool) {
			return 4096, 2048, 3.42, 0.88, true
		},
		BandwidthHistory: func() []caps.BandwidthHistorySample {
			return []caps.BandwidthHistorySample{{At: started, TotalIn: 1, TotalOut: 2, RateIn: 3, RateOut: 4}}
		},
	}

	snapshot := caps.NodeStatusSnapshot(materials)

	encoded, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatalf("marshal snapshot: %v", err)
	}
	var decoded map[string]interface{}
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("unmarshal snapshot: %v", err)
	}

	if got := decoded["uptime_seconds"]; got != float64(90) {
		t.Fatalf("uptime_seconds = %v, want 90", got)
	}

	// Exactly the keys the dashboard's widgets read. snake_case is the wire
	// contract (these are host runtime facts, not SDS record fields).
	service, _ := decoded["service"].(map[string]interface{})
	if service["state"] != "running" || service["mode"] != "full" {
		t.Fatalf("service = %v", service)
	}
	if known, ok := service["autostart_known"].(bool); !ok || known {
		t.Fatalf("autostart_known = %v; the host has no surface into systemd/desktop autostart and must keep saying so", service["autostart_known"])
	}

	store, _ := decoded["store"].(map[string]interface{})
	if store["total_bytes"] != float64(1240876) || store["total_records"] != float64(332) {
		t.Fatalf("store = %v", store)
	}

	disk, _ := decoded["disk"].(map[string]interface{})
	if disk["capacity_bytes"] != float64(84<<30) {
		t.Fatalf("disk = %v", disk)
	}

	bw, _ := decoded["bandwidth"].(map[string]interface{})
	if bw["rate_in_bps"] != 3.42 || bw["rate_out_bps"] != 0.88 {
		t.Fatalf("bandwidth rates = %v", bw)
	}
	if history, _ := bw["history"].([]interface{}); len(history) != 1 {
		t.Fatalf("bandwidth history = %v", bw["history"])
	}
}

// TestNodeStatusSnapshotNeverFabricatesADisk locks the nullable half of
// constraint (c): an unavailable statfs probe reports null, never a 0-byte
// disk, because a 0-capacity storage bar actively misleads an operator.
func TestNodeStatusSnapshotNeverFabricatesADisk(t *testing.T) {
	t.Parallel()

	snapshot := caps.NodeStatusSnapshot(caps.NodeStatusMaterials{
		StartedAt: time.Date(2026, 7, 30, 0, 0, 0, 0, time.UTC),
		Now:       func() time.Time { return time.Date(2026, 7, 30, 0, 0, 0, 0, time.UTC) },
	})
	if snapshot["disk"] != nil {
		t.Fatalf("disk = %v, want nil when no statfs probe is wired", snapshot["disk"])
	}
	if snapshot["bandwidth"] != nil {
		t.Fatalf("bandwidth = %v, want nil when no counter is wired", snapshot["bandwidth"])
	}
}
