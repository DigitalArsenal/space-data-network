package license

import (
	"errors"
	"strings"
	"testing"

	lch "github.com/DigitalArsenal/spacedatastandards.org/lib/go/LCH"
	flatbuffers "github.com/google/flatbuffers/go"
)

// A delivery the provider never answers must still be countable.
//
// The gallery lost modules to a stream that was read, admitted, and then closed
// with no frame at all. Node-side that produced a bare WARN with no module and
// no requester; browser-side it read "Read aborted". Neither could be joined to
// the other, and a demo that silently fell back to an in-engine implementation
// looked exactly like a demo that never asked for the module. This makes the
// non-answer a line with the same shape as every other delivery outcome.
func TestAuditDeliveryAbortedNamesTheModuleAndFingerprintsTheRequester(t *testing.T) {
	var lines []string
	SetGrantAuditSink(func(line string) { lines = append(lines, line) }, nil)
	defer SetGrantAuditSink(nil, nil)

	const xpub = "xpub6DKCyLbCHZLFR4XpFg26royZdkxExSMHTjNorEgkn1kgvQbLF5sts9RfNt3PbGhphVUh7WsFQ5H6GJBh4LhmRL27oSPt1qDkJ5mAr6FZ3Wa"
	frame := buildAuditChallengeRequestFrame(t, "com.orbpro.rf-fspl", "0.1.1", xpub, "req-abc123")

	AuditDeliveryAborted("invoke_failed", frame, "16Uiu2HAmTestPeer", errors.New("plugin invoke failed (1)"))

	if len(lines) != 1 {
		t.Fatalf("expected exactly one audit line, got %d: %v", len(lines), lines)
	}
	line := lines[0]

	for _, want := range []string{
		"module=com.orbpro.rf-fspl",
		"version=0.1.1",
		"outcome=aborted",
		"reason=invoke_failed",
		`cause="plugin invoke failed (1)"`,
	} {
		if !strings.Contains(line, want) {
			t.Fatalf("audit line missing %q:\n%s", want, line)
		}
	}

	// Fingerprints only. An xpub in a log is the thing the audit exists to avoid.
	if strings.Contains(line, xpub) {
		t.Fatalf("audit line leaked the requester xpub:\n%s", line)
	}
	if !strings.Contains(line, "requester=xpub:") {
		t.Fatalf("audit line does not fingerprint the requester:\n%s", line)
	}
}

// An undecodable request still has to be reported: "we answered nothing" is the
// fact worth keeping, and dropping the line because the frame was malformed
// would restore exactly the silence this replaces.
func TestAuditDeliveryAbortedReportsEvenAnUndecodableRequest(t *testing.T) {
	var lines []string
	SetGrantAuditSink(func(line string) { lines = append(lines, line) }, nil)
	defer SetGrantAuditSink(nil, nil)

	AuditDeliveryAborted("read_failed", []byte("not a flatbuffer"), "16Uiu2HAmTestPeer", errors.New("stream reset"))

	if len(lines) != 1 {
		t.Fatalf("expected one audit line for an undecodable request, got %d", len(lines))
	}
	if !strings.Contains(lines[0], "outcome=aborted") || !strings.Contains(lines[0], "reason=read_failed") {
		t.Fatalf("unexpected audit line: %s", lines[0])
	}
	if !strings.Contains(lines[0], "peer=16Uiu2HAmTestPeer") {
		t.Fatalf("audit line lost the peer it did know: %s", lines[0])
	}
}

func buildAuditChallengeRequestFrame(t *testing.T, moduleID, moduleVersion, xpub, requestID string) []byte {
	t.Helper()
	builder := flatbuffers.NewBuilder(512)

	requestIDOffset := builder.CreateString(requestID)
	moduleIDOffset := builder.CreateString(moduleID)
	moduleVersionOffset := builder.CreateString(moduleVersion)
	requesterPeerIDOffset := builder.CreateString("16Uiu2HAmTestPeer")
	requesterXPubOffset := builder.CreateString(xpub)

	lch.LCHStart(builder)
	lch.LCHAddMESSAGE_TYPE(builder, 0) // Request
	lch.LCHAddROLE(builder, 0)         // Requester
	lch.LCHAddREQUEST_ID(builder, requestIDOffset)
	lch.LCHAddMODULE_ID(builder, moduleIDOffset)
	lch.LCHAddMODULE_VERSION(builder, moduleVersionOffset)
	lch.LCHAddREQUESTER_PEER_ID(builder, requesterPeerIDOffset)
	lch.LCHAddREQUESTER_XPUB(builder, requesterXPubOffset)
	root := lch.LCHEnd(builder)
	builder.FinishWithFileIdentifier(root, []byte(lch.LCHIdentifier))

	return builder.FinishedBytes()
}
