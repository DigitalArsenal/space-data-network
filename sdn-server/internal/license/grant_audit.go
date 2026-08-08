package license

import (
	"fmt"
	"strings"

	lch "github.com/DigitalArsenal/spacedatastandards.org/lib/go/LCH"
	lgr "github.com/DigitalArsenal/spacedatastandards.org/lib/go/LGR"
)

// Grant auditing.
//
// Every grant decision is made INSIDE the licensing WASM key server — that is
// application logic and it belongs there. But every one of those decisions
// leaves the node as a frame on a libp2p stream the HOST owns, and logging what
// crosses a boundary is what a connector is for. This file decodes only the
// envelope: which module, which requester (fingerprinted), granted or refused,
// and why. It makes no decision and changes no byte.
//
// The audit line answers the question the P1 was filed on — "who was granted
// what, under which policy" — without ever printing an xpub.

// GrantAuditEvent is one decoded delivery-lane exchange.
type GrantAuditEvent struct {
	// Direction is "request" (inbound, from the requester) or "response"
	// (outbound, this provider's answer).
	Direction string

	// Kind is the SDS frame identifier: "$LCH" (challenge) or "$LGR" (grant).
	Kind string

	ModuleID      string
	ModuleVersion string
	RequestID     string

	// XpubFingerprint is a fingerprint. Never an xpub.
	XpubFingerprint string
	PeerID          string

	// Outcome is one of: challenge-issued, challenge-requested,
	// granted, refused, error.
	Outcome string

	// Reason carries the provider's error/denial code, e.g.
	// "xpub_not_allowed", "module_not_found".
	Reason string

	// Policy is the effective grant policy for ModuleID, filled in by the
	// caller from the resolved publication decision when it knows it.
	Policy string
}

// String renders the single audit line. Fixed key=value shape so it greps.
func (e GrantAuditEvent) String() string {
	fields := []string{
		"grant-audit",
		"dir=" + orNone(e.Direction),
		"frame=" + orNone(e.Kind),
		"module=" + orNone(e.ModuleID),
	}
	if e.ModuleVersion != "" {
		fields = append(fields, "version="+e.ModuleVersion)
	}
	fields = append(fields,
		"requester="+orNone(e.XpubFingerprint),
		"policy="+orNone(e.Policy),
		"outcome="+orNone(e.Outcome),
	)
	if e.Reason != "" {
		fields = append(fields, "reason="+e.Reason)
	}
	if e.PeerID != "" {
		fields = append(fields, "peer="+e.PeerID)
	}
	if e.RequestID != "" {
		fields = append(fields, "request="+e.RequestID)
	}
	return strings.Join(fields, " ")
}

func orNone(v string) string {
	if strings.TrimSpace(v) == "" {
		return "-"
	}
	return v
}

// LGR message-type wire values (the generated enum constants are unexported).
const (
	grantMessageTypeGranted = 1
	grantMessageTypeDenied  = 2
)

// LCH message-type wire values.
const (
	challengeMessageTypeRequest  = 0
	challengeMessageTypeResponse = 1
	challengeMessageTypeError    = 2
)

// DecodeGrantAuditEvent classifies one frame from the delivery lane. It returns
// ok=false for anything that is not a licensing frame, so the caller can tap a
// generic stream handler without knowing which module it is serving.
//
// A REQUEST frame here is attacker-controlled: any peer may open the stream and
// send bytes that merely look like a licensing frame. The flatbuffers Go
// accessors do not verify offsets, so eight crafted bytes carrying the "$LCH"
// identifier and a bogus root offset make a field read run off the buffer — and
// an unrecovered panic in a libp2p stream-handler goroutine takes the WHOLE
// daemon down. The module itself is safe because it verifies its own input; the
// tap is the new exposure. An audit tap must never be the thing that kills the
// node it audits, so a malformed frame is simply not audited.
func DecodeGrantAuditEvent(direction string, frame []byte) (event GrantAuditEvent, ok bool) {
	defer func() {
		if recover() != nil {
			event = GrantAuditEvent{}
			ok = false
		}
	}()
	if len(frame) < 8 {
		return GrantAuditEvent{}, false
	}
	switch {
	case lch.LCHBufferHasIdentifier(frame):
		msg := lch.GetRootAsLCH(frame, 0)
		event := GrantAuditEvent{
			Direction:       direction,
			Kind:            "$LCH",
			ModuleID:        string(msg.MODULE_ID()),
			ModuleVersion:   string(msg.MODULE_VERSION()),
			RequestID:       string(msg.REQUEST_ID()),
			XpubFingerprint: XpubFingerprint(string(msg.REQUESTER_XPUB())),
			PeerID:          string(msg.REQUESTER_PEER_ID()),
			Reason:          string(msg.ERROR_CODE()),
		}
		switch int(msg.MESSAGE_TYPE()) {
		case challengeMessageTypeRequest:
			event.Outcome = "challenge-requested"
		case challengeMessageTypeResponse:
			event.Outcome = "challenge-issued"
		case challengeMessageTypeError:
			event.Outcome = "refused"
		default:
			event.Outcome = "unknown"
		}
		return event, true
	case lgr.LGRBufferHasIdentifier(frame):
		msg := lgr.GetRootAsLGR(frame, 0)
		event := GrantAuditEvent{
			Direction:       direction,
			Kind:            "$LGR",
			ModuleID:        string(msg.MODULE_ID()),
			ModuleVersion:   string(msg.MODULE_VERSION()),
			RequestID:       string(msg.REQUEST_ID()),
			XpubFingerprint: XpubFingerprint(string(msg.REQUESTER_XPUB())),
			PeerID:          string(msg.REQUESTER_PEER_ID()),
			Reason:          string(msg.DENIAL_REASON()),
		}
		switch int(byte(msg.MESSAGE_TYPE())) {
		case grantMessageTypeGranted:
			event.Outcome = "granted"
		case grantMessageTypeDenied:
			event.Outcome = "refused"
		default:
			event.Outcome = "grant-requested"
		}
		return event, true
	}
	return GrantAuditEvent{}, false
}

// GrantAuditPolicyLookup resolves the effective policy for a module ID so the
// audit line can name it. The node installs one; when none is installed the
// policy field reads "-" rather than a guess.
type GrantAuditPolicyLookup func(moduleID string) string

// GrantAuditSink receives finished audit lines. The node points it at the
// daemon log.
type GrantAuditSink func(line string)

var (
	auditSink   GrantAuditSink
	auditPolicy GrantAuditPolicyLookup
)

// SetGrantAuditSink installs the audit destination and the policy lookup. It is
// called once during node wiring, before any stream handler is registered.
func SetGrantAuditSink(sink GrantAuditSink, policy GrantAuditPolicyLookup) {
	auditSink = sink
	auditPolicy = policy
}

// AuditDeliveryFrame decodes and emits one frame if it is a licensing frame.
// Anything else is ignored. Safe to call with no sink installed.
func AuditDeliveryFrame(direction string, frame []byte, peerID string) {
	sink := auditSink
	if sink == nil {
		return
	}
	event, ok := DecodeGrantAuditEvent(direction, frame)
	if !ok {
		return
	}
	if event.PeerID == "" {
		event.PeerID = peerID
	}
	if lookup := auditPolicy; lookup != nil {
		event.Policy = lookup(event.ModuleID)
	}
	sink(event.String())
}

// AuditDeliveryAborted records a delivery this provider did NOT answer.
//
// Why this exists. Every audit line above describes a frame that CROSSED the
// boundary. The failure that cost the gallery its modules crossed nothing: the
// stream handler read a request, failed to produce a response, and returned —
// closing the stream with a clean EOF and no frame at all. Node-side that was a
// bare WARN with no module and no requester; browser-side it surfaced as
// "Read aborted", which names the symptom and nothing else. The two were
// impossible to correlate, so a demo that silently substituted an in-engine
// fallback looked identical to a demo that never asked.
//
// The connector decides nothing here. It reports that a request it admitted got
// no answer, naming the module the REQUEST asked for and the requester's
// fingerprint — never an xpub. A delivery that is granted but never fetched, or
// fetched but never completed, is now a countable line rather than an absence.
func AuditDeliveryAborted(stage string, requestFrame []byte, peerID string, cause error) {
	sink := auditSink
	if sink == nil {
		return
	}
	event, ok := DecodeGrantAuditEvent("response", requestFrame)
	if !ok {
		// Undecodable request: still say a delivery died, with what is known.
		event = GrantAuditEvent{Direction: "response", Kind: "$LCH"}
	}
	if event.PeerID == "" {
		event.PeerID = peerID
	}
	if lookup := auditPolicy; lookup != nil {
		event.Policy = lookup(event.ModuleID)
	}
	event.Outcome = "aborted"
	event.Reason = strings.TrimSpace(stage)
	if event.Reason == "" {
		event.Reason = "unknown_stage"
	}
	line := event.String()
	if cause != nil {
		line += fmt.Sprintf(" cause=%q", cause.Error())
	}
	sink(line)
}

// FormatPublicationAudit renders the boot-time ledger line for one module's
// publication decision. This is the host's OWN decision — the one place where
// the host, not the key server, rules on entitlement.
func FormatPublicationAudit(decision PublicationDecision) string {
	outcome := "published"
	if !decision.Publish {
		outcome = "REFUSED"
	}
	line := fmt.Sprintf(
		"grant-audit dir=publish frame=$PLG module=%s policy=%s source=%s allowed_xpubs=%d outcome=%s",
		orNone(decision.ModuleID), orNone(decision.Policy), orNone(decision.Source),
		decision.AllowedXpubCount, outcome,
	)
	if decision.Note != "" {
		line += fmt.Sprintf(" note=%q", decision.Note)
	}
	if decision.Reason != "" {
		line += fmt.Sprintf(" reason=%q", decision.Reason)
	}
	return line
}
