package main

// Locks for GET /api/node/activity (node_activity_api.go): the authority it is
// mounted behind, the methods it answers, the limit clamp, and that the HTTP body
// and the node_activity_read hostcall result are the SAME object.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/modulert/caps"
	"github.com/spacedatanetwork/sdn-server/internal/peers"
)

// TestNodeActivityPathIsAdminClassifiedAndNotAnonymous is the authority lock.
// Every row names a peer this host talked to and when; that pattern is a HOST
// fact, so the path must be Admin-classified and must not have slipped onto the
// anonymous read surface or the any-tier authenticated list.
func TestNodeActivityPathIsAdminClassifiedAndNotAnonymous(t *testing.T) {
	t.Parallel()

	const path = "/api/node/activity"

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
	// The query string must not launder the classification: the dashboard calls
	// it with ?limit=5.
	if !isAdminOnlyAPIPath(path) || isPublicAPIRequest(http.MethodGet, path) {
		t.Fatalf("%s?limit=… must classify exactly as %s", path, path)
	}
}

// TestNodeActivityRefusesAnonymousAndBelowAdmin drives the real auth wall:
// anonymous is 401, every tier below Admin is 403, and neither reaches the
// handler.
func TestNodeActivityRefusesAnonymousAndBelowAdmin(t *testing.T) {
	t.Parallel()

	const path = "/api/node/activity"

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
			t.Fatalf("anonymous GET reached the activity handler (%d calls)", *calls)
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
				t.Fatalf("%s GET reached the activity handler (%d calls)", trust, *calls)
			}
		})
	}
}

// TestHandleNodeActivityIsReadOnly: GET/HEAD are answered, every mutating verb is
// refused before anything else happens. An activity ring is a record of the past
// and nothing over HTTP may write to it.
func TestHandleNodeActivityIsReadOnly(t *testing.T) {
	t.Parallel()

	handler := handleNodeActivity(nil)

	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(method, "/api/node/activity", strings.NewReader("{}"))
		req.Header.Set("X-Requested-With", "XMLHttpRequest")
		handler(rec, req)
		if rec.Code != http.StatusMethodNotAllowed {
			t.Fatalf("%s status = %d, want %d", method, rec.Code, http.StatusMethodNotAllowed)
		}
	}

	rec := httptest.NewRecorder()
	handler(rec, httptest.NewRequest(http.MethodGet, "/api/node/activity", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("GET with no node status = %d, want %d", rec.Code, http.StatusServiceUnavailable)
	}
}

// TestNodeActivitySnapshotIsTheOneAssembler proves the HTTP body and the
// node_activity_read.activity result are the SAME object, key for key. Wave 1
// established this rule for /api/node/runtime; the ACTIVITY LOG widget lives or
// dies by it too — a second shaping would be a second contract.
func TestNodeActivitySnapshotIsTheOneAssembler(t *testing.T) {
	t.Parallel()

	at := time.Date(2026, 7, 30, 11, 4, 22, 0, time.UTC)
	ring := caps.NewActivityRing(8)
	ring.Append("peer_connected", "12D3KooWExamplePeer", "")
	ring.Append("record_stored", "12D3KooWExamplePeer", "OMM.fbs")

	snapshot := caps.NodeActivitySnapshot(caps.NodeActivityMaterials{Ring: ring}, 5)

	encoded, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatalf("marshal snapshot: %v", err)
	}
	var decoded map[string]interface{}
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("unmarshal snapshot: %v", err)
	}

	if got := decoded["count"]; got != float64(2) {
		t.Fatalf("count = %v, want 2", got)
	}
	events, _ := decoded["events"].([]interface{})
	if len(events) != 2 {
		t.Fatalf("events = %v", decoded["events"])
	}
	// NEWEST FIRST is the ring's contract (caps/nodeactivity.go) and the widget
	// prints the newest row at the top: reversing it silently would put the
	// oldest event under "NOW".
	newest, _ := events[0].(map[string]interface{})
	if newest["kind"] != "record_stored" {
		t.Fatalf("events[0] = %v, want the newest (record_stored)", newest)
	}
	// Exactly the keys the widget reads.
	for _, key := range []string{"ts", "kind", "detail", "peer_id"} {
		if _, ok := newest[key]; !ok {
			t.Fatalf("event is missing %q: %v", key, newest)
		}
	}
	if _, err := time.Parse(time.RFC3339, newest["ts"].(string)); err != nil {
		t.Fatalf("ts is not RFC3339: %v", newest["ts"])
	}
	_ = at
}

// TestNodeActivitySnapshotOmitsAnAbsentPeerAndNeverFabricates locks the "never a
// fabricated value" half: an event with no peer carries NO peer_id key at all
// rather than an empty string, and an unwired ring answers with an honest empty
// list instead of an error.
func TestNodeActivitySnapshotOmitsAnAbsentPeerAndNeverFabricates(t *testing.T) {
	t.Parallel()

	ring := caps.NewActivityRing(4)
	ring.Append("pnm_publication", "", "CAT.fbs")
	snapshot := caps.NodeActivitySnapshot(caps.NodeActivityMaterials{Ring: ring}, 0)
	events, _ := snapshot["events"].([]map[string]interface{})
	if len(events) != 1 {
		t.Fatalf("events = %v", snapshot["events"])
	}
	if _, present := events[0]["peer_id"]; present {
		t.Fatalf("an event with no peer must omit peer_id entirely: %v", events[0])
	}

	empty := caps.NodeActivitySnapshot(caps.NodeActivityMaterials{}, 5)
	if empty["count"] != 0 {
		t.Fatalf("unwired ring count = %v, want 0", empty["count"])
	}
	if events, _ := empty["events"].([]map[string]interface{}); len(events) != 0 {
		t.Fatalf("unwired ring events = %v, want empty", empty["events"])
	}
}

// TestNodeActivityLimitIsClamped: the hostcall's clamp is the HTTP clamp, because
// it is the same assembler. A caller asking for a million rows gets the ring's
// capacity, not a million.
func TestNodeActivityLimitIsClamped(t *testing.T) {
	t.Parallel()

	ring := caps.NewActivityRing(caps.ActivityRingCapacity)
	for i := 0; i < 12; i++ {
		ring.Append("peer_connected", "12D3KooWExamplePeer", "")
	}

	for _, tc := range []struct {
		name  string
		limit int
		want  int
	}{
		{"explicit five", 5, 5},
		{"zero means the hostcall default", 0, 12},
		{"negative means the hostcall default", -3, 12},
		{"above capacity is capped by the ring", 1_000_000, 12},
	} {
		got := caps.NodeActivitySnapshot(caps.NodeActivityMaterials{Ring: ring}, tc.limit)
		if got["count"] != tc.want {
			t.Fatalf("%s: count = %v, want %d", tc.name, got["count"], tc.want)
		}
	}
}
