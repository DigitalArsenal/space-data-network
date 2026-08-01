package peers

import (
	"testing"

	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/test"
)

// switchablePersistence models the hydration race: at construction time (the
// registry's boot Load) the projection reads EMPTY because the PRR stream has
// not been replayed; after hydration the same provider serves the full
// projection.
type switchablePersistence struct {
	peers  map[peer.ID]*TrustedPeer
	groups map[string]*PeerGroup
}

func (p *switchablePersistence) Save(map[peer.ID]*TrustedPeer, map[string]*PeerGroup) error {
	return nil
}

func (p *switchablePersistence) Load() (map[peer.ID]*TrustedPeer, map[string]*PeerGroup, error) {
	peersCopy := make(map[peer.ID]*TrustedPeer, len(p.peers))
	for id, tp := range p.peers {
		clone := *tp
		peersCopy[id] = &clone
	}
	groupsCopy := make(map[string]*PeerGroup, len(p.groups))
	for name, g := range p.groups {
		clone := *g
		groupsCopy[name] = &clone
	}
	return peersCopy, groupsCopy, nil
}

// Regression for sdn-peer-registry-load-races-hydration: learned rows,
// EPMData and owner-set trust must survive a restart once hydration completes
// and ReloadFromPersistence runs — without clobbering state the live registry
// acquired in the boot window.
func TestReloadFromPersistenceRestoresRowsWithoutClobbering(t *testing.T) {
	learnedID := test.RandPeerIDFatal(t)
	configID := test.RandPeerIDFatal(t)
	freshID := test.RandPeerIDFatal(t)

	provider := &switchablePersistence{}
	registry := NewRegistry(false, provider)

	// Boot window: config re-adds a peer (default trust, no EPM), and the
	// exchange pump already stored a FRESH EPM for another peer.
	if err := registry.AddPeer(&TrustedPeer{ID: configID, TrustLevel: Standard}); err != nil {
		t.Fatalf("AddPeer(config): %v", err)
	}
	if err := registry.AddPeer(&TrustedPeer{
		ID:         freshID,
		TrustLevel: Standard,
		EPMData:    []byte("fresh-epm-from-boot-window"),
	}); err != nil {
		t.Fatalf("AddPeer(fresh): %v", err)
	}

	// Hydration completes: the projection now serves what the boot Load
	// missed — a learned peer with EPM, owner trust on the config peer, and a
	// STALE EPM for the peer the boot window already refreshed.
	provider.peers = map[peer.ID]*TrustedPeer{
		learnedID: {
			ID:         learnedID,
			TrustLevel: Standard,
			Name:       "space-data-network-02",
			EPMData:    []byte("learned-epm"),
			VCardData:  "BEGIN:VCARD...",
		},
		configID: {
			ID:         configID,
			TrustLevel: Admin,
			Notes:      "owner-set trust persisted before the restart",
		},
		freshID: {
			ID:         freshID,
			TrustLevel: Standard,
			EPMData:    []byte("stale-epm-from-before-restart"),
		},
	}
	provider.groups = map[string]*PeerGroup{
		"fleet": {Name: "fleet", DefaultTrustLevel: Standard},
	}

	adopted, err := registry.ReloadFromPersistence()
	if err != nil {
		t.Fatalf("ReloadFromPersistence: %v", err)
	}
	if adopted != 2 {
		t.Fatalf("adopted = %d, want 2 (learned row + config trust fill; fresh peer untouched)", adopted)
	}

	learned, err := registry.GetPeer(learnedID)
	if err != nil || learned == nil {
		t.Fatalf("learned peer missing after reload: %v", err)
	}
	if string(learned.EPMData) != "learned-epm" || learned.Name != "space-data-network-02" {
		t.Fatalf("learned row not restored: %+v", learned)
	}

	config, err := registry.GetPeer(configID)
	if err != nil || config == nil {
		t.Fatalf("config peer missing after reload: %v", err)
	}
	if config.TrustLevel != Admin {
		t.Fatalf("owner-set trust not restored over the config default: %v", config.TrustLevel)
	}

	fresh, err := registry.GetPeer(freshID)
	if err != nil || fresh == nil {
		t.Fatalf("fresh peer missing after reload: %v", err)
	}
	if string(fresh.EPMData) != "fresh-epm-from-boot-window" {
		t.Fatalf("reload clobbered a boot-window EPM with the stale persisted one: %q", fresh.EPMData)
	}

	if _, err := registry.GetGroup("fleet"); err != nil {
		t.Fatalf("persisted group not adopted on reload: %v", err)
	}
}

// A registry with no persistence provider must no-op, not panic.
func TestReloadFromPersistenceWithoutProvider(t *testing.T) {
	registry := NewRegistry(false, nil)
	adopted, err := registry.ReloadFromPersistence()
	if err != nil || adopted != 0 {
		t.Fatalf("no-provider reload = (%d, %v), want (0, nil)", adopted, err)
	}
}
