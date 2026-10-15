package license

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"sync"
	"time"

	lch "github.com/DigitalArsenal/spacedatastandards.org/lib/go/LCH"
	lpf "github.com/DigitalArsenal/spacedatastandards.org/lib/go/LPF"
	flatbuffers "github.com/google/flatbuffers/go"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/protocol"
)

// Delivery admission: the host's abuse limits on the module-delivery protocol.
//
// WHY THE HOST. The licensing key server decides grants, and it bounds its own
// table of handshakes waiting for a proof (key_server.cpp kMaxPendingGrants,
// 512; a full table answers provider_busy). That bound is shared: before this
// file one anonymous client could fill all 512 slots with open-policy
// challenges and every paying requester met provider_busy for the 30 s the
// challenges lived. The libp2p resource manager cannot help either — browser
// traffic reaches the node through the WSS proxy on an allowlisted loopback
// address, so every browser arrives with the allowlist's limits.
//
// So the host, which owns the stream, admits each frame before the key server
// sees it:
//
//  1. one node-wide token bucket for open-policy $LCH and $LPF frames
//     (50/s, burst 200; grant-policy.json delivery_limits);
//  2. open-policy handshakes hold at most half of the key server's 512 pending
//     slots, so the other half stays for allowlisted (paid) modules;
//  3. at most 32 handshakes in flight per connection, open or paid;
//  4. a handshake is pending for the challenge TTL the node configures the key
//     server with, 30 s, and no longer.
//
// The gate makes no grant decision and changes no byte of an admitted frame. A
// refused $LCH is answered with an $LCH Error (ERROR_CODE rate_limited,
// provider_busy, too_many_in_flight or version_retired), the shape the key
// server itself uses; a refused $LPF gets no frame, which is how the key server
// answers a proof it will not grant.

// ModuleDeliveryProtocolID is the libp2p protocol the licensing module serves.
const ModuleDeliveryProtocolID = "/space-data-network/module-delivery/1.0.0"

const (
	// DefaultOpenDeliveryRatePerSecond refills the open-policy bucket.
	DefaultOpenDeliveryRatePerSecond = 50
	// DefaultOpenDeliveryBurst is the open-policy bucket's depth.
	DefaultOpenDeliveryBurst = 200
	// KeyServerPendingSlots mirrors key_server.cpp kMaxPendingGrants.
	KeyServerPendingSlots = 512
	// OpenPendingSlots is the share of those slots open-policy handshakes may
	// hold: half. The rest stay for allowlisted modules.
	OpenPendingSlots = KeyServerPendingSlots / 2
	// MaxInFlightHandshakesPerConnection caps one connection's handshakes.
	MaxInFlightHandshakesPerConnection = 32
	// DeliveryChallengeTTL is the challenge lifetime the node configures the
	// key server with (licensingChallengeTTLMS in internal/node). It is not
	// tunable here: the gate only mirrors it.
	DeliveryChallengeTTL = 30 * time.Second

	// maxDeliveryRequestBytes is the module handler's own request bound.
	maxDeliveryRequestBytes = 16384
	// maxCapturedResponseBytes bounds what the gate keeps of an answer to
	// classify it; an $LCH answer is a few hundred bytes.
	maxCapturedResponseBytes = 64 << 10
	deliveryReadTimeout      = 15 * time.Second
)

// Refusal codes, written as the $LCH ERROR_CODE.
const (
	DeliveryRefusedRateLimited     = "rate_limited"
	DeliveryRefusedProviderBusy    = "provider_busy"
	DeliveryRefusedTooManyInFlight = "too_many_in_flight"
	DeliveryRefusedVersionRetired  = "version_retired"
)

// DeliveryLimits are the tunable limits.
type DeliveryLimits struct {
	OpenRatePerSecond float64
	OpenBurst         int
}

// DefaultDeliveryLimits returns the built-in limits.
func DefaultDeliveryLimits() DeliveryLimits {
	return DeliveryLimits{
		OpenRatePerSecond: DefaultOpenDeliveryRatePerSecond,
		OpenBurst:         DefaultOpenDeliveryBurst,
	}
}

// DeliveryReservedLookup reports whether a module ID names an allowlisted
// (paid) publication, whose handshakes are not drawn from the open bucket and
// are not counted against the open share of the pending table. Anything else —
// open, link-key, refused or unknown — is open traffic: a requester must not be
// able to escape the open limits by naming a module that does not exist.
type DeliveryReservedLookup func(moduleID string) bool

type pendingHandshake struct {
	conn    string
	open    bool
	expires time.Time
}

// DeliveryAdmission is the gate. The zero value is not usable; use
// NewDeliveryAdmission.
type DeliveryAdmission struct {
	mu  sync.Mutex
	now func() time.Time

	rate   float64
	burst  float64
	tokens float64
	filled time.Time

	reserved DeliveryReservedLookup
	retired  map[string]struct{}

	pending     map[string]pendingHandshake
	openPending int
	// openAdmitted counts open $LCH frames admitted and not yet answered, so
	// a burst of concurrent requests cannot overshoot the open share.
	openAdmitted int
	conns        map[string]int
}

// NewDeliveryAdmission builds a gate with the given limits. now may be nil.
func NewDeliveryAdmission(limits DeliveryLimits, now func() time.Time) *DeliveryAdmission {
	if now == nil {
		now = time.Now
	}
	a := &DeliveryAdmission{
		now:     now,
		retired: make(map[string]struct{}),
		pending: make(map[string]pendingHandshake),
		conns:   make(map[string]int),
	}
	a.setLimitsLocked(limits)
	a.tokens = a.burst
	a.filled = now()
	return a
}

var (
	defaultDeliveryAdmissionOnce sync.Once
	defaultDeliveryAdmission     *DeliveryAdmission
)

// DefaultDeliveryAdmission is the node's one gate. Every module-delivery stream
// handler the plugin manager installs is wrapped with it, and the licensing
// bootstrap configures it from grant-policy.json.
func DefaultDeliveryAdmission() *DeliveryAdmission {
	defaultDeliveryAdmissionOnce.Do(func() {
		defaultDeliveryAdmission = NewDeliveryAdmission(DefaultDeliveryLimits(), nil)
	})
	return defaultDeliveryAdmission
}

// Configure applies limits and the reserved-module lookup, and starts the
// bucket full. A nil lookup falls back to the audit policy table (allowlist
// means reserved). The licensing bootstrap calls it once per configuration.
func (a *DeliveryAdmission) Configure(limits DeliveryLimits, reserved DeliveryReservedLookup) {
	if a == nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.setLimitsLocked(limits)
	a.tokens = a.burst
	a.filled = a.now()
	a.reserved = reserved
}

func (a *DeliveryAdmission) setLimitsLocked(limits DeliveryLimits) {
	a.rate = limits.OpenRatePerSecond
	if a.rate <= 0 {
		a.rate = DefaultOpenDeliveryRatePerSecond
	}
	a.burst = float64(limits.OpenBurst)
	if a.burst < 1 {
		a.burst = DefaultOpenDeliveryBurst
	}
}

// RetireKeyID refuses new handshakes for a publication KEY_ID
// (<moduleId>:<version>). The key server keeps its copy until it is told to
// drop it; the gate makes the version unreachable either way.
func (a *DeliveryAdmission) RetireKeyID(keyID string) {
	if a == nil || strings.TrimSpace(keyID) == "" {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.retired[strings.TrimSpace(keyID)] = struct{}{}
}

// ReinstateKeyID undoes RetireKeyID, for a version published again.
func (a *DeliveryAdmission) ReinstateKeyID(keyID string) {
	if a == nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.retired, strings.TrimSpace(keyID))
}

// IsKeyIDRetired reports whether a KEY_ID is refused.
func (a *DeliveryAdmission) IsKeyIDRetired(keyID string) bool {
	if a == nil {
		return false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	_, ok := a.retired[strings.TrimSpace(keyID)]
	return ok
}

// DeliveryAdmissionStats is a snapshot for tests and status.
type DeliveryAdmissionStats struct {
	OpenPending  int
	TotalPending int
	Tokens       float64
	InFlight     map[string]int
}

// Stats returns a snapshot.
func (a *DeliveryAdmission) Stats() DeliveryAdmissionStats {
	a.mu.Lock()
	defer a.mu.Unlock()
	now := a.now()
	a.expireLocked(now)
	a.refillLocked(now)
	inFlight := make(map[string]int, len(a.conns))
	for k, v := range a.conns {
		inFlight[k] = v
	}
	return DeliveryAdmissionStats{
		OpenPending:  a.openPending,
		TotalPending: len(a.pending),
		Tokens:       a.tokens,
		InFlight:     inFlight,
	}
}

type deliveryFrameKind int

const (
	deliveryFrameOther deliveryFrameKind = iota
	deliveryFrameChallengeRequest
	deliveryFrameProof
)

type deliveryFrame struct {
	kind          deliveryFrameKind
	requestID     string
	moduleID      string
	moduleVersion string
}

// decodeDeliveryFrame classifies an inbound frame. Like the audit tap it reads
// attacker-controlled bytes with accessors that do not verify offsets, so a
// malformed frame is "other" rather than a panic in a stream goroutine.
func decodeDeliveryFrame(frame []byte) (out deliveryFrame) {
	defer func() {
		if recover() != nil {
			out = deliveryFrame{}
		}
	}()
	if len(frame) < 8 {
		return deliveryFrame{}
	}
	switch {
	case lch.LCHBufferHasIdentifier(frame):
		msg := lch.GetRootAsLCH(frame, 0)
		if int(msg.MESSAGE_TYPE()) != challengeMessageTypeRequest {
			return deliveryFrame{}
		}
		return deliveryFrame{
			kind:          deliveryFrameChallengeRequest,
			requestID:     string(msg.REQUEST_ID()),
			moduleID:      strings.TrimSpace(string(msg.MODULE_ID())),
			moduleVersion: strings.TrimSpace(string(msg.MODULE_VERSION())),
		}
	case lpf.LPFBufferHasIdentifier(frame):
		msg := lpf.GetRootAsLPF(frame, 0)
		return deliveryFrame{
			kind:          deliveryFrameProof,
			requestID:     string(msg.REQUEST_ID()),
			moduleID:      strings.TrimSpace(string(msg.MODULE_ID())),
			moduleVersion: strings.TrimSpace(string(msg.MODULE_VERSION())),
		}
	}
	return deliveryFrame{}
}

// challengeIssued reports whether an answer is an $LCH Response for requestID.
func challengeIssued(answer []byte, requestID string) (issued bool) {
	defer func() {
		if recover() != nil {
			issued = false
		}
	}()
	if len(answer) < 8 || !lch.LCHBufferHasIdentifier(answer) {
		return false
	}
	msg := lch.GetRootAsLCH(answer, 0)
	return int(msg.MESSAGE_TYPE()) == challengeMessageTypeResponse && string(msg.REQUEST_ID()) == requestID
}

// admissionTicket is what Admit hands back for Settle.
type admissionTicket struct {
	frame deliveryFrame
	conn  string
	open  bool
	// counted: the frame holds a per-connection slot while it is processed.
	counted bool
}

func (a *DeliveryAdmission) isReserved(moduleID string) bool {
	if moduleID == "" {
		return false
	}
	if a.reserved != nil {
		return a.reserved(moduleID)
	}
	if lookup := auditPolicy; lookup != nil {
		return lookup(moduleID) == GrantPolicyAllowlist
	}
	return false
}

func (a *DeliveryAdmission) refillLocked(now time.Time) {
	if elapsed := now.Sub(a.filled); elapsed > 0 {
		a.tokens += elapsed.Seconds() * a.rate
		if a.tokens > a.burst {
			a.tokens = a.burst
		}
	}
	a.filled = now
}

func (a *DeliveryAdmission) takeTokenLocked(now time.Time) bool {
	a.refillLocked(now)
	if a.tokens < 1 {
		return false
	}
	a.tokens--
	return true
}

func (a *DeliveryAdmission) expireLocked(now time.Time) {
	for id, p := range a.pending {
		if now.Before(p.expires) {
			continue
		}
		a.dropPendingLocked(id, p)
	}
}

func (a *DeliveryAdmission) dropPendingLocked(id string, p pendingHandshake) {
	delete(a.pending, id)
	if p.open {
		a.openPending--
	}
	a.releaseConnLocked(p.conn)
}

func (a *DeliveryAdmission) releaseConnLocked(conn string) {
	if n := a.conns[conn] - 1; n > 0 {
		a.conns[conn] = n
	} else {
		delete(a.conns, conn)
	}
}

// admit rules on one inbound frame from one connection. It returns a refusal
// code, or "" with a ticket that must be settled with the answer.
func (a *DeliveryAdmission) admit(conn string, frame deliveryFrame) (admissionTicket, string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	now := a.now()
	a.expireLocked(now)
	ticket := admissionTicket{frame: frame, conn: conn}

	switch frame.kind {
	case deliveryFrameChallengeRequest:
		if frame.moduleVersion != "" {
			if _, retired := a.retired[frame.moduleID+":"+frame.moduleVersion]; retired {
				return ticket, DeliveryRefusedVersionRetired
			}
		}
		if a.conns[conn] >= MaxInFlightHandshakesPerConnection {
			return ticket, DeliveryRefusedTooManyInFlight
		}
		ticket.open = !a.isReserved(frame.moduleID)
		if ticket.open {
			// A repeated REQUEST_ID replaces its own key-server slot, so it
			// does not need a new one.
			_, replacing := a.pending[frame.requestID]
			if !replacing && a.openPending+a.openAdmitted >= OpenPendingSlots {
				return ticket, DeliveryRefusedProviderBusy
			}
			if !a.takeTokenLocked(now) {
				return ticket, DeliveryRefusedRateLimited
			}
			a.openAdmitted++
		}
		a.conns[conn]++
		ticket.counted = true
		return ticket, ""

	case deliveryFrameProof:
		// A proof only ever frees a slot, so the per-connection cap does not
		// apply to it; open proofs still draw from the bucket.
		ticket.open = !a.isReserved(frame.moduleID)
		if ticket.open && !a.takeTokenLocked(now) {
			return ticket, DeliveryRefusedRateLimited
		}
		return ticket, ""

	default:
		// Not a licensing request: the module refuses it, but it still costs a
		// guest invocation, so it holds a connection slot while it runs.
		if a.conns[conn] >= MaxInFlightHandshakesPerConnection {
			return ticket, DeliveryRefusedTooManyInFlight
		}
		a.conns[conn]++
		ticket.counted = true
		return ticket, ""
	}
}

// settle records what the key server answered.
func (a *DeliveryAdmission) settle(ticket admissionTicket, answer []byte) {
	a.mu.Lock()
	defer a.mu.Unlock()
	now := a.now()
	if ticket.counted {
		a.releaseConnLocked(ticket.conn)
	}
	switch ticket.frame.kind {
	case deliveryFrameChallengeRequest:
		if ticket.open {
			a.openAdmitted--
		}
		if !challengeIssued(answer, ticket.frame.requestID) {
			return
		}
		id := ticket.frame.requestID
		if previous, ok := a.pending[id]; ok {
			a.dropPendingLocked(id, previous)
		}
		a.pending[id] = pendingHandshake{conn: ticket.conn, open: ticket.open, expires: now.Add(DeliveryChallengeTTL)}
		if ticket.open {
			a.openPending++
		}
		a.conns[ticket.conn]++
	case deliveryFrameProof:
		// The key server consumes the pending entry for any proof that names
		// it, granted or not (key_server.cpp proof path: find, zero, erase).
		if p, ok := a.pending[ticket.frame.requestID]; ok {
			a.dropPendingLocked(ticket.frame.requestID, p)
		}
	}
}

// Guard wraps a module-delivery stream handler with the gate.
func (a *DeliveryAdmission) Guard(next network.StreamHandler) network.StreamHandler {
	return func(s network.Stream) {
		_ = s.SetReadDeadline(time.Now().Add(deliveryReadTimeout))
		request, readErr := io.ReadAll(io.LimitReader(s, maxDeliveryRequestBytes+1))
		replay := &admittedStream{Stream: s, reader: bytes.NewReader(request), readErr: readErr}
		if readErr != nil || len(request) == 0 || len(request) > maxDeliveryRequestBytes {
			// The handler reports and drops these itself; nothing to admit.
			next(replay)
			return
		}

		conn := ""
		remote := ""
		if c := s.Conn(); c != nil {
			conn = c.ID()
			remote = c.RemotePeer().String()
		}
		frame := decodeDeliveryFrame(request)
		ticket, refusal := a.admit(conn, frame)
		if refusal != "" {
			AuditDeliveryAborted("admission_"+refusal, request, remote, nil)
			if frame.kind == deliveryFrameChallengeRequest {
				_ = s.SetWriteDeadline(time.Now().Add(10 * time.Second))
				_, _ = s.Write(buildChallengeRefusal(frame, refusal))
			}
			_ = s.Close()
			return
		}
		next(replay)
		a.settle(ticket, replay.answer.Bytes())
	}
}

// admittedStream replays the request the gate already read and keeps a copy
// of the answer the handler writes.
type admittedStream struct {
	network.Stream
	reader  *bytes.Reader
	readErr error
	answer  bytes.Buffer
}

func (s *admittedStream) Read(p []byte) (int, error) {
	n, err := s.reader.Read(p)
	if errors.Is(err, io.EOF) {
		if s.readErr != nil {
			return n, s.readErr
		}
	}
	return n, err
}

func (s *admittedStream) Write(p []byte) (int, error) {
	if room := maxCapturedResponseBytes - s.answer.Len(); room > 0 {
		if len(p) < room {
			room = len(p)
		}
		s.answer.Write(p[:room])
	}
	return s.Stream.Write(p)
}

// buildChallengeRefusal answers a refused $LCH request the way the key server
// answers one it refuses: an $LCH Error from the provider role, echoing the
// request's identifiers.
func buildChallengeRefusal(frame deliveryFrame, code string) []byte {
	builder := flatbuffers.NewBuilder(256)
	requestID := builder.CreateString(frame.requestID)
	moduleID := builder.CreateString(frame.moduleID)
	moduleVersion := builder.CreateString(frame.moduleVersion)
	errorCode := builder.CreateString(code)
	errorMessage := builder.CreateString(deliveryRefusalMessage(code))
	lch.LCHStart(builder)
	lch.LCHAddMESSAGE_TYPE(builder, lch.EnumValueslicensingChallengeMessageType["Error"])
	lch.LCHAddROLE(builder, lch.EnumValueslicensingChallengeRole["Provider"])
	lch.LCHAddREQUEST_ID(builder, requestID)
	lch.LCHAddMODULE_ID(builder, moduleID)
	lch.LCHAddMODULE_VERSION(builder, moduleVersion)
	lch.LCHAddERROR_CODE(builder, errorCode)
	lch.LCHAddERROR_MESSAGE(builder, errorMessage)
	lch.FinishLCHBuffer(builder, lch.LCHEnd(builder))
	return builder.FinishedBytes()
}

func deliveryRefusalMessage(code string) string {
	switch code {
	case DeliveryRefusedRateLimited:
		return "the provider is rate limiting open module handshakes; retry shortly"
	case DeliveryRefusedProviderBusy:
		return "the provider's open handshake slots are full; retry shortly"
	case DeliveryRefusedTooManyInFlight:
		return "too many module handshakes are in flight on this connection"
	case DeliveryRefusedVersionRetired:
		return "this module version has been superseded and is no longer granted"
	}
	return "module handshake refused"
}

// WrapDeliveryHost returns a host whose module-delivery stream handlers pass
// through the gate. Every other protocol is untouched.
func WrapDeliveryHost(h host.Host, a *DeliveryAdmission) host.Host {
	if h == nil || a == nil {
		return h
	}
	if wrapped, ok := h.(*deliveryAdmissionHost); ok && wrapped.admission == a {
		return h
	}
	return &deliveryAdmissionHost{Host: h, admission: a}
}

type deliveryAdmissionHost struct {
	host.Host
	admission *DeliveryAdmission
}

func (h *deliveryAdmissionHost) SetStreamHandler(pid protocol.ID, handler network.StreamHandler) {
	if string(pid) == ModuleDeliveryProtocolID && handler != nil {
		handler = h.admission.Guard(handler)
	}
	h.Host.SetStreamHandler(pid, handler)
}

func (h *deliveryAdmissionHost) SetStreamHandlerMatch(pid protocol.ID, match func(protocol.ID) bool, handler network.StreamHandler) {
	if string(pid) == ModuleDeliveryProtocolID && handler != nil {
		handler = h.admission.Guard(handler)
	}
	h.Host.SetStreamHandlerMatch(pid, match, handler)
}
