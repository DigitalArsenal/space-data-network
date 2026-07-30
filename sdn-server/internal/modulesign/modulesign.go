// Package modulesign is the node's CONTENT-BOUND module signer: it takes
// artifact BYTES, hashes them itself, and returns a detached Ed25519 signature
// over a domain-separated statement made with the node's own signing key.
//
// SEAL COUNCIL RULING 2026-07-30 (graph/tasks/sdn-module-signing-endpoint.md,
// Hermes in-node + Hephaestus ship-time, JOINT CONCUR). Two owner laws collide
// and this package is the resolution:
//
//   - build-locally-ship-binaries: release artifacts are built LOCALLY (Docker
//     linux/amd64) and NEVER on a host.
//   - machine-bound key-at-rest (2026-07-28): the node key is encrypted at rest
//     under a machine-derived key, fails closed on machine change, and cannot
//     be copied off the host or into the repo.
//
// Under the owner's Adversarial-Security ruling the PUBLISHER KEY IS THE NODE
// KEY (internal/node/module_signature_policy.go:5-31). So the signer can only
// live on the host and the build can only live off it. The bytes travel to the
// key; the key never travels.
//
// THE THREE NON-OPTIONAL PROPERTIES (Hephaestus), all enforced here:
//
//  1. DOMAIN SEPARATION. The signed preimage is
//     sigdomain.Statement(DomainModulePublicationV1, sha256(portable)), never a
//     bare digest. See package sigdomain for the cross-protocol forgery this
//     closes against internal/storage/manifest.go:815.
//
//  2. NEVER SIGN A CALLER-SUPPLIED DIGEST. Sign takes ARTIFACT BYTES and
//     computes the hash itself. It additionally requires the input to be a real
//     wasm module (magic + version), so a 32-byte digest is not merely
//     "not what we asked for" — it is structurally unsignable. Rejecting
//     caller-supplied hashes at the transport layer is the HTTP handler's job
//     (internal/api/module_signing.go); this package's guarantee is that no
//     code path here ever hashes anything but bytes it was handed.
//
//  3. APPEND-ONLY AUDIT LINE PER SIGNATURE, and it is a GATE, not a log: if the
//     audit line cannot be durably appended, the signature is DISCARDED and the
//     call fails. An unauditable signature over the bonded key is exactly the
//     event the audit exists to make impossible to hide.
//
// The signer hashes the TRAILER-STRIPPED payload, so the signature always
// covers the same ContentHashHex the loader recomputes at admit time
// (internal/modulert/publication_signature.go). A caller may therefore POST an
// already-published artifact for RE-signing without stripping it first, and get
// a signature that is correct for the module rather than for the envelope.
package modulesign

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/modulert"
	"github.com/spacedatanetwork/sdn-server/internal/sigdomain"
)

// SDK wire constants. These mirror space-data-module-sdk/src/bundle/signing.js
// (MODULE_SIGNATURE_ALGORITHM:19, LEGACY_MODULE_SIGNATURE_HASH_ALGORITHM:21)
// so the entry this package emits drops straight into the SDK's existing
// publication-trailer writer with no new writer on either side.
const (
	SignatureAlgorithm  = "ed25519"
	SignedHashAlgorithm = "sha256-canonical-module-hash"
)

// wasmMagic is the WebAssembly binary preamble: "\0asm" then a little-endian
// u32 version. Requiring it is the structural half of "never sign a
// caller-supplied digest".
var wasmMagic = []byte{0x00, 0x61, 0x73, 0x6d}

const wasmPreambleLen = 8

// MaxArtifactBytes caps a single signing request. 64 MiB is far above any real
// composed flow runtime.wasm and far below anything that could be used to wedge
// the daemon by memory pressure.
const MaxArtifactBytes = 64 << 20

// Refusal is a REFUSED signing request: the input was not something this node
// will put its bonded key behind. Code is a stable machine token (it appears in
// the audit line and in the HTTP error envelope); a Refusal is always the
// caller's fault, never the node's, and always maps to 4xx.
type Refusal struct {
	Code    string
	Message string
}

func (e *Refusal) Error() string { return e.Code + ": " + e.Message }

func refuse(code, format string, args ...any) *Refusal {
	return &Refusal{Code: code, Message: fmt.Sprintf(format, args...)}
}

// Refusal codes.
const (
	CodeEmptyPayload    = "EMPTY_PAYLOAD"
	CodePayloadTooLarge = "PAYLOAD_TOO_LARGE"
	CodeNotWasmModule   = "NOT_A_WASM_MODULE"
	// CodeDigestNotAccepted is raised by the transport layer, and is declared
	// here so the code vocabulary lives in one place.
	CodeDigestNotAccepted = "DIGEST_NOT_ACCEPTED"
)

// SignatureEntry is the MBL "signature"-role entry payload, in the SDK's exact
// on-wire shape. Keys are camelCase because this object is the SDK's JSON, not
// an SDN API surface — the module SDK reads these names verbatim
// (space-data-module-sdk/src/bundle/signing.js:362-369) and
// internal/modulert/publication_signature.go's moduleSignaturePayload parses
// them. Returning it pre-shaped means the build box appends the trailer with
// the SDK writer it already has and invents nothing.
type SignatureEntry struct {
	Algorithm           string `json:"algorithm"`
	KeyID               string `json:"keyId"`
	PublicKeyHex        string `json:"publicKeyHex"`
	SignatureHex        string `json:"signatureHex"`
	SignedHashHex       string `json:"signedHashHex"`
	SignedHashAlgorithm string `json:"signedHashAlgorithm"`

	// StatementDomain is the field that makes this signature verifiable — and
	// unforgeable across protocols. Its presence tells a verifier the signature
	// covers sigdomain.Statement(domain, hash) rather than the bare digest the
	// legacy SDK-signed artifacts use. Verifiers require it to be exactly the
	// module domain.
	StatementDomain string `json:"statementDomain"`
}

// Result is one issued signature.
type Result struct {
	ContentHash     string    // lowercase hex SHA-256 of the PORTABLE payload
	StatementDomain string    // the registered domain bound into the preimage
	SignatureHex    string    // 64-byte detached Ed25519 signature, lowercase hex
	PublicKeyHex    string    // node signing public key, lowercase hex
	Algorithm       string    // "ed25519"
	PortableBytes   int       // size of the trailer-stripped payload that was hashed
	TrailerStripped bool      // whether the submitted artifact carried a publication trailer
	SignedAt        time.Time // UTC
	Entry           SignatureEntry
}

// Signer holds the node's signing key. Construct one per node; it is safe for
// concurrent use.
type Signer struct {
	key   ed25519.PrivateKey
	pub   ed25519.PublicKey
	keyID string
	audit *AuditLog
	now   func() time.Time
}

// NewSigner wraps the node's raw Ed25519 private key (Node.SigningKey()).
//
// keyID is a non-secret label recorded in the emitted signature entry — the
// node's peer id, in practice — so a downstream reader can say WHICH node
// signed without resolving the key. audit is REQUIRED: a signer that cannot
// audit must not exist, so a nil audit log is a construction error rather than
// a silently unaudited signer.
func NewSigner(rawKey []byte, keyID string, audit *AuditLog) (*Signer, error) {
	if len(rawKey) != ed25519.PrivateKeySize {
		return nil, fmt.Errorf("module signer requires a %d-byte ed25519 private key, got %d", ed25519.PrivateKeySize, len(rawKey))
	}
	if audit == nil {
		return nil, fmt.Errorf("module signer requires an audit log: an unauditable signature over the node publisher key is not permitted")
	}
	key := ed25519.PrivateKey(append([]byte(nil), rawKey...))
	pub, ok := key.Public().(ed25519.PublicKey)
	if !ok || len(pub) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("module signer key does not yield an ed25519 public half")
	}
	return &Signer{
		key:   key,
		pub:   append(ed25519.PublicKey(nil), pub...),
		keyID: strings.TrimSpace(keyID),
		audit: audit,
		now:   func() time.Time { return time.Now().UTC() },
	}, nil
}

// PublicKeyHex is the node's advertised publisher key, lowercase hex — the same
// value internal/node/module_signature_policy.go self-trusts and the EPM
// directory advertises.
func (s *Signer) PublicKeyHex() string { return hex.EncodeToString(s.pub) }

// Request is one signing call. Requester and RemoteIP are recorded in the audit
// line only; neither influences what is signed.
//
// Requester MUST already be a non-reversible fingerprint of the calling
// principal, never a raw xpub — see internal/api/module_signing.go, which
// derives it. This package will not fingerprint for you, because a package that
// accepts "either form" eventually gets handed the raw one.
type Request struct {
	Artifact  []byte
	Requester string
	RemoteIP  string
}

// Sign hashes req.Artifact's portable payload, signs the domain-separated
// statement, appends the audit line, and only then returns the signature.
//
// Every exit — refusal, internal error, or success — writes exactly one audit
// line. On audit-append failure the computed signature is discarded and an
// error is returned: property 3 is a gate.
func (s *Signer) Sign(req Request) (*Result, error) {
	at := s.now()

	portable, stripped, refusal := s.portablePayload(req.Artifact)
	if refusal != nil {
		_ = s.audit.Append(Entry{
			Timestamp:       at,
			Event:           EventRefused,
			StatementDomain: sigdomain.DomainModulePublicationV1,
			SignerPubKeyHex: s.PublicKeyHex(),
			SubmittedBytes:  len(req.Artifact),
			Requester:       req.Requester,
			RemoteIP:        req.RemoteIP,
			Reason:          refusal.Code,
			Detail:          refusal.Message,
		})
		return nil, refusal
	}

	sum := sha256.Sum256(portable)
	contentHash := hex.EncodeToString(sum[:])

	statement, err := sigdomain.Statement(sigdomain.DomainModulePublicationV1, sum[:])
	if err != nil {
		// Unreachable with a registered domain and a 32-byte hash; treated as
		// an internal fault, still audited.
		_ = s.audit.Append(Entry{
			Timestamp:       at,
			Event:           EventFailed,
			ContentHash:     contentHash,
			StatementDomain: sigdomain.DomainModulePublicationV1,
			SignerPubKeyHex: s.PublicKeyHex(),
			SubmittedBytes:  len(req.Artifact),
			PortableBytes:   len(portable),
			Requester:       req.Requester,
			RemoteIP:        req.RemoteIP,
			Reason:          "STATEMENT_BUILD_FAILED",
			Detail:          err.Error(),
		})
		return nil, fmt.Errorf("build signing statement: %w", err)
	}

	signature := ed25519.Sign(s.key, statement)
	signatureHex := hex.EncodeToString(signature)

	// Audit BEFORE the signature leaves this function. If this append fails the
	// signature never reaches the caller, so there is no such thing as an
	// issued-but-unrecorded signature.
	if auditErr := s.audit.Append(Entry{
		Timestamp:       at,
		Event:           EventIssued,
		ContentHash:     contentHash,
		StatementDomain: sigdomain.DomainModulePublicationV1,
		SignerPubKeyHex: s.PublicKeyHex(),
		SignatureHex:    signatureHex,
		SubmittedBytes:  len(req.Artifact),
		PortableBytes:   len(portable),
		TrailerStripped: stripped,
		Requester:       req.Requester,
		RemoteIP:        req.RemoteIP,
		Reason:          "ok",
	}); auditErr != nil {
		return nil, fmt.Errorf("module signature discarded: audit line could not be appended: %w", auditErr)
	}

	return &Result{
		ContentHash:     contentHash,
		StatementDomain: sigdomain.DomainModulePublicationV1,
		SignatureHex:    signatureHex,
		PublicKeyHex:    s.PublicKeyHex(),
		Algorithm:       SignatureAlgorithm,
		PortableBytes:   len(portable),
		TrailerStripped: stripped,
		SignedAt:        at,
		Entry: SignatureEntry{
			Algorithm:           SignatureAlgorithm,
			KeyID:               s.keyID,
			PublicKeyHex:        s.PublicKeyHex(),
			SignatureHex:        signatureHex,
			SignedHashHex:       contentHash,
			SignedHashAlgorithm: SignedHashAlgorithm,
			StatementDomain:     sigdomain.DomainModulePublicationV1,
		},
	}, nil
}

// portablePayload strips any publication trailer and validates that what
// remains is a wasm module. It reports whether a trailer was present.
func (s *Signer) portablePayload(artifact []byte) (portable []byte, stripped bool, refusal *Refusal) {
	switch {
	case len(artifact) == 0:
		return nil, false, refuse(CodeEmptyPayload, "request body is empty; POST the module artifact bytes")
	case len(artifact) > MaxArtifactBytes:
		return nil, false, refuse(CodePayloadTooLarge, "artifact is %d bytes, limit is %d", len(artifact), MaxArtifactBytes)
	}

	portable = modulert.StripPublicationTrailer(artifact)
	stripped = len(portable) != len(artifact)

	if len(portable) < wasmPreambleLen {
		return nil, stripped, refuse(CodeNotWasmModule,
			"portable payload is %d bytes, too short to be a wasm module; this endpoint signs module ARTIFACT BYTES, never a digest", len(portable))
	}
	if string(portable[:4]) != string(wasmMagic) {
		return nil, stripped, refuse(CodeNotWasmModule,
			"portable payload does not begin with the wasm magic \\0asm; this endpoint signs module ARTIFACT BYTES, never a digest or a manifest")
	}
	version := uint32(portable[4]) | uint32(portable[5])<<8 | uint32(portable[6])<<16 | uint32(portable[7])<<24
	if version != 1 {
		return nil, stripped, refuse(CodeNotWasmModule, "unsupported wasm binary version %d, want 1", version)
	}
	return portable, stripped, nil
}
