package modulert

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"sort"
	"strings"
	"testing"

	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/MBL"
	flatbuffers "github.com/google/flatbuffers/go"
	"github.com/ipfs/kubo/sdn/license"
	"github.com/ipfs/kubo/sdn/testsupport"
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

func buildRECTrailerWithMBLSignature(t *testing.T, signaturePayloadJSON []byte) []byte {
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

	standardOff := b.CreateString("MBL")

	// Hand-rolled REC.fbs "Record" wrapper: value_type=MBL(80), value=mblOff,
	// standard="MBL".
	b.StartObject(3)
	b.PrependUOffsetTSlot(2, standardOff, 0)
	b.PrependUOffsetTSlot(1, mblOff, 0)
	b.PrependByteSlot(0, recRecordTypeMBL, 0)
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

type testBundleMember struct {
	ID          string
	Role        MBL.ModuleBundleEntryRole
	SectionName string
	Encoding    MBL.ModulePayloadEncoding
	MediaType   string
	Flags       uint32
	Payload     []byte
	Description string
}

func bundleMemberStatement(member testBundleMember) map[string]interface{} {
	sum := sha256.Sum256(member.Payload)
	return map[string]interface{}{
		"entryId":         member.ID,
		"role":            int(member.Role),
		"sectionName":     member.SectionName,
		"typeRef":         nil,
		"payloadEncoding": int(member.Encoding),
		"mediaType":       member.MediaType,
		"flags":           int(member.Flags),
		"sha256Hex":       hex.EncodeToString(sum[:]),
		"payloadLength":   len(member.Payload),
		"description":     member.Description,
	}
}

func sdkBundleSignatureDigest(t *testing.T, portable []byte, members []testBundleMember) []byte {
	t.Helper()
	sorted := append([]testBundleMember(nil), members...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].ID < sorted[j].ID })
	entries := make([]map[string]interface{}, 0, len(sorted))
	for _, member := range sorted {
		entries = append(entries, bundleMemberStatement(member))
	}
	moduleHash := sha256.Sum256(portable)
	manifestHashHex := ""
	for _, member := range members {
		if member.ID == "manifest" || member.Role == MBL.ModuleBundleEntryRoleMANIFEST {
			sum := sha256.Sum256(member.Payload)
			manifestHashHex = hex.EncodeToString(sum[:])
			break
		}
	}
	statement := map[string]interface{}{
		"version":       1,
		"bundleVersion": 1,
		"moduleFormat":  "space-data-module",
		"canonicalization": map[string]interface{}{
			"version":                     1,
			"strippedCustomSectionPrefix": "sds.",
			"bundleSectionName":           "rec.mbl",
			"hashAlgorithm":               "sha256",
		},
		"canonicalModuleHashHex": hex.EncodeToString(moduleHash[:]),
		"manifestHashHex":        manifestHashHex,
		"manifestExportSymbol":   "plugin_get_manifest_flatbuffer",
		"manifestSizeSymbol":     "plugin_get_manifest_flatbuffer_size",
		"entries":                entries,
	}
	canonical, err := json.Marshal(statement)
	if err != nil {
		t.Fatalf("marshal SDK bundle statement: %v", err)
	}
	digest := sha256.Sum256(canonical)
	return digest[:]
}

func buildSDKBundleScopedArtifact(t *testing.T, portable []byte, members []testBundleMember, signer ed25519.PrivateKey, preservedSignature []byte) ([]byte, []byte) {
	t.Helper()
	signatureJSON := append([]byte(nil), preservedSignature...)
	if signatureJSON == nil {
		pub := signer.Public().(ed25519.PublicKey)
		digest := sdkBundleSignatureDigest(t, portable, members)
		payload := moduleSignaturePayload{
			Algorithm:           moduleSignatureAlgorithm,
			KeyID:               "sdk-bundle-test",
			PublicKeyHex:        hex.EncodeToString(pub),
			SignatureHex:        hex.EncodeToString(ed25519.Sign(signer, digest)),
			SignedHashHex:       hex.EncodeToString(digest),
			SignedHashAlgorithm: "sha256-sdn-module-bundle-v1",
		}
		var err error
		signatureJSON, err = json.Marshal(payload)
		if err != nil {
			t.Fatalf("marshal bundle signature: %v", err)
		}
	}

	b := flatbuffers.NewBuilder(2048)
	all := append([]testBundleMember(nil), members...)
	all = append(all, testBundleMember{
		ID:          moduleSignatureEntryID,
		Role:        MBL.ModuleBundleEntryRoleSIGNATURE,
		SectionName: moduleSignatureSectionName,
		Encoding:    MBL.ModulePayloadEncodingJSON_UTF8,
		MediaType:   "application/json",
		Payload:     signatureJSON,
	})
	entryOffsets := make([]flatbuffers.UOffsetT, 0, len(all))
	for _, member := range all {
		id := b.CreateString(member.ID)
		section := b.CreateString(member.SectionName)
		mediaType := b.CreateString(member.MediaType)
		description := b.CreateString(member.Description)
		payload := b.CreateByteVector(member.Payload)
		hash := sha256.Sum256(member.Payload)
		hashOffset := b.CreateByteVector(hash[:])
		MBL.ModuleBundleEntryStart(b)
		MBL.ModuleBundleEntryAddEntryId(b, id)
		MBL.ModuleBundleEntryAddRole(b, member.Role)
		MBL.ModuleBundleEntryAddSectionName(b, section)
		MBL.ModuleBundleEntryAddPayloadEncoding(b, member.Encoding)
		MBL.ModuleBundleEntryAddMediaType(b, mediaType)
		MBL.ModuleBundleEntryAddFlags(b, member.Flags)
		MBL.ModuleBundleEntryAddSha256(b, hashOffset)
		MBL.ModuleBundleEntryAddPayload(b, payload)
		MBL.ModuleBundleEntryAddDescription(b, description)
		entryOffsets = append(entryOffsets, MBL.ModuleBundleEntryEnd(b))
	}

	MBL.MBLStartEntriesVector(b, len(entryOffsets))
	for i := len(entryOffsets) - 1; i >= 0; i-- {
		b.PrependUOffsetT(entryOffsets[i])
	}
	entriesOffset := b.EndVector(len(entryOffsets))
	prefix := b.CreateString("sds.")
	section := b.CreateString("rec.mbl")
	hashAlgorithm := b.CreateString("sha256")
	MBL.CanonicalizationRuleStart(b)
	MBL.CanonicalizationRuleAddVersion(b, 1)
	MBL.CanonicalizationRuleAddStrippedCustomSectionPrefix(b, prefix)
	MBL.CanonicalizationRuleAddBundleSectionName(b, section)
	MBL.CanonicalizationRuleAddHashAlgorithm(b, hashAlgorithm)
	canonicalization := MBL.CanonicalizationRuleEnd(b)
	moduleHash := sha256.Sum256(portable)
	moduleHashOffset := b.CreateByteVector(moduleHash[:])
	var manifestHash []byte
	for _, member := range members {
		if member.ID == "manifest" || member.Role == MBL.ModuleBundleEntryRoleMANIFEST {
			sum := sha256.Sum256(member.Payload)
			manifestHash = sum[:]
			break
		}
	}
	manifestHashOffset := b.CreateByteVector(manifestHash)
	format := b.CreateString("space-data-module")
	manifestExport := b.CreateString("plugin_get_manifest_flatbuffer")
	manifestSize := b.CreateString("plugin_get_manifest_flatbuffer_size")
	MBL.MBLStart(b)
	MBL.MBLAddBundleVersion(b, 1)
	MBL.MBLAddModuleFormat(b, format)
	MBL.MBLAddCanonicalization(b, canonicalization)
	MBL.MBLAddCanonicalModuleHash(b, moduleHashOffset)
	MBL.MBLAddManifestHash(b, manifestHashOffset)
	MBL.MBLAddManifestExportSymbol(b, manifestExport)
	MBL.MBLAddManifestSizeSymbol(b, manifestSize)
	MBL.MBLAddEntries(b, entriesOffset)
	mblOffset := MBL.MBLEnd(b)

	standard := b.CreateString("MBL")
	b.StartObject(3)
	b.PrependUOffsetTSlot(2, standard, 0)
	b.PrependUOffsetTSlot(1, mblOffset, 0)
	b.PrependByteSlot(0, recRecordTypeMBL, 0)
	recordOffset := b.EndObject()
	b.StartVector(4, 1, 4)
	b.PrependUOffsetT(recordOffset)
	recordsOffset := b.EndVector(1)
	version := b.CreateString("1.0.0")
	b.StartObject(2)
	b.PrependUOffsetTSlot(1, recordsOffset, 0)
	b.PrependUOffsetTSlot(0, version, 0)
	recOffset := b.EndObject()
	b.FinishWithFileIdentifier(recOffset, []byte(publicationTrailerMagic))
	return appendPublicationTrailer(portable, b.FinishedBytes()), signatureJSON
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

func TestVerifyModuleBundleAcceptsSDKBundleScopedMembers(t *testing.T) {
	pub, priv := mustGenerateEd25519Key(t)
	members := []testBundleMember{
		{ID: "app.app", Role: MBL.ModuleBundleEntryRoleAUXILIARY, SectionName: "sdn.app.record", Encoding: MBL.ModulePayloadEncodingFLATBUFFER, MediaType: "application/octet-stream", Payload: []byte{5, 6, 7, 8}, Description: "opaque app"},
		{ID: "flow.plg", Role: MBL.ModuleBundleEntryRoleMANIFEST, SectionName: "sdn.flow.plg", Encoding: MBL.ModulePayloadEncodingFLATBUFFER, MediaType: "application/octet-stream", Payload: []byte{1, 2, 3, 4}, Description: "opaque graph"},
	}
	artifact, _ := buildSDKBundleScopedArtifact(t, wasmHeader, members, priv, nil)

	portable, bundle, status, err := VerifyModuleBundle(artifact, []ed25519.PublicKey{pub})
	if err != nil {
		t.Fatalf("VerifyModuleBundle() error = %v", err)
	}
	if !bytesEqual(portable, wasmHeader) || !status.Verified || status.SignatureScope != "bundle" {
		t.Fatalf("portable/status = %x %+v", portable, status)
	}
	if len(bundle.Entries) != len(members) {
		t.Fatalf("verified member count = %d, want %d", len(bundle.Entries), len(members))
	}
	for i, want := range []string{"app.app", "flow.plg"} {
		if bundle.Entries[i].EntryID != want {
			t.Fatalf("entry %d id = %q, want %q", i, bundle.Entries[i].EntryID, want)
		}
	}
}

func TestVerifyModuleBundleRejectsRehashedMemberTampering(t *testing.T) {
	pub, priv := mustGenerateEd25519Key(t)
	members := []testBundleMember{{ID: "opaque.bin", Role: MBL.ModuleBundleEntryRoleAUXILIARY, SectionName: "sdn.test.opaque", Encoding: MBL.ModulePayloadEncodingRAW_BYTES, MediaType: "application/octet-stream", Payload: []byte{1, 2, 3}}}
	_, signature := buildSDKBundleScopedArtifact(t, wasmHeader, members, priv, nil)
	members[0].Payload = []byte{1, 2, 4}
	tampered, _ := buildSDKBundleScopedArtifact(t, wasmHeader, members, priv, signature)

	if _, _, _, err := VerifyModuleBundle(tampered, []ed25519.PublicKey{pub}); err == nil || !strings.Contains(err.Error(), "signed digest") {
		t.Fatalf("VerifyModuleBundle(rehashed tamper) error = %v, want signed digest rejection", err)
	}
}

func TestVerifyModuleBundleRejectsDuplicateEntryID(t *testing.T) {
	pub, priv := mustGenerateEd25519Key(t)
	original := []testBundleMember{{ID: "opaque.bin", Role: MBL.ModuleBundleEntryRoleAUXILIARY, Payload: []byte{1}}}
	_, signature := buildSDKBundleScopedArtifact(t, wasmHeader, original, priv, nil)
	duplicate := append(original, testBundleMember{ID: "opaque.bin", Role: MBL.ModuleBundleEntryRoleAUXILIARY, Payload: []byte{2}})
	artifact, _ := buildSDKBundleScopedArtifact(t, wasmHeader, duplicate, priv, signature)

	if _, _, _, err := VerifyModuleBundle(artifact, []ed25519.PublicKey{pub}); err == nil || !strings.Contains(err.Error(), "duplicate entryId") {
		t.Fatalf("VerifyModuleBundle(duplicate) error = %v, want duplicate entryId rejection", err)
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
