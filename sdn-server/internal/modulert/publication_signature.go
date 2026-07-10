package modulert

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/MBL"
	flatbuffers "github.com/google/flatbuffers/go"
)

// Module publication-trailer signature verification (loop I1 — defensive
// hardening, fail closed). Module artifacts produced by the module SDK
// (space-data-module-sdk/src/bundle/signing.js, signModuleArtifact) carry a
// detached Ed25519 signature as a "signature"-role entry inside the MBL
// bundle listing embedded in the artifact's appended SDS $REC publication
// trailer (see publication.go and
// space-data-module-sdk/docs/module-publication-standard.md). The signed
// message is the raw SHA-256 digest of the *portable* (trailer-stripped)
// wasm payload — the exact identity ContentHashHex already computes for the
// capability policy (capability_policy.go). This file recovers that
// signature at load time and checks it against an operator-supplied trusted
// signer set, using the same primitive (crypto/ed25519, 32-byte hex public
// keys, 64-byte hex signatures) internal/license/publish_protocol.go
// already uses to verify admin-wallet module-publish requests — this is a
// second *gate* over the same key/algorithm family, not a second signing
// scheme. See ModuleSignaturePolicy for how a node opts in and the
// allowlist escape hatch.

const (
	moduleSignatureAlgorithm   = "ed25519"
	moduleSignatureEntryID     = "signature"
	moduleSignatureSectionName = "sds.signature"
)

// moduleSignaturePayload mirrors the JSON object
// space-data-module-sdk/src/bundle/signing.js's signModuleArtifact embeds as
// the "signature"-role MBL bundle entry payload. Field names/casing must
// match the SDK exactly for interop with SDK-signed artifacts.
type moduleSignaturePayload struct {
	Algorithm           string `json:"algorithm"`
	KeyID               string `json:"keyId"`
	PublicKeyHex        string `json:"publicKeyHex"`
	SignatureHex        string `json:"signatureHex"`
	SignedHashHex       string `json:"signedHashHex"`
	SignedHashAlgorithm string `json:"signedHashAlgorithm"`
}

// ModuleSignaturePolicy is the operator-controlled trust policy for
// publication-trailer module signatures, consulted by every
// modulert.NewModule call via NodeContext.ModuleSignaturePolicy (mirrors
// CapabilityPolicy's shape/wiring — see hostbridge.go).
//
// A nil *ModuleSignaturePolicy (the NodeContext zero value) means signature
// enforcement is not configured for this node: the publication trailer is
// still always stripped before wasm compilation, but no signature is
// required. This preserves today's behavior for callers that have not
// opted in (mirrors the module SDK's own resolveModuleSignaturePolicy:
// "no policy configured" => unverified load) — it is the coordinator's
// node.go wiring that must attach a real policy for production nodes to
// actually enforce this gate.
//
// A non-nil policy is fail closed: TrustedSigners defaults to empty (no
// signer is trusted) and AllowUnsignedByContentHash defaults to empty (no
// bypass) — an empty-but-non-nil policy rejects every artifact, signed or
// not, until an operator explicitly trusts a signer or allowlists a
// content hash.
type ModuleSignaturePolicy struct {
	// TrustedSigners is the set of Ed25519 publisher public keys a
	// publication-trailer signature must chain to in order to be
	// Verified. This is the same signer-key model
	// publish_protocol.go uses for admin-wallet publish requests
	// (Ed25519 keys bound to an admin xpub via ModulePublishAuthorizer) —
	// an operator populates this with the same admin/publisher wallet
	// key(s) so a publish_protocol-authorized signer and a load-time
	// trusted signer are the same identity, not a parallel trust root.
	TrustedSigners []ed25519.PublicKey

	// AllowUnsignedByContentHash is the explicit dev/local escape hatch:
	// a module whose portable-payload content hash (ContentHashHex
	// semantics, lowercase hex SHA-256) is a key in this map loads even
	// when unsigned or signed by an untrusted key. Every bypass is logged
	// at Warn level (see enforceModuleSignaturePolicy) so production
	// operators can grep for it. Empty by default — production stays
	// fail closed. MUST NOT be populated outside local development.
	AllowUnsignedByContentHash map[string]bool
}

// ModuleSignatureStatus reports the outcome of publication-trailer
// signature verification for one module artifact. Zero value means
// verification was never attempted.
type ModuleSignatureStatus struct {
	// Signed reports whether the artifact carried a recognizable
	// signature entry at all.
	Signed bool
	// Verified reports whether a present signature checked out against a
	// trusted signer key. Always false when Signed is false.
	Verified bool
	// ContentHash is the lowercase hex SHA-256 of the portable
	// (trailer-stripped) wasm payload — the same identity ContentHashHex
	// computes for the capability policy.
	ContentHash string
	// SignerPubKeyHex is the lowercase hex Ed25519 public key that
	// produced the signature, when present, regardless of trust.
	SignerPubKeyHex string
	// KeyID is the optional signer-supplied key identifier from the
	// signature entry.
	KeyID string
	// Reason is a short machine-checkable explanation: "unsigned", "ok",
	// "untrusted_signer", "invalid_signature", "hash_mismatch",
	// "invalid_trailer", "unsupported_algorithm", "invalid_public_key",
	// "invalid_signature_payload".
	Reason string
}

// verifyPublicationSignature inspects wasmBytes (the raw artifact bytes,
// trailer included when present) for an SDS $REC publication trailer
// carrying an MBL "signature" bundle entry, and checks it against
// trustedKeys. It always returns the portable (trailer-stripped) payload —
// the exact bytes the runtime instantiates and capability policy hashes —
// regardless of verification outcome, so callers can strip the trailer even
// when not enforcing.
//
// trustedKeys empty means no signer is trusted: a signed artifact comes
// back Verified=false/Reason="untrusted_signer"; an unsigned artifact comes
// back Signed=false/Reason="unsigned" either way.
//
// err is non-nil exactly when Verified is false and Signed the artifact
// (or the trailer itself) could not be trusted as-is; it is nil for the
// "no policy configured, don't care" unsigned case so callers that ignore
// enforcement never need to unwrap it.
func verifyPublicationSignature(wasmBytes []byte, trustedKeys []ed25519.PublicKey) (portable []byte, status ModuleSignatureStatus, err error) {
	portable = StripPublicationTrailer(wasmBytes)
	sum := sha256.Sum256(portable)
	status.ContentHash = hex.EncodeToString(sum[:])

	recBytes := PublicationTrailerRecordBytes(wasmBytes)
	if recBytes == nil {
		status.Reason = "unsigned"
		return portable, status, nil
	}

	entryPayload, findErr := findModuleSignatureEntry(recBytes)
	if findErr != nil {
		status.Reason = "invalid_trailer"
		return portable, status, findErr
	}
	if entryPayload == nil {
		status.Reason = "unsigned"
		return portable, status, nil
	}
	status.Signed = true

	var payload moduleSignaturePayload
	if jsonErr := json.Unmarshal(entryPayload, &payload); jsonErr != nil {
		status.Reason = "invalid_signature_payload"
		return portable, status, fmt.Errorf("module signature entry payload is not valid JSON: %w", jsonErr)
	}
	if !strings.EqualFold(strings.TrimSpace(payload.Algorithm), moduleSignatureAlgorithm) {
		status.Reason = "unsupported_algorithm"
		return portable, status, fmt.Errorf("unsupported module signature algorithm %q", payload.Algorithm)
	}
	status.KeyID = payload.KeyID

	sigBytes, decErr := hex.DecodeString(strings.TrimSpace(payload.SignatureHex))
	if decErr != nil || len(sigBytes) != ed25519.SignatureSize {
		status.Reason = "invalid_signature"
		return portable, status, errors.New("module signature must be a 64-byte hex Ed25519 signature")
	}
	if isAllZeroBytes(sigBytes) {
		status.Reason = "invalid_signature"
		return portable, status, errors.New("module signature must not be all zeroes")
	}

	pubKeyHex := strings.ToLower(strings.TrimSpace(payload.PublicKeyHex))
	pubKeyBytes, decErr := hex.DecodeString(pubKeyHex)
	if decErr != nil || len(pubKeyBytes) != ed25519.PublicKeySize {
		status.Reason = "invalid_public_key"
		return portable, status, errors.New("module signer public key must be 32-byte hex")
	}
	status.SignerPubKeyHex = pubKeyHex

	if signedHash := strings.ToLower(strings.TrimSpace(payload.SignedHashHex)); signedHash != "" && signedHash != status.ContentHash {
		status.Reason = "hash_mismatch"
		return portable, status, fmt.Errorf("module signature covers content hash %s, portable artifact hashes to %s", signedHash, status.ContentHash)
	}

	trustedSigner := false
	for _, key := range trustedKeys {
		if len(key) == ed25519.PublicKeySize && hex.EncodeToString(key) == pubKeyHex {
			trustedSigner = true
			break
		}
	}
	if !trustedSigner {
		status.Reason = "untrusted_signer"
		return portable, status, fmt.Errorf("module signer %s is not a trusted publisher key", pubKeyHex)
	}

	if !ed25519.Verify(ed25519.PublicKey(pubKeyBytes), sum[:], sigBytes) {
		status.Reason = "invalid_signature"
		return portable, status, errors.New("module publication signature verification failed")
	}

	status.Verified = true
	status.Reason = "ok"
	return portable, status, nil
}

// enforceModuleSignaturePolicy is the load/install gate (loop I1): called
// once per NewModule/Load, before any wasm compilation or execution. See
// ModuleSignaturePolicy's doc for the nil-policy / fail-closed semantics.
func enforceModuleSignaturePolicy(policy *ModuleSignaturePolicy, wasmBytes []byte) ([]byte, ModuleSignatureStatus, error) {
	var trusted []ed25519.PublicKey
	if policy != nil {
		trusted = policy.TrustedSigners
	}
	portable, status, verifyErr := verifyPublicationSignature(wasmBytes, trusted)
	if policy == nil {
		// Enforcement not configured for this node: the trailer is still
		// stripped (never compiled as wasm) but nothing gates on it.
		return portable, status, nil
	}
	if status.Verified {
		return portable, status, nil
	}
	if policy.AllowUnsignedByContentHash[status.ContentHash] {
		log.Warnf(
			"module signature bypass: content_hash=%s reason=%q — explicitly allowlisted via NodeContext.ModuleSignaturePolicy.AllowUnsignedByContentHash for local development; this MUST NOT be set in production",
			status.ContentHash, status.Reason,
		)
		return portable, status, nil
	}
	if verifyErr != nil {
		return nil, status, fmt.Errorf("module publication signature rejected (content_hash=%s): %w", status.ContentHash, verifyErr)
	}
	return nil, status, fmt.Errorf(
		"module publication signature rejected (content_hash=%s, reason=%s): unsigned/untrusted-signer artifacts are refused by default — trust the signer or add content_hash=%s to ModuleSignaturePolicy.AllowUnsignedByContentHash for local development only",
		status.ContentHash, status.Reason, status.ContentHash,
	)
}

// recTrailerRecordType mirrors REC.fbs's RecordType enum value for "MBL"
// (see third_party/spacedatastandards-go/REC/RecordType.go). The REC.fbs
// collection wrapper (root table + heterogeneous Record union vector) is
// hand-parsed here with the raw flatbuffers primitives instead of importing
// the generated github.com/DigitalArsenal/spacedatastandards.org/lib/go/REC
// package: that vendored package currently fails to build on its own
// (duplicate method declarations from a codegen bug — PRW.Init, SDL.*,
// SPP.DataLength — unrelated to modulert, out of this file's scope to fix,
// see third_party/spacedatastandards-go/REC). The MBL/ModuleBundleEntry
// sub-package builds fine and is used normally below. The hand-rolled
// recRoot/recRecord readers implement exactly the same vtable-offset
// contract REC.go's generated GetRootAsREC/Record accessors do (same
// FlatBuffers wire format, just read directly).
const recRecordTypeMBL byte = 80

// recRoot is a minimal reader for the REC.fbs collection wrapper's root
// table: field 0 (vtable slot 4) is the version string, field 1 (vtable
// slot 6) is the RECORDS vector of heterogeneous Record tables.
type recRoot struct {
	tab flatbuffers.Table
}

func getRootAsRECTrailer(buf []byte) recRoot {
	n := flatbuffers.GetUOffsetT(buf)
	return recRoot{tab: flatbuffers.Table{Bytes: buf, Pos: n}}
}

func (r recRoot) recordsLength() int {
	if o := flatbuffers.UOffsetT(r.tab.Offset(6)); o != 0 {
		return r.tab.VectorLen(o)
	}
	return 0
}

func (r recRoot) record(j int) (recRecord, bool) {
	o := flatbuffers.UOffsetT(r.tab.Offset(6))
	if o == 0 {
		return recRecord{}, false
	}
	x := r.tab.Vector(o)
	x += flatbuffers.UOffsetT(j) * 4
	x = r.tab.Indirect(x)
	return recRecord{tab: flatbuffers.Table{Bytes: r.tab.Bytes, Pos: x}}, true
}

// recRecord is a minimal reader for REC.fbs's Record wrapper: field 0
// (vtable slot 4) is value_type (byte RecordType), field 1 (vtable slot 6)
// is the value union table, field 2 (vtable slot 8) is the standard string
// (unused here).
type recRecord struct {
	tab flatbuffers.Table
}

func (r recRecord) valueType() byte {
	if o := flatbuffers.UOffsetT(r.tab.Offset(4)); o != 0 {
		return r.tab.GetByte(o + r.tab.Pos)
	}
	return 0
}

func (r recRecord) value() (flatbuffers.Table, bool) {
	o := flatbuffers.UOffsetT(r.tab.Offset(6))
	if o == 0 {
		return flatbuffers.Table{}, false
	}
	var out flatbuffers.Table
	r.tab.Union(&out, o)
	return out, true
}

// findModuleSignatureEntry parses recBytes as an SDS $REC record collection
// and returns the raw payload bytes of the MBL bundle's "signature"-role
// entry, or nil if the trailer has no MBL record or no signature entry
// (both are "unsigned", not errors). recBytes comes from an untrusted wasm
// artifact, so every flatbuffers accessor call is guarded by recover: a
// crafted/corrupt trailer must produce an error, never a runtime panic that
// would take the whole load path down with it.
func findModuleSignatureEntry(recBytes []byte) (payload []byte, err error) {
	defer func() {
		if r := recover(); r != nil {
			payload = nil
			err = fmt.Errorf("malformed publication trailer record collection: %v", r)
		}
	}()

	if len(recBytes) < 4 || !flatbuffers.BufferHasIdentifier(recBytes, publicationTrailerMagic) {
		return nil, errors.New("publication trailer record collection missing $REC identifier")
	}
	rec := getRootAsRECTrailer(recBytes)

	for i := 0; i < rec.recordsLength(); i++ {
		record, ok := rec.record(i)
		if !ok {
			continue
		}
		if record.valueType() != recRecordTypeMBL {
			continue
		}
		mblTable, ok := record.value()
		if !ok {
			continue
		}
		mbl := &MBL.MBL{}
		mbl.Init(mblTable.Bytes, mblTable.Pos)

		var entry MBL.ModuleBundleEntry
		for j := 0; j < mbl.EntriesLength(); j++ {
			if !mbl.Entries(&entry, j) {
				continue
			}
			entryID := strings.ToLower(strings.TrimSpace(string(entry.EntryId())))
			sectionName := strings.ToLower(strings.TrimSpace(string(entry.SectionName())))
			if entry.Role() == MBL.ModuleBundleEntryRoleSIGNATURE ||
				entryID == moduleSignatureEntryID ||
				sectionName == moduleSignatureSectionName {
				return append([]byte(nil), entry.PayloadBytes()...), nil
			}
		}
		// An MBL record was found but carries no signature entry: unsigned,
		// not an error. Publication trailers carry exactly one MBL record
		// per the module-publication-standard, so stop here.
		return nil, nil
	}
	return nil, nil
}

func isAllZeroBytes(b []byte) bool {
	for _, v := range b {
		if v != 0 {
			return false
		}
	}
	return true
}
