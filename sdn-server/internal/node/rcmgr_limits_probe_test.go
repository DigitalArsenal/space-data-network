package node

// Diagnostic probe for `sdn-ws-inbound-accept-wedge`. Prints the EFFECTIVE
// autoscaled rcmgr limits for a given (memory, numFD) profile, so the inbound
// connection ceilings this node actually runs with are a MEASURED number rather
// than a recollection. Two earlier passes on this defect quoted conflicting
// values for the same box (System ConnsInbound 576 vs 2112), which is exactly
// the kind of unverifiable premise that sends an investigation sideways.
//
// Run: go test ./internal/node/ -run TestPrintEffectiveRcmgrLimits -v

import (
	"testing"

	"github.com/libp2p/go-libp2p"
	rcmgr "github.com/libp2p/go-libp2p/p2p/host/resource-manager"
)

func TestPrintEffectiveRcmgrLimits(t *testing.T) {
	profiles := []struct {
		name   string
		memory int64
		numFD  int
	}{
		// host-01 / sdn.spaceaware.io — 2 vCPU / 8 GB, LimitNOFILE=65536.
		{"host-01 (8GB, 65536 fd)", 8 << 30, 65536},
		// host-02 / celestrak.eth — 1 vCPU / 2 GB.
		{"host-02 (2GB, 65536 fd)", 2 << 30, 65536},
	}

	for _, p := range profiles {
		limits := rcmgr.DefaultLimits
		libp2p.SetDefaultServiceLimits(&limits)
		applyFlatSQLSyncResourceLimits(&limits)
		applyInboundAdmissionLimits(&limits)
		scaled := limits.Scale(p.memory, p.numFD).ToPartialLimitConfig()

		t.Logf("=== %s ===", p.name)
		t.Logf("  System.ConnsInbound      = %v", scaled.System.ConnsInbound)
		t.Logf("  System.Conns             = %v", scaled.System.Conns)
		t.Logf("  System.FD                = %v", scaled.System.FD)
		t.Logf("  Transient.ConnsInbound   = %v", scaled.Transient.ConnsInbound)
		t.Logf("  Transient.Conns          = %v", scaled.Transient.Conns)
		t.Logf("  Transient.FD             = %v", scaled.Transient.FD)
		t.Logf("  PeerDefault.ConnsInbound = %v", scaled.PeerDefault.ConnsInbound)
	}
}
