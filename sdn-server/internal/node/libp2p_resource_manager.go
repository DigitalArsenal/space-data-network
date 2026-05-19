package node

import (
	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/network"
	coreprotocol "github.com/libp2p/go-libp2p/core/protocol"
	rcmgr "github.com/libp2p/go-libp2p/p2p/host/resource-manager"

	"github.com/spacedatanetwork/sdn-server/internal/protocol"
)

const flatSQLSyncBulkStreamLimit = 512

func newFlatSQLSyncResourceManager() (network.ResourceManager, error) {
	limits := rcmgr.DefaultLimits
	libp2p.SetDefaultServiceLimits(&limits)
	applyFlatSQLSyncResourceLimits(&limits)
	return rcmgr.NewResourceManager(rcmgr.NewFixedLimiter(limits.AutoScale()))
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
