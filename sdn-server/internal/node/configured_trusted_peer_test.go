package node

import (
	"testing"

	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/multiformats/go-multiaddr"

	"github.com/spacedatanetwork/sdn-server/internal/peers"
)

func TestUpsertConfiguredTrustedPeerPromotesExistingStandardPeer(t *testing.T) {
	id, err := peer.Decode("16Uiu2HAmV963F8WEK6V1jTMNWrjFBkrKodB53RqsDA3qTsFcz3y4")
	if err != nil {
		t.Fatalf("decode peer id: %v", err)
	}
	oldAddr := multiaddr.StringCast("/ip4/127.0.0.1/tcp/4001")
	newAddr := multiaddr.StringCast("/ip4/167.172.219.213/tcp/4001")

	registry := peers.NewRegistry(false, nil)
	if err := registry.AddPeer(&peers.TrustedPeer{
		ID:         id,
		Addrs:      []multiaddr.Multiaddr{oldAddr},
		TrustLevel: peers.Standard,
		Name:       "discovered peer",
	}); err != nil {
		t.Fatalf("add standard peer: %v", err)
	}

	if err := upsertConfiguredTrustedPeer(registry, &peers.TrustedPeer{
		ID:         id,
		Addrs:      []multiaddr.Multiaddr{newAddr},
		TrustLevel: peers.Trusted,
		Name:       "Config Trusted Peer",
	}); err != nil {
		t.Fatalf("upsert configured peer: %v", err)
	}

	if !registry.IsTrusted(id) {
		t.Fatal("configured trusted peer should be trusted even when it already exists")
	}
	got, err := registry.GetPeer(id)
	if err != nil {
		t.Fatalf("get peer: %v", err)
	}
	if len(got.Addrs) != 1 || !got.Addrs[0].Equal(newAddr) {
		t.Fatalf("addresses = %#v, want refreshed configured address %s", got.Addrs, newAddr)
	}
	if got.Name != "discovered peer" {
		t.Fatalf("name = %q, want existing name preserved", got.Name)
	}
}
