package peers

import (
	"strings"
	"testing"

	"github.com/libp2p/go-libp2p/core/peer"
)

// A nameless trusted peer must never surface libp2p's "<peer.ID 16*abc123>"
// machine form as its contact name, and header filename suffixes must stay
// header-safe.
func TestTrustedPeerToVCardNamelessPeerGetsSDNNodeName(t *testing.T) {
	peerID, err := peer.Decode("12D3KooWDpJ7As7BWAwRMfu1VU2WCqNjvq387JEYKDBj4kx6nXTN")
	if err != nil {
		t.Fatalf("decode peer id: %v", err)
	}
	card := TrustedPeerToVCard(&TrustedPeer{ID: peerID})
	if strings.Contains(card, "peer.ID") {
		t.Errorf("FN leaked the peer.ID short form:\n%s", card)
	}
	if !strings.Contains(card, "FN:SDN Node ") {
		t.Errorf("expected FN:SDN Node <suffix> fallback:\n%s", card)
	}
}

func TestPeerFilenameSuffixIsHeaderSafe(t *testing.T) {
	peerID, err := peer.Decode("12D3KooWDpJ7As7BWAwRMfu1VU2WCqNjvq387JEYKDBj4kx6nXTN")
	if err != nil {
		t.Fatalf("decode peer id: %v", err)
	}
	suffix := peerFilenameSuffix(peerID)
	if len(suffix) != 8 || strings.ContainsAny(suffix, "<>* \t\"") {
		t.Errorf("suffix %q is not a header-safe 8-char id tail", suffix)
	}
}
