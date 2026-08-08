package node

import (
	"sync/atomic"
	"time"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/protocol"
	"github.com/libp2p/go-libp2p/p2p/net/upgrader"
)

// INBOUND ADMISSION: OBSERVABILITY AND HEADROOM
//
// This file exists because a total, public, multi-hour inbound outage produced
// ZERO log lines. Diagnosing it cost a SIGQUIT dump of production and then two
// further investigations, and the deciding facts still had to be inferred from
// `ss` on the host. That is the defect being fixed here, as much as the ceiling
// itself.
//
// THE FAILURE, measured on host-01 (2026-07-28..30, task
// sdn-ws-inbound-accept-wedge). go-libp2p's websocket listener hands an accepted
// connection to the libp2p upgrader over an UNBUFFERED channel with no timeout
// (v0.46 p2p/transport/websocket/listener.go ServeHTTP):
//
//	select {
//	case l.incoming <- conn:
//	case <-l.closed:        // <-- the ONLY other case. No deadline.
//	}
//
// By the time it blocks there, that connection has ALREADY reserved an rcmgr
// inbound connection scope (reserved in GatedMaListener.Accept, which the ws
// transport uses as its net.Listener) and has ALREADY been answered HTTP 101. So
// every connection waiting to be drained pins one scarce global inbound slot for
// as long as it waits.
//
// The drain side is deliberately narrow: the upgrader negotiates at most
// upgrader.AcceptQueueLength connections concurrently and gives each up to its
// accept timeout. When wild internet traffic arrives faster than that drains,
// pinned connections accumulate until the inbound ceiling is reached — and from
// that moment rcmgr denies EVERY new inbound connection at accept, BEFORE the
// HTTP request is read. Hence the signature that looked so strange: TCP accepted
// then closed with no response, protocol-blind, instant rather than a timeout,
// and identical on the public listener AND on loopback, because the System and
// Transient scopes are global, not per-listener.
//
// Effective ceilings for host-01 (8 GB / 65536 FD), computed from this package's
// own limit config — see TestPrintEffectiveRcmgrLimits, which exists so these are
// measured rather than remembered (two earlier passes quoted conflicting values):
//
//	System.ConnsInbound    576      Transient.ConnsInbound   160
//	System.Conns          1152      Transient.Conns          320
//	System.FD           65536      Transient.FD           16384
//
// 160 is the number that matters, and it is the wrong number for a publicly
// exposed ingress: the box holds ~250 open FDs against an RLIMIT_NOFILE of
// 65536, so the ceiling is ~0.25% of available descriptors. It also predicts the
// observed relapse arithmetic — wild inbound was measured arriving at ~5
// conns/min, and 160 slots / 5 per min ~= 32 min, against a measured relapse
// half-life of ~25 minutes and ~6-7 minutes of relief after a restart.
//
// WHAT THIS DOES, and what it deliberately does not claim:
//   - It reports admission decisions, so the next occurrence is one journal grep
//     instead of a production dump.
//   - It raises the inbound ceilings to values justified by this host's actual
//     descriptor and memory headroom, so a bounded backlog of pinned handoffs
//     cannot exhaust admission.
//   - It widens the upgrader's concurrent-negotiation window so the backlog
//     drains faster than wild traffic can build it.
//
// It does NOT pretend to remove the upstream hazard: the unbuffered, untimed
// handoff in go-libp2p is still there, and a sufficiently large flood would still
// reach any finite ceiling. That is why the reporting is the primary deliverable
// — the ceiling is sized from evidence, and the node now tells us if it is ever
// wrong again instead of going silently dark.

// inboundAdmissionCeilings are the raised inbound connection ceilings for a
// publicly exposed node.
//
// Sizing rationale, not a guess: transient connections are PRE-handshake, so
// each costs approximately one file descriptor plus a small rcmgr memory
// reservation. host-01 runs ~250 FDs against RLIMIT_NOFILE 65536 and ~900 MB
// against 8 GB. 1024 transient inbound slots is ~1.6% of descriptors and leaves
// the existing Transient.FD budget (16384) untouched as the real backstop, while
// requiring a ~200x increase over the measured wild-inbound rate to reach.
const (
	inboundAdmissionTransientConns = 1024
	inboundAdmissionSystemConns    = 2048

	// inboundAdmissionPeerConns is the number of simultaneous inbound
	// connections ONE remote peer identity may hold.
	//
	// THIS IS THE CEILING THAT BROKE THE LIVE GALLERY. go-libp2p's default is
	// 8, and its own comment explains why: "8 for now so that it matches the
	// number of concurrent dials we may do in swarm_dial.go". That is a sizing
	// argument about how many addresses WE dial when reaching out — it is not
	// an argument about how many connections a legitimate CLIENT may open to
	// us, and it was never revisited for a node whose clients are browsers.
	//
	// Worse, it cannot be outgrown: PeerLimitIncrease declares no ConnsInbound
	// and no Conns field, so rcmgr's scale() computes
	// `8 + (0 * mebibytes)>>10` and the limit stays 8 on a 512 MB VPS and on a
	// 512 GB server alike. Raising System and Transient ceilings — which this
	// file already did — does nothing for it.
	//
	// The measured client: one browser opening the RF gallery derives ONE
	// identity and then issues Promise.all over TEN module fetches, each of
	// which builds its own delivery client and its own connection. The provider
	// therefore sees ten simultaneous inbound connections from a single peer ID
	// against a hard ceiling of eight. Measured against live prod on
	// 2026-08-08, ten such clients lost 9-10 of 10 module-delivery streams to
	// resets carrying StreamErrorCode 0x1002, byte-identical to the error the
	// owner reported; at four clients the same probe was clean.
	//
	// 256 is sized for the real unit of work — a browser TAB — with room for a
	// user opening several, reloading, or sitting behind a shared NAT, while
	// remaining far below the System and Transient ceilings above so a single
	// peer still cannot exhaust the node.
	inboundAdmissionPeerConns = 256

	// upgraderAcceptQueueLength widens how many inbound connections the libp2p
	// upgrader negotiates concurrently (go-libp2p default 16). This is the DRAIN
	// rate for the handoff described above: with 16, a burst of slow or junk
	// connections serialises behind a narrow window and pins slots while it
	// waits. 256 keeps the backlog draining well ahead of the measured arrival
	// rate. It is a package-level `var` in go-libp2p precisely so hosts can tune
	// it; libp2p exposes no Option that plumbs upgrader settings.
	upgraderAcceptQueueLength = 256

	// inboundAdmissionLogInterval throttles the admission report. Denials are
	// summarised rather than logged per-connection: under a flood, per-event
	// logging is itself an outage amplifier.
	inboundAdmissionLogInterval = 30 * time.Second
)

// applyUpgraderAcceptQueueLength widens the upgrader's concurrent-negotiation
// window. MUST run before any listener is created — it is read when a listener's
// threshold is constructed. Called from newFlatSQLSyncResourceManager, which
// libp2p evaluates during host construction, well before Start.
func applyUpgraderAcceptQueueLength() {
	if upgrader.AcceptQueueLength < upgraderAcceptQueueLength {
		upgrader.AcceptQueueLength = upgraderAcceptQueueLength
	}
}

// inboundAdmissionReporter implements rcmgr.MetricsReporter for the single
// question that matters operationally: is this node still admitting inbound
// connections, and if not, since when and how many has it refused?
//
// A blocked INBOUND connection is logged at ERROR, because on a node whose whole
// purpose is to be dialled — by browsers over /p2p/, by host-02, by the module
// publish lane — refusing inbound is an outage, not a statistic. Everything else
// is counted and summarised.
type inboundAdmissionReporter struct {
	allowedInbound  atomic.Uint64
	blockedInbound  atomic.Uint64
	allowedOutbound atomic.Uint64
	blockedOutbound atomic.Uint64

	blockedStreams       atomic.Uint64
	blockedPeers         atomic.Uint64
	blockedProtocols     atomic.Uint64
	blockedProtocolPeers atomic.Uint64

	// firstStreamBlockUnixNano / lastStreamLogUnixNano mirror the connection
	// counters below for STREAM denials, which are a different failure with a
	// different remedy: connections going dark is a total outage, streams being
	// refused is a per-protocol budget that is too small.
	firstStreamBlockUnixNano atomic.Int64
	lastStreamLogUnixNano    atomic.Int64

	// firstBlockUnixNano records when admission first started refusing, so the
	// journal shows the ONSET rather than only the current state. Zero = never.
	firstBlockUnixNano atomic.Int64
	lastLogUnixNano    atomic.Int64
}

func newInboundAdmissionReporter() *inboundAdmissionReporter {
	return &inboundAdmissionReporter{}
}

func (r *inboundAdmissionReporter) AllowConn(dir network.Direction, _ bool) {
	if dir == network.DirInbound {
		r.allowedInbound.Add(1)
		return
	}
	r.allowedOutbound.Add(1)
}

func (r *inboundAdmissionReporter) BlockConn(dir network.Direction, usefd bool) {
	if dir != network.DirInbound {
		r.blockedOutbound.Add(1)
		return
	}
	n := r.blockedInbound.Add(1)
	now := time.Now()
	r.firstBlockUnixNano.CompareAndSwap(0, now.UnixNano())

	// Always report the FIRST refusal immediately — that is the moment the node
	// went dark, and it is the line whose absence made this defect invisible.
	last := r.lastLogUnixNano.Load()
	if n == 1 || now.UnixNano()-last >= int64(inboundAdmissionLogInterval) {
		if r.lastLogUnixNano.CompareAndSwap(last, now.UnixNano()) {
			since := time.Duration(0)
			if first := r.firstBlockUnixNano.Load(); first != 0 {
				since = now.Sub(time.Unix(0, first)).Truncate(time.Second)
			}
			log.Errorf(
				"INBOUND ADMISSION REFUSED by the resource manager: %d inbound connections denied (refusing for %s, usefd=%t). "+
					"The node is accepting TCP and closing it before reading a request, so /p2p/ upgrades, browser dials and "+
					"module publishes will all fail. Inbound allowed=%d, outbound allowed=%d/blocked=%d. "+
					"Ceilings: Transient.ConnsInbound=%d System.ConnsInbound=%d.",
				n, since, usefd,
				r.allowedInbound.Load(), r.allowedOutbound.Load(), r.blockedOutbound.Load(),
				inboundAdmissionTransientConns, inboundAdmissionSystemConns,
			)
		}
	}
}

func (r *inboundAdmissionReporter) AllowStream(_ peer.ID, _ network.Direction) {}

// BlockStream records a stream denied at the system/transient/peer scopes.
//
// A blocked INBOUND stream is logged at ERROR for the same reason a blocked
// connection is: go-libp2p answers it by resetting the stream with
// StreamErrorCode 0x1002, and the client sees a bare "stream reset" that is
// indistinguishable from a network fault. Three separate investigations
// attributed this node's resets to the network before the code was read; the
// node has always known the answer and never said it.
func (r *inboundAdmissionReporter) BlockStream(p peer.ID, dir network.Direction) {
	n := r.blockedStreams.Add(1)
	if dir != network.DirInbound {
		return
	}
	r.logStreamDenial(n, "peer/system scope", string(p))
}

// BlockProtocolPeer records a stream denied at the per-protocol-per-peer scope.
//
// This is the denial that produced the reported defect and it was previously
// dropped on the floor entirely — the method existed only to satisfy the
// interface. It is also the most actionable one, because it names the protocol
// whose budget is too small.
func (r *inboundAdmissionReporter) BlockProtocolPeer(proto protocol.ID, p peer.ID) {
	n := r.blockedProtocolPeers.Add(1)
	r.logStreamDenial(n, "protocol-peer scope for "+string(proto), string(p))
}

// BlockProtocol records a stream denied at the whole-protocol scope.
func (r *inboundAdmissionReporter) BlockProtocol(proto protocol.ID) {
	n := r.blockedProtocols.Add(1)
	r.logStreamDenial(n, "protocol scope for "+string(proto), "")
}

// logStreamDenial emits a throttled, self-explaining denial line. Throttled for
// the same reason connection denials are: under a flood, per-event logging is
// itself an outage amplifier.
func (r *inboundAdmissionReporter) logStreamDenial(count uint64, scope string, remote string) {
	now := time.Now()
	r.firstStreamBlockUnixNano.CompareAndSwap(0, now.UnixNano())
	last := r.lastStreamLogUnixNano.Load()
	if count != 1 && now.UnixNano()-last < int64(inboundAdmissionLogInterval) {
		return
	}
	if !r.lastStreamLogUnixNano.CompareAndSwap(last, now.UnixNano()) {
		return
	}
	since := time.Duration(0)
	if first := r.firstStreamBlockUnixNano.Load(); first != 0 {
		since = now.Sub(time.Unix(0, first)).Truncate(time.Second)
	}
	peerNote := ""
	if remote != "" {
		peerNote = " remote=" + remote
	}
	log.Errorf(
		"INBOUND STREAM REFUSED by the resource manager at the %s%s: %d denied (refusing for %s). "+
			"go-libp2p answers this by RESETTING the stream with code 0x1002 (StreamResourceLimitExceeded), so the "+
			"client sees a bare \"stream reset\" and will blame the network. Raise the limit for this scope — see "+
			"applyModuleDeliveryResourceLimits — or shed the load that is consuming it.",
		scope, peerNote, count, since,
	)
}

func (r *inboundAdmissionReporter) AllowPeer(_ peer.ID) {}

func (r *inboundAdmissionReporter) BlockPeer(_ peer.ID) { r.blockedPeers.Add(1) }

func (r *inboundAdmissionReporter) AllowProtocol(_ protocol.ID) {}

func (r *inboundAdmissionReporter) AllowService(_ string) {}

func (r *inboundAdmissionReporter) BlockService(_ string) {}

func (r *inboundAdmissionReporter) BlockServicePeer(_ string, _ peer.ID) {}

func (r *inboundAdmissionReporter) AllowMemory(_ int) {}

func (r *inboundAdmissionReporter) BlockMemory(_ int) {}

// Snapshot returns the current admission counters. Exposed so an admin/status
// surface can read them without reaching into atomics; the ops monitor
// (ops-sdn-4004-accept-wedge) wants "inbound refused > 0" as its alarm.
type InboundAdmissionStats struct {
	AllowedInbound  uint64 `json:"allowed_inbound"`
	BlockedInbound  uint64 `json:"blocked_inbound"`
	AllowedOutbound uint64 `json:"allowed_outbound"`
	BlockedOutbound uint64 `json:"blocked_outbound"`
	BlockedStreams  uint64 `json:"blocked_streams"`
	BlockedPeers    uint64 `json:"blocked_peers"`
	// BlockedProtocols / BlockedProtocolPeers are stream denials attributed to
	// a protocol budget. These are the ones that surface to a client as a bare
	// "stream reset" on an otherwise healthy node.
	BlockedProtocols     uint64 `json:"blocked_protocols"`
	BlockedProtocolPeers uint64 `json:"blocked_protocol_peers"`
	// RefusingSince is how long inbound admission has been refusing, or 0.
	RefusingSinceSeconds float64 `json:"refusing_since_seconds"`
	// StreamRefusingSinceSeconds is the same for STREAM denials, or 0.
	StreamRefusingSinceSeconds float64 `json:"stream_refusing_since_seconds"`
}

func (r *inboundAdmissionReporter) Snapshot() InboundAdmissionStats {
	s := InboundAdmissionStats{
		AllowedInbound:       r.allowedInbound.Load(),
		BlockedInbound:       r.blockedInbound.Load(),
		AllowedOutbound:      r.allowedOutbound.Load(),
		BlockedOutbound:      r.blockedOutbound.Load(),
		BlockedStreams:       r.blockedStreams.Load(),
		BlockedPeers:         r.blockedPeers.Load(),
		BlockedProtocols:     r.blockedProtocols.Load(),
		BlockedProtocolPeers: r.blockedProtocolPeers.Load(),
	}
	if first := r.firstBlockUnixNano.Load(); first != 0 {
		s.RefusingSinceSeconds = time.Since(time.Unix(0, first)).Seconds()
	}
	if first := r.firstStreamBlockUnixNano.Load(); first != 0 {
		s.StreamRefusingSinceSeconds = time.Since(time.Unix(0, first)).Seconds()
	}
	return s
}
