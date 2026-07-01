package deliveryclient

import (
	"strings"
	"testing"

	lgr "github.com/DigitalArsenal/spacedatastandards.org/lib/go/LGR"
	flatbuffers "github.com/google/flatbuffers/go"
)

type testGrant struct {
	denied           bool
	requestID        string
	moduleID         string
	moduleVersion    string
	grantedDomain    string
	grantedTimeoutMs uint64
	expiresAtMs      uint64
	grantStatus      string
	denialReason     string
	moduleCID        string
	wrappedPayload   []byte
	verifierPubKey   []byte
	providerSig      []byte
}

// encodeTestGrant builds a provider-side LGR grant frame for decode/validate
// tests, including an embedded PLG module descriptor carrying the bundle CID.
func encodeTestGrant(g testGrant) []byte {
	b := flatbuffers.NewBuilder(512)

	// Nested PLG descriptor must be finished before the enclosing LGR opens.
	var descriptorOff flatbuffers.UOffsetT
	if g.moduleCID != "" {
		cid := b.CreateString(g.moduleCID)
		lgr.PLGStart(b)
		lgr.PLGAddWASM_CID(b, cid)
		descriptorOff = lgr.PLGEnd(b)
	}

	rid := b.CreateString(g.requestID)
	mid := b.CreateString(g.moduleID)
	mv := b.CreateString(g.moduleVersion)
	gd := b.CreateString(g.grantedDomain)
	gs := b.CreateString(g.grantStatus)
	dr := b.CreateString(g.denialReason)

	var payloadOff, verifierOff, sigOff flatbuffers.UOffsetT
	if len(g.wrappedPayload) > 0 {
		payloadOff = b.CreateByteVector(g.wrappedPayload)
	}
	if len(g.verifierPubKey) > 0 {
		verifierOff = b.CreateByteVector(g.verifierPubKey)
	}
	if len(g.providerSig) > 0 {
		sigOff = b.CreateByteVector(g.providerSig)
	}

	lgr.LGRStart(b)
	if g.denied {
		lgr.LGRAddMESSAGE_TYPE(b, grantMessageTypeDenied)
	} else {
		lgr.LGRAddMESSAGE_TYPE(b, grantMessageTypeGranted)
	}
	lgr.LGRAddREQUEST_ID(b, rid)
	lgr.LGRAddMODULE_ID(b, mid)
	lgr.LGRAddMODULE_VERSION(b, mv)
	lgr.LGRAddGRANTED_DOMAIN(b, gd)
	lgr.LGRAddGRANTED_TIMEOUT_MS(b, g.grantedTimeoutMs)
	lgr.LGRAddEXPIRES_AT(b, g.expiresAtMs)
	lgr.LGRAddGRANT_STATUS(b, gs)
	lgr.LGRAddDENIAL_REASON(b, dr)
	if descriptorOff != 0 {
		lgr.LGRAddMODULE_DESCRIPTOR(b, descriptorOff)
	}
	if payloadOff != 0 {
		lgr.LGRAddWRAPPED_CONTENT_KEY_PAYLOAD(b, payloadOff)
	}
	if verifierOff != 0 {
		lgr.LGRAddGRANT_VERIFIER_PUBKEY(b, verifierOff)
	}
	if sigOff != 0 {
		lgr.LGRAddPROVIDER_SIGNATURE(b, sigOff)
	}
	root := lgr.LGREnd(b)
	lgr.FinishLGRBuffer(b, root)
	return b.FinishedBytes()
}

func grantedFixture() testGrant {
	return testGrant{
		requestID: "req-1", moduleID: "com.orbpro.x", moduleVersion: "1.0.0",
		grantedDomain: "orbpro.default", grantedTimeoutMs: 30_000, expiresAtMs: 1_000_000,
		grantStatus: "granted", moduleCID: "bafymodulecid",
		wrappedPayload: []byte{1, 2, 3}, verifierPubKey: []byte{4, 5}, providerSig: []byte{6, 7},
	}
}

func TestDecodeGrant(t *testing.T) {
	frame := encodeTestGrant(grantedFixture())
	g, err := DecodeGrant(frame)
	if err != nil {
		t.Fatalf("DecodeGrant() error = %v", err)
	}
	if !g.Granted() {
		t.Error("Granted() = false, want true")
	}
	if g.RequestID != "req-1" || g.ModuleID != "com.orbpro.x" || g.ModuleVersion != "1.0.0" {
		t.Errorf("identity = %+v", g)
	}
	if g.GrantedDomain != "orbpro.default" || g.GrantedTimeoutMs != 30_000 || g.ExpiresAtMs != 1_000_000 {
		t.Errorf("grant scope = %+v", g)
	}
	if g.ModuleDescriptorCID != "bafymodulecid" {
		t.Errorf("ModuleDescriptorCID = %q, want bafymodulecid", g.ModuleDescriptorCID)
	}
	if string(g.WrappedContentKeyPayload) != string([]byte{1, 2, 3}) {
		t.Errorf("WrappedContentKeyPayload = %v", g.WrappedContentKeyPayload)
	}
	if string(g.GrantVerifierPublicKey) != string([]byte{4, 5}) {
		t.Errorf("GrantVerifierPublicKey = %v", g.GrantVerifierPublicKey)
	}
	if string(g.ProviderSignature) != string([]byte{6, 7}) {
		t.Errorf("ProviderSignature = %v", g.ProviderSignature)
	}
}

func TestDecodeGrantRejectsNonLGR(t *testing.T) {
	if _, err := DecodeGrant([]byte("nope")); err == nil {
		t.Error("expected error for non-$LGR bytes")
	}
}

func TestGrantValidateHappyPath(t *testing.T) {
	g, err := DecodeGrant(encodeTestGrant(grantedFixture()))
	if err != nil {
		t.Fatal(err)
	}
	err = g.Validate(GrantExpectations{
		RequestID: "req-1", ModuleID: "com.orbpro.x", ModuleVersion: "1.0.0",
		ExpectedDomain: "orbpro.default", RequestedTimeoutMs: 30_000, NowMs: 500_000,
	})
	if err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
}

func TestGrantValidateDenied(t *testing.T) {
	f := grantedFixture()
	f.denied = true
	f.denialReason = "requester not in allowlist"
	g, _ := DecodeGrant(encodeTestGrant(f))
	err := g.Validate(GrantExpectations{RequestID: "req-1", ModuleID: "com.orbpro.x"})
	if err == nil || !strings.Contains(err.Error(), "requester not in allowlist") {
		t.Fatalf("Validate() error = %v, want denial reason surfaced", err)
	}
}

func TestGrantValidateExpired(t *testing.T) {
	g, _ := DecodeGrant(encodeTestGrant(grantedFixture()))
	err := g.Validate(GrantExpectations{RequestID: "req-1", ModuleID: "com.orbpro.x", NowMs: 2_000_000})
	if err == nil || !strings.Contains(err.Error(), "expired") {
		t.Fatalf("Validate() error = %v, want expired", err)
	}
}

func TestGrantValidateMismatchAndScope(t *testing.T) {
	g, _ := DecodeGrant(encodeTestGrant(grantedFixture()))
	if err := g.Validate(GrantExpectations{RequestID: "other"}); err == nil {
		t.Error("expected request id mismatch error")
	}
	if err := g.Validate(GrantExpectations{ModuleID: "other"}); err == nil {
		t.Error("expected module id mismatch error")
	}
	if err := g.Validate(GrantExpectations{ExpectedDomain: "other.domain"}); err == nil {
		t.Error("expected domain mismatch error")
	}
	// Granted timeout (30000) exceeds a smaller requested cap.
	if err := g.Validate(GrantExpectations{RequestedTimeoutMs: 10_000}); err == nil {
		t.Error("expected granted-timeout-exceeds-requested error")
	}
}

func TestGrantValidateMissingCID(t *testing.T) {
	f := grantedFixture()
	f.moduleCID = ""
	g, _ := DecodeGrant(encodeTestGrant(f))
	if err := g.Validate(GrantExpectations{RequestID: "req-1"}); err == nil {
		t.Error("expected missing-CID error")
	}
}
