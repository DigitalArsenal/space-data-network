package node

import (
	"github.com/spacedatanetwork/sdn-server/internal/config"
	"testing"
)

func TestDisabledMDNSDoesNotOpenDiscovery(t *testing.T) {
	cfg := config.Default()
	cfg.Peers.EnableMDNS = false
	n := &Node{config: cfg}
	n.wg.Add(1)
	// A nil host is sufficient when discovery is disabled.
	n.runMDNS()
	n.wg.Wait()
}
