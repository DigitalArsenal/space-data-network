package peers

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/libp2p/go-libp2p/core/peer"
)

func TestAPIHandler_VCardImport(t *testing.T) {
	registry := NewRegistry(false, nil)
	gater := NewTrustedConnectionGater(registry)
	handler := NewAPIHandler(registry, gater)

	vcardData := `BEGIN:VCARD
VERSION:4.0
FN:API Test Peer
ORG:TestOrg
X-SDN-PEER-ID:12D3KooWDpJ7As7BWAwRMfu1VU2WCqNjvq387JEYKDBj4kx6nXTN
X-SDN-MULTIADDR:/ip4/192.168.1.1/tcp/4001
X-SDN-TRUST-LEVEL:trusted
END:VCARD`

	req := httptest.NewRequest("POST", "/api/peers/import/vcard", strings.NewReader(vcardData))
	req.Header.Set("Content-Type", "text/vcard")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Errorf("Expected status 201, got %d: %s", w.Code, w.Body.String())
	}

	// Verify peer was added
	if registry.PeerCount() != 1 {
		t.Errorf("Expected 1 peer, got %d", registry.PeerCount())
	}

	peerID, _ := peer.Decode("12D3KooWDpJ7As7BWAwRMfu1VU2WCqNjvq387JEYKDBj4kx6nXTN")
	tp, err := registry.GetPeer(peerID)
	if err != nil {
		t.Fatalf("Failed to get peer: %v", err)
	}

	if tp.Name != "API Test Peer" {
		t.Errorf("Name mismatch: got %q", tp.Name)
	}
	if tp.TrustLevel != Trusted {
		t.Errorf("Trust level mismatch: got %s", tp.TrustLevel)
	}
}

func TestAPIHandler_VCardImport_Duplicate(t *testing.T) {
	registry := NewRegistry(false, nil)
	gater := NewTrustedConnectionGater(registry)
	handler := NewAPIHandler(registry, gater)

	vcardData := `BEGIN:VCARD
VERSION:4.0
FN:Dup Test
X-SDN-PEER-ID:12D3KooWDpJ7As7BWAwRMfu1VU2WCqNjvq387JEYKDBj4kx6nXTN
END:VCARD`

	// First import
	req := httptest.NewRequest("POST", "/api/peers/import/vcard", strings.NewReader(vcardData))
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("First import failed: %d", w.Code)
	}

	// Second import should report error in response but still 400 since no peers imported
	req = httptest.NewRequest("POST", "/api/peers/import/vcard", strings.NewReader(vcardData))
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("Expected status 400 for duplicate, got %d: %s", w.Code, w.Body.String())
	}
}

func TestAPIHandler_VCardImport_Invalid(t *testing.T) {
	registry := NewRegistry(false, nil)
	gater := NewTrustedConnectionGater(registry)
	handler := NewAPIHandler(registry, gater)

	req := httptest.NewRequest("POST", "/api/peers/import/vcard", strings.NewReader("not a vcard"))
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("Expected status 400, got %d", w.Code)
	}
}

func TestAPIHandler_VCardExport(t *testing.T) {
	registry := NewRegistry(false, nil)
	gater := NewTrustedConnectionGater(registry)
	handler := NewAPIHandler(registry, gater)

	peerID, _ := peer.Decode("12D3KooWDpJ7As7BWAwRMfu1VU2WCqNjvq387JEYKDBj4kx6nXTN")
	registry.AddPeer(&TrustedPeer{
		ID:           peerID,
		TrustLevel:   Trusted,
		Name:         "Export Test",
		Organization: "TestOrg",
	})

	req := httptest.NewRequest("GET", "/api/peers/export/vcard/"+peerID.String(), nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d: %s", w.Code, w.Body.String())
	}

	if ct := w.Header().Get("Content-Type"); ct != "text/vcard" {
		t.Errorf("Expected Content-Type text/vcard, got %q", ct)
	}

	body := w.Body.String()
	if !strings.Contains(body, "BEGIN:VCARD") {
		t.Error("Response should contain vCard data")
	}
	if !strings.Contains(body, peerID.String()) {
		t.Error("Response should contain peer ID")
	}
}

func TestAPIHandler_VCardExport_NotFound(t *testing.T) {
	registry := NewRegistry(false, nil)
	gater := NewTrustedConnectionGater(registry)
	handler := NewAPIHandler(registry, gater)

	req := httptest.NewRequest("GET", "/api/peers/export/vcard/12D3KooWDpJ7As7BWAwRMfu1VU2WCqNjvq387JEYKDBj4kx6nXTN", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("Expected status 404, got %d", w.Code)
	}
}

func TestAPIHandler_VCardImport_MethodNotAllowed(t *testing.T) {
	registry := NewRegistry(false, nil)
	gater := NewTrustedConnectionGater(registry)
	handler := NewAPIHandler(registry, gater)

	req := httptest.NewRequest("GET", "/api/peers/import/vcard", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("Expected status 405, got %d", w.Code)
	}
}
