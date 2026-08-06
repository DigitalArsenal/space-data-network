package node

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	"github.com/libp2p/go-libp2p/core/connmgr"
	"github.com/libp2p/go-libp2p/core/event"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"
	coreprotocol "github.com/libp2p/go-libp2p/core/protocol"

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

	// admissionSDNPeerTagValue is large enough that any SDN peer outranks every
	// untagged peer, and small enough to stay well inside the int arithmetic
	// BasicConnMgr does over the sum of a peer's tags.
	admissionSDNPeerTagValue = 100

	defaultAdmissionCeiling          = 1000
	defaultAdmissionReservedHeadroom = 128
	defaultAdmissionGracePeriod      = 30 * time.Second
	defaultAdmissionSilencePeriod    = 10 * time.Second

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

// libp2pCommonsPrefixes are the protocols this node serves because it is a
// libp2p/kubo node, not because it is an SDN node. A peer advertising only
// these is exactly the churn this policy is about — including, critically,
// /ipfs/kad/1.0.0, which every public DHT crawler speaks and which this node
// also serves.
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
			"registry trust >= %s; peers advertising an SDN protocol are tagged +%d so anonymous churn is trimmed first",
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
}

// peerAdmissionController applies the resolved policy to a live host.
//
// It touches go-libp2p only through the core connmgr.ConnManager interface, so
// it is exercisable in tests with a recording fake and no swarm.
type peerAdmissionController struct {
	policy   admissionPolicy
	connMgr  connmgr.ConnManager
	registry *peers.Registry

	protectedPeers  atomic.Int64
	sdnTaggedPeers  atomic.Int64
	identifiedPeers atomic.Uint64
	anonymousPeers  atomic.Uint64
}

func newPeerAdmissionController(policy admissionPolicy, cm connmgr.ConnManager, registry *peers.Registry) *peerAdmissionController {
	return &peerAdmissionController{policy: policy, connMgr: cm, registry: registry}
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

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case raw, ok := <-sub.Out():
			if !ok {
				return nil
			}
			evt, ok := raw.(event.EvtPeerIdentificationCompleted)
			if !ok {
				continue
			}
			c.observeIdentified(evt.Peer, evt.Protocols, servedProtocolSet(h))
		}
	}
}

// observeIdentified applies the reputation decision for one identified peer.
// Split out from Run so the decision is testable without an event bus.
func (c *peerAdmissionController) observeIdentified(id peer.ID, advertised []coreprotocol.ID, served map[coreprotocol.ID]struct{}) {
	if c == nil || !c.policy.Enabled || c.connMgr == nil || id == "" {
		return
	}
	c.identifiedPeers.Add(1)

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
	}
}
