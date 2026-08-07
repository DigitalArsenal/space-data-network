package modulert

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/MBL"
	flatbuffers "github.com/google/flatbuffers/go"
	"github.com/spacedatanetwork/sdn-server/internal/license"
	"github.com/spacedatanetwork/sdn-server/internal/testsupport"
)

// --- test fixture construction -------------------------------------------
//
// buildSignedModuleArtifact/buildRECTrailerWithMBLSignature hand-build the
// exact on-wire shape space-data-module-sdk/src/bundle/signing.js's
// signModuleArtifact produces: a portable payload followed by an SDS $REC
// trailer carrying one MBL record whose "signature" entry is a JSON
// envelope {algorithm, keyId, publicKeyHex, signatureHex, signedHashHex,
// signedHashAlgorithm}. The REC/Record wrapper itself is hand-built with
// raw flatbuffers.Builder calls (mirroring what the generated REC.go
// builder functions would emit) because the vendored
// spacedatastandards.org REC Go package fails to build on its own — see
// findModuleSignatureEntry's doc comment in publication_signature.go.

func mustGenerateEd25519Key(t *testing.T) (ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("ed25519.GenerateKey failed: %v", err)
	}
	return pub, priv
}

// recRecordTypeMBLCurrent is the RecordType ordinal MBL happens to hold TODAY
// (SDS v1.183.0, schema/REC/RECORDTYPE_ORDINALS.json). It exists ONLY so the
// fixtures below can write a realistic byte and so the ordinal-independence
// tests have something to vary. Production code must never compare against it.
const recRecordTypeMBLCurrent byte = 80

// recRecordTypeMBLLegacy is the ordinal MBL held before the 2026-07-08 union
// renumber, and is what every artifact published on or before 2026-07-10 still
// carries on disk. A reader that keys on the ordinal cannot see these at all.
const recRecordTypeMBLLegacy byte = 67

func buildRECTrailerWithMBLSignature(t *testing.T, signaturePayloadJSON []byte) []byte {
	t.Helper()
	return buildRECTrailerWithMBLSignatureAs(t, signaturePayloadJSON, recRecordTypeMBLCurrent, "MBL")
}

// buildRECTrailerWithMBLSignatureAs builds the same trailer with a caller-chosen
// union ordinal and standard string, so a test can prove which of the two the
// verifier actually keys on.
func buildRECTrailerWithMBLSignatureAs(t *testing.T, signaturePayloadJSON []byte, valueType byte, standard string) []byte {
	t.Helper()

	b := flatbuffers.NewBuilder(512)

	entryIDOff := b.CreateString(moduleSignatureEntryID)
	payloadOff := b.CreateByteVector(signaturePayloadJSON)

	MBL.ModuleBundleEntryStart(b)
	MBL.ModuleBundleEntryAddEntryId(b, entryIDOff)
	MBL.ModuleBundleEntryAddRole(b, MBL.ModuleBundleEntryRoleSIGNATURE)
	MBL.ModuleBundleEntryAddPayloadEncoding(b, MBL.ModulePayloadEncodingJSON_UTF8)
	MBL.ModuleBundleEntryAddPayload(b, payloadOff)
	entryOff := MBL.ModuleBundleEntryEnd(b)

	MBL.MBLStartEntriesVector(b, 1)
	b.PrependUOffsetT(entryOff)
	entriesVecOff := b.EndVector(1)

	MBL.MBLStart(b)
	MBL.MBLAddEntries(b, entriesVecOff)
	mblOff := MBL.MBLEnd(b)

	standardOff := b.CreateString(standard)

	// Hand-rolled REC.fbs "Record" wrapper: value_type=valueType, value=mblOff,
	// standard=standard. The verifier keys on the STANDARD; value_type is
	// written only because a real publisher writes it.
	b.StartObject(3)
	b.PrependUOffsetTSlot(2, standardOff, 0)
	b.PrependUOffsetTSlot(1, mblOff, 0)
	b.PrependByteSlot(0, valueType, 0)
	recordOff := b.EndObject()

	b.StartVector(4, 1, 4)
	b.PrependUOffsetT(recordOff)
	recordsVecOff := b.EndVector(1)

	versionOff := b.CreateString("1.0.0")

	// Hand-rolled REC.fbs "REC" root wrapper: version=versionOff,
	// RECORDS=recordsVecOff.
	b.StartObject(2)
	b.PrependUOffsetTSlot(1, recordsVecOff, 0)
	b.PrependUOffsetTSlot(0, versionOff, 0)
	recOff := b.EndObject()
	b.FinishWithFileIdentifier(recOff, []byte(publicationTrailerMagic))

	return b.FinishedBytes()
}

// buildSignedModuleArtifact appends a publication trailer over portable,
// signed by signer, in the same shape verifyPublicationSignature expects.
func buildSignedModuleArtifact(t *testing.T, portable []byte, signer ed25519.PrivateKey, keyID string) []byte {
	t.Helper()

	pub, ok := signer.Public().(ed25519.PublicKey)
	if !ok {
		t.Fatalf("signer.Public() did not return an ed25519.PublicKey")
	}
	sum := sha256.Sum256(portable)
	sig := ed25519.Sign(signer, sum[:])

	payload := moduleSignaturePayload{
		Algorithm:           moduleSignatureAlgorithm,
		KeyID:               keyID,
		PublicKeyHex:        hex.EncodeToString(pub),
		SignatureHex:        hex.EncodeToString(sig),
		SignedHashHex:       hex.EncodeToString(sum[:]),
		SignedHashAlgorithm: "sha256-canonical-module-hash",
	}
	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal signature payload: %v", err)
	}

	recBytes := buildRECTrailerWithMBLSignature(t, payloadJSON)
	// appendPublicationTrailer is defined in publication_test.go (same
	// package) and produces exactly the
	// "payload || REC-bytes || uint32le(len) || $REC" layout
	// StripPublicationTrailer expects.
	return appendPublicationTrailer(portable, recBytes)
}

// --- verifyPublicationSignature / enforceModuleSignaturePolicy -----------

func TestVerifyPublicationSignatureAcceptsCorrectlySignedArtifact(t *testing.T) {
	pub, priv := mustGenerateEd25519Key(t)
	artifact := buildSignedModuleArtifact(t, wasmHeader, priv, "test-key-1")

	portable, status, err := verifyPublicationSignature(artifact, []ed25519.PublicKey{pub})
	if err != nil {
		t.Fatalf("verifyPublicationSignature() error = %v", err)
	}
	if !status.Signed || !status.Verified {
		t.Fatalf("status = %+v, want Signed=true Verified=true", status)
	}
	if status.Reason != "ok" {
		t.Fatalf("status.Reason = %q, want %q", status.Reason, "ok")
	}
	if !bytesEqual(portable, wasmHeader) {
		t.Fatalf("portable = %x, want %x", portable, wasmHeader)
	}
	if got, want := status.SignerPubKeyHex, hex.EncodeToString(pub); got != want {
		t.Fatalf("status.SignerPubKeyHex = %q, want %q", got, want)
	}
}

func TestEnforceModuleSignaturePolicyRejectsUnsignedUnlessAllowlisted(t *testing.T) {
	unsigned := append([]byte(nil), wasmHeader...)
	sum := sha256.Sum256(unsigned)
	contentHash := hex.EncodeToString(sum[:])

	// Default posture with a configured (non-nil) policy and no allowlist
	// entry: reject.
	policy := &ModuleSignaturePolicy{}
	if _, _, err := enforceModuleSignaturePolicy(policy, unsigned); err == nil {
		t.Fatal("enforceModuleSignaturePolicy() with empty policy = nil error, want rejection")
	}

	// Explicit dev/local allowlist escape: same artifact now loads, and the
	// outcome is observable via ModuleSignatureStatus (Signed=false,
	// Verified=false, Reason="unsigned" — the bypass, not a false claim of
	// authenticity) — the bypass itself is also logged (log.Warnf in
	// enforceModuleSignaturePolicy).
	policy.AllowUnsignedByContentHash = map[string]bool{contentHash: true}
	portable, status, err := enforceModuleSignaturePolicy(policy, unsigned)
	if err != nil {
		t.Fatalf("enforceModuleSignaturePolicy() with allowlisted content hash failed: %v", err)
	}
	if status.Signed || status.Verified {
		t.Fatalf("status = %+v, want Signed=false Verified=false (bypass, not authenticity)", status)
	}
	if !bytesEqual(portable, unsigned) {
		t.Fatalf("portable = %x, want %x", portable, unsigned)
	}

	// nil policy (enforcement not configured for this node): pass-through,
	// same as today's unenforced behavior — no error either way.
	if _, _, err := enforceModuleSignaturePolicy(nil, unsigned); err != nil {
		t.Fatalf("enforceModuleSignaturePolicy(nil, ...) = %v, want nil (enforcement not configured)", err)
	}
}

func TestVerifyPublicationSignatureRejectsTamperedPortable(t *testing.T) {
	pub, priv := mustGenerateEd25519Key(t)
	portable := append([]byte(nil), wasmHeader...)
	artifact := buildSignedModuleArtifact(t, portable, priv, "test-key-2")

	// Flip a byte inside the portable region, keep the original trailer —
	// the signature was computed over the old portable bytes.
	tampered := append([]byte(nil), artifact...)
	tampered[0] ^= 0xFF

	_, status, err := verifyPublicationSignature(tampered, []ed25519.PublicKey{pub})
	if err == nil {
		t.Fatal("verifyPublicationSignature() on tampered artifact = nil error, want rejection")
	}
	if status.Verified {
		t.Fatalf("status.Verified = true on tampered artifact, want false")
	}
	if status.Reason != "hash_mismatch" {
		t.Fatalf("status.Reason = %q, want %q", status.Reason, "hash_mismatch")
	}
}

func TestVerifyPublicationSignatureRejectsWrongSigner(t *testing.T) {
	_, signerPriv := mustGenerateEd25519Key(t)
	trustedPub, _ := mustGenerateEd25519Key(t) // a different keypair entirely

	artifact := buildSignedModuleArtifact(t, wasmHeader, signerPriv, "test-key-3")

	_, status, err := verifyPublicationSignature(artifact, []ed25519.PublicKey{trustedPub})
	if err == nil {
		t.Fatal("verifyPublicationSignature() with wrong-signer artifact = nil error, want rejection")
	}
	if status.Verified {
		t.Fatalf("status.Verified = true for an untrusted signer, want false")
	}
	if status.Reason != "untrusted_signer" {
		t.Fatalf("status.Reason = %q, want %q", status.Reason, "untrusted_signer")
	}
}

// TestModuleSignatureReconcilesWithPublishProtocolSignerKey demonstrates
// that publication-trailer verification here and
// license.VerifyModulePublishRequest (publish_protocol.go) trust the same
// signer-key model: one Ed25519 admin-wallet keypair is both (a) accepted
// by VerifyModulePublishRequest as an authorized admin publisher and (b)
// accepted here as a trusted module-artifact signer. This is the
// reconciliation point the task calls for — not a shared wire format
// (publish_protocol signs a batch publish *request*; the publication
// trailer signs one module *artifact* — genuinely different objects) but a
// shared identity and primitive: crypto/ed25519, 32-byte hex public keys,
// 64-byte hex signatures, the same admin/publisher wallet key.
func TestModuleSignatureReconcilesWithPublishProtocolSignerKey(t *testing.T) {
	adminPub, adminPriv := mustGenerateEd25519Key(t)

	// (a) publish_protocol.go's admin-wallet scheme accepts this key.
	req := &license.ModulePublishRequest{
		IssuedAtMs: 1,
		Nonce:      "reconciliation-test-nonce",
		Modules: []license.ModulePublishEntry{{
			ID:              "com.example.reconciliation-test",
			Version:         "1.0.0",
			EncryptedBundle: []byte{0x01},
			KeyMaterial:     []byte{0x02},
		}},
	}
	if err := license.SignModulePublishRequest(req, "xpub-admin-test", adminPub, adminPriv); err != nil {
		t.Fatalf("SignModulePublishRequest failed: %v", err)
	}
	authorizer := func(xpub string) (license.ModulePublishPrincipal, error) {
		return license.ModulePublishPrincipal{
			XPub:             xpub,
			SigningPubKeyHex: hex.EncodeToString(adminPub),
			Admin:            true,
		}, nil
	}
	if err := license.VerifyModulePublishRequest(*req, authorizer); err != nil {
		t.Fatalf("VerifyModulePublishRequest() with admin wallet key failed: %v", err)
	}

	// (b) the SAME keypair, trusted as a module-artifact signer here, loads.
	artifact := buildSignedModuleArtifact(t, wasmHeader, adminPriv, "admin-wallet-key")
	_, status, err := verifyPublicationSignature(artifact, []ed25519.PublicKey{adminPub})
	if err != nil {
		t.Fatalf("verifyPublicationSignature() with the publish_protocol admin key failed: %v", err)
	}
	if !status.Verified {
		t.Fatalf("status.Verified = false, want true for the reconciled admin wallet key")
	}
}

func bytesEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// --- entrypoint wiring (NewModule) -----------------------------------

// TestNewModuleAppliesSignatureGateAtLoadEntrypoint exercises the actual
// load entrypoint (NewModule -> instantiateWASM) against a real module
// artifact, confirming the signature gate runs before the module is
// admitted: a signed artifact from a trusted signer loads and reports
// Verified=true via Module.SignatureStatus(); the same artifact unsigned,
// under an otherwise-identical (capability-preapproved) policy, is refused
// — and the rejection is attributable to the signature gate, not the
// capability gate, since it runs first.
func TestNewModuleAppliesSignatureGateAtLoadEntrypoint(t *testing.T) {
	t.Parallel()

	wasmPath := testsupport.SkipIfNoLicensingModuleWasm(t)
	wasmBytes, err := os.ReadFile(wasmPath)
	if err != nil {
		t.Fatalf("ReadFile(%q) failed: %v", wasmPath, err)
	}

	moduleHash := ContentHashHex(wasmBytes)
	capPolicy, err := NewCapabilityPolicyStore("")
	if err != nil {
		t.Fatalf("NewCapabilityPolicyStore failed: %v", err)
	}
	for _, capability := range []string{"ipfs", "protocol_dial", "wallet_sign"} {
		if _, err := capPolicy.Approve(CapabilityApproval{
			ModuleHash: moduleHash,
			Capability: capability,
			PluginID:   "licensing",
			ApprovedBy: "test",
		}); err != nil {
			t.Fatalf("Approve(%s) failed: %v", capability, err)
		}
	}

	pub, priv := mustGenerateEd25519Key(t)
	sigPolicy := &ModuleSignaturePolicy{TrustedSigners: []ed25519.PublicKey{pub}}

	signedArtifact := buildSignedModuleArtifact(t, wasmBytes, priv, "licensing-test-key")
	mod, err := NewModule(signedArtifact, nil, &NodeContext{
		CapabilityPolicy:      capPolicy,
		ModuleSignaturePolicy: sigPolicy,
	})
	if err != nil {
		t.Fatalf("NewModule(signed artifact, trusted signer) failed: %v", err)
	}
	defer func() {
		if closeErr := mod.Close(); closeErr != nil {
			t.Fatalf("Close() failed: %v", closeErr)
		}
	}()
	if got := mod.SignatureStatus(); !got.Verified {
		t.Fatalf("mod.SignatureStatus() = %+v, want Verified=true", got)
	}

	_, err = NewModule(wasmBytes, nil, &NodeContext{
		CapabilityPolicy:      capPolicy,
		ModuleSignaturePolicy: sigPolicy,
	})
	if err == nil {
		t.Fatal("NewModule(unsigned artifact, configured signature policy) = nil error, want rejection")
	}
	if !strings.Contains(err.Error(), "publication signature") {
		t.Fatalf("expected rejection to name the publication signature gate, got: %v", err)
	}
}
