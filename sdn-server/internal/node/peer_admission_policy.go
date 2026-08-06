package node

import (
	"context"
	"fmt"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/libp2p/go-libp2p/core/connmgr"
	"github.com/libp2p/go-libp2p/core/event"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"
	coreprotocol "github.com/libp2p/go-libp2p/core/protocol"
	"github.com/multiformats/go-multiaddr"

	"github.com/spacedatanetwork/sdn-server/internal/config"
	"github.com/spacedatanetwork/sdn-server/internal/peers"
)

// PEER ADMISSION POLICY
//
// THE MEASUREMENT (host-01, task sdn-inbound-junk-flood-policy). After the
// accept-wedge fix raised the inbound ceilings, the node did not recover — it
// sat PINNED at the ceiling: ~1150 inbound connections on :4004 from ~1095
// DISTINCT IPs, refilling any headroom in ~65 minutes. Re-measured twice
// since, most recently 2026-08-06 at ~40 minutes uptime: 1276 established on
// :4004 from 1198 distinct IPs, 1460 process-wide, 1462 open FDs. Unchanged,
// still active, and still climbing.
//
// THE NUMBER THAT DECIDES THE POLICY is 1276/1198 ~= 1.07 connections per
// distinct IP (busiest single IP: 7). This is NOT few-IPs-many-connections
// abuse. A per-IP inbound connection cap — the natural first reach, and the
// first option this task listed — would free approximately NOTHING at any cap
// >= 2, because almost every distinct IP already holds exactly one connection.
// Sampled remote ranges were diverse consumer/residential /8s, which is the
// signature of organic public-IPFS/libp2p DHT churn. This node is a kubo fork
// on the public bootstrap and DHT: that traffic is legitimate, it is simply
// not ours.
//
// AND IT IS NOT THE RESOURCE MANAGER'S DOING. The rcmgr ceilings raised by the
// accept-wedge fix (Transient.ConnsInbound 1024, System.ConnsInbound 2048) are
// NOT the binding constraint: host-01 logged ZERO "INBOUND ADMISSION REFUSED"
// lines in the 24h around the measurement — the last twelve in the entire
// retained journal are from 2026-07-31, a previous invocation. The node is not
// being refused at admission. It is holding 1276 connections against a
// configured max_connections of 1000, which is a connection-MANAGER failure,
// and raising the ceiling a third time would have changed nothing at all.
//
// So the policy is NOT "reject at accept". Identity is unknown at
// InterceptAccept and protocol is unknown until Identify completes, so an
// accept-time filter can only discriminate by IP — the one axis the
// measurement rules out. Raising the ceiling again is a treadmill (Hephaestus
// ruled that out explicitly). What is left, and what actually matters, is
// WHICH connections the node KEEPS when the pool is full.
//
// THE ROOT CAUSE OF "NO HEADROOM" turned out to be in our own wiring, not in
// the flood. The node built its connection manager as:
//
//	connmgr.NewConnManager(1000, n.config.Network.MaxConns)
//
// a hard-coded low water against a configured high water, and then:
//
//   - never called Protect() on ANY peer, so a trim could evict host-02, a
//     browser mid-session or the module-publish lane as readily as a crawler;
//   - never called TagPeer()/UpsertTag() on ANY peer, so every peer had value
//     0 and go-libp2p's value-ordered trim (SortByValueAndStreams) degenerated
//     to a near-arbitrary choice;
//   - on host-01's config (max_connections: 1000) produced low == high == 1000:
//     no band at all, and a trim that bails outright unless it can find 1000
//     non-protected, non-grace-period candidates. That is precisely the
//     observed "pinned at the ceiling" state;
//   - on a node configured BELOW 1000 produced low > high — celestrak.eth runs
//     max_connections: 64, i.e. NewConnManager(1000, 64). go-libp2p does not
//     validate that, and getConnsToClose() returns early whenever
//     connCount <= lowWater, so on that node the connection manager has never
//     trimmed a single connection in its life. Measured 2026-08-06: 443
//     established connections from 411 distinct IPs against a configured
//     ceiling of 64. Seven times its own limit, silently.
//
// WHAT THIS FILE DOES, using go-libp2p's own machinery and inventing none of
// it:
//
//  1. A real trim BAND below the ceiling: low_water < high_water <
//     max_connections, with reserved_headroom slots kept free so a pinned peer
//     always has somewhere to land while the generic pool is saturated.
//  2. PROTECTION (connmgr.Protect) for the set that must never be gated out:
//     configured trusted peers, operator/config pins, configured bootstrap
//     peers (the fleet — host-02, the vm), and registry peers at or above the
//     configured trust level, kept live through OnTrustChange.
//  3. REPUTATION (connmgr.UpsertTag) from Identify: a peer that advertises an
//     SDN protocol gets a positive tag value, so go-libp2p's own trim ordering
//     evicts anonymous churn first, by construction.
//
// WHAT THIS FILE DELIBERATELY DOES NOT DO: it does not add a bespoke
// "unidentified connection reaper". That was on the table, and it is
// redundant — BasicConnMgr.getConnsToClose already skips protected peers,
// already skips connections inside the grace period, already sorts ascending
// by tag value, and already prefers stream-less and INBOUND connections as
// tie-breaks. "Prove you speak an SDN protocol within N seconds or be pruned"
// is fully expressed by (grace_period, SDN tag value): the grace period IS the
// N seconds, and the tag value IS the proof. Writing a second, parallel
// pruning loop over network.Conns would duplicate the library, and it would do
// so on the exact code path whose last bespoke addition took every connection
// on host-01 down for 41 minutes (see the InterceptUpgraded scar in
// internal/peers/gater.go). Everything here is either construction-time or
// event-bus driven; NOTHING runs on the connection critical path.

const (
	// Protection tags. Distinct names on purpose: Unprotect removes ONE tag and
	// reports whether the peer is still protected by another, so a peer that is
	// both config-trusted and registry-trusted survives a trust demotion
	// without losing its config protection.
	admissionTagConfigTrusted = "sdn-config-trusted"
	admissionTagPinned        = "sdn-pinned"
	admissionTagBootstrap     = "sdn-bootstrap"
	admissionTagRegistryTrust = "sdn-registry-trust"

	// admissionTagSDNPeer is a VALUE tag, not a protection tag: it feeds
	// BasicConnMgr's ascending value sort so anonymous churn is trimmed first.
	// A peer holding only this tag is still trimmable under enough pressure —
	// that is intended graceful degradation. Peers that must never be trimmed
	// are protected instead.
	admissionTagSDNPeer = "sdn-peer"

	// admissionTagSDNTopic is the BROWSER signal, and it exists because the
	// protocol-prefix test alone got the browsers wrong (see the OUTAGE note
	// at the top of this file). A browser running sdn-js registers NO stream
	// handlers at all: its entire relationship with this node is gossipsub on
	// SDN topics, and /meshsub/ is — correctly, for a DHT crawler — classed as
	// commons churn. Topic MEMBERSHIP is the honest discriminator: a peer
	// subscribed to one of THIS node's SDN topics is consuming the SDN data
	// path, whatever protocol prefixes it happens to advertise.
	admissionTagSDNTopic = "sdn-topic-member"

	// admissionTagTunnelled marks a peer that reached us through the node's
	// OWN loopback websocket tunnel — the :443 root-path upgrade proxy in
	// cmd/spacedatanetwork (resolveLocalLibp2pWsProxyTarget). Those connections
	// are not public swarm churn by construction: this process proxied them in
	// itself, from a browser on our own admin listener. Trimming them is
	// trimming our own users.
	admissionTagTunnelled = "sdn-tunnelled"

	// admissionSDNPeerTagValue is large enough that any SDN peer outranks every
	// untagged peer, and small enough to stay well inside the int arithmetic
	// BasicConnMgr does over the sum of a peer's tags.
	admissionSDNPeerTagValue = 100

	defaultAdmissionCeiling          = 1000
	defaultAdmissionReservedHeadroom = 128
	defaultAdmissionGracePeriod      = 30 * time.Second
	defaultAdmissionSilencePeriod    = 10 * time.Second

	// defaultAdmissionTopicSweep is how often topic membership is re-read.
	// Well inside the 30s grace period, so a browser that joins its topics a
	// second after connecting is tagged long before it is ever trimmable, and
	// far cheaper than the connection critical path this file refuses to touch
	// (it is a pubsub.Topic.ListPeers() read on a timer).
	defaultAdmissionTopicSweep = 10 * time.Second

	// admissionLowWaterNumerator/Denominator derive the low water as a fraction
	// of the high water when the operator did not set one. 3/4 leaves a band
	// wide enough that a trim buys real time instead of re-firing on the next
	// silence-period tick.
	admissionLowWaterNumerator   = 3
	admissionLowWaterDenominator = 4

	// minAdmissionBand keeps a derived low water from collapsing onto the high
	// water on very small ceilings.
	minAdmissionBand = 4
)

// sdnProtocolPrefixes are the protocol-ID namespaces that identify a peer as
// ours. All three are live: /spacedatanetwork/ (sds-exchange, epm-exchange),
// /space-data-network/ (flatsql-sync, id-exchange, chat) and /sdn/ (module and
// update lanes).
var sdnProtocolPrefixes = []string{
	"/spacedatanetwork/",
	"/space-data-network/",
	"/sdn/",
}

// THE OUTAGE THIS FILE CAUSED, AND THE TWO SIGNALS THAT CLOSE IT
// (task sdn-ws-upgrade-regression-82cdbf50, 2026-08-06, owner-reported:
// "SpaceAware.io and the beta currently aren't receiving data").
//
// The policy above is right about the churn and did exactly what it says: on
// host-01 it took the node from "never trims" (low == high == 1000, so
// getConnsToClose bailed outright) to a live 654..872 band against ~1276 held
// connections, and trimmed ~600 of them. Measured effect at 22:08Z: :4004 fell
// from 1276 to 804 established — and :443, the browser websocket tunnel, fell
// to FOUR.
//
// The browsers were in the trimmed population because NOTHING here could tell
// them apart from a DHT crawler:
//
//   - an sdn-js browser node registers no stream handlers whatsoever (verified
//     against sdn-js/src: the only `/spacedatanetwork/...` string in it is a
//     pubsub TOPIC name, and every .handle() call is in a test), so Identify
//     reports /ipfs/id/1.0.0, /ipfs/ping/1.0.0 and /meshsub/1.1.0 — all three
//     in libp2pCommonsPrefixes below, by design. sdnPeerTagValue therefore
//     returned 0: value 0, unprotected, INBOUND, few streams, i.e. the top of
//     BasicConnMgr's kill list on every tie-break it applies;
//   - and the boot log said it plainly: "Peer admission: 1 peers protected
//     from trimming". One.
//
// So the fix is not to weaken the trim — the trim is correct and stays. It is
// to give the policy the two facts it was missing, both of which are
// observable without touching the connection path:
//
//  1. TOPIC MEMBERSHIP (admissionTagSDNTopic). A peer subscribed to one of this
//     node's own SDN pubsub topics is consuming the SDN data path. That is what
//     a browser IS to us, and it is not something a crawler does by accident.
//  2. LOOPBACK TUNNEL PROVENANCE (admissionTagTunnelled). A connection whose
//     remote address is loopback did not come off the public swarm: this
//     process proxied it in itself, from the :443 root-path websocket upgrade
//     interceptor. Public churn cannot forge that — it arrives on :4004 from a
//     public IP.
//
// Both are VALUE tags, not protection: browsers still degrade gracefully under
// genuine pressure. They simply stop being the FIRST thing evicted.

// libp2pCommonsPrefixes are the protocols this node serves because it is a
// libp2p/kubo node, not because it is an SDN node. A peer advertising only
// these is exactly the churn this policy is about — including, critically,
// /ipfs/kad/1.0.0, which every public DHT crawler speaks and which this node
// also serves.
//
// NOTE /meshsub/ and /floodsub/: pubsub is commons at the PROTOCOL level and
// must stay here (every gossipsub-speaking crawler advertises it), which is
// exactly why browser recognition is done by TOPIC MEMBERSHIP instead — see
// admissionTagSDNTopic.
var libp2pCommonsPrefixes = []string{
	"/ipfs/",
	"/libp2p/",
	"/p2p/",
	"/meshsub/",
	"/floodsub/",
	"/x/",
}

// admissionPolicy is the RESOLVED policy: every value already clamped,
// validated and safe to hand to go-libp2p. Produced by resolveAdmissionPolicy,
// which is a pure function so the arithmetic is unit-testable without a host.
type admissionPolicy struct {
	Enabled bool

	// Ceiling is the operator-declared connection ceiling
	// (network.max_connections). HighWater is kept strictly below it.
	Ceiling   int
	HighWater int
	LowWater  int

	// Headroom is the number of slots actually reserved below the ceiling
	// (Ceiling - HighWater), i.e. what the policy DELIVERS rather than what was
	// requested.
	Headroom int

	GracePeriod   time.Duration
	SilencePeriod time.Duration

	// ProtectTrustLevel is the registry trust level at and above which a peer is
	// protected from trimming.
	ProtectTrustLevel peers.TrustLevel

	// Notes records every clamp, correction and fallback applied while
	// resolving, so the boot log can say WHY the active numbers differ from the
	// configured ones instead of silently substituting them.
	Notes []string
}

// resolveAdmissionPolicy turns network config into a policy whose invariants
// go-libp2p does not check for itself: 0 < LowWater <= HighWater <= Ceiling.
//
// Pure by design — no host, no clock, no I/O.
func resolveAdmissionPolicy(cfg config.NetworkConfig) admissionPolicy {
	ac := cfg.Admission
	p := admissionPolicy{
		Enabled:           !ac.Disabled,
		Ceiling:           cfg.MaxConns,
		GracePeriod:       defaultAdmissionGracePeriod,
		SilencePeriod:     defaultAdmissionSilencePeriod,
		ProtectTrustLevel: peers.Trusted,
	}

	if p.Ceiling <= 0 {
		p.Notef("network.max_connections %d is not usable; using %d", cfg.MaxConns, defaultAdmissionCeiling)
		p.Ceiling = defaultAdmissionCeiling
	}

	// Disabled is a genuine escape hatch: no headroom, no protection, no
	// tagging. It is NOT a return to the pre-policy wiring, because that wiring
	// was a low>high inversion that disabled trimming outright — reproducing a
	// bug is not a supported configuration.
	if !p.Enabled {
		p.HighWater = p.Ceiling
		p.LowWater = p.Ceiling
		p.Headroom = 0
		return p
	}

	reserved := ac.ReservedHeadroom
	switch {
	case reserved < 0:
		p.Notef("admission.reserved_headroom %d is negative; using 0", ac.ReservedHeadroom)
		reserved = 0
	case reserved == 0 && ac.HighWater <= 0:
		reserved = defaultAdmissionReservedHeadroom
	}
	// Never let headroom eat more than a quarter of the ceiling: on a node
	// configured at max_connections: 64 the 128 default would otherwise take
	// the high water negative.
	if maxReserved := p.Ceiling / 4; reserved > maxReserved {
		if ac.ReservedHeadroom > 0 {
			p.Notef("admission.reserved_headroom %d exceeds a quarter of max_connections %d; using %d",
				ac.ReservedHeadroom, p.Ceiling, maxReserved)
		}
		reserved = maxReserved
	}

	switch {
	case ac.HighWater > 0:
		p.HighWater = ac.HighWater
		if p.HighWater > p.Ceiling {
			p.Notef("admission.high_water %d is above max_connections %d; clamped to %d",
				ac.HighWater, p.Ceiling, p.Ceiling)
			p.HighWater = p.Ceiling
		}
	default:
		p.HighWater = p.Ceiling - reserved
	}
	if p.HighWater < 1 {
		p.Notef("derived high_water %d is unusable; using 1", p.HighWater)
		p.HighWater = 1
	}
	p.Headroom = p.Ceiling - p.HighWater

	switch {
	case ac.LowWater > 0:
		p.LowWater = ac.LowWater
		if p.LowWater > p.HighWater {
			p.Notef("admission.low_water %d is above high_water %d; clamped to %d",
				ac.LowWater, p.HighWater, p.HighWater)
			p.LowWater = p.HighWater
		}
	default:
		p.LowWater = p.HighWater * admissionLowWaterNumerator / admissionLowWaterDenominator
		// Keep a real band on small ceilings, where integer division can land
		// the derived low water on top of the high water.
		if p.HighWater > minAdmissionBand && p.HighWater-p.LowWater < 1 {
			p.LowWater = p.HighWater - 1
		}
	}
	if p.LowWater < 1 {
		p.LowWater = 1
	}

	p.GracePeriod = resolveAdmissionDuration(&p, "grace_period", ac.GracePeriod, defaultAdmissionGracePeriod)
	p.SilencePeriod = resolveAdmissionDuration(&p, "silence_period", ac.SilencePeriod, defaultAdmissionSilencePeriod)
	// connmgr.WithSilencePeriod rejects a non-positive period outright, which
	// would fail host construction — a config typo must not stop the node from
	// booting.
	if p.SilencePeriod <= 0 {
		p.Notef("admission.silence_period must be positive; using %s", defaultAdmissionSilencePeriod)
		p.SilencePeriod = defaultAdmissionSilencePeriod
	}

	if lvl := strings.TrimSpace(ac.ProtectTrustLevel); lvl != "" {
		parsed, err := peers.ParseTrustLevel(lvl)
		if err != nil {
			p.Notef("admission.protect_trust_level %q is not a trust level (%v); using %s",
				ac.ProtectTrustLevel, err, peers.Trusted.String())
		} else {
			p.ProtectTrustLevel = parsed
		}
	}

	return p
}

// Notef appends a resolution note. Notes exist so the boot log can explain a
// substituted value rather than quietly running a number the operator never
// wrote.
func (p *admissionPolicy) Notef(format string, args ...any) {
	p.Notes = append(p.Notes, fmt.Sprintf(format, args...))
}

func resolveAdmissionDuration(p *admissionPolicy, name, raw string, fallback time.Duration) time.Duration {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return fallback
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		p.Notef("admission.%s %q is not a duration (%v); using %s", name, raw, err, fallback)
		return fallback
	}
	if d < 0 {
		p.Notef("admission.%s %s is negative; using %s", name, d, fallback)
		return fallback
	}
	return d
}

// Summary is the boot log line. It states the ACTIVE policy — not the
// configured one — because the whole point of the Notes is that they can
// differ.
func (p admissionPolicy) Summary() string {
	if !p.Enabled {
		return fmt.Sprintf(
			"PEER ADMISSION POLICY DISABLED (network.admission.disabled): connection manager band %d..%d, "+
				"no reserved headroom, pinned/trusted peers are NOT protected from trimming, "+
				"SDN peers are NOT prioritised. Anonymous inbound churn competes with the fleet for every slot.",
			p.LowWater, p.HighWater)
	}
	s := fmt.Sprintf(
		"PEER ADMISSION POLICY: ceiling=%d (network.max_connections), trim band %d..%d, headroom=%d slots reserved "+
			"below the ceiling for pinned peers; grace=%s (an inbound peer's window to prove it speaks an SDN "+
			"protocol), trim check every %s; protected from trimming: config trusted peers + pins + bootstrap peers + "+
			"registry trust >= %s; tagged +%d so anonymous churn is trimmed first: peers advertising an SDN "+
			"protocol, peers subscribed to one of this node's SDN pubsub topics (browsers), and peers arriving "+
			"through the local websocket upgrade tunnel",
		p.Ceiling, p.LowWater, p.HighWater, p.Headroom,
		p.GracePeriod, p.SilencePeriod, p.ProtectTrustLevel.String(), admissionSDNPeerTagValue)
	if len(p.Notes) > 0 {
		s += " | ADJUSTED: " + strings.Join(p.Notes, "; ")
	}
	return s
}

// isSDNProtocol reports whether a protocol ID advertised by a peer marks that
// peer as one of ours.
//
// `served` is this host's own registered stream handlers. Including it is what
// makes module-registered protocol IDs count: a module may register any
// protocol ID it likes, and a peer speaking a protocol we chose to serve is by
// definition relevant to us — EXCEPT for the libp2p/IPFS commons, which we
// serve only because this is a kubo fork and which every DHT crawler on the
// public swarm also speaks.
func isSDNProtocol(id coreprotocol.ID, served map[coreprotocol.ID]struct{}) bool {
	s := string(id)
	if s == "" {
		return false
	}
	for _, prefix := range sdnProtocolPrefixes {
		if strings.HasPrefix(s, prefix) {
			return true
		}
	}
	if _, ok := served[id]; !ok {
		return false
	}
	for _, prefix := range libp2pCommonsPrefixes {
		if strings.HasPrefix(s, prefix) {
			return false
		}
	}
	return true
}

// sdnPeerTagValue is the connmgr tag value for a peer that advertised
// `advertised` during Identify, given the protocols this host serves. Pure, so
// the reputation decision is unit-testable without a swarm.
func sdnPeerTagValue(advertised []coreprotocol.ID, served map[coreprotocol.ID]struct{}) int {
	for _, id := range advertised {
		if isSDNProtocol(id, served) {
			return admissionSDNPeerTagValue
		}
	}
	return 0
}

// isTunnelledPeerAddr reports whether a connection's REMOTE multiaddr is
// loopback, i.e. the peer reached this node through something on this box
// rather than off the public swarm.
//
// On an SDN node the only producer of such connections is the node's own
// admin-listener websocket tunnel: cmd/spacedatanetwork terminates TLS on :443,
// intercepts a root-path `Connection: Upgrade` and reverse-proxies it to the
// local libp2p /ws listener (resolveLocalLibp2pWsProxyTarget). Every browser on
// https://sdn.spaceaware.io/ arrives this way, and arrives looking like
// 127.0.0.1. Public churn cannot present a loopback remote address.
//
// Pure over multiaddrs so the provenance decision is testable without a swarm.
func isTunnelledPeerAddr(addr multiaddr.Multiaddr) bool {
	if addr == nil {
		return false
	}
	for _, code := range []int{multiaddr.P_IP4, multiaddr.P_IP6} {
		value, err := addr.ValueForProtocol(code)
		if err != nil {
			continue
		}
		ip := net.ParseIP(strings.TrimSpace(value))
		if ip != nil && ip.IsLoopback() {
			return true
		}
	}
	return false
}

// anyTunnelledAddr reports whether ANY of a peer's live connections came in
// through the loopback tunnel. Any is the right quantifier: a browser that also
// happens to hold a public connection is still a browser.
func anyTunnelledAddr(remoteAddrs []multiaddr.Multiaddr) bool {
	for _, addr := range remoteAddrs {
		if isTunnelledPeerAddr(addr) {
			return true
		}
	}
	return false
}

// PeerAdmissionStats is the observable state of the policy. Exposed for the
// same reason InboundAdmission is: "is this node keeping the right peers?"
// must be answerable from the node, not inferred from `ss` on the host.
type PeerAdmissionStats struct {
	Enabled            bool    `json:"enabled"`
	Ceiling            int     `json:"ceiling"`
	HighWater          int     `json:"high_water"`
	LowWater           int     `json:"low_water"`
	ReservedHeadroom   int     `json:"reserved_headroom"`
	GracePeriodSeconds float64 `json:"grace_period_seconds"`
	ProtectTrustLevel  string  `json:"protect_trust_level"`
	// ProtectedPeers is the number of peers currently protected from trimming.
	ProtectedPeers int64 `json:"protected_peers"`
	// SDNTaggedPeers is the number of peers that have proved an SDN protocol.
	SDNTaggedPeers int64 `json:"sdn_tagged_peers"`
	// IdentifiedPeers is how many Identify completions the policy has seen.
	IdentifiedPeers uint64 `json:"identified_peers"`
	// AnonymousPeers is how many identified peers advertised no SDN protocol —
	// the size of the churn population, measured rather than estimated.
	AnonymousPeers uint64 `json:"anonymous_peers"`
	// TopicMemberPeers is how many peers are currently tagged because they are
	// subscribed to one of this node's SDN pubsub topics. This is the BROWSER
	// count: it is the number the 2026-08-06 outage would have shown collapsing.
	TopicMemberPeers int64 `json:"topic_member_peers"`
	// TunnelledPeers is how many peers are currently tagged because they
	// arrived through the node's own loopback websocket tunnel (:443 root-path
	// upgrade proxy).
	TunnelledPeers int64 `json:"tunnelled_peers"`
}

// peerAdmissionController applies the resolved policy to a live host.
//
// It touches go-libp2p only through the core connmgr.ConnManager interface, so
// it is exercisable in tests with a recording fake and no swarm.
type peerAdmissionController struct {
	policy   admissionPolicy
	connMgr  connmgr.ConnManager
	registry *peers.Registry

	// topicMembers reports the peers currently subscribed to one of THIS
	// node's SDN pubsub topics. Injected (rather than reaching into the node)
	// so the browser signal is testable with a plain closure and no gossipsub.
	// nil disables the signal.
	topicMembers func() []peer.ID
	// topicSweep is the membership re-read interval; zero means the default.
	topicSweep time.Duration

	protectedPeers   atomic.Int64
	sdnTaggedPeers   atomic.Int64
	identifiedPeers  atomic.Uint64
	anonymousPeers   atomic.Uint64
	topicMemberPeers atomic.Int64
	tunnelledPeers   atomic.Int64

	taggedTopicMu sync.Mutex
	taggedTopic   map[peer.ID]struct{}
}

func newPeerAdmissionController(policy admissionPolicy, cm connmgr.ConnManager, registry *peers.Registry) *peerAdmissionController {
	return &peerAdmissionController{
		policy:      policy,
		connMgr:     cm,
		registry:    registry,
		taggedTopic: make(map[peer.ID]struct{}),
	}
}

// SetTopicMembers installs the SDN pubsub topic-membership source (see
// admissionTagSDNTopic). Called once at wiring time, before Run.
func (c *peerAdmissionController) SetTopicMembers(fn func() []peer.ID) {
	if c == nil {
		return
	}
	c.topicMembers = fn
}

// protect marks a peer as never-trimmable under `tag`. Idempotent: connmgr
// stores tags in a set, and the counter only moves on a genuine first
// protection so the stat stays a peer count rather than a call count.
func (c *peerAdmissionController) protect(id peer.ID, tag string) {
	if c == nil || c.connMgr == nil || !c.policy.Enabled || id == "" {
		return
	}
	if c.connMgr.IsProtected(id, "") {
		c.connMgr.Protect(id, tag)
		return
	}
	c.connMgr.Protect(id, tag)
	c.protectedPeers.Add(1)
}

func (c *peerAdmissionController) unprotect(id peer.ID, tag string) {
	if c == nil || c.connMgr == nil || !c.policy.Enabled || id == "" {
		return
	}
	if stillProtected := c.connMgr.Unprotect(id, tag); !stillProtected {
		if c.connMgr.IsProtected(id, "") {
			return
		}
		if c.protectedPeers.Load() > 0 {
			c.protectedPeers.Add(-1)
		}
	}
}

// ProtectFleet protects the set that must NEVER be gated out, from the three
// sources that can each independently declare a peer essential.
//
// Order matters only for the log line; a peer named by several sources simply
// carries several protection tags, and losing one (a trust demotion) leaves the
// others standing.
func (c *peerAdmissionController) ProtectFleet(bootstrapAddrs []string, trustedPeers []string, pins []peers.Pin) {
	if c == nil || !c.policy.Enabled {
		return
	}

	for _, addr := range bootstrapAddrs {
		if info, err := peer.AddrInfoFromString(strings.TrimSpace(addr)); err == nil {
			c.protect(info.ID, admissionTagBootstrap)
		}
	}
	for _, addr := range trustedPeers {
		if info, err := peer.AddrInfoFromString(strings.TrimSpace(addr)); err == nil {
			c.protect(info.ID, admissionTagConfigTrusted)
		}
	}
	for _, pin := range pins {
		if id, err := peer.Decode(strings.TrimSpace(pin.PeerID)); err == nil {
			c.protect(id, admissionTagPinned)
		}
	}

	// Registry peers that already sit at or above the protected trust level —
	// operator promotions from a previous run, restored from persistence before
	// the host existed. OnTrustChange only reports FUTURE changes.
	if c.registry != nil {
		for _, tp := range c.registry.ListPeers() {
			if tp == nil {
				continue
			}
			if c.registry.EffectiveTrustLevel(tp.ID) >= c.policy.ProtectTrustLevel {
				c.protect(tp.ID, admissionTagRegistryTrust)
			}
		}
	}
}

// HandleTrustChange keeps protection in step with the registry. Registered via
// Registry.OnTrustChange, whose dispatch is asynchronous — this never runs on a
// connection path.
func (c *peerAdmissionController) HandleTrustChange(id peer.ID, _, newLevel peers.TrustLevel) {
	if c == nil || !c.policy.Enabled {
		return
	}
	if newLevel >= c.policy.ProtectTrustLevel {
		c.protect(id, admissionTagRegistryTrust)
		return
	}
	c.unprotect(id, admissionTagRegistryTrust)
}

// Run tags peers that prove an SDN protocol, driven by the identify event bus.
//
// The event bus is the correct surface here: Identify is the ONLY point at
// which a peer's protocols become known, and subscribing costs nothing on the
// connection critical path. Polling network.Conns() would be both slower to
// react and a second implementation of something the library already publishes.
//
// Returns once ctx is cancelled or the subscription closes.
func (c *peerAdmissionController) Run(ctx context.Context, h host.Host) error {
	if c == nil || !c.policy.Enabled || h == nil {
		return nil
	}
	sub, err := h.EventBus().Subscribe(new(event.EvtPeerIdentificationCompleted))
	if err != nil {
		return fmt.Errorf("subscribe to peer identification events: %w", err)
	}
	defer sub.Close()

	sweep := c.topicSweep
	if sweep <= 0 {
		sweep = defaultAdmissionTopicSweep
	}
	ticker := time.NewTicker(sweep)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			c.refreshTopicMembers()
		case raw, ok := <-sub.Out():
			if !ok {
				return nil
			}
			evt, ok := raw.(event.EvtPeerIdentificationCompleted)
			if !ok {
				continue
			}
			c.observeIdentified(evt.Peer, evt.Protocols, servedProtocolSet(h), remoteAddrsForPeer(h, evt.Peer))
		}
	}
}

// remoteAddrsForPeer collects the remote multiaddrs of a peer's live
// connections. Off the connection critical path: this runs on the event bus
// goroutine, after Identify has already completed.
func remoteAddrsForPeer(h host.Host, id peer.ID) []multiaddr.Multiaddr {
	if h == nil || h.Network() == nil || id == "" {
		return nil
	}
	conns := h.Network().ConnsToPeer(id)
	addrs := make([]multiaddr.Multiaddr, 0, len(conns))
	for _, conn := range conns {
		if conn == nil {
			continue
		}
		addrs = append(addrs, conn.RemoteMultiaddr())
	}
	return addrs
}

// refreshTopicMembers re-reads SDN pubsub topic membership and keeps the
// browser tag in step with it: joiners are tagged, leavers untagged.
//
// Untagging matters as much as tagging. A crawler that briefly joins a topic
// and leaves must not keep a permanent value bonus, or the signal becomes a
// way to opt out of the trim.
func (c *peerAdmissionController) refreshTopicMembers() {
	if c == nil || !c.policy.Enabled || c.connMgr == nil || c.topicMembers == nil {
		return
	}

	current := make(map[peer.ID]struct{})
	for _, id := range c.topicMembers() {
		if id == "" {
			continue
		}
		current[id] = struct{}{}
	}

	c.taggedTopicMu.Lock()
	defer c.taggedTopicMu.Unlock()

	for id := range current {
		if _, already := c.taggedTopic[id]; already {
			continue
		}
		c.connMgr.UpsertTag(id, admissionTagSDNTopic, func(int) int { return admissionSDNPeerTagValue })
		c.taggedTopic[id] = struct{}{}
		c.topicMemberPeers.Add(1)
	}
	for id := range c.taggedTopic {
		if _, still := current[id]; still {
			continue
		}
		c.connMgr.UntagPeer(id, admissionTagSDNTopic)
		delete(c.taggedTopic, id)
		if c.topicMemberPeers.Load() > 0 {
			c.topicMemberPeers.Add(-1)
		}
	}
}

// observeIdentified applies the reputation decision for one identified peer.
// Split out from Run so the decision is testable without an event bus.
func (c *peerAdmissionController) observeIdentified(id peer.ID, advertised []coreprotocol.ID, served map[coreprotocol.ID]struct{}, remoteAddrs []multiaddr.Multiaddr) {
	if c == nil || !c.policy.Enabled || c.connMgr == nil || id == "" {
		return
	}
	c.identifiedPeers.Add(1)

	// Loopback provenance is decided FIRST and independently of protocols,
	// because the peer this rescues — a browser on the :443 tunnel — advertises
	// nothing but commons and would otherwise be scored 0 and evicted first.
	if anyTunnelledAddr(remoteAddrs) {
		tunnelFirst := false
		c.connMgr.UpsertTag(id, admissionTagTunnelled, func(existing int) int {
			if existing == 0 {
				tunnelFirst = true
			}
			return admissionSDNPeerTagValue
		})
		if tunnelFirst {
			c.tunnelledPeers.Add(1)
		}
	}

	value := sdnPeerTagValue(advertised, served)
	if value == 0 {
		c.anonymousPeers.Add(1)
		return
	}

	// UpsertTag rather than TagPeer: a peer re-identifying on a second
	// connection must not stack its value, which would let a churning peer
	// out-rank the fleet by reconnecting.
	first := false
	c.connMgr.UpsertTag(id, admissionTagSDNPeer, func(existing int) int {
		if existing == 0 {
			first = true
		}
		return value
	})
	if first {
		c.sdnTaggedPeers.Add(1)
	}
}

func servedProtocolSet(h host.Host) map[coreprotocol.ID]struct{} {
	if h == nil || h.Mux() == nil {
		return nil
	}
	ids := h.Mux().Protocols()
	served := make(map[coreprotocol.ID]struct{}, len(ids))
	for _, id := range ids {
		served[id] = struct{}{}
	}
	return served
}

// Stats returns the observable policy state.
func (c *peerAdmissionController) Stats() PeerAdmissionStats {
	if c == nil {
		return PeerAdmissionStats{}
	}
	return PeerAdmissionStats{
		Enabled:            c.policy.Enabled,
		Ceiling:            c.policy.Ceiling,
		HighWater:          c.policy.HighWater,
		LowWater:           c.policy.LowWater,
		ReservedHeadroom:   c.policy.Headroom,
		GracePeriodSeconds: c.policy.GracePeriod.Seconds(),
		ProtectTrustLevel:  c.policy.ProtectTrustLevel.String(),
		ProtectedPeers:     c.protectedPeers.Load(),
		SDNTaggedPeers:     c.sdnTaggedPeers.Load(),
		IdentifiedPeers:    c.identifiedPeers.Load(),
		AnonymousPeers:     c.anonymousPeers.Load(),
		TopicMemberPeers:   c.topicMemberPeers.Load(),
		TunnelledPeers:     c.tunnelledPeers.Load(),
	}
}
