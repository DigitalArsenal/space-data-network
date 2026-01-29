package peers

import (
	"strings"
	"testing"

	"github.com/libp2p/go-libp2p/core/peer"
)

const testVCard = `BEGIN:VCARD
VERSION:4.0
FN:ISS Tracking Node
ORG:NASA
NOTE:Primary ISS tracking relay
X-SDN-PEER-ID:12D3KooWDpJ7As7BWAwRMfu1VU2WCqNjvq387JEYKDBj4kx6nXTN
X-SDN-MULTIADDR:/ip4/192.168.1.1/tcp/4001
X-SDN-MULTIADDR:/ip4/10.0.0.1/tcp/4001
X-SDN-TRUST-LEVEL:trusted
X-SDN-GROUP:satellite-operators
END:VCARD`

const testVCardMinimal = `BEGIN:VCARD
VERSION:4.0
FN:Minimal Peer
X-SDN-PEER-ID:12D3KooWNvSZnPi3RrhrTwEY4LuuBeB6K6facKUCJcyWG1aoDd2p
END:VCARD`

const testVCardNoPeerID = `BEGIN:VCARD
VERSION:4.0
FN:No Peer ID
END:VCARD`

func TestParseVCard(t *testing.T) {
	info, err := ParseVCard(testVCard)
	if err != nil {
		t.Fatalf("ParseVCard failed: %v", err)
	}

	expectedPeerID, _ := peer.Decode("12D3KooWDpJ7As7BWAwRMfu1VU2WCqNjvq387JEYKDBj4kx6nXTN")
	if info.PeerID != expectedPeerID {
		t.Errorf("PeerID mismatch: got %s, want %s", info.PeerID, expectedPeerID)
	}

	if info.Name != "ISS Tracking Node" {
		t.Errorf("Name mismatch: got %q, want %q", info.Name, "ISS Tracking Node")
	}

	if info.Organization != "NASA" {
		t.Errorf("Organization mismatch: got %q, want %q", info.Organization, "NASA")
	}

	if info.Notes != "Primary ISS tracking relay" {
		t.Errorf("Notes mismatch: got %q", info.Notes)
	}

	if len(info.Addrs) != 2 {
		t.Errorf("Expected 2 addresses, got %d", len(info.Addrs))
	}

	if info.Metadata["trust_level"] != "trusted" {
		t.Errorf("Expected trust_level metadata 'trusted', got %q", info.Metadata["trust_level"])
	}

	if info.Metadata["groups"] != "satellite-operators" {
		t.Errorf("Expected groups metadata, got %q", info.Metadata["groups"])
	}
}

func TestParseVCard_Minimal(t *testing.T) {
	info, err := ParseVCard(testVCardMinimal)
	if err != nil {
		t.Fatalf("ParseVCard failed: %v", err)
	}

	if info.Name != "Minimal Peer" {
		t.Errorf("Name mismatch: got %q", info.Name)
	}
	if len(info.Addrs) != 0 {
		t.Errorf("Expected 0 addresses, got %d", len(info.Addrs))
	}
}

func TestParseVCard_NoPeerID(t *testing.T) {
	_, err := ParseVCard(testVCardNoPeerID)
	if err == nil {
		t.Error("Expected error for vCard without peer ID")
	}
}

func TestParseVCards_Multiple(t *testing.T) {
	multiVCard := testVCard + "\n" + testVCardMinimal

	infos, err := ParseVCards(multiVCard)
	if err != nil {
		t.Fatalf("ParseVCards failed: %v", err)
	}

	if len(infos) != 2 {
		t.Errorf("Expected 2 peers, got %d", len(infos))
	}
}

func TestParseVCards_NoneValid(t *testing.T) {
	_, err := ParseVCards(testVCardNoPeerID)
	if err == nil {
		t.Error("Expected error when no valid peer vCards found")
	}
}

func TestTrustedPeerToVCard(t *testing.T) {
	peerID, _ := peer.Decode("12D3KooWDpJ7As7BWAwRMfu1VU2WCqNjvq387JEYKDBj4kx6nXTN")

	tp := &TrustedPeer{
		ID:           peerID,
		TrustLevel:   Trusted,
		Name:         "Test Node",
		Organization: "Test Org",
		Notes:        "Some notes",
		Groups:       []string{"group1", "group2"},
	}

	vcardStr := TrustedPeerToVCard(tp)

	// Verify key fields are present
	if !strings.Contains(vcardStr, "BEGIN:VCARD") {
		t.Error("Missing BEGIN:VCARD")
	}
	if !strings.Contains(vcardStr, "FN:Test Node") {
		t.Error("Missing FN field")
	}
	if !strings.Contains(vcardStr, "X-SDN-PEER-ID:"+peerID.String()) {
		t.Error("Missing X-SDN-PEER-ID field")
	}
	if !strings.Contains(vcardStr, "X-SDN-TRUST-LEVEL:trusted") {
		t.Error("Missing X-SDN-TRUST-LEVEL field")
	}
	if !strings.Contains(vcardStr, "END:VCARD") {
		t.Error("Missing END:VCARD")
	}
}

func TestTrustedPeerToVCard_RoundTrip(t *testing.T) {
	peerID, _ := peer.Decode("12D3KooWDpJ7As7BWAwRMfu1VU2WCqNjvq387JEYKDBj4kx6nXTN")

	tp := &TrustedPeer{
		ID:           peerID,
		TrustLevel:   Trusted,
		Name:         "Round Trip Test",
		Organization: "Test Org",
	}

	vcardStr := TrustedPeerToVCard(tp)

	// Parse back
	info, err := ParseVCard(vcardStr)
	if err != nil {
		t.Fatalf("Failed to parse generated vCard: %v", err)
	}

	if info.PeerID != peerID {
		t.Errorf("PeerID mismatch after round trip")
	}
	if info.Name != "Round Trip Test" {
		t.Errorf("Name mismatch: got %q", info.Name)
	}
	if info.Organization != "Test Org" {
		t.Errorf("Organization mismatch: got %q", info.Organization)
	}
}

func TestImportPeerFromVCard(t *testing.T) {
	registry := NewRegistry(false, nil)

	tp, err := ImportPeerFromVCard(registry, testVCard)
	if err != nil {
		t.Fatalf("ImportPeerFromVCard failed: %v", err)
	}

	if tp.Name != "ISS Tracking Node" {
		t.Errorf("Name mismatch: got %q", tp.Name)
	}
	if tp.TrustLevel != Trusted {
		t.Errorf("Trust level mismatch: got %s", tp.TrustLevel)
	}

	// Verify it's in the registry
	if registry.PeerCount() != 1 {
		t.Errorf("Expected 1 peer in registry, got %d", registry.PeerCount())
	}

	// Import again should fail (duplicate)
	_, err = ImportPeerFromVCard(registry, testVCard)
	if err != ErrPeerAlreadyExists {
		t.Errorf("Expected ErrPeerAlreadyExists, got %v", err)
	}
}

func TestImportPeerFromVCard_InvalidVCard(t *testing.T) {
	registry := NewRegistry(false, nil)

	_, err := ImportPeerFromVCard(registry, "not a vcard")
	if err == nil {
		t.Error("Expected error for invalid vCard")
	}
}
