package node

// Regression gate for `sdn-ws-inbound-accept-wedge`.
//
// Two properties are pinned, and the SECOND one is the one that actually cost us
// the outage:
//
//  1. The inbound connection ceilings are large enough that a backlog of pinned
//     handoffs cannot exhaust admission on a publicly exposed node.
//  2. A refused inbound connection is REPORTED. rcmgr denies connections
//     silently by default, and that silence is why a total public inbound outage
//     produced zero log lines and needed a SIGQUIT dump of production to find.

import (
	"testing"

	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/network"
	rcmgr "github.com/libp2p/go-libp2p/p2p/host/resource-manager"
	"github.com/libp2p/go-libp2p/p2p/net/upgrader"
)

// effectiveInboundCeilings returns the post-AutoScale inbound ceilings for a
// (memory, numFD) profile, exactly as the daemon computes them.
func effectiveInboundCeilings(t *testing.T, memory int64, numFD int) (systemInbound, transientInbound int) {
	t.Helper()
	limits := rcmgr.DefaultLimits
	libp2p.SetDefaultServiceLimits(&limits)
	applyFlatSQLSyncResourceLimits(&limits)
	applyInboundAdmissionLimits(&limits)
	c := limits.Scale(memory, numFD).ToPartialLimitConfig()
	return int(c.System.ConnsInbound), int(c.Transient.ConnsInbound)
}

// TestInboundCeilingsExceedTheMeasuredWedgeLevel is the numeric gate. host-01
// wedged with ~121-137 pinned inbound connections against a transient ceiling of
// 160. Anything at that order of magnitude is not a ceiling, it is a fuse.
func TestInboundCeilingsExceedTheMeasuredWedgeLevel(t *testing.T) {
	// The peak inbound census measured on the wedged host.
	const measuredWedgeLevel = 137

	sys, transient := effectiveInboundCeilings(t, 8<<30, 65536)
	t.Logf("host-01 profile (8GB/65536fd): System.ConnsInbound=%d Transient.ConnsInbound=%d", sys, transient)

	if transient <= measuredWedgeLevel {
		t.Fatalf("Transient.ConnsInbound=%d is at or below the level that ALREADY wedged production (%d) — "+
			"a publicly exposed node cannot admit inbound behind this ceiling", transient, measuredWedgeLevel)
	}
	if transient < inboundAdmissionTransientConns {
		t.Fatalf("Transient.ConnsInbound=%d is below the configured floor %d — applyInboundAdmissionLimits is not taking effect",
			transient, inboundAdmissionTransientConns)
	}
	if sys < inboundAdmissionSystemConns {
		t.Fatalf("System.ConnsInbound=%d is below the configured floor %d", sys, inboundAdmissionSystemConns)
	}
	// Sanity: the ceiling must stay well inside the descriptor budget, or we have
	// traded one exhaustion for another.
	if transient > 16384 {
		t.Fatalf("Transient.ConnsInbound=%d exceeds the Transient.FD backstop (16384)", transient)
	}
}

// TestSmallHostStillGetsHeadroom guards the 1 vCPU / 2 GB box: AutoScale sizes
// DOWN with memory, and host-02's transient inbound ceiling was only 64. The
// floors must apply there too, or the smaller box stays wedgeable.
func TestSmallHostStillGetsHeadroom(t *testing.T) {
	_, transient := effectiveInboundCeilings(t, 2<<30, 65536)
	t.Logf("host-02 profile (2GB/65536fd): Transient.ConnsInbound=%d", transient)
	if transient < inboundAdmissionTransientConns {
		t.Fatalf("Transient.ConnsInbound=%d on the 2GB profile is below the floor %d — the small host is still wedgeable",
			transient, inboundAdmissionTransientConns)
	}
}

// TestUpgraderAcceptQueueIsWidened pins the DRAIN rate. go-libp2p's default of
// 16 concurrent inbound negotiations is what let a backlog of pinned handoffs
// build faster than it cleared.
func TestUpgraderAcceptQueueIsWidened(t *testing.T) {
	original := upgrader.AcceptQueueLength
	t.Cleanup(func() { upgrader.AcceptQueueLength = original })

	applyUpgraderAcceptQueueLength()
	if upgrader.AcceptQueueLength < upgraderAcceptQueueLength {
		t.Fatalf("upgrader.AcceptQueueLength=%d, want >= %d", upgrader.AcceptQueueLength, upgraderAcceptQueueLength)
	}
	t.Logf("upgrader.AcceptQueueLength raised %d -> %d", original, upgrader.AcceptQueueLength)
}

// TestApplyUpgraderAcceptQueueNeverLowers: the helper is a floor, not an
// assignment. If a future host or a test raises it further, we must not undo it.
func TestApplyUpgraderAcceptQueueNeverLowers(t *testing.T) {
	original := upgrader.AcceptQueueLength
	t.Cleanup(func() { upgrader.AcceptQueueLength = original })

	upgrader.AcceptQueueLength = upgraderAcceptQueueLength * 4
	applyUpgraderAcceptQueueLength()
	if upgrader.AcceptQueueLength != upgraderAcceptQueueLength*4 {
		t.Fatalf("applyUpgraderAcceptQueueLength lowered a larger existing value to %d", upgrader.AcceptQueueLength)
	}
}

// TestRefusedInboundConnectionIsReported is the observability gate — the
// property whose absence made this defect invisible. A silent refusal is the
// defect, independent of any ceiling.
func TestRefusedInboundConnectionIsReported(t *testing.T) {
	r := newInboundAdmissionReporter()

	if got := r.Snapshot(); got.BlockedInbound != 0 || got.RefusingSinceSeconds != 0 {
		t.Fatalf("fresh reporter is not clean: %+v", got)
	}

	r.AllowConn(network.DirInbound, true)
	r.AllowConn(network.DirOutbound, true)
	r.BlockConn(network.DirInbound, true)
	r.BlockConn(network.DirInbound, true)
	r.BlockConn(network.DirOutbound, true)

	got := r.Snapshot()
	if got.BlockedInbound != 2 {
		t.Fatalf("BlockedInbound=%d, want 2", got.BlockedInbound)
	}
	if got.AllowedInbound != 1 {
		t.Fatalf("AllowedInbound=%d, want 1", got.AllowedInbound)
	}
	if got.BlockedOutbound != 1 {
		t.Fatalf("BlockedOutbound=%d, want 1", got.BlockedOutbound)
	}
	// The ONSET must be recorded, not just the current state: "since when" is
	// the question a restart-relief defect actually turns on.
	if got.RefusingSinceSeconds <= 0 {
		t.Fatal("RefusingSinceSeconds is 0 after an inbound refusal — the onset of an outage must be recorded")
	}
}

// TestInboundAdmissionSnapshotIsReadableFromTheNode pins that the counters are
// reachable through the package-level accessor an admin/status surface would use
// (task item 4: observable from the node itself, not inferred from `ss`).
func TestInboundAdmissionSnapshotIsReadableFromTheNode(t *testing.T) {
	before := InboundAdmission().BlockedInbound
	inboundAdmissionReport.BlockConn(network.DirInbound, false)
	if after := InboundAdmission().BlockedInbound; after != before+1 {
		t.Fatalf("InboundAdmission() did not observe the refusal: before=%d after=%d", before, after)
	}
}
