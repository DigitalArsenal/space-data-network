package node

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNodeSourceIncludesAutoRelayPeerSource(t *testing.T) {
	t.Parallel()

	sourcePath := filepath.Join(".", "node.go")
	data, err := os.ReadFile(sourcePath)
	if err != nil {
		t.Fatalf("os.ReadFile(%q) failed: %v", sourcePath, err)
	}

	source := string(data)
	if !strings.Contains(source, "libp2p.EnableAutoRelayWithPeerSource(") {
		t.Fatalf("node host config no longer enables auto relay with peer source")
	}
}

func TestNodeSourceDoesNotTreatBootstrapOrMDNSPeersAsAdvertisementDiscovered(t *testing.T) {
	t.Parallel()

	sourcePath := filepath.Join(".", "node.go")
	data, err := os.ReadFile(sourcePath)
	if err != nil {
		t.Fatalf("os.ReadFile(%q) failed: %v", sourcePath, err)
	}

	source := string(data)
	if strings.Contains(source, "n.recordCurrentSDNAdvertisementPeerInfo(peerInfo.AddrInfo)") {
		t.Fatalf("bootstrap peer path should not mark peers as SDN advertisement-discovered")
	}
	if strings.Contains(source, "m.node.recordCurrentSDNAdvertisementPeerInfo(pi)") {
		t.Fatalf("mDNS peer path should not mark peers as SDN advertisement-discovered")
	}
}
