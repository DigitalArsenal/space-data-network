package deliveryclient

import (
	"bytes"
	"testing"

	lch "github.com/DigitalArsenal/spacedatastandards.org/lib/go/LCH"
	flatbuffers "github.com/google/flatbuffers/go"
)

func TestEncodeChallengeRequestRoundTrip(t *testing.T) {
	req := ChallengeRequest{
		RequestID:                   "req-1",
		ModuleID:                    "com.orbpro.starlink-source",
		ModuleVersion:               "1.2.3",
		RequesterPeerID:             "peerA",
		RequesterXPub:               "xpubA",
		RequesterSigningPublicKey:   []byte{1, 2, 3, 4},
		RequesterEphemeralPublicKey: []byte{5, 6, 7, 8},
		RequestedDomain:             "orbpro.default",
		RequestedTimeoutMs:          30_000,
		RequestedAtMs:               1234,
		ProviderPeerID:              "peerP",
	}
	frame, err := EncodeChallengeRequest(req)
	if err != nil {
		t.Fatalf("EncodeChallengeRequest() error = %v", err)
	}
	if !lch.LCHBufferHasIdentifier(frame) {
		t.Fatal("frame missing $LCH file identifier")
	}

	m := lch.GetRootAsLCH(frame, 0)
	if got := byte(m.MESSAGE_TYPE()); got != challengeMessageTypeRequest {
		t.Errorf("MESSAGE_TYPE = %d, want Request(%d)", got, challengeMessageTypeRequest)
	}
	if got := byte(m.ROLE()); got != challengeRoleRequester {
		t.Errorf("ROLE = %d, want Requester(%d)", got, challengeRoleRequester)
	}
	if got := string(m.REQUEST_ID()); got != "req-1" {
		t.Errorf("REQUEST_ID = %q", got)
	}
	if got := string(m.MODULE_ID()); got != "com.orbpro.starlink-source" {
		t.Errorf("MODULE_ID = %q", got)
	}
	if got := string(m.MODULE_VERSION()); got != "1.2.3" {
		t.Errorf("MODULE_VERSION = %q", got)
	}
	if got := string(m.REQUESTER_PEER_ID()); got != "peerA" {
		t.Errorf("REQUESTER_PEER_ID = %q", got)
	}
	if got := string(m.REQUESTER_XPUB()); got != "xpubA" {
		t.Errorf("REQUESTER_XPUB = %q", got)
	}
	if got := m.RequesterSigningPubkeyBytes(); !bytes.Equal(got, []byte{1, 2, 3, 4}) {
		t.Errorf("REQUESTER_SIGNING_PUBKEY = %v", got)
	}
	if got := m.RequesterEphemeralPubkeyBytes(); !bytes.Equal(got, []byte{5, 6, 7, 8}) {
		t.Errorf("REQUESTER_EPHEMERAL_PUBKEY = %v", got)
	}
	if got := string(m.REQUESTED_DOMAIN()); got != "orbpro.default" {
		t.Errorf("REQUESTED_DOMAIN = %q", got)
	}
	if got := m.REQUESTED_TIMEOUT_MS(); got != 30_000 {
		t.Errorf("REQUESTED_TIMEOUT_MS = %d", got)
	}
	if got := m.REQUESTED_AT(); got != 1234 {
		t.Errorf("REQUESTED_AT = %d", got)
	}
	if got := string(m.PROVIDER_PEER_ID()); got != "peerP" {
		t.Errorf("PROVIDER_PEER_ID = %q", got)
	}
}

func TestEncodeChallengeRequestValidation(t *testing.T) {
	if _, err := EncodeChallengeRequest(ChallengeRequest{ModuleID: "x"}); err == nil {
		t.Error("expected error for missing request id")
	}
	if _, err := EncodeChallengeRequest(ChallengeRequest{RequestID: "r"}); err == nil {
		t.Error("expected error for missing module id")
	}
}

// encodeTestChallengeResponse builds a provider-side LCH response frame for
// decode tests. isError selects an Error message carrying an access-denied code;
// otherwise a normal Response. Message type / role are passed as the package's
// untyped constants (the generated enum type is package-private and cannot be
// named from here, but untyped constants convert to it).
func encodeTestChallengeResponse(reqID, moduleID, moduleVersion, providerPeerID string, nonce []byte, expiresAt uint64, isError bool) []byte {
	b := flatbuffers.NewBuilder(256)
	rid := b.CreateString(reqID)
	mid := b.CreateString(moduleID)
	mv := b.CreateString(moduleVersion)
	pid := b.CreateString(providerPeerID)
	var nonceOff flatbuffers.UOffsetT
	if len(nonce) > 0 {
		nonceOff = b.CreateByteVector(nonce)
	}
	var codeOff, msgOff flatbuffers.UOffsetT
	if isError {
		codeOff = b.CreateString("access_denied")
		msgOff = b.CreateString("requester not authorized")
	}

	lch.LCHStart(b)
	if isError {
		lch.LCHAddMESSAGE_TYPE(b, challengeMessageTypeError)
	} else {
		lch.LCHAddMESSAGE_TYPE(b, challengeMessageTypeResponse)
	}
	lch.LCHAddROLE(b, challengeRoleProvider)
	lch.LCHAddREQUEST_ID(b, rid)
	lch.LCHAddMODULE_ID(b, mid)
	lch.LCHAddMODULE_VERSION(b, mv)
	lch.LCHAddPROVIDER_PEER_ID(b, pid)
	if nonceOff != 0 {
		lch.LCHAddCHALLENGE_NONCE(b, nonceOff)
	}
	lch.LCHAddEXPIRES_AT(b, expiresAt)
	if codeOff != 0 {
		lch.LCHAddERROR_CODE(b, codeOff)
	}
	if msgOff != 0 {
		lch.LCHAddERROR_MESSAGE(b, msgOff)
	}
	root := lch.LCHEnd(b)
	lch.FinishLCHBuffer(b, root)
	return b.FinishedBytes()
}

func TestDecodeChallengeResponse(t *testing.T) {
	nonce := []byte{9, 8, 7, 6, 5}
	frame := encodeTestChallengeResponse("req-1", "com.orbpro.x", "1.2.3", "peerP", nonce, 55_555, false)

	resp, err := DecodeChallengeResponse(frame)
	if err != nil {
		t.Fatalf("DecodeChallengeResponse() error = %v", err)
	}
	if resp.RequestID != "req-1" || resp.ModuleID != "com.orbpro.x" || resp.ModuleVersion != "1.2.3" {
		t.Errorf("resp identity = %+v", resp)
	}
	if resp.ProviderPeerID != "peerP" {
		t.Errorf("ProviderPeerID = %q", resp.ProviderPeerID)
	}
	if !bytes.Equal(resp.ChallengeNonce, nonce) {
		t.Errorf("ChallengeNonce = %v, want %v", resp.ChallengeNonce, nonce)
	}
	if resp.ExpiresAtMs != 55_555 {
		t.Errorf("ExpiresAtMs = %d", resp.ExpiresAtMs)
	}
	// The proof step signs the response verbatim: RawBytes must equal the frame.
	if !bytes.Equal(resp.RawBytes, frame) {
		t.Error("RawBytes must be preserved byte-for-byte for proof signing")
	}
}

func TestDecodeChallengeResponseRejectsNonLCH(t *testing.T) {
	if _, err := DecodeChallengeResponse([]byte("not a flatbuffer")); err == nil {
		t.Error("expected error for non-$LCH bytes")
	}
	if _, err := DecodeChallengeResponse(nil); err == nil {
		t.Error("expected error for empty bytes")
	}
}

func TestDecodeChallengeResponseRejectsWrongType(t *testing.T) {
	// A request frame (not a response) must be rejected.
	reqFrame, err := EncodeChallengeRequest(ChallengeRequest{RequestID: "r", ModuleID: "m"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeChallengeResponse(reqFrame); err == nil {
		t.Error("expected error decoding a Request frame as a Response")
	}
}

func TestDecodeChallengeResponseSurfacesProviderError(t *testing.T) {
	frame := encodeTestChallengeResponse("req-1", "m", "1.0.0", "peerP", nil, 0, true)
	_, err := DecodeChallengeResponse(frame)
	if err == nil {
		t.Fatal("expected provider error to surface")
	}
	if got := err.Error(); !bytes.Contains([]byte(got), []byte("access_denied")) {
		t.Errorf("error = %q, want it to include the provider error code", got)
	}
}
