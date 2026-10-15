package node

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/spacedatanetwork/sdn-server/internal/trustanchors"
)

// SEC-001: a node whose stored seed is a denylisted identity refuses to come
// up as it. A freshly generated identity stands in for the published one.
func TestIdentityBundleRefusesADenylistedSeed(t *testing.T) {
	n := newTestIdentityBundleNode(t)
	bundle, err := n.loadOrCreateIdentityBundle()
	if err != nil {
		t.Fatalf("control: %v", err)
	}

	data, err := json.Marshal(trustanchors.Load())
	if err != nil {
		t.Fatal(err)
	}
	poisoned, err := trustanchors.Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	poisoned.Denylist.PeerIDs = append(poisoned.Denylist.PeerIDs, bundle.PeerID.String())
	data, _ = json.Marshal(poisoned)
	if poisoned, err = trustanchors.Parse(data); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(trustanchors.Use(poisoned))

	if _, err := n.loadOrCreateIdentityBundle(); !errors.Is(err, trustanchors.ErrDenied) {
		t.Fatalf("loading a denylisted identity = %v, want ErrDenied", err)
	}
}
