package peers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/libp2p/go-libp2p/core/peer"
)

func newPinAPI(t *testing.T) (*APIHandler, *Registry) {
	t.Helper()
	registry := NewRegistry(false, nil)
	store, err := NewPinStore(filepath.Join(t.TempDir(), "peer-pins.json"))
	if err != nil {
		t.Fatalf("NewPinStore: %v", err)
	}
	registry.SetPinStore(store)
	return NewAPIHandler(registry, nil), registry
}

func doPin(t *testing.T, h *APIHandler, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	var reader *strings.Reader
	if body == "" {
		reader = strings.NewReader("")
	} else {
		reader = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, reader)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// THE PIN INTERFACE the owner asked for: manual add, then unpin.
func TestPinAPILifecycle(t *testing.T) {
	h, registry := newPinAPI(t)

	// Empty to start.
	rec := doPin(t, h, http.MethodGet, "/api/peers/pins", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET pins = %d, body %s", rec.Code, rec.Body.String())
	}
	var listed struct {
		Pins []Pin `json:"pins"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &listed); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(listed.Pins) != 0 {
		t.Fatalf("expected no pins, got %d", len(listed.Pins))
	}

	// Pin by FULL MULTIADDR — an operator adding a box by hand usually has one.
	body := `{"peer_id":"/ip4/10.100.10.20/tcp/4001/p2p/` + testPinPeerA + `","name":"vm-orbit-det-01","note":"owner LAN dev box"}`
	rec = doPin(t, h, http.MethodPost, "/api/peers/pins", body)
	if rec.Code != http.StatusCreated {
		t.Fatalf("POST pins = %d, body %s", rec.Code, rec.Body.String())
	}
	var created struct {
		Pin Pin `json:"pin"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode created: %v", err)
	}
	if created.Pin.PeerID != testPinPeerA {
		t.Fatalf("pinned %q, want %q", created.Pin.PeerID, testPinPeerA)
	}
	if len(created.Pin.Addrs) != 1 || created.Pin.Addrs[0] != "/ip4/10.100.10.20/tcp/4001" {
		t.Fatalf("pin addrs = %v", created.Pin.Addrs)
	}
	if created.Pin.Source != PinSourceOperator {
		t.Fatalf("source = %q", created.Pin.Source)
	}

	// The pin gave the peer a registry entry, but NOT trust: pinning decides
	// what an operator sees, never what a peer may do.
	id, _ := peer.Decode(testPinPeerA)
	tp, err := registry.GetPeer(id)
	if err != nil {
		t.Fatalf("pinned peer has no registry entry: %v", err)
	}
	if tp.TrustLevel != Standard {
		t.Fatalf("pinning granted trust %s; it must never do that", tp.TrustLevel)
	}

	// Unpin.
	rec = doPin(t, h, http.MethodDelete, "/api/peers/pins/"+testPinPeerA, "")
	if rec.Code != http.StatusNoContent {
		t.Fatalf("DELETE pin = %d, body %s", rec.Code, rec.Body.String())
	}
	rec = doPin(t, h, http.MethodDelete, "/api/peers/pins/"+testPinPeerA, "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("second DELETE = %d, want 404", rec.Code)
	}
}

// A locked row must name a real file and key the operator can edit — the whole
// point of the owner's "what does the first row 'config trusted peer' mean?".
func TestPinAPIRefusesToUnpinConfigPinAndNamesTheFile(t *testing.T) {
	h, registry := newPinAPI(t)
	id, _ := peer.Decode(testPinPeerB)
	registry.Pins().DeclareConfigPin(id, nil, "/etc/space-data-network/config.yaml · peers.trusted_peers")

	rec := doPin(t, h, http.MethodDelete, "/api/peers/pins/"+testPinPeerB, "")
	if rec.Code != http.StatusConflict {
		t.Fatalf("DELETE config pin = %d, want 409", rec.Code)
	}
	var errBody struct{ Code, Message string }
	if err := json.Unmarshal(rec.Body.Bytes(), &errBody); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if errBody.Code != "config_pin" {
		t.Fatalf("code = %q, want config_pin", errBody.Code)
	}
	if !strings.Contains(errBody.Message, "peers.trusted_peers") ||
		!strings.Contains(errBody.Message, "/etc/space-data-network/config.yaml") {
		t.Fatalf("a locked row must name the real file and key; got %q", errBody.Message)
	}

	// Re-pinning over a config pin is refused the same way.
	rec = doPin(t, h, http.MethodPost, "/api/peers/pins", `{"peer_id":"`+testPinPeerB+`"}`)
	if rec.Code != http.StatusConflict {
		t.Fatalf("POST over config pin = %d, want 409", rec.Code)
	}
}

func TestPinAPIRejectsMalformedInput(t *testing.T) {
	h, _ := newPinAPI(t)

	for name, body := range map[string]string{
		"empty peer id": `{"peer_id":""}`,
		"not a peer id": `{"peer_id":"definitely-not-a-peer"}`,
		"bad addr":      `{"peer_id":"` + testPinPeerA + `","addrs":["not-a-multiaddr"]}`,
	} {
		rec := doPin(t, h, http.MethodPost, "/api/peers/pins", body)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%s: POST = %d, want 400 (body %s)", name, rec.Code, rec.Body.String())
		}
	}

	if rec := doPin(t, h, http.MethodDelete, "/api/peers/pins/nope", ""); rec.Code != http.StatusBadRequest {
		t.Fatalf("DELETE bad id = %d, want 400", rec.Code)
	}
	if rec := doPin(t, h, http.MethodPut, "/api/peers/pins", "{}"); rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("PUT = %d, want 405", rec.Code)
	}
}

// /api/peers/pins must not be swallowed by the /api/peers/ subtree, which would
// try to decode "pins" as a peer id.
func TestPinRouteIsNotShadowedByPeerByID(t *testing.T) {
	h, _ := newPinAPI(t)
	rec := doPin(t, h, http.MethodGet, "/api/peers/pins", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/peers/pins = %d (%s) — the peer-id subtree shadowed the pin route",
			rec.Code, strings.TrimSpace(rec.Body.String()))
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "application/json") {
		t.Fatalf("content-type = %q", ct)
	}
}
