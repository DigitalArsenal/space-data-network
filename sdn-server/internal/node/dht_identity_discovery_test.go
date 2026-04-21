package node

import (
	"encoding/hex"
	"testing"

	"github.com/ipfs/go-cid"
	"github.com/libp2p/go-libp2p/core/peer"
)

func TestComputeModuleDeliveryDiscoveryCID(t *testing.T) {
	t.Parallel()

	pubKey, err := hex.DecodeString("021111111111111111111111111111111111111111111111111111111111111111")
	if err != nil {
		t.Fatalf("hex.DecodeString failed: %v", err)
	}

	discoveryCID, err := computeModuleDeliveryDiscoveryCID(pubKey)
	if err != nil {
		t.Fatalf("computeModuleDeliveryDiscoveryCID failed: %v", err)
	}

	const expected = "bafkreicfbpehrnn2ynqbs7tf7cu4rj57tsw222kkna33ytyfaz6v37oniu"
	if got := discoveryCID.String(); got != expected {
		t.Fatalf("discovery CID = %q, want %q", got, expected)
	}
}

func TestComputeModuleDeliveryDiscoveryCIDRejectsNonCompressedKey(t *testing.T) {
	t.Parallel()

	if _, err := computeModuleDeliveryDiscoveryCID(make([]byte, 32)); err == nil {
		t.Fatal("expected error for non-compressed provider key")
	}
}

func TestModuleDeliveryDiscoveryTargetsUseProviderIdentityOnly(t *testing.T) {
	t.Parallel()

	providerPubKey, err := hex.DecodeString("021111111111111111111111111111111111111111111111111111111111111111")
	if err != nil {
		t.Fatalf("hex.DecodeString failed: %v", err)
	}
	providerDiscoveryCID, err := computeModuleDeliveryDiscoveryCID(providerPubKey)
	if err != nil {
		t.Fatalf("computeModuleDeliveryDiscoveryCID failed: %v", err)
	}

	targets := moduleDeliveryDiscoveryTargets(providerDiscoveryCID)
	if len(targets) != 1 {
		t.Fatalf("discovery target count = %d, want 1", len(targets))
	}
	if got := targets[0]; got != providerDiscoveryCID {
		t.Fatalf("discovery target = %s, want %s", got, providerDiscoveryCID)
	}
	for _, target := range targets {
		if target == cid.Undef {
			t.Fatal("discovery target must be defined")
		}
	}
}

func TestSDNAdvertisementDiscoveryTargetsUseSupportedFlagWindow(t *testing.T) {
	t.Parallel()

	announce, discover, err := sdnAdvertisementDiscoveryTargets(
		"spacedatanetwork/1.2.0",
		[]string{
			"spacedatanetwork/1.2.0",
			"spacedatanetwork/1.1.0",
			"spacedatanetwork/1.0.0",
			"spacedatanetwork/1.2.0",
		},
	)
	if err != nil {
		t.Fatalf("sdnAdvertisementDiscoveryTargets failed: %v", err)
	}

	if announce.Flag != "spacedatanetwork/1.2.0" {
		t.Fatalf("announce flag = %q, want current flag", announce.Flag)
	}
	if announce.CID == cid.Undef {
		t.Fatal("announce CID must be defined")
	}

	if len(discover) != 3 {
		t.Fatalf("discover target count = %d, want 3 unique flags", len(discover))
	}
	if discover[0].Flag != "spacedatanetwork/1.2.0" {
		t.Fatalf("discover[0] flag = %q, want current flag first", discover[0].Flag)
	}
	if discover[1].Flag != "spacedatanetwork/1.1.0" {
		t.Fatalf("discover[1] flag = %q, want previous supported flag", discover[1].Flag)
	}
	if discover[2].Flag != "spacedatanetwork/1.0.0" {
		t.Fatalf("discover[2] flag = %q, want oldest supported flag", discover[2].Flag)
	}
	for _, target := range discover {
		if target.CID == cid.Undef {
			t.Fatalf("discover target %q must have a defined CID", target.Flag)
		}
	}
}

func TestRecordCurrentSDNAdvertisementDiscoveryUsesAnnouncedFlag(t *testing.T) {
	n := &Node{
		sdnAdvertisementTarget: sdnAdvertisementDiscoveryTarget{Flag: "spacedatanetwork/1.2.3"},
	}

	pid, err := peer.Decode("12D3KooWJQvxYjnF8UARVq8hdD2WmT9N4xJm9kMumZ5qX6Ch12yv")
	if err != nil {
		t.Fatalf("decode peer id: %v", err)
	}

	n.recordCurrentSDNAdvertisementDiscovery(pid)

	flagsByPeer := n.SDNAdvertisementFlagsByPeer()
	flags := flagsByPeer[pid.String()]
	if len(flags) != 1 || flags[0] != "spacedatanetwork/1.2.3" {
		t.Fatalf("recorded advertisement flags = %v, want [spacedatanetwork/1.2.3]", flags)
	}
}
