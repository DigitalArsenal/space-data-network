package sdnflag

import (
	"testing"
	"time"

	plugin "github.com/ipfs/kubo/plugin"
	"github.com/libp2p/go-libp2p/core/peer"
)

func TestInitDefaults(t *testing.T) {
	p := &sdnFlagPlugin{}
	if err := p.Init(nil); err != nil {
		t.Fatalf("Init(nil): %v", err)
	}
	if !p.enabled {
		t.Error("SDN flag must be enabled by default (this fork exists for SDN membership)")
	}
	// The namespace must stay byte-identical to the pre-rebase node so a
	// kubo-based SDN node and a legacy node rendezvous on the same key.
	const want = "space-data-network/discovery/advertisement-flag/spacedatanetwork/1.0.0"
	if p.namespace != want {
		t.Errorf("namespace = %q, want %q", p.namespace, want)
	}
}

func TestInitConfigOverride(t *testing.T) {
	env := &plugin.Environment{Config: map[string]interface{}{
		"Enabled": false,
		"Flag":    "spacedatanetwork/2.0.0",
	}}
	p := &sdnFlagPlugin{}
	if err := p.Init(env); err != nil {
		t.Fatalf("Init(env): %v", err)
	}
	if p.enabled {
		t.Error("Enabled=false in config must disable the plugin")
	}
	if want := "space-data-network/discovery/advertisement-flag/spacedatanetwork/2.0.0"; p.namespace != want {
		t.Errorf("namespace = %q, want %q", p.namespace, want)
	}
}

func TestInitIgnoresBlankFlag(t *testing.T) {
	env := &plugin.Environment{Config: map[string]interface{}{"Flag": "   "}}
	p := &sdnFlagPlugin{}
	if err := p.Init(env); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if want := "space-data-network/discovery/advertisement-flag/spacedatanetwork/1.0.0"; p.namespace != want {
		t.Errorf("blank Flag should fall back to default; got namespace %q", p.namespace)
	}
}

func TestSDNPeersTracking(t *testing.T) {
	p := &sdnFlagPlugin{peers: map[peer.ID]time.Time{}}
	if got := p.SDNPeers(); len(got) != 0 {
		t.Fatalf("expected 0 peers, got %d", len(got))
	}
	p.peers["12D3KooWA"] = time.Now()
	p.peers["12D3KooWB"] = time.Now()
	if got := p.SDNPeers(); len(got) != 2 {
		t.Errorf("expected 2 SDN peers, got %d", len(got))
	}
}

// Interface compliance is also asserted at package scope by the
// `var _ plugin.PluginDaemonInternal` declaration in sdnflag.go.
func TestImplementsPluginDaemonInternal(t *testing.T) {
	var _ plugin.PluginDaemonInternal = (*sdnFlagPlugin)(nil)
}
