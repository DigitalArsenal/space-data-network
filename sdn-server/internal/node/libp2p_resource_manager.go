package node

import (
	"net/netip"
	"time"

	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/network"
	coreprotocol "github.com/libp2p/go-libp2p/core/protocol"
	rcmgr "github.com/libp2p/go-libp2p/p2p/host/resource-manager"
	"github.com/libp2p/go-libp2p/x/rate"

	"github.com/spacedatanetwork/sdn-server/internal/license"
	"github.com/spacedatanetwork/sdn-server/internal/modulert"
	"github.com/spacedatanetwork/sdn-server/internal/protocol"
)

const flatSQLSyncBulkStreamLimit = 512

// deliveryBurstStreamLimit is the per-peer inbound stream budget granted to the
// module delivery/publish protocols.
//
// SIZED FROM THE CLIENT'S ACTUAL SHAPE, not from a round number. A browser
// opening the RF gallery requests ten modules, each module costs TWO
// round trips (challenge, then grant proof), and every request may arrive on
// its own connection because the delivery client is constructed per call — so
// one page load is a burst of ~20 streams from what rcmgr sees as one peer, and
// several tabs or a reload multiply it. 256 leaves two orders of magnitude of
// headroom over that while remaining far below the peer/protocol ceilings.
//
// Why an EXPLICIT limit rather than the upstream default: the default
// ProtocolPeerBaseLimit is the one scope on the delivery path that nothing in
// this package raised, and a denial there is not a slow path or a queue — it is
// `SetProtocol` failing inside BasicHost.newStreamHandler, which resets the
// stream with StreamErrorCode 0x1002 (StreamResourceLimitExceeded). That reset
// is indistinguishable on the wire from a network fault and is exactly the
// "stream reset" the owner saw on a first attempt. A protocol whose whole job
// is to answer a burst of short-lived requests must have a budget that says so.
const deliveryBurstStreamLimit = 256

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
	applyModuleDeliveryResourceLimits(&limits)
	applyInboundAdmissionLimits(&limits)

	// WithMetrics is what makes admission refusals visible at all. Without a
	// reporter, rcmgr denies connections completely silently, which is precisely
	// how a total public inbound outage produced zero log lines.
	return rcmgr.NewResourceManager(
		rcmgr.NewFixedLimiter(limits.AutoScale()),
		rcmgr.WithMetrics(inboundAdmissionReport),
		rcmgr.WithLimitPerSubnet(browserFriendlySubnetLimitsV4(), browserFriendlySubnetLimitsV6()),
		rcmgr.WithConnRateLimiters(browserFriendlyConnRateLimiter()),
	)
}

// browserFriendlyConnRateLimiter raises rcmgr's per-source-IP connection RATE
// ceiling — the FOURTH limit derived from upstream's `defaultMaxConcurrentConns
// = 8`, and the one that explains the specific shape of the reported failure.
//
// The upstream default for IPv4 is RPS 0.2 with a burst of 16 per /32: a source
// IP may open sixteen connections at once and then ONE MORE EVERY FIVE SECONDS.
// A single gallery tab opens ten connections, so the first load fits and the
// SECOND does not — which is exactly what was measured live on 2026-08-08:
// two clean loads from a fresh browser, then a failure on essentially every
// load after that, naming a different module each time because the loser is
// whichever request arrives once the bucket is empty.
//
// A user reloading a page, opening two tabs, or sitting behind a shared egress
// is not abuse, and answering them with a stream reset is not rate limiting —
// it is an outage with a misleading error message.
//
// Loopback keeps its unlimited entry, which the TLS-proxy lane depends on: the
// node's own :443 server reverse-proxies browser /p2p/ upgrades to the loopback
// libp2p listener, so every Cloudflare-fronted client shares 127.0.0.1 as its
// apparent source.
func browserFriendlyConnRateLimiter() *rate.Limiter {
	return &rate.Limiter{
		NetworkPrefixLimits: []rate.PrefixLimit{
			{Prefix: netip.MustParsePrefix("127.0.0.0/8"), Limit: rate.Limit{}},
			{Prefix: netip.MustParsePrefix("::1/128"), Limit: rate.Limit{}},
		},
		SubnetRateLimiter: rate.SubnetLimiter{
			IPv4SubnetLimits: []rate.SubnetLimit{
				{PrefixLength: 32, Limit: rate.Limit{RPS: browserConnRPS, Burst: browserConnBurst}},
			},
			IPv6SubnetLimits: []rate.SubnetLimit{
				{PrefixLength: 56, Limit: rate.Limit{RPS: browserConnRPS, Burst: browserConnBurst}},
				{PrefixLength: 48, Limit: rate.Limit{RPS: browserConnRPS * 4, Burst: browserConnBurst * 4}},
			},
			GracePeriod: time.Minute,
		},
	}
}

const (
	// browserConnRPS sustains repeated page loads from one egress IP: a gallery
	// tab costs ~10 connections, so 20/s carries two full loads per second
	// indefinitely instead of upstream's one connection per five seconds.
	browserConnRPS = 20
	// browserConnBurst absorbs a tab-open spike and several tabs behind one NAT.
	browserConnBurst = 512
)

// browserFriendlySubnetLimitsV4 / V6 raise rcmgr's PER-SOURCE-IP connection
// ceiling.
//
// This is the THIRD independent ceiling of 8 on the delivery path, and it is
// the one that governs the direct `/ip4/…/tcp/4004/ws` lane. rcmgr's default is
// eight connections per single IPv4 address (defaultMaxConcurrentConns, carried
// verbatim from the same "matches the number of concurrent dials we may do"
// comment as the per-peer ceiling). It is a sensible anti-abuse default for a
// node whose peers are other servers, and a wrong one for a node whose clients
// are browsers:
//
//   - ONE gallery tab opens ten connections, so a single user exceeds it alone;
//   - every user behind one corporate NAT, one CGNAT range or one campus egress
//     shares that budget, so the ninth visitor from a company is refused because
//     the first eight were not;
//   - the refusal is delivered as a stream reset, so it reads to the user as a
//     broken network rather than as a policy.
//
// The loopback exemption in DefaultNetworkPrefixLimitV4 is retained implicitly
// (WithLimitPerSubnet only replaces the per-subnet table, not the network-prefix
// table), which matters because Cloudflare-fronted /p2p/ upgrades reach the
// libp2p listener from 127.0.0.1 via this node's own reverse proxy.
//
// 512 per /32 with a wider /24 backstop keeps a single abusive host bounded
// while putting the ceiling far above any honest browser population.
func browserFriendlySubnetLimitsV4() []rcmgr.ConnLimitPerSubnet {
	return []rcmgr.ConnLimitPerSubnet{
		{PrefixLength: 32, ConnCount: browserSubnetConnLimit},
		{PrefixLength: 24, ConnCount: browserSubnetConnLimit * 4},
	}
}

func browserFriendlySubnetLimitsV6() []rcmgr.ConnLimitPerSubnet {
	return []rcmgr.ConnLimitPerSubnet{
		{PrefixLength: 56, ConnCount: browserSubnetConnLimit},
		{PrefixLength: 48, ConnCount: browserSubnetConnLimit * 4},
	}
}

// browserSubnetConnLimit is the per-source-IP inbound connection ceiling. Sized
// like inboundAdmissionPeerConns: the unit of demand is a browser TAB (ten
// connections measured), and many tabs may share one egress IP.
const browserSubnetConnLimit = 512

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

	// PER-PEER inbound connections. Raised here rather than left to AutoScale
	// because AutoScale cannot raise it: PeerLimitIncrease declares no Conns
	// fields, so the upstream default of 8 is a hard ceiling on every host
	// size. See inboundAdmissionPeerConns for the measurement — this is the
	// limit a single browser tab exceeded, and exceeding it is answered with a
	// stream reset that reads to the client as a network fault.
	if limits.PeerBaseLimit.ConnsInbound < inboundAdmissionPeerConns {
		limits.PeerBaseLimit.ConnsInbound = inboundAdmissionPeerConns
	}
	if limits.PeerBaseLimit.Conns < inboundAdmissionPeerConns*2 {
		limits.PeerBaseLimit.Conns = inboundAdmissionPeerConns * 2
	}
	// Each inbound connection costs a descriptor; the per-peer FD budget must
	// keep pace or the raise above just moves the denial one scope over.
	if limits.PeerBaseLimit.FD < inboundAdmissionPeerConns {
		limits.PeerBaseLimit.FD = inboundAdmissionPeerConns
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

// applyModuleDeliveryResourceLimits gives the module delivery and publish
// protocols an explicit per-peer stream budget.
//
// These two protocols are the node's public product surface: a browser cannot
// use ANY paid module without completing the delivery handshake, and the
// publish lane is how every module reaches the catalog in the first place. They
// were the only busy SDN protocols with no declared limit, so both inherited
// upstream's generic ProtocolPeerBaseLimit and both were observed being reset
// under a burst that a client cannot avoid making.
//
// The wire IDs are taken from their owning packages rather than retyped, so a
// protocol rename can never silently drop the limit and reintroduce the defect.
func applyModuleDeliveryResourceLimits(limits *rcmgr.ScalingLimitConfig) {
	if limits == nil {
		return
	}

	for _, pid := range deliveryBurstProtocols() {
		limits.AddProtocolPeerLimit(
			pid,
			rcmgr.BaseLimit{
				StreamsInbound:  deliveryBurstStreamLimit,
				StreamsOutbound: deliveryBurstStreamLimit,
				Streams:         deliveryBurstStreamLimit * 2,
				Memory:          64 << 20,
			},
			rcmgr.BaseLimitIncrease{
				StreamsInbound:  deliveryBurstStreamLimit / 4,
				StreamsOutbound: deliveryBurstStreamLimit / 4,
				Streams:         deliveryBurstStreamLimit / 2,
				Memory:          16 << 20,
			},
		)
	}
}

// deliveryBurstProtocols are the request/response protocols that must survive a
// client-side burst. Kept as one list so the limit config and the admission
// reporting below can never disagree about which protocols matter.
func deliveryBurstProtocols() []coreprotocol.ID {
	return []coreprotocol.ID{
		coreprotocol.ID(modulert.ModuleDeliveryWireID),
		coreprotocol.ID(license.ModulePublishProtocolID),
	}
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
