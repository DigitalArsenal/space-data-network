package license

import (
	"strings"
	"testing"

	lch "github.com/DigitalArsenal/spacedatastandards.org/lib/go/LCH"
	flatbuffers "github.com/google/flatbuffers/go"
)

const testXpub = "xpub6CUGRUonZSQ4TWtTMmzXdrXDtypWKiKrhko4egpiMZbpiaQZY2s"

// buildChallengeFrame assembles an $LCH the way the requester and the provider
// put one on the wire, so the audit tap is exercised against a real frame and
// not a hand-rolled struct.
func buildChallengeFrame(t *testing.T, messageType int, moduleID, xpub, errorCode string) []byte {
	t.Helper()
	b := flatbuffers.NewBuilder(256)
	requestID := b.CreateString("req-1")
	moduleIDOffset := b.CreateString(moduleID)
	versionOffset := b.CreateString("0.1.0")
	peerOffset := b.CreateString("12D3KooWTestRequester")
	xpubOffset := b.CreateString(xpub)
	errorOffset := flatbuffers.UOffsetT(0)
	if errorCode != "" {
		errorOffset = b.CreateString(errorCode)
	}

	lch.LCHStart(b)
	// The generated enum type is unexported, so the wire value goes in as an
	// untyped constant.
	switch messageType {
	case challengeMessageTypeRequest:
		lch.LCHAddMessageType(b, challengeMessageTypeRequest)
	case challengeMessageTypeResponse:
		lch.LCHAddMessageType(b, challengeMessageTypeResponse)
	default:
		lch.LCHAddMessageType(b, challengeMessageTypeError)
	}
	lch.LCHAddREQUEST_ID(b, requestID)
	lch.LCHAddMODULE_ID(b, moduleIDOffset)
	lch.LCHAddMODULE_VERSION(b, versionOffset)
	lch.LCHAddREQUESTER_PEER_ID(b, peerOffset)
	lch.LCHAddREQUESTER_XPUB(b, xpubOffset)
	if errorOffset != 0 {
		lch.LCHAddERROR_CODE(b, errorOffset)
	}
	root := lch.LCHEnd(b)
	lch.FinishLCHBuffer(b, root)
	return b.FinishedBytes()
}

func TestDecodeGrantAuditEventReadsAChallengeRequest(t *testing.T) {
	frame := buildChallengeFrame(t, challengeMessageTypeRequest, "com.orbpro.hpop", testXpub, "")

	event, ok := DecodeGrantAuditEvent("request", frame)

	if !ok {
		t.Fatal("an $LCH frame was not recognised by the audit tap")
	}
	if event.ModuleID != "com.orbpro.hpop" {
		t.Fatalf("ModuleID = %q", event.ModuleID)
	}
	if event.Outcome != "challenge-requested" {
		t.Fatalf("Outcome = %q, want challenge-requested", event.Outcome)
	}
	if event.XpubFingerprint != XpubFingerprint(testXpub) {
		t.Fatalf("XpubFingerprint = %q", event.XpubFingerprint)
	}
}

func TestDecodeGrantAuditEventReadsARefusal(t *testing.T) {
	frame := buildChallengeFrame(t, challengeMessageTypeError, "com.orbpro.hpop", testXpub, "xpub_not_allowed")

	event, ok := DecodeGrantAuditEvent("response", frame)

	if !ok {
		t.Fatal("an $LCH error frame was not recognised")
	}
	if event.Outcome != "refused" {
		t.Fatalf("Outcome = %q, want refused", event.Outcome)
	}
	if event.Reason != "xpub_not_allowed" {
		t.Fatalf("Reason = %q, want the provider's error code", event.Reason)
	}
}

// The whole point of fingerprinting: a delivery-lane log must never become an
// index of which wallet asked for which module.
func TestAuditLineNeverPrintsAnXpub(t *testing.T) {
	frame := buildChallengeFrame(t, challengeMessageTypeRequest, "com.orbpro.hpop", testXpub, "")
	event, _ := DecodeGrantAuditEvent("request", frame)
	event.Policy = GrantPolicyAllowlist

	line := event.String()

	if strings.Contains(line, testXpub) {
		t.Fatalf("audit line leaks the raw xpub: %q", line)
	}
	for _, needle := range []string{"module=com.orbpro.hpop", "policy=allowlist", "outcome=challenge-requested", "requester=xpub:"} {
		if !strings.Contains(line, needle) {
			t.Fatalf("audit line %q is missing %q", line, needle)
		}
	}
}

func TestDecodeGrantAuditEventIgnoresNonLicensingFrames(t *testing.T) {
	for name, frame := range map[string][]byte{
		"empty":  nil,
		"short":  []byte("abc"),
		"random": []byte("this is not a flatbuffer at all"),
	} {
		if _, ok := DecodeGrantAuditEvent("request", frame); ok {
			t.Fatalf("%s frame was decoded as a licensing frame", name)
		}
	}
}

func TestAuditDeliveryFrameIsSafeWithNoSinkInstalled(t *testing.T) {
	SetGrantAuditSink(nil, nil)
	AuditDeliveryFrame("request", buildChallengeFrame(t, challengeMessageTypeRequest, "m", testXpub, ""), "peer")
}

func TestAuditDeliveryFrameUsesTheInstalledPolicyLookup(t *testing.T) {
	var lines []string
	SetGrantAuditSink(
		func(line string) { lines = append(lines, line) },
		func(moduleID string) string {
			if moduleID == "com.orbpro.rf-fspl" {
				return GrantPolicyLinkKey
			}
			return GrantPolicyAllowlist
		},
	)
	defer SetGrantAuditSink(nil, nil)

	AuditDeliveryFrame("request", buildChallengeFrame(t, challengeMessageTypeRequest, "com.orbpro.rf-fspl", testXpub, ""), "peer")

	if len(lines) != 1 {
		t.Fatalf("got %d audit lines, want 1", len(lines))
	}
	if !strings.Contains(lines[0], "policy=link-key") {
		t.Fatalf("audit line %q did not name the policy in force", lines[0])
	}
}
