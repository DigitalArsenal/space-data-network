package peers

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
)

// Test peer IDs (using valid base58 encoded Ed25519 peer IDs)
var (
	testPeerID1, _ = peer.Decode("12D3KooWDpJ7As7BWAwRMfu1VU2WCqNjvq387JEYKDBj4kx6nXTN")
	testPeerID2, _ = peer.Decode("12D3KooWNvSZnPi3RrhrTwEY4LuuBeB6K6facKUCJcyWG1aoDd2p")
	testPeerID3, _ = peer.Decode("12D3KooWP5MYTnN8DcQDw7aDUFZY2vQAhvMwZZZ1XN3U9Wh3mJUW")
)

func TestTrustLevel_String(t *testing.T) {
	tests := []struct {
		level    TrustLevel
		expected string
	}{
		{Untrusted, "untrusted"},
		{Limited, "limited"},
		{Standard, "standard"},
		{Trusted, "trusted"},
		{Admin, "admin"},
		{TrustLevel(99), "unknown"},
	}

	for _, tt := range tests {
		if got := tt.level.String(); got != tt.expected {
			t.Errorf("TrustLevel(%d).String() = %q, want %q", tt.level, got, tt.expected)
		}
	}
}

func TestParseTrustLevel(t *testing.T) {
	tests := []struct {
		input    string
		expected TrustLevel
		wantErr  bool
	}{
		{"untrusted", Untrusted, false},
		{"limited", Limited, false},
		{"standard", Standard, false},
		{"trusted", Trusted, false},
		{"admin", Admin, false},
		{"invalid", Untrusted, true},
		{"", Untrusted, true},
	}

	for _, tt := range tests {
		got, err := ParseTrustLevel(tt.input)
		if (err != nil) != tt.wantErr {
			t.Errorf("ParseTrustLevel(%q) error = %v, wantErr %v", tt.input, err, tt.wantErr)
			continue
		}
		if !tt.wantErr && got != tt.expected {
			t.Errorf("ParseTrustLevel(%q) = %v, want %v", tt.input, got, tt.expected)
		}
	}
}

func TestTrustLevel_JSON(t *testing.T) {
	tests := []TrustLevel{Untrusted, Limited, Standard, Trusted, Admin}

	for _, level := range tests {
		data, err := json.Marshal(level)
		if err != nil {
			t.Errorf("Marshal TrustLevel(%d) failed: %v", level, err)
			continue
		}

		var decoded TrustLevel
		if err := json.Unmarshal(data, &decoded); err != nil {
			t.Errorf("Unmarshal TrustLevel(%d) failed: %v", level, err)
			continue
		}

		if decoded != level {
			t.Errorf("JSON round-trip: got %v, want %v", decoded, level)
		}
	}
}

func TestRegistry_AddPeer(t *testing.T) {
	registry := NewRegistry(false, nil)

	tp := &TrustedPeer{
		ID:         testPeerID1,
		TrustLevel: Standard,
		Name:       "Test Peer",
	}

	// Add peer
	if err := registry.AddPeer(tp); err != nil {
		t.Fatalf("AddPeer failed: %v", err)
	}

	// Verify peer was added
	got, err := registry.GetPeer(testPeerID1)
	if err != nil {
		t.Fatalf("GetPeer failed: %v", err)
	}

	if got.Name != "Test Peer" {
		t.Errorf("Got name %q, want %q", got.Name, "Test Peer")
	}

	if got.AddedAt.IsZero() {
		t.Error("AddedAt should be set automatically")
	}

	// Try to add duplicate
	if err := registry.AddPeer(tp); err != ErrPeerAlreadyExists {
		t.Errorf("Expected ErrPeerAlreadyExists, got %v", err)
	}
}

func TestRegistry_RemovePeer(t *testing.T) {
	registry := NewRegistry(false, nil)

	tp := &TrustedPeer{
		ID:         testPeerID1,
		TrustLevel: Standard,
	}
	registry.AddPeer(tp)

	// Remove peer
	if err := registry.RemovePeer(testPeerID1); err != nil {
		t.Fatalf("RemovePeer failed: %v", err)
	}

	// Verify peer was removed
	if _, err := registry.GetPeer(testPeerID1); err != ErrPeerNotFound {
		t.Errorf("Expected ErrPeerNotFound, got %v", err)
	}

	// Remove non-existent peer
	if err := registry.RemovePeer(testPeerID1); err != ErrPeerNotFound {
		t.Errorf("Expected ErrPeerNotFound, got %v", err)
	}
}

func TestRegistry_SetTrustLevel(t *testing.T) {
	registry := NewRegistry(false, nil)

	tp := &TrustedPeer{
		ID:         testPeerID1,
		TrustLevel: Standard,
	}
	registry.AddPeer(tp)

	// Update trust level
	if err := registry.SetTrustLevel(testPeerID1, Trusted); err != nil {
		t.Fatalf("SetTrustLevel failed: %v", err)
	}

	// Verify
	got := registry.GetTrustLevel(testPeerID1)
	if got != Trusted {
		t.Errorf("Got trust level %v, want %v", got, Trusted)
	}
}

func TestRegistry_GetTrustLevel_StrictMode(t *testing.T) {
	// Non-strict mode: unknown peers get Standard
	registry := NewRegistry(false, nil)
	if got := registry.GetTrustLevel(testPeerID1); got != Standard {
		t.Errorf("Non-strict mode: got %v, want %v", got, Standard)
	}

	// Strict mode: unknown peers get Untrusted
	strictRegistry := NewRegistry(true, nil)
	if got := strictRegistry.GetTrustLevel(testPeerID1); got != Untrusted {
		t.Errorf("Strict mode: got %v, want %v", got, Untrusted)
	}
}

func TestRegistry_IsAllowed(t *testing.T) {
	registry := NewRegistry(false, nil)

	// Add peer with different trust levels
	registry.AddPeer(&TrustedPeer{ID: testPeerID1, TrustLevel: Untrusted})
	registry.AddPeer(&TrustedPeer{ID: testPeerID2, TrustLevel: Limited})
	registry.AddPeer(&TrustedPeer{ID: testPeerID3, TrustLevel: Trusted})

	tests := []struct {
		peerID  peer.ID
		allowed bool
	}{
		{testPeerID1, false}, // Untrusted
		{testPeerID2, true},  // Limited
		{testPeerID3, true},  // Trusted
	}

	for _, tt := range tests {
		if got := registry.IsAllowed(tt.peerID); got != tt.allowed {
			t.Errorf("IsAllowed(%s) = %v, want %v", tt.peerID.ShortString(), got, tt.allowed)
		}
	}
}

func TestRegistry_Groups(t *testing.T) {
	registry := NewRegistry(false, nil)

	// Add a peer
	tp := &TrustedPeer{
		ID:         testPeerID1,
		TrustLevel: Standard,
	}
	registry.AddPeer(tp)

	// Create a group
	group := &PeerGroup{
		Name:              "test-group",
		Description:       "Test Group",
		DefaultTrustLevel: Trusted,
	}

	if err := registry.AddGroup(group); err != nil {
		t.Fatalf("AddGroup failed: %v", err)
	}

	// Add peer to group
	if err := registry.AddPeerToGroup(testPeerID1, "test-group"); err != nil {
		t.Fatalf("AddPeerToGroup failed: %v", err)
	}

	// Verify peer is in group
	peers := registry.ListPeersByGroup("test-group")
	if len(peers) != 1 || peers[0].ID != testPeerID1 {
		t.Error("Peer should be in group")
	}

	// Verify peer has group in their list
	updatedPeer, _ := registry.GetPeer(testPeerID1)
	found := false
	for _, g := range updatedPeer.Groups {
		if g == "test-group" {
			found = true
			break
		}
	}
	if !found {
		t.Error("Peer should have group in their list")
	}

	// Remove peer from group
	if err := registry.RemovePeerFromGroup(testPeerID1, "test-group"); err != nil {
		t.Fatalf("RemovePeerFromGroup failed: %v", err)
	}

	peers = registry.ListPeersByGroup("test-group")
	if len(peers) != 0 {
		t.Error("Peer should not be in group after removal")
	}
}

func TestRegistry_ListPeersByTrustLevel(t *testing.T) {
	registry := NewRegistry(false, nil)

	registry.AddPeer(&TrustedPeer{ID: testPeerID1, TrustLevel: Standard})
	registry.AddPeer(&TrustedPeer{ID: testPeerID2, TrustLevel: Trusted})
	registry.AddPeer(&TrustedPeer{ID: testPeerID3, TrustLevel: Standard})

	standardPeers := registry.ListPeersByTrustLevel(Standard)
	if len(standardPeers) != 2 {
		t.Errorf("Expected 2 standard peers, got %d", len(standardPeers))
	}

	trustedPeers := registry.ListPeersByTrustLevel(Trusted)
	if len(trustedPeers) != 1 {
		t.Errorf("Expected 1 trusted peer, got %d", len(trustedPeers))
	}
}

func TestRegistry_RecordConnection(t *testing.T) {
	registry := NewRegistry(false, nil)

	tp := &TrustedPeer{
		ID:         testPeerID1,
		TrustLevel: Standard,
	}
	registry.AddPeer(tp)

	// Record connection
	registry.RecordConnection(testPeerID1)

	got, _ := registry.GetPeer(testPeerID1)
	if got.ConnectionCount != 1 {
		t.Errorf("ConnectionCount = %d, want 1", got.ConnectionCount)
	}
	if got.LastConnected.IsZero() {
		t.Error("LastConnected should be set")
	}
}

func TestRegistry_RecordMessage(t *testing.T) {
	registry := NewRegistry(false, nil)

	tp := &TrustedPeer{
		ID:         testPeerID1,
		TrustLevel: Standard,
	}
	registry.AddPeer(tp)

	// Record sent message
	registry.RecordMessage(testPeerID1, true, 1000)

	got, _ := registry.GetPeer(testPeerID1)
	if got.MessagesSent != 1 {
		t.Errorf("MessagesSent = %d, want 1", got.MessagesSent)
	}
	if got.BytesSent != 1000 {
		t.Errorf("BytesSent = %d, want 1000", got.BytesSent)
	}

	// Record received message
	registry.RecordMessage(testPeerID1, false, 2000)

	got, _ = registry.GetPeer(testPeerID1)
	if got.MessagesReceived != 1 {
		t.Errorf("MessagesReceived = %d, want 1", got.MessagesReceived)
	}
	if got.BytesReceived != 2000 {
		t.Errorf("BytesReceived = %d, want 2000", got.BytesReceived)
	}
}

func TestRegistry_ExportImport(t *testing.T) {
	registry := NewRegistry(false, nil)

	// Add peers and groups
	registry.AddPeer(&TrustedPeer{
		ID:           testPeerID1,
		TrustLevel:   Trusted,
		Name:         "Peer 1",
		Organization: "Org 1",
	})
	registry.AddPeer(&TrustedPeer{
		ID:           testPeerID2,
		TrustLevel:   Standard,
		Name:         "Peer 2",
		Organization: "Org 2",
	})
	registry.AddGroup(&PeerGroup{
		Name:              "group-1",
		Description:       "Test Group",
		DefaultTrustLevel: Trusted,
	})

	// Export
	data, err := registry.Export()
	if err != nil {
		t.Fatalf("Export failed: %v", err)
	}

	// Create new registry and import
	newRegistry := NewRegistry(false, nil)
	if err := newRegistry.Import(data, false); err != nil {
		t.Fatalf("Import failed: %v", err)
	}

	// Verify
	if newRegistry.PeerCount() != 2 {
		t.Errorf("PeerCount = %d, want 2", newRegistry.PeerCount())
	}
	if newRegistry.GroupCount() != 1 {
		t.Errorf("GroupCount = %d, want 1", newRegistry.GroupCount())
	}

	peer1, _ := newRegistry.GetPeer(testPeerID1)
	if peer1.Name != "Peer 1" || peer1.TrustLevel != Trusted {
		t.Error("Imported peer 1 data mismatch")
	}
}

func TestTrustedPeer_JSON(t *testing.T) {
	tp := &TrustedPeer{
		ID:           testPeerID1,
		TrustLevel:   Trusted,
		Name:         "Test Peer",
		Organization: "Test Org",
		Groups:       []string{"group1", "group2"},
		AddedAt:      time.Now(),
		Metadata:     map[string]string{"key": "value"},
	}

	data, err := json.Marshal(tp)
	if err != nil {
		t.Fatalf("Marshal failed: %v", err)
	}

	var decoded TrustedPeer
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}

	if decoded.ID != tp.ID {
		t.Errorf("ID mismatch: got %s, want %s", decoded.ID, tp.ID)
	}
	if decoded.TrustLevel != tp.TrustLevel {
		t.Errorf("TrustLevel mismatch: got %v, want %v", decoded.TrustLevel, tp.TrustLevel)
	}
	if decoded.Name != tp.Name {
		t.Errorf("Name mismatch: got %s, want %s", decoded.Name, tp.Name)
	}
	if len(decoded.Groups) != 2 {
		t.Errorf("Groups length mismatch: got %d, want 2", len(decoded.Groups))
	}
}

func TestRegistry_GetTrustedAddrInfos(t *testing.T) {
	registry := NewRegistry(false, nil)

	// Add peers with different trust levels
	registry.AddPeer(&TrustedPeer{
		ID:         testPeerID1,
		TrustLevel: Standard,
	})
	registry.AddPeer(&TrustedPeer{
		ID:         testPeerID2,
		TrustLevel: Trusted,
	})
	registry.AddPeer(&TrustedPeer{
		ID:         testPeerID3,
		TrustLevel: Admin,
	})

	// Note: No addresses, so all should have empty Addrs
	// GetTrustedAddrInfos only returns peers with Trusted+ AND addresses
	infos := registry.GetTrustedAddrInfos()

	// Without addresses, no peers should be returned
	if len(infos) != 0 {
		t.Errorf("Expected 0 AddrInfos (no addresses), got %d", len(infos))
	}
}
