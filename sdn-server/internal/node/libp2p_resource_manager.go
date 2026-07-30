package node

import (
	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/network"
	coreprotocol "github.com/libp2p/go-libp2p/core/protocol"
	rcmgr "github.com/libp2p/go-libp2p/p2p/host/resource-manager"

	"github.com/spacedatanetwork/sdn-server/internal/protocol"
)

const flatSQLSyncBulkStreamLimit = 512

// inboundAdmissionReport is the process-wide admission reporter, kept so a
// status surface can read the counters after construction.
var inboundAdmissionReport = newInboundAdmissionReporter()

// InboundAdmission returns the live inbound-admission counters. "Is this node
// still admitting inbound connections?" must be answerable from the node itself
// — inferring it from `ss` on the host is what made
// sdn-ws-inbound-accept-wedge cost three investigations.
func InboundAdmission() InboundAdmissionStats { return inboundAdmissionReport.Snapshot() }

func newFlatSQLSyncResourceManager() (network.ResourceManager, error) {
	// Must precede listener construction — see the function's doc comment.
	applyUpgraderAcceptQueueLength()

	limits := rcmgr.DefaultLimits
	libp2p.SetDefaultServiceLimits(&limits)
	applyFlatSQLSyncResourceLimits(&limits)
	applyInboundAdmissionLimits(&limits)

	// WithMetrics is what makes admission refusals visible at all. Without a
	// reporter, rcmgr denies connections completely silently, which is precisely
	// how a total public inbound outage produced zero log lines.
	return rcmgr.NewResourceManager(
		rcmgr.NewFixedLimiter(limits.AutoScale()),
		rcmgr.WithMetrics(inboundAdmissionReport),
	)
}

// applyInboundAdmissionLimits raises the INBOUND CONNECTION ceilings only.
//
// Scoped deliberately narrowly: stream, memory and FD budgets are left exactly
// as upstream sizes them, because the measured failure was connection admission
// and nothing else. The BaseLimit values here are pre-AutoScale, and AutoScale
// only ever increases them with available memory, so these act as floors.
//
// See libp2p_inbound_admission.go for the full measurement and the sizing
// rationale (host-01 ran a 160-slot transient inbound ceiling against 65536
// available descriptors with ~250 in use).
func applyInboundAdmissionLimits(limits *rcmgr.ScalingLimitConfig) {
	if limits == nil {
		return
	}

	if limits.TransientBaseLimit.ConnsInbound < inboundAdmissionTransientConns {
		limits.TransientBaseLimit.ConnsInbound = inboundAdmissionTransientConns
	}
	if limits.TransientBaseLimit.Conns < inboundAdmissionTransientConns*2 {
		limits.TransientBaseLimit.Conns = inboundAdmissionTransientConns * 2
	}
	if limits.SystemBaseLimit.ConnsInbound < inboundAdmissionSystemConns {
		limits.SystemBaseLimit.ConnsInbound = inboundAdmissionSystemConns
	}
	if limits.SystemBaseLimit.Conns < inboundAdmissionSystemConns*2 {
		limits.SystemBaseLimit.Conns = inboundAdmissionSystemConns * 2
	}
}

func applyFlatSQLSyncResourceLimits(limits *rcmgr.ScalingLimitConfig) {
	if limits == nil {
		return
	}

	ensureBaseStreamLimit(&limits.PeerBaseLimit, flatSQLSyncBulkStreamLimit*2, flatSQLSyncBulkStreamLimit*2, flatSQLSyncBulkStreamLimit*4)
	ensureBaseStreamLimit(&limits.ProtocolBaseLimit, flatSQLSyncBulkStreamLimit*4, flatSQLSyncBulkStreamLimit*4, flatSQLSyncBulkStreamLimit*8)

	limits.AddProtocolPeerLimit(
		coreprotocol.ID(protocol.FlatSQLSyncProtocolID),
		rcmgr.BaseLimit{
			StreamsInbound:  flatSQLSyncBulkStreamLimit,
			StreamsOutbound: flatSQLSyncBulkStreamLimit,
			Streams:         flatSQLSyncBulkStreamLimit * 2,
			Memory:          256 << 20,
		},
		rcmgr.BaseLimitIncrease{
			StreamsInbound:  flatSQLSyncBulkStreamLimit / 4,
			StreamsOutbound: flatSQLSyncBulkStreamLimit / 4,
			Streams:         flatSQLSyncBulkStreamLimit / 2,
			Memory:          64 << 20,
		},
	)
}

func ensureBaseStreamLimit(limit *rcmgr.BaseLimit, inbound int, outbound int, total int) {
	if limit.StreamsInbound < inbound {
		limit.StreamsInbound = inbound
	}
	if limit.StreamsOutbound < outbound {
		limit.StreamsOutbound = outbound
	}
	if limit.Streams < total {
		limit.Streams = total
	}
}
