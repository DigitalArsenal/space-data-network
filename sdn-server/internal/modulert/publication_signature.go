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
	"github.com/spacedatanetwork/sdn-server/internal/sigdomain"
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
	// bundleSignatureHashAlgorithm labels the BUNDLE-SCOPE basis: the signature
	// covers a canonical statement over the whole MBL rather than the portable
	// payload digest. See publication_signature_bundle.go.
	bundleSignatureHashAlgorithm = "sha256-sdn-module-bundle-v1"

	signatureScopeModule = "module"
	signatureScopeBundle = "bundle"
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

	// StatementDomain names the DOMAIN-SEPARATED statement the signature
	// covers, and is the field the node's own signing endpoint sets
	// (internal/modulesign, Seal Council 2026-07-30). See
	// signedMessageForPayload for the two-form verification contract.
	StatementDomain string `json:"statementDomain"`
}

// signedMessageForPayload returns the exact bytes an artifact's signature must
// verify over, and is where the DOMAIN SEPARATION contract is enforced on the
// reading side.
//
// TWO FORMS, deliberately, and the choice is made by the artifact, not by
// policy:
//
//   - statementDomain PRESENT — the node-signed form (internal/modulesign).
//     The signature covers sigdomain.Statement(domain, contentHash). The domain
//     must be EXACTLY DomainModulePublicationV1: not merely "registered". A
//     registered-but-different domain — the update-manifest domain, say — is
//     refused here, which is what stops a signature minted for a signed update
//     from being stapled into a module trailer and admitted as a module.
//
//   - statementDomain ABSENT — the legacy SDK-signed form
//     (space-data-module-sdk/src/bundle/signing.js:361, ed25519Sign over the
//     bare hash bytes). Verification is byte-identical to what it has always
//     been, so every artifact already in the catalog keeps verifying and this
//     change has zero blast radius on the admit path.
//
// The asymmetry is intentional and is not a weakening: the legacy form remains
// ACCEPTABLE to the verifier, but the node's signer never PRODUCES it. Closing
// the cross-protocol oracle at the signer is what matters; the reader stays
// backward compatible until the SDK emits the domain and the ecosystem has
// re-signed (tracked as sdn-sdk-statement-domain-parity).
func signedMessageForPayload(payload moduleSignaturePayload, contentHash []byte) ([]byte, string, error) {
	domain := strings.TrimSpace(payload.StatementDomain)
	if domain == "" {
		return contentHash, "", nil
	}
	if domain != sigdomain.DomainModulePublicationV1 {
		return nil, domain, fmt.Errorf(
			"module signature declares statement domain %q; a module artifact must be signed under %q",
			domain, sigdomain.DomainModulePublicationV1)
	}
	statement, err := sigdomain.Statement(domain, contentHash)
	if err != nil {
		return nil, domain, err
	}
	return statement, domain, nil
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
// content hash — UNLESS ReportOnly is set, which is the observe stage
// (evaluate everything, refuse nothing). So there are three states, not
// two: nil = inert/no verification, ReportOnly = verified-and-logged, and
// enforcing = fail closed.
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

	// ReportOnly is the OBSERVE stage of the seal-council rollout
	// (graph/tasks/saw-module-signing-enforcement.md, owner ruling
	// 2026-07-30). When true this policy is attached and fully evaluated —
	// every artifact is verified and every would-be rejection is logged
	// with the machine-greppable token "module_signature_observe" carrying
	// content_hash, reason, and the OBSERVED signer/keyId — but nothing is
	// refused: enforceModuleSignaturePolicy admits the artifact anyway.
	//
	// This exists because a nil policy is INERT (no verification happens at
	// all, so an operator cannot discover what a flip would break) while a
	// non-nil enforcing policy is FAIL CLOSED (a flip with unsigned prod
	// artifacts in the catalog takes the node's modules down). ReportOnly
	// is the third state the rollout actually needs: full evaluation, zero
	// blast radius, so the rejection log can be drained to empty by
	// re-signing BEFORE enforcement is turned on.
	//
	// ReportOnly is NOT a bypass and MUST NOT be confused with
	// AllowUnsignedByContentHash: it blesses nothing permanently and
	// records no artifact into policy. Clearing this one field is the whole
	// enforce flip — which is deliberately a single, reversible step.
	ReportOnly bool
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
	// StatementDomain is the domain-separation label the signature declared,
	// or "" for the legacy bare-digest form. A non-empty value here means the
	// signature covered sigdomain.Statement(domain, ContentHash) rather than
	// the bare hash — see signedMessageForPayload.
	StatementDomain string
	// SignatureScope is "module" when the signature covers the portable-payload
	// digest (bare or domain separated) and "bundle" when it covers the whole
	// MBL. Empty when nothing was verified.
	SignatureScope string
	// SignedHash is the lowercase SHA-256 digest the signature actually
	// covered. For module scope that is ContentHash; for bundle scope it is the
	// canonical bundle statement digest, which is NOT ContentHash.
	SignedHash string
	// Reason is a short machine-checkable explanation: "unsigned", "ok",
	// "untrusted_signer", "invalid_signature", "hash_mismatch",
	// "invalid_trailer", "unsupported_algorithm", "invalid_public_key",
	// "invalid_signature_payload", "unsupported_statement_domain",
	// "unsupported_hash_algorithm", "invalid_bundle",
	// "ambiguous_signature_scope".
	Reason string
}

// decodeModuleSignaturePayload decodes the signature entry STRICTLY.
//
// Go's encoding/json matches field names case-insensitively. That is a
// cross-language divergence hiding in plain sight: a trailer carrying
// "StatementDomain" populates the Go struct and takes the domain path here,
// while the SDK's JS verifier reads payload.statementDomain, sees nothing, and
// falls to the legacy bare-digest path — same artifact, opposite verdicts, and
// the node is the permissive one. The wire form is the SDK's camelCase JSON and
// it is singular, so the fix belongs on the Go side: reject any key that is not
// spelled exactly as the SDK writes it, rather than teaching JS to accept
// case variants.
func decodeModuleSignaturePayload(entryPayload []byte, out *moduleSignaturePayload) error {
	if err := json.Unmarshal(entryPayload, out); err != nil {
		return fmt.Errorf("module signature entry payload is not valid JSON: %w", err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(entryPayload, &raw); err != nil {
		return fmt.Errorf("module signature entry payload is not a JSON object: %w", err)
	}
	known := map[string]bool{
		"algorithm": true, "keyId": true, "publicKeyHex": true,
		"signatureHex": true, "signedHashHex": true,
		"signedHashAlgorithm": true, "statementDomain": true,
	}
	for key := range raw {
		if known[key] {
			continue
		}
		for exact := range known {
			if strings.EqualFold(key, exact) {
				return fmt.Errorf(
					"module signature entry payload spells %q; the wire form is the module SDK's camelCase JSON and the correct spelling is %q",
					key, exact)
			}
		}
	}
	return nil
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
	if decodeErr := decodeModuleSignaturePayload(entryPayload, &payload); decodeErr != nil {
		status.Reason = "invalid_signature_payload"
		return portable, status, decodeErr
	}
	if !strings.EqualFold(strings.TrimSpace(payload.Algorithm), moduleSignatureAlgorithm) {
		status.Reason = "unsupported_algorithm"
		return portable, status, fmt.Errorf("unsupported module signature algorithm %q", payload.Algorithm)
	}
	status.KeyID = payload.KeyID

	// WHICH BASIS DID THIS SIGNATURE COVER? Answered once, here, before any
	// hash is compared — and an artifact that answers TWICE is refused.
	//
	// A statement domain says "the signature covers domain||0x00||sha256(
	// portable)". The bundle hash algorithm says "the signature covers the
	// canonical whole-bundle statement". They are different preimages, so an
	// artifact declaring both has not stated what its signature covers. Picking
	// one and ignoring the other is how a cross-domain replay guard gets
	// silently switched off: the kubo twin evaluated the bundle branch FIRST,
	// so a domain-carrying artifact that also claimed the bundle algorithm
	// would have verified with its domain never checked. Unreachable today,
	// which is precisely why it had to be closed before it became reachable.
	declaresDomain := strings.TrimSpace(payload.StatementDomain) != ""
	declaresBundle := strings.TrimSpace(payload.SignedHashAlgorithm) == bundleSignatureHashAlgorithm
	if declaresDomain && declaresBundle {
		status.StatementDomain = strings.TrimSpace(payload.StatementDomain)
		status.Reason = "ambiguous_signature_scope"
		return portable, status, fmt.Errorf(
			"module signature declares statement domain %q AND hash algorithm %q; these are different preimages and an artifact must declare exactly one",
			status.StatementDomain, bundleSignatureHashAlgorithm)
	}
	if declaresBundle {
		return verifyWholeBundleSignature(wasmBytes, trustedKeys)
	}

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

	// Resolve WHAT the signature must cover before asking WHO signed it: a
	// wrong statement domain is a property of the artifact and must be
	// reported as such whether or not the signer happens to be trusted.
	signedMessage, statementDomain, domainErr := signedMessageForPayload(payload, sum[:])
	status.StatementDomain = statementDomain
	if domainErr != nil {
		status.Reason = "unsupported_statement_domain"
		return portable, status, domainErr
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

	if !ed25519.Verify(ed25519.PublicKey(pubKeyBytes), signedMessage, sigBytes) {
		status.Reason = "invalid_signature"
		return portable, status, errors.New("module publication signature verification failed")
	}

	status.Verified = true
	status.Reason = "ok"
	status.SignatureScope = signatureScopeModule
	status.SignedHash = status.ContentHash
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
	if policy.ReportOnly {
		// OBSERVE stage: evaluate fully, refuse nothing. The token below is
		// the operator's drain-to-empty signal during the observe window —
		// every line here is an artifact that WOULD die on the enforce flip.
		log.Warnf(
			"module_signature_observe: content_hash=%s reason=%s signed=%t observed_signer=%s observed_key_id=%q statement_domain=%q — ADMITTED because ModuleSignaturePolicy.ReportOnly is set; this artifact WOULD BE REFUSED under enforcement",
			status.ContentHash, status.Reason, status.Signed, status.SignerPubKeyHex, status.KeyID, status.StatementDomain,
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

// EnforceModuleSignaturePolicy is the exported form of
// enforceModuleSignaturePolicy (loop I2): a thin wrapper with no logic of
// its own, so that callers outside package modulert — namely
// internal/flowrt's flow-bundle admit path (install-time FlowManager.Deploy,
// load-time LoadMountedFlow/LoadFlowService) — reuse the EXACT SAME
// publication-signature gate the MODULE load path (instantiateWASM, loop I1)
// applies, rather than reimplementing it. See enforceModuleSignaturePolicy's
// doc for the nil-policy-is-inert / fail-closed-once-configured semantics
// and the AllowUnsignedByContentHash dev escape hatch: they apply here
// identically.
func EnforceModuleSignaturePolicy(policy *ModuleSignaturePolicy, wasmBytes []byte) ([]byte, ModuleSignatureStatus, error) {
	return enforceModuleSignaturePolicy(policy, wasmBytes)
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
