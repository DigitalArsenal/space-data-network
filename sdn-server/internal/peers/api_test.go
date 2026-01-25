package peers

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/libp2p/go-libp2p/core/peer"
)

func TestAPIHandler_ListPeers(t *testing.T) {
	registry := NewRegistry(false, nil)
	gater := NewTrustedConnectionGater(registry)
	handler := NewAPIHandler(registry, gater)

	// Add some peers
	peerID1, _ := peer.Decode("12D3KooWDpJ7As7BWAwRMfu1VU2WCqNjvq387JEYKDBj4kx6nXTN")
	peerID2, _ := peer.Decode("12D3KooWNvSZnPi3RrhrTwEY4LuuBeB6K6facKUCJcyWG1aoDd2p")

	registry.AddPeer(&TrustedPeer{ID: peerID1, TrustLevel: Standard, Name: "Peer 1"})
	registry.AddPeer(&TrustedPeer{ID: peerID2, TrustLevel: Trusted, Name: "Peer 2"})

	// Make request
	req := httptest.NewRequest("GET", "/api/peers", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d", w.Code)
	}

	var peers []*TrustedPeer
	if err := json.Unmarshal(w.Body.Bytes(), &peers); err != nil {
		t.Fatalf("Failed to unmarshal response: %v", err)
	}

	if len(peers) != 2 {
		t.Errorf("Expected 2 peers, got %d", len(peers))
	}
}

func TestAPIHandler_AddPeer(t *testing.T) {
	registry := NewRegistry(false, nil)
	gater := NewTrustedConnectionGater(registry)
	handler := NewAPIHandler(registry, gater)

	peerReq := AddPeerRequest{
		ID:           "12D3KooWDpJ7As7BWAwRMfu1VU2WCqNjvq387JEYKDBj4kx6nXTN",
		TrustLevel:   "trusted",
		Name:         "Test Peer",
		Organization: "Test Org",
	}

	body, _ := json.Marshal(peerReq)
	req := httptest.NewRequest("POST", "/api/peers", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Errorf("Expected status 201, got %d: %s", w.Code, w.Body.String())
	}

	// Verify peer was added
	if registry.PeerCount() != 1 {
		t.Errorf("Expected 1 peer, got %d", registry.PeerCount())
	}

	peerID, _ := peer.Decode(peerReq.ID)
	tp, err := registry.GetPeer(peerID)
	if err != nil {
		t.Fatalf("Failed to get added peer: %v", err)
	}

	if tp.Name != "Test Peer" {
		t.Errorf("Expected name 'Test Peer', got '%s'", tp.Name)
	}
	if tp.TrustLevel != Trusted {
		t.Errorf("Expected trust level Trusted, got %v", tp.TrustLevel)
	}
}

func TestAPIHandler_GetPeer(t *testing.T) {
	registry := NewRegistry(false, nil)
	gater := NewTrustedConnectionGater(registry)
	handler := NewAPIHandler(registry, gater)

	peerID, _ := peer.Decode("12D3KooWDpJ7As7BWAwRMfu1VU2WCqNjvq387JEYKDBj4kx6nXTN")
	registry.AddPeer(&TrustedPeer{ID: peerID, TrustLevel: Standard, Name: "Test Peer"})

	req := httptest.NewRequest("GET", "/api/peers/"+peerID.String(), nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d", w.Code)
	}

	var tp TrustedPeer
	if err := json.Unmarshal(w.Body.Bytes(), &tp); err != nil {
		t.Fatalf("Failed to unmarshal response: %v", err)
	}

	if tp.Name != "Test Peer" {
		t.Errorf("Expected name 'Test Peer', got '%s'", tp.Name)
	}
}

func TestAPIHandler_RemovePeer(t *testing.T) {
	registry := NewRegistry(false, nil)
	gater := NewTrustedConnectionGater(registry)
	handler := NewAPIHandler(registry, gater)

	peerID, _ := peer.Decode("12D3KooWDpJ7As7BWAwRMfu1VU2WCqNjvq387JEYKDBj4kx6nXTN")
	registry.AddPeer(&TrustedPeer{ID: peerID, TrustLevel: Standard})

	req := httptest.NewRequest("DELETE", "/api/peers/"+peerID.String(), nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusNoContent {
		t.Errorf("Expected status 204, got %d", w.Code)
	}

	if registry.PeerCount() != 0 {
		t.Errorf("Expected 0 peers, got %d", registry.PeerCount())
	}
}

func TestAPIHandler_UpdateTrust(t *testing.T) {
	registry := NewRegistry(false, nil)
	gater := NewTrustedConnectionGater(registry)
	handler := NewAPIHandler(registry, gater)

	peerID, _ := peer.Decode("12D3KooWDpJ7As7BWAwRMfu1VU2WCqNjvq387JEYKDBj4kx6nXTN")
	registry.AddPeer(&TrustedPeer{ID: peerID, TrustLevel: Standard})

	updateReq := UpdateTrustRequest{TrustLevel: "admin"}
	body, _ := json.Marshal(updateReq)
	req := httptest.NewRequest("PUT", "/api/peers/"+peerID.String()+"/trust", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d: %s", w.Code, w.Body.String())
	}

	level := registry.GetTrustLevel(peerID)
	if level != Admin {
		t.Errorf("Expected trust level Admin, got %v", level)
	}
}

func TestAPIHandler_Blocklist(t *testing.T) {
	registry := NewRegistry(false, nil)
	gater := NewTrustedConnectionGater(registry)
	handler := NewAPIHandler(registry, gater)

	// Block a peer
	blockReq := struct {
		PeerID string `json:"peer_id"`
	}{
		PeerID: "12D3KooWDpJ7As7BWAwRMfu1VU2WCqNjvq387JEYKDBj4kx6nXTN",
	}
	body, _ := json.Marshal(blockReq)
	req := httptest.NewRequest("POST", "/api/blocklist", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Errorf("Expected status 201, got %d", w.Code)
	}

	// Verify blocked
	peerID, _ := peer.Decode(blockReq.PeerID)
	if !gater.IsBlocked(peerID) {
		t.Error("Peer should be blocked")
	}

	// List blocklist
	req = httptest.NewRequest("GET", "/api/blocklist", nil)
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d", w.Code)
	}

	var blocked []string
	json.Unmarshal(w.Body.Bytes(), &blocked)
	if len(blocked) != 1 {
		t.Errorf("Expected 1 blocked peer, got %d", len(blocked))
	}

	// Unblock
	req = httptest.NewRequest("DELETE", "/api/blocklist/"+peerID.String(), nil)
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusNoContent {
		t.Errorf("Expected status 204, got %d", w.Code)
	}

	if gater.IsBlocked(peerID) {
		t.Error("Peer should not be blocked")
	}
}

func TestAPIHandler_Settings(t *testing.T) {
	registry := NewRegistry(false, nil)
	gater := NewTrustedConnectionGater(registry)
	handler := NewAPIHandler(registry, gater)

	// Get settings
	req := httptest.NewRequest("GET", "/api/settings", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d", w.Code)
	}

	var settings SettingsResponse
	json.Unmarshal(w.Body.Bytes(), &settings)
	if settings.StrictMode {
		t.Error("Strict mode should be false initially")
	}

	// Update settings
	updateReq := struct {
		StrictMode *bool `json:"strict_mode"`
	}{
		StrictMode: boolPtr(true),
	}
	body, _ := json.Marshal(updateReq)
	req = httptest.NewRequest("PUT", "/api/settings", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d", w.Code)
	}

	if !registry.IsStrictMode() {
		t.Error("Strict mode should be enabled")
	}
}

func TestAPIHandler_ExportImport(t *testing.T) {
	registry := NewRegistry(false, nil)
	gater := NewTrustedConnectionGater(registry)
	handler := NewAPIHandler(registry, gater)

	// Add a peer
	peerID, _ := peer.Decode("12D3KooWDpJ7As7BWAwRMfu1VU2WCqNjvq387JEYKDBj4kx6nXTN")
	registry.AddPeer(&TrustedPeer{ID: peerID, TrustLevel: Trusted, Name: "Export Test"})

	// Export
	req := httptest.NewRequest("GET", "/api/export", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d", w.Code)
	}

	exportData := w.Body.Bytes()

	// Create new registry and import
	newRegistry := NewRegistry(false, nil)
	newGater := NewTrustedConnectionGater(newRegistry)
	newHandler := NewAPIHandler(newRegistry, newGater)

	req = httptest.NewRequest("POST", "/api/import", bytes.NewReader(exportData))
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	newHandler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d: %s", w.Code, w.Body.String())
	}

	if newRegistry.PeerCount() != 1 {
		t.Errorf("Expected 1 peer after import, got %d", newRegistry.PeerCount())
	}

	tp, _ := newRegistry.GetPeer(peerID)
	if tp.Name != "Export Test" {
		t.Errorf("Expected name 'Export Test', got '%s'", tp.Name)
	}
}

func TestAPIHandler_Groups(t *testing.T) {
	registry := NewRegistry(false, nil)
	gater := NewTrustedConnectionGater(registry)
	handler := NewAPIHandler(registry, gater)

	// Create group
	groupReq := PeerGroup{
		Name:              "test-group",
		Description:       "Test Group",
		DefaultTrustLevel: Trusted,
	}
	body, _ := json.Marshal(groupReq)
	req := httptest.NewRequest("POST", "/api/groups", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Errorf("Expected status 201, got %d: %s", w.Code, w.Body.String())
	}

	// List groups
	req = httptest.NewRequest("GET", "/api/groups", nil)
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d", w.Code)
	}

	var groups []*PeerGroup
	json.Unmarshal(w.Body.Bytes(), &groups)
	if len(groups) != 1 {
		t.Errorf("Expected 1 group, got %d", len(groups))
	}

	// Delete group
	req = httptest.NewRequest("DELETE", "/api/groups/test-group", nil)
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusNoContent {
		t.Errorf("Expected status 204, got %d", w.Code)
	}

	if registry.GroupCount() != 0 {
		t.Errorf("Expected 0 groups, got %d", registry.GroupCount())
	}
}

func boolPtr(b bool) *bool {
	return &b
}
