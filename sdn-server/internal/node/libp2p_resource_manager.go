package node

import (
	"net/netip"
	"time"

	"github.com/multiformats/go-multiaddr"
	"github.com/pbnjay/memory"

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

// newFlatSQLSyncResourceManager is newNodeResourceManager with no allowlist
// and no connection-manager high water (tests and embedders).
func newFlatSQLSyncResourceManager() (network.ResourceManager, error) {
	return newNodeResourceManager(nil, 0)
}

// newNodeResourceManager builds the node's resource manager: kubo's limit
// policy with the SDN floors (kuboResourceLimits), go-libp2p's per-subnet and
// connection-rate limits, and the SDN allowlist.
func newNodeResourceManager(allowlist []multiaddr.Multiaddr, connMgrHighWater int) (network.ResourceManager, error) {
	// Must precede listener construction — see the function's doc comment.
	applyUpgraderAcceptQueueLength()

	limits := kuboResourceLimits(uint64(memory.TotalMemory())/2, numFDs()/2, connMgrHighWater)

	// WithMetrics is what makes admission refusals visible at all. Without a
	// reporter, rcmgr denies connections completely silently, which is precisely
	// how a total public inbound outage produced zero log lines.
	opts := []rcmgr.Option{
		rcmgr.WithMetrics(inboundAdmissionReport),
		rcmgr.WithLimitPerSubnet(browserFriendlySubnetLimitsV4(), browserFriendlySubnetLimitsV6()),
		rcmgr.WithConnRateLimiters(browserFriendlyConnRateLimiter()),
	}
	// The SDN fleet and the node's own loopback tunnel (Cloudflare-fronted
	// browsers) are allowlisted, exactly as kubo operators allowlist their
	// peering set through Swarm.ResourceMgr.Allowlist: when the public swarm
	// fills the normal scopes, allowlisted connections still get in.
	if len(allowlist) > 0 {
		opts = append(opts, rcmgr.WithAllowlistedMultiaddrs(allowlist))
	}
	return rcmgr.NewResourceManager(rcmgr.NewFixedLimiter(limits), opts...)
}

// resourceAllowlist is the rcmgr allowlist: the configured SDN fleet
// (bootstrap and trusted peers that carry an IP address; DNS names cannot be
// matched against a remote address) and loopback, where the node's own :443
// server hands over Cloudflare-fronted browser connections.
func resourceAllowlist(bootstrap, trusted []string) []multiaddr.Multiaddr {
	out := []multiaddr.Multiaddr{
		multiaddr.StringCast("/ip4/127.0.0.0/ipcidr/8"),
		multiaddr.StringCast("/ip6/::1/ipcidr/128"),
	}
	for _, raw := range append(append([]string{}, bootstrap...), trusted...) {
		addr, err := multiaddr.NewMultiaddr(raw)
		if err != nil {
			continue
		}
		var ip multiaddr.Component
		found := false
		var peerPart multiaddr.Multiaddr
		for _, c := range addr {
			switch c.Protocol().Code {
			case multiaddr.P_IP4, multiaddr.P_IP6:
				ip, found = c, true
			case multiaddr.P_P2P:
				peerPart = multiaddr.Multiaddr{c}
			}
		}
		if !found {
			continue
		}
		entry := multiaddr.Multiaddr{ip}
		if peerPart != nil {
			entry = append(entry, peerPart...)
		}
		out = append(out, entry)
	}
	return out
}

// kuboMinInboundConns is kubo's DefaultResourceMgrMinInboundConns.
const kuboMinInboundConns = 800

// kuboResourceLimits is kubo's own resource-manager policy
// (createDefaultLimitConfig in kubo v0.43.1 core/node/libp2p/rcmgr_defaults.go,
// documented in its docs/libp2p-resource-management.md), so this node limits
// the public swarm the way every kubo node does, plus the SDN raises:
//
//   - System and Transient STREAMS are unlimited. Only inbound CONNECTIONS
//     are capped (kubo: one per MB of the memory budget, at least twice the
//     connection manager's high water and at least 800; Transient a quarter
//     of System). Streams ride on admitted connections, and a stream budget
//     smaller than the connections it serves is what host-01 hit on
//     2026-09-25: go-libp2p's default ~2,048 inbound streams against ~2,200
//     connections, 425,350 streams refused in 45 hours, Sandcastle module
//     grants among them.
//   - Service, protocol, connection and stream scopes are unlimited; each PEER
//     is bounded (inbound connections and streams) to contain a buggy peer.
//   - The allowlisted scopes are unlimited, for rcmgr.WithAllowlistedMultiaddrs.
//
// The SDN raises are floors on top: the per-peer browser budget
// (applyInboundAdmissionLimits), the FlatSQL sync and module-delivery budgets,
// and the measured connection floors — kubo's "a quarter of System" would
// give a 2 GB host 256 transient connections, below the 160-slot level that
// wedged production (libp2p_inbound_admission.go).
func kuboResourceLimits(maxMemory uint64, maxFD int, connMgrHighWater int) rcmgr.ConcreteLimitConfig {
	maxMemoryMB := maxMemory / (1024 * 1024)
	systemConnsInbound := int(maxMemoryMB)
	infinite := rcmgr.InfiniteLimits.ToPartialLimitConfig().System

	partial := rcmgr.PartialLimitConfig{
		System: rcmgr.ResourceLimits{
			Memory:          rcmgr.LimitVal64(maxMemory),
			FD:              rcmgr.LimitVal(maxFD),
			Conns:           rcmgr.Unlimited,
			ConnsInbound:    rcmgr.LimitVal(systemConnsInbound),
			ConnsOutbound:   rcmgr.Unlimited,
			Streams:         rcmgr.Unlimited,
			StreamsOutbound: rcmgr.Unlimited,
			StreamsInbound:  rcmgr.Unlimited,
		},
		Transient: rcmgr.ResourceLimits{
			Memory:          rcmgr.LimitVal64(maxMemory / 4),
			FD:              rcmgr.LimitVal(maxFD / 4),
			Conns:           rcmgr.Unlimited,
			ConnsInbound:    rcmgr.LimitVal(systemConnsInbound / 4),
			ConnsOutbound:   rcmgr.Unlimited,
			Streams:         rcmgr.Unlimited,
			StreamsInbound:  rcmgr.Unlimited,
			StreamsOutbound: rcmgr.Unlimited,
		},
		AllowlistedSystem:    infinite,
		AllowlistedTransient: infinite,
		ServiceDefault:       infinite,
		ServicePeerDefault:   infinite,
		ProtocolDefault:      infinite,
		ProtocolPeerDefault:  infinite,
		Conn:                 infinite,
		Stream:               infinite,
		PeerDefault: rcmgr.ResourceLimits{
			Memory:          rcmgr.Unlimited64,
			FD:              rcmgr.Unlimited,
			Conns:           rcmgr.Unlimited,
			ConnsInbound:    rcmgr.DefaultLimit,
			ConnsOutbound:   rcmgr.Unlimited,
			Streams:         rcmgr.Unlimited,
			StreamsInbound:  rcmgr.DefaultLimit,
			StreamsOutbound: rcmgr.Unlimited,
		},
	}

	scaling := rcmgr.DefaultLimits
	libp2p.SetDefaultServiceLimits(&scaling)
	applyFlatSQLSyncResourceLimits(&scaling)
	applyModuleDeliveryResourceLimits(&scaling)
	applyInboundAdmissionLimits(&scaling)

	// As kubo: every DefaultLimit above takes the scaled value, and every
	// scaled entry the partial config does not name (libp2p's service limits,
	// the SDN protocol-peer budgets) is kept.
	partial = partial.Build(scaling.Scale(int64(maxMemory), maxFD)).ToPartialLimitConfig()

	// kubo's connection-manager consistency check (kubo issue 9545).
	maxInbound := int64(partial.System.ConnsInbound)
	if hw := int64(connMgrHighWater) * 2; maxInbound < hw {
		maxInbound = hw
	}
	if maxInbound < kuboMinInboundConns {
		maxInbound = kuboMinInboundConns
	}
	if maxInbound < inboundAdmissionSystemConns {
		maxInbound = inboundAdmissionSystemConns
	}
	partial.System.ConnsInbound = rcmgr.LimitVal(maxInbound)
	if int(partial.Transient.ConnsInbound) < inboundAdmissionTransientConns {
		partial.Transient.ConnsInbound = rcmgr.LimitVal(inboundAdmissionTransientConns)
	}

	return partial.Build(rcmgr.ConcreteLimitConfig{})
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
