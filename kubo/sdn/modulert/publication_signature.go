package modulert

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/MBL"
	flatbuffers "github.com/google/flatbuffers/go"
	"github.com/ipfs/kubo/sdn/sigdomain"
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
	moduleSignatureAlgorithm     = "ed25519"
	moduleSignatureEntryID       = "signature"
	moduleSignatureSectionName   = "sds.signature"
	bundleSignatureHashAlgorithm = "sha256-sdn-module-bundle-v1"
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
	// (sdn-server/internal/modulesign, Seal Council 2026-07-30). See
	// signedMessageForPayload.
	StatementDomain string `json:"statementDomain"`
}

// decodeModuleSignaturePayload decodes the signature entry STRICTLY. Go's
// encoding/json matches field names case-insensitively; the SDK's JS verifier
// does not. A trailer spelling "StatementDomain" would therefore take the
// domain path here and the LEGACY path in the SDK — same artifact, opposite
// verdicts, node permissive. MUST STAY IN LOCKSTEP with the sdn-server twin.
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

// signedMessageForPayload returns the exact bytes an artifact's signature must
// verify over, and is where the DOMAIN SEPARATION contract is enforced on the
// reading side.
//
// MUST STAY BYTE-COMPATIBLE with sdn-server/internal/modulert's function of the
// same name: both binaries run on host-01 and must agree about every artifact.
//
// TWO FORMS, and the artifact chooses, not policy:
//
//   - statementDomain PRESENT — the node-signed form. The signature covers
//     sigdomain.Statement(domain, contentHash), and the domain must be EXACTLY
//     DomainModulePublicationV1: a registered-but-different domain (the
//     update-manifest domain) is refused, so a signature minted for a signed
//     update can never be stapled into a module trailer.
//
//   - statementDomain ABSENT — the legacy SDK-signed form
//     (space-data-module-sdk/src/bundle/signing.js:361, a signature over the
//     bare hash bytes). Byte-identical to previous behavior, so every artifact
//     already in the catalog keeps verifying.
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
	// StatementDomain is the domain-separation label the signature declared,
	// or "" for the legacy bare-digest form. Non-empty means the signature
	// covered sigdomain.Statement(domain, ContentHash).
	StatementDomain string
	// Reason is a short machine-checkable explanation: "unsigned", "ok",
	// "untrusted_signer", "invalid_signature", "hash_mismatch",
	// "invalid_trailer", "unsupported_algorithm", "invalid_public_key",
	// "invalid_signature_payload", "unsupported_statement_domain".
	Reason string
	// SignatureScope is "module" for the legacy portable-module digest and
	// "bundle" when the signature binds every non-signature MBL member.
	SignatureScope string
	// SignedHash is the lowercase SHA-256 digest actually covered by the
	// signature. For bundle scope this is the canonical bundle statement hash.
	SignedHash string
}

// VerifiedBundleEntry is one hash-checked, non-signature REC+MBL member. Its
// payload remains opaque to the host.
type VerifiedBundleEntry struct {
	EntryID         string
	Role            MBL.ModuleBundleEntryRole
	SectionName     string
	TypeRef         string
	PayloadEncoding MBL.ModulePayloadEncoding
	MediaType       string
	Flags           uint32
	SHA256Hex       string
	Payload         []byte
	Description     string
}

// VerifiedModuleBundle is application-blind metadata recovered from an SDK
// whole-bundle-signed artifact after every member hash and the signature have
// been verified.
type VerifiedModuleBundle struct {
	BundleVersion          uint16
	ModuleFormat           string
	CanonicalModuleHashHex string
	ManifestHashHex        string
	ManifestExportSymbol   string
	ManifestSizeSymbol     string
	Entries                []VerifiedBundleEntry
}

func verifiedBundleManifestPayload(bundle *VerifiedModuleBundle) ([]byte, error) {
	if bundle == nil {
		return nil, errors.New("verified module bundle is missing")
	}
	var manifest []byte
	for _, entry := range bundle.Entries {
		isManifest := entry.EntryID == "manifest" || entry.SectionName == "sds.manifest" || entry.Role == MBL.ModuleBundleEntryRoleMANIFEST
		if !isManifest {
			continue
		}
		if entry.EntryID != "manifest" || entry.SectionName != "sds.manifest" || entry.Role != MBL.ModuleBundleEntryRoleMANIFEST {
			return nil, fmt.Errorf("verified module manifest entry %q has noncanonical identity", entry.EntryID)
		}
		if manifest != nil {
			return nil, errors.New("verified module bundle contains multiple manifest entries")
		}
		if len(entry.Payload) == 0 {
			return nil, errors.New("verified module manifest payload is empty")
		}
		manifest = append([]byte(nil), entry.Payload...)
	}
	if manifest == nil {
		return nil, errors.New("verified module bundle contains no canonical sds.manifest entry")
	}
	return manifest, nil
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

	// WHICH BASIS DID THIS SIGNATURE COVER? See the sdn-server twin for the
	// full reasoning. This branch used to test ONLY the bundle algorithm, and
	// tested it before the domain was ever looked at, so an artifact declaring
	// both would have been verified with its statement domain ignored — the
	// cross-domain replay guard silently off.
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
	status.SignatureScope = "module"
	status.SignedHash = status.ContentHash
	return portable, status, nil
}

type decodedBundleEntry struct {
	verified         VerifiedBundleEntry
	role             MBL.ModuleBundleEntryRole
	sectionValue     interface{}
	typeRefValue     interface{}
	mediaValue       interface{}
	descriptionValue interface{}
}

type decodedModuleBundle struct {
	version              uint16
	moduleFormat         interface{}
	canonicalPresent     bool
	canonicalVersion     uint16
	customSectionPrefix  interface{}
	bundleSectionName    interface{}
	hashAlgorithm        interface{}
	canonicalModuleHash  []byte
	manifestHash         []byte
	manifestExportSymbol interface{}
	manifestSizeSymbol   interface{}
	entries              []decodedBundleEntry
	signaturePayload     []byte
}

// VerifyModuleBundle verifies an SDK bundle-scoped signature and returns only
// hash-checked non-signature members. It never interprets member payloads.
func VerifyModuleBundle(wasmBytes []byte, trustedKeys []ed25519.PublicKey) ([]byte, *VerifiedModuleBundle, ModuleSignatureStatus, error) {
	portable, status, err := verifyWholeBundleSignature(wasmBytes, trustedKeys)
	if err != nil {
		return portable, nil, status, err
	}
	decoded, err := decodeModuleBundle(PublicationTrailerRecordBytes(wasmBytes), portable)
	if err != nil {
		status.Verified = false
		status.Reason = "invalid_bundle"
		return portable, nil, status, err
	}
	entries := make([]VerifiedBundleEntry, 0, len(decoded.entries))
	for _, entry := range decoded.entries {
		if isSignatureBundleEntry(entry) {
			continue
		}
		entries = append(entries, entry.verified)
	}
	return portable, &VerifiedModuleBundle{
		BundleVersion:          decoded.version,
		ModuleFormat:           stringValue(decoded.moduleFormat),
		CanonicalModuleHashHex: hex.EncodeToString(decoded.canonicalModuleHash),
		ManifestHashHex:        hex.EncodeToString(decoded.manifestHash),
		ManifestExportSymbol:   stringValue(decoded.manifestExportSymbol),
		ManifestSizeSymbol:     stringValue(decoded.manifestSizeSymbol),
		Entries:                entries,
	}, status, nil
}

func verifyWholeBundleSignature(wasmBytes []byte, trustedKeys []ed25519.PublicKey) (portable []byte, status ModuleSignatureStatus, err error) {
	portable = StripPublicationTrailer(wasmBytes)
	portableHash := sha256.Sum256(portable)
	status.ContentHash = hex.EncodeToString(portableHash[:])

	decoded, err := decodeModuleBundle(PublicationTrailerRecordBytes(wasmBytes), portable)
	if err != nil {
		status.Reason = "invalid_bundle"
		return portable, status, err
	}
	if len(decoded.signaturePayload) == 0 {
		status.Reason = "unsigned"
		return portable, status, errors.New("module bundle has no signature entry")
	}
	status.Signed = true

	var payload moduleSignaturePayload
	if err := decodeModuleSignaturePayload(decoded.signaturePayload, &payload); err != nil {
		status.Reason = "invalid_signature_payload"
		return portable, status, err
	}
	if !strings.EqualFold(strings.TrimSpace(payload.Algorithm), moduleSignatureAlgorithm) {
		status.Reason = "unsupported_algorithm"
		return portable, status, fmt.Errorf("unsupported module signature algorithm %q", payload.Algorithm)
	}
	if payload.SignedHashAlgorithm != bundleSignatureHashAlgorithm {
		status.Reason = "unsupported_hash_algorithm"
		return portable, status, fmt.Errorf("unsupported module bundle signature hash algorithm %q", payload.SignedHashAlgorithm)
	}
	status.KeyID = payload.KeyID
	status.SignatureScope = "bundle"

	pubKeyHex := strings.ToLower(strings.TrimSpace(payload.PublicKeyHex))
	pubKeyBytes, decodeErr := hex.DecodeString(pubKeyHex)
	if decodeErr != nil || len(pubKeyBytes) != ed25519.PublicKeySize {
		status.Reason = "invalid_public_key"
		return portable, status, errors.New("module signer public key must be 32-byte hex")
	}
	status.SignerPubKeyHex = pubKeyHex
	trusted := false
	for _, key := range trustedKeys {
		if len(key) == ed25519.PublicKeySize && hex.EncodeToString(key) == pubKeyHex {
			trusted = true
			break
		}
	}
	if !trusted {
		status.Reason = "untrusted_signer"
		return portable, status, fmt.Errorf("module signer %s is not a trusted publisher key", pubKeyHex)
	}

	sigBytes, decodeErr := hex.DecodeString(strings.TrimSpace(payload.SignatureHex))
	if decodeErr != nil || len(sigBytes) != ed25519.SignatureSize || isAllZeroBytes(sigBytes) {
		status.Reason = "invalid_signature"
		return portable, status, errors.New("module signature must be a non-zero 64-byte hex Ed25519 signature")
	}
	digest, err := moduleBundleSignatureDigest(decoded)
	if err != nil {
		status.Reason = "invalid_bundle"
		return portable, status, err
	}
	status.SignedHash = hex.EncodeToString(digest)
	if strings.ToLower(strings.TrimSpace(payload.SignedHashHex)) != status.SignedHash {
		status.Reason = "hash_mismatch"
		return portable, status, fmt.Errorf("module bundle signed digest mismatch: recorded %s computed %s", payload.SignedHashHex, status.SignedHash)
	}
	if !ed25519.Verify(ed25519.PublicKey(pubKeyBytes), digest, sigBytes) {
		status.Reason = "invalid_signature"
		return portable, status, errors.New("module bundle signature verification failed")
	}
	status.Verified = true
	status.Reason = "ok"
	return portable, status, nil
}

func decodeModuleBundle(recBytes, portable []byte) (decoded *decodedModuleBundle, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			decoded = nil
			err = fmt.Errorf("malformed module bundle record: %v", recovered)
		}
	}()
	if len(recBytes) < 8 || !flatbuffers.BufferHasIdentifier(recBytes, publicationTrailerMagic) {
		return nil, errors.New("module bundle publication trailer is missing")
	}
	rec := getRootAsRECTrailer(recBytes)
	var root *MBL.MBL
	for i := 0; i < rec.recordsLength(); i++ {
		record, ok := rec.record(i)
		if !ok || record.valueType() != recRecordTypeMBL {
			continue
		}
		table, ok := record.value()
		if !ok {
			continue
		}
		root = &MBL.MBL{}
		root.Init(table.Bytes, table.Pos)
		break
	}
	if root == nil {
		return nil, errors.New("publication trailer carries no MBL record")
	}

	out := &decodedModuleBundle{
		version:              root.BundleVersion(),
		moduleFormat:         nullableString(root.ModuleFormat()),
		canonicalModuleHash:  append([]byte(nil), root.CanonicalModuleHashBytes()...),
		manifestHash:         append([]byte(nil), root.ManifestHashBytes()...),
		manifestExportSymbol: nullableString(root.ManifestExportSymbol()),
		manifestSizeSymbol:   nullableString(root.ManifestSizeSymbol()),
	}
	var canonical MBL.CanonicalizationRule
	if root.Canonicalization(&canonical) != nil {
		out.canonicalPresent = true
		out.canonicalVersion = canonical.Version()
		out.customSectionPrefix = nullableString(canonical.StrippedCustomSectionPrefix())
		out.bundleSectionName = nullableString(canonical.BundleSectionName())
		out.hashAlgorithm = nullableString(canonical.HashAlgorithm())
	}
	prefix := "sds."
	if value, ok := out.customSectionPrefix.(string); ok {
		prefix = value
	}
	canonicalPortable, err := stripWasmCustomSectionsWithPrefix(portable, prefix)
	if err != nil {
		return nil, fmt.Errorf("canonicalize module: %w", err)
	}
	moduleHash := sha256.Sum256(canonicalPortable)
	if len(out.canonicalModuleHash) != sha256.Size || !bytesEqualConstantTime(out.canonicalModuleHash, moduleHash[:]) {
		return nil, errors.New("module canonical hash does not match the bundle's recorded hash")
	}

	seen := make(map[string]bool)
	var manifestPayload []byte
	var entry MBL.ModuleBundleEntry
	for i := 0; i < root.EntriesLength(); i++ {
		if !root.Entries(&entry, i) {
			continue
		}
		id := string(entry.EntryId())
		if id == "" {
			return nil, errors.New("module bundle contains an entry without an entryId")
		}
		if seen[id] {
			return nil, fmt.Errorf("module bundle contains duplicate entryId %q", id)
		}
		seen[id] = true
		payload := append([]byte(nil), entry.PayloadBytes()...)
		role := entry.Role()
		decodedEntry := decodedBundleEntry{
			verified: VerifiedBundleEntry{
				EntryID:         id,
				Role:            role,
				SectionName:     string(entry.SectionName()),
				TypeRef:         string(entry.TypeRef()),
				PayloadEncoding: entry.PayloadEncoding(),
				MediaType:       string(entry.MediaType()),
				Flags:           entry.Flags(),
				SHA256Hex:       hex.EncodeToString(entry.Sha256Bytes()),
				Payload:         payload,
				Description:     string(entry.Description()),
			},
			role:             role,
			sectionValue:     nullableString(entry.SectionName()),
			typeRefValue:     normalizedTypeRef(entry.TypeRef()),
			mediaValue:       nullableString(entry.MediaType()),
			descriptionValue: nullableString(entry.Description()),
		}
		if !isSignatureBundleEntry(decodedEntry) {
			recordedHash := entry.Sha256Bytes()
			hash := sha256.Sum256(payload)
			if len(recordedHash) != sha256.Size || !bytesEqualConstantTime(recordedHash, hash[:]) {
				return nil, fmt.Errorf("module bundle entry %q payload hash does not match its recorded sha256", id)
			}
			if manifestPayload == nil && (id == "manifest" || role == MBL.ModuleBundleEntryRoleMANIFEST) {
				manifestPayload = payload
			}
		} else {
			if out.signaturePayload != nil {
				return nil, errors.New("module bundle contains multiple signature entries")
			}
			out.signaturePayload = payload
		}
		out.entries = append(out.entries, decodedEntry)
	}
	if manifestPayload != nil {
		hash := sha256.Sum256(manifestPayload)
		if len(out.manifestHash) != sha256.Size || !bytesEqualConstantTime(out.manifestHash, hash[:]) {
			return nil, errors.New("module manifest payload hash does not match the bundle's manifestHash")
		}
	} else if len(out.manifestHash) != 0 {
		return nil, errors.New("module bundle records a manifestHash but contains no manifest entry")
	}
	return out, nil
}

func moduleBundleSignatureDigest(bundle *decodedModuleBundle) ([]byte, error) {
	entries := make([]map[string]interface{}, 0, len(bundle.entries))
	for _, entry := range bundle.entries {
		if isSignatureBundleEntry(entry) {
			continue
		}
		entries = append(entries, map[string]interface{}{
			"entryId":         entry.verified.EntryID,
			"role":            int(entry.role),
			"sectionName":     entry.sectionValue,
			"typeRef":         entry.typeRefValue,
			"payloadEncoding": int(entry.verified.PayloadEncoding),
			"mediaType":       entry.mediaValue,
			"flags":           int(entry.verified.Flags),
			"sha256Hex":       entry.verified.SHA256Hex,
			"payloadLength":   len(entry.verified.Payload),
			"description":     entry.descriptionValue,
		})
	}
	sort.Slice(entries, func(i, j int) bool {
		return entries[i]["entryId"].(string) < entries[j]["entryId"].(string)
	})
	canonicalization := map[string]interface{}{
		"version":                     1,
		"strippedCustomSectionPrefix": nil,
		"bundleSectionName":           nil,
		"hashAlgorithm":               nil,
	}
	if bundle.canonicalPresent {
		canonicalization["version"] = int(bundle.canonicalVersion)
		canonicalization["strippedCustomSectionPrefix"] = bundle.customSectionPrefix
		canonicalization["bundleSectionName"] = bundle.bundleSectionName
		canonicalization["hashAlgorithm"] = bundle.hashAlgorithm
	}
	statement := map[string]interface{}{
		"version":                1,
		"bundleVersion":          int(bundle.version),
		"moduleFormat":           bundle.moduleFormat,
		"canonicalization":       canonicalization,
		"canonicalModuleHashHex": hex.EncodeToString(bundle.canonicalModuleHash),
		"manifestHashHex":        hex.EncodeToString(bundle.manifestHash),
		"manifestExportSymbol":   bundle.manifestExportSymbol,
		"manifestSizeSymbol":     bundle.manifestSizeSymbol,
		"entries":                entries,
	}
	canonical, err := json.Marshal(statement)
	if err != nil {
		return nil, fmt.Errorf("canonicalize module bundle statement: %w", err)
	}
	digest := sha256.Sum256(canonical)
	return digest[:], nil
}

func isSignatureBundleEntry(entry decodedBundleEntry) bool {
	return entry.role == MBL.ModuleBundleEntryRoleSIGNATURE ||
		strings.EqualFold(entry.verified.EntryID, moduleSignatureEntryID) ||
		strings.EqualFold(entry.verified.SectionName, moduleSignatureSectionName)
}

func nullableString(raw []byte) interface{} {
	if raw == nil {
		return nil
	}
	return string(raw)
}

func stringValue(value interface{}) string {
	if value == nil {
		return ""
	}
	return fmt.Sprintf("%v", value)
}

func normalizedTypeRef(raw []byte) interface{} {
	if raw == nil {
		return nil
	}
	text := string(raw)
	var value interface{}
	if json.Unmarshal(raw, &value) != nil {
		return text
	}
	object, ok := value.(map[string]interface{})
	if !ok {
		return text
	}
	wire := "flatbuffer"
	if rawWire, ok := object["wireFormat"]; ok {
		switch typed := rawWire.(type) {
		case float64:
			if typed == 1 {
				wire = "aligned-binary"
			}
		case string:
			if strings.EqualFold(strings.ReplaceAll(typed, "_", "-"), "aligned-binary") {
				wire = "aligned-binary"
			}
		}
	}
	object["wireFormat"] = wire
	for _, key := range []string{"fixedStringLength", "byteLength", "requiredAlignment"} {
		if _, ok := object[key]; !ok {
			object[key] = 0
		}
	}
	return object
}

func bytesEqualConstantTime(left, right []byte) bool {
	if len(left) != len(right) {
		return false
	}
	var different byte
	for i := range left {
		different |= left[i] ^ right[i]
	}
	return different == 0
}

func stripWasmCustomSectionsWithPrefix(wasm []byte, prefix string) ([]byte, error) {
	if len(wasm) < 8 || string(wasm[:4]) != "\x00asm" || string(wasm[4:8]) != "\x01\x00\x00\x00" {
		return nil, errors.New("WASM module header is invalid")
	}
	out := append([]byte(nil), wasm[:8]...)
	for offset := 8; offset < len(wasm); {
		start := offset
		sectionID := wasm[offset]
		offset++
		size, next, err := decodeULEB128(wasm, offset)
		if err != nil {
			return nil, err
		}
		payloadEnd := next + size
		if payloadEnd < next || payloadEnd > len(wasm) {
			return nil, errors.New("WASM section exceeds module bounds")
		}
		strip := false
		if sectionID == 0 {
			nameLength, nameStart, err := decodeULEB128(wasm, next)
			if err != nil || nameStart+nameLength < nameStart || nameStart+nameLength > payloadEnd {
				return nil, errors.New("WASM custom section name exceeds section bounds")
			}
			strip = strings.HasPrefix(string(wasm[nameStart:nameStart+nameLength]), prefix)
		}
		if !strip {
			out = append(out, wasm[start:payloadEnd]...)
		}
		offset = payloadEnd
	}
	return out, nil
}

func decodeULEB128(data []byte, offset int) (value, next int, err error) {
	var shift uint
	for offset < len(data) {
		current := data[offset]
		offset++
		value |= int(current&0x7f) << shift
		if current&0x80 == 0 {
			return value, offset, nil
		}
		shift += 7
		if shift > 56 {
			return 0, 0, errors.New("WASM ULEB128 value is too large")
		}
	}
	return 0, 0, errors.New("WASM ULEB128 value is truncated")
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
