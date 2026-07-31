// Package updatesign is the node's CONTENT-BOUND update-manifest signer: it
// takes a manifest DOCUMENT, canonicalizes and hashes it itself, and returns a
// detached Ed25519 signature over a domain-separated statement made with the
// node's own signing key.
//
// SEAL COUNCIL 2026-07-30/31 (graph/tasks/sdn-signed-updater.md — Hephaestus
// ship-time producer, Hermes in-node CONCUR on all three questions put to him:
// the additive verifier form, this sibling package, and the audit convention).
//
// It is the SIBLING of internal/modulesign, deliberately not an extension of
// it. Both packages implement the same three non-optional properties over the
// same bonded key, but they validate different content and must never share a
// route: modulesign refuses anything that is not a wasm module, which is
// correct for a module and wrong for a manifest.
//
// THE THREE NON-OPTIONAL PROPERTIES (Hephaestus), all enforced here:
//
//  1. DOMAIN SEPARATION. The signed preimage is
//     sigdomain.Statement(DomainUpdateManifestV1, sha256(canonical)) — never a
//     bare digest, and never the module domain. A signature minted here can
//     therefore never be stapled into a module publication trailer, nor a
//     module signature presented as an update manifest.
//
//  2. NEVER SIGN A CALLER-SUPPLIED DIGEST. Sign takes the manifest DOCUMENT and
//     derives the canonical bytes and their hash itself. The structural half of
//     that guarantee is the manifest validation below: a 64-character hex
//     digest is not a well-formed org.spacedatanetwork.update.v1 document, so
//     it is not merely "not what we asked for" — it is unsignable. This is the
//     manifest analogue of modulesign's wasm-magic check.
//
//  3. APPEND-ONLY AUDIT LINE PER SIGNATURE, and it is a GATE, not a log: if the
//     audit line cannot be durably appended, the signature is DISCARDED and the
//     call fails.
//
// WHY THE NODE CANONICALIZES RATHER THAN SIGNING THE SUBMITTED BYTES VERBATIM.
// The verifier does not check a signature over the bytes it downloaded; it
// checks one over update.CanonicalManifestBytes of those bytes (sorted keys,
// signing.signature deleted). If this package signed the caller's exact byte
// formatting, any whitespace or key-order difference between producer and
// verifier would silently produce an unverifiable release. Signing the same
// canonical form the verifier computes makes the caller's formatting
// irrelevant by construction, which is the only way producer and verifier
// cannot drift.
//
// WHY THE CALLER SUPPLIES signing.statement_domain AND THE NODE ONLY CHECKS IT.
// The domain label lives INSIDE the signed document (canonicalization removes
// only signing.signature), so it is covered by the signature — that is what
// makes it unforgeable rather than a hint. The node therefore cannot insert it
// after the fact without changing the bytes the caller would have to
// reconstruct. Requiring the caller to state it and refusing anything but the
// one registered value keeps the node from ever returning a document, only a
// signature over the document it was handed.
package updatesign

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/sigdomain"
	"github.com/spacedatanetwork/sdn-server/internal/update"
)

// SignatureAlgorithm is the value the manifest's signing.algorithm field must
// carry. It matches desktop/src/sdn-updater/manifest.js:103 and
// internal/update/manifest.go:215 exactly — both verifiers refuse any other
// value, so accepting one here would mint a signature nothing can check.
const SignatureAlgorithm = "Ed25519"

// MaxManifestBytes caps a single signing request. An update manifest is a few
// kilobytes of JSON; 1 MiB is orders of magnitude above any real one and far
// below anything that could wedge the daemon.
const MaxManifestBytes = 1 << 20

// Refusal is a REFUSED signing request: the input was not something this node
// will put its bonded key behind. Code is a stable machine token appearing in
// both the audit line and the HTTP error envelope; a Refusal is always the
// caller's fault and always maps to 4xx.
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
	CodeEmptyPayload      = "EMPTY_PAYLOAD"
	CodePayloadTooLarge   = "PAYLOAD_TOO_LARGE"
	CodeNotAManifest      = "NOT_AN_UPDATE_MANIFEST"
	CodeBadStatementScope = "STATEMENT_DOMAIN_INVALID"
	// CodeDigestNotAccepted is raised by the transport layer, and is declared
	// here so the code vocabulary lives in one place.
	CodeDigestNotAccepted = "DIGEST_NOT_ACCEPTED"
)

var hexHash = regexp.MustCompile(`^[a-f0-9]{64}$`)

// Result is one issued signature.
type Result struct {
	ContentHash     string    // lowercase hex SHA-256 of the CANONICAL manifest bytes
	StatementDomain string    // the registered domain bound into the preimage
	SignatureB64    string    // detached Ed25519 signature, standard base64 — the form
	SignatureHex    string    // the same signature as lowercase hex, for the audit/log lane
	PublicKeyB64    string    // node signing public key, SPKI DER base64 (trust-store form)
	PublicKeyHex    string    // node signing public key, raw lowercase hex
	Algorithm       string    // "Ed25519"
	CanonicalBytes  int       // size of the canonical document that was hashed
	Resigned        bool      // the submitted document already carried a signature
	SignedAt        time.Time // UTC

	// Release identity, echoed back so a caller can log what it just shipped
	// without re-parsing its own submission.
	UpdateID string
	Version  string
	Sequence int64
	Channel  string
	Target   string
}

// Signer holds the node's signing key. Construct one per node; it is safe for
// concurrent use.
type Signer struct {
	key   ed25519.PrivateKey
	pub   ed25519.PublicKey
	audit *AuditLog
	now   func() time.Time
}

// NewSigner wraps the node's raw Ed25519 private key (Node.SigningKey()).
//
// audit is REQUIRED: a signer that cannot audit must not exist, so a nil audit
// log is a construction error rather than a silently unaudited signer.
func NewSigner(rawKey []byte, audit *AuditLog) (*Signer, error) {
	if len(rawKey) != ed25519.PrivateKeySize {
		return nil, fmt.Errorf("update manifest signer requires a %d-byte ed25519 private key, got %d", ed25519.PrivateKeySize, len(rawKey))
	}
	if audit == nil {
		return nil, fmt.Errorf("update manifest signer requires an audit log: an unauditable signature over the node publisher key is not permitted")
	}
	key := ed25519.PrivateKey(append([]byte(nil), rawKey...))
	pub, ok := key.Public().(ed25519.PublicKey)
	if !ok || len(pub) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("update manifest signer key does not yield an ed25519 public half")
	}
	return &Signer{
		key:   key,
		pub:   append(ed25519.PublicKey(nil), pub...),
		audit: audit,
		now:   func() time.Time { return time.Now().UTC() },
	}, nil
}

// PublicKeyHex is the node's advertised publisher key, raw lowercase hex — the
// same value internal/node/module_signature_policy.go self-trusts.
func (s *Signer) PublicKeyHex() string { return hex.EncodeToString(s.pub) }

// PublicKeyB64 is the SPKI DER form the update trust store holds
// (internal/update/manifest.go decodeTrustedPublicKey accepts both, but the
// desktop verifier's publicKeyFromBase64 accepts ONLY SPKI DER, so this is the
// form an operator must install).
func (s *Signer) PublicKeyB64() string { return spkiBase64(s.pub) }

// KeyID is the node key's fingerprint in the house convention: sha256 of the
// raw public key, truncated to PrincipalFingerprintLen hex characters. It is
// the value a manifest's signing.key_id and the trust store's key must both
// carry, so an operator installing a trust root and a producer minting a
// manifest cannot disagree about the label.
func (s *Signer) KeyID() string {
	sum := sha256.Sum256(s.pub)
	return hex.EncodeToString(sum[:])[:PrincipalFingerprintLen]
}

// Request is one signing call. Requester and RemoteIP are recorded in the audit
// line only; neither influences what is signed.
//
// Requester MUST already be a non-reversible fingerprint of the calling
// principal, never a raw xpub — see internal/api/update_signing.go, which
// derives it.
type Request struct {
	Manifest  []byte
	Requester string
	RemoteIP  string
}

// Sign canonicalizes and hashes req.Manifest, signs the domain-separated
// statement, appends the audit line, and only then returns the signature.
//
// Every exit — refusal, internal error, or success — writes exactly one audit
// line. On audit-append failure the computed signature is discarded and an
// error is returned: property 3 is a gate.
func (s *Signer) Sign(req Request) (*Result, error) {
	at := s.now()

	facts, canonical, refusal := s.validate(req.Manifest)
	if refusal != nil {
		_ = s.audit.Append(Entry{
			Timestamp:       at,
			Event:           EventRefused,
			StatementDomain: sigdomain.DomainUpdateManifestV1,
			SignerPubKeyHex: s.PublicKeyHex(),
			SubmittedBytes:  len(req.Manifest),
			Requester:       req.Requester,
			RemoteIP:        req.RemoteIP,
			Reason:          refusal.Code,
			Detail:          refusal.Message,
		})
		return nil, refusal
	}

	sum := sha256.Sum256(canonical)
	contentHash := hex.EncodeToString(sum[:])

	statement, err := sigdomain.Statement(sigdomain.DomainUpdateManifestV1, sum[:])
	if err != nil {
		// Unreachable with a registered domain and a 32-byte hash; treated as
		// an internal fault, still audited.
		_ = s.audit.Append(Entry{
			Timestamp:       at,
			Event:           EventFailed,
			ContentHash:     contentHash,
			StatementDomain: sigdomain.DomainUpdateManifestV1,
			SignerPubKeyHex: s.PublicKeyHex(),
			SubmittedBytes:  len(req.Manifest),
			CanonicalBytes:  len(canonical),
			Requester:       req.Requester,
			RemoteIP:        req.RemoteIP,
			Reason:          "STATEMENT_BUILD_FAILED",
			Detail:          err.Error(),
		})
		return nil, fmt.Errorf("build signing statement: %w", err)
	}

	signature := ed25519.Sign(s.key, statement)

	// Audit BEFORE the signature leaves this function. If this append fails the
	// signature never reaches the caller, so there is no such thing as an
	// issued-but-unrecorded release signature.
	if auditErr := s.audit.Append(Entry{
		Timestamp:       at,
		Event:           EventIssued,
		ContentHash:     contentHash,
		StatementDomain: sigdomain.DomainUpdateManifestV1,
		SignerPubKeyHex: s.PublicKeyHex(),
		SignatureHex:    hex.EncodeToString(signature),
		SubmittedBytes:  len(req.Manifest),
		CanonicalBytes:  len(canonical),
		UpdateID:        facts.UpdateID,
		Version:         facts.Version,
		Sequence:        facts.Sequence,
		Channel:         facts.Channel,
		Target:          facts.Target,
		Resigned:        facts.Resigned,
		Requester:       req.Requester,
		RemoteIP:        req.RemoteIP,
		Reason:          "ok",
	}); auditErr != nil {
		return nil, fmt.Errorf("update manifest signature discarded: audit line could not be appended: %w", auditErr)
	}

	return &Result{
		ContentHash:     contentHash,
		StatementDomain: sigdomain.DomainUpdateManifestV1,
		SignatureB64:    stdBase64(signature),
		SignatureHex:    hex.EncodeToString(signature),
		PublicKeyB64:    s.PublicKeyB64(),
		PublicKeyHex:    s.PublicKeyHex(),
		Algorithm:       SignatureAlgorithm,
		CanonicalBytes:  len(canonical),
		Resigned:        facts.Resigned,
		SignedAt:        at,
		UpdateID:        facts.UpdateID,
		Version:         facts.Version,
		Sequence:        facts.Sequence,
		Channel:         facts.Channel,
		Target:          facts.Target,
	}, nil
}

// manifestFacts is what the audit line and the response echo back.
type manifestFacts struct {
	UpdateID string
	Version  string
	Sequence int64
	Channel  string
	Target   string
	Resigned bool
}

// submitted is the subset of the manifest this package must understand to
// decide whether it is signable. It deliberately does NOT model the whole
// document: canonicalization covers every field, modelled or not, so unknown
// fields are signed like any other — the same additive property
// internal/update/manifest.go:103-110 already relies on.
type submitted struct {
	Schema   string `json:"schema"`
	UpdateID string `json:"update_id"`
	Version  string `json:"version"`
	Sequence *int64 `json:"sequence"`
	Channel  string `json:"channel"`

	CreatedAt string `json:"created_at"`
	ExpiresAt string `json:"expires_at"`

	Target struct {
		Platform string `json:"platform"`
		Arch     string `json:"arch"`
		Kind     string `json:"kind"`
	} `json:"target"`

	Bundle struct {
		Hash   string `json:"hash"`
		Size   *int64 `json:"size"`
		Format string `json:"format"`
	} `json:"bundle"`

	Wasm struct {
		Hash string `json:"hash"`
	} `json:"wasm"`

	Signing struct {
		KeyID           string `json:"key_id"`
		Algorithm       string `json:"algorithm"`
		StatementDomain string `json:"statement_domain"`
		Signature       string `json:"signature"`
	} `json:"signing"`
}

// validate is the structural half of "never sign a caller-supplied digest".
// It answers exactly one question: is this a well-formed, unambiguous
// org.spacedatanetwork.update.v1 manifest that names this node's reserved
// statement domain? Anything else is refused before a hash is ever computed.
func (s *Signer) validate(body []byte) (manifestFacts, []byte, *Refusal) {
	var facts manifestFacts

	switch {
	case len(body) == 0:
		return facts, nil, refuse(CodeEmptyPayload, "request body is empty; POST the update manifest document")
	case len(body) > MaxManifestBytes:
		return facts, nil, refuse(CodePayloadTooLarge, "manifest is %d bytes, limit is %d", len(body), MaxManifestBytes)
	}

	var doc submitted
	decoder := json.NewDecoder(strings.NewReader(string(body)))
	decoder.UseNumber()
	if err := decoder.Decode(&doc); err != nil {
		return facts, nil, refuse(CodeNotAManifest,
			"body is not a JSON object: this endpoint signs an update MANIFEST DOCUMENT, never a digest (%v)", err)
	}

	if doc.Schema != update.ManifestSchema {
		return facts, nil, refuse(CodeNotAManifest,
			"schema is %q, want %q: this endpoint signs an update MANIFEST DOCUMENT, never a digest", doc.Schema, update.ManifestSchema)
	}

	// Every field a verifier requires must be present and well-formed BEFORE a
	// signature exists. Signing a manifest that no verifier can accept would
	// spend the bonded key on an artifact that can only ever fail closed.
	for _, check := range []struct {
		value string
		name  string
	}{
		{doc.UpdateID, "update_id"},
		{doc.Version, "version"},
		{doc.Channel, "channel"},
		{doc.CreatedAt, "created_at"},
		{doc.ExpiresAt, "expires_at"},
		{doc.Target.Platform, "target.platform"},
		{doc.Target.Arch, "target.arch"},
		{doc.Target.Kind, "target.kind"},
		{doc.Bundle.Format, "bundle.format"},
		{doc.Signing.KeyID, "signing.key_id"},
	} {
		if strings.TrimSpace(check.value) == "" {
			return facts, nil, refuse(CodeNotAManifest, "manifest is missing %s", check.name)
		}
	}
	if doc.Sequence == nil {
		return facts, nil, refuse(CodeNotAManifest, "manifest is missing an integer sequence")
	}
	if doc.Bundle.Size == nil || *doc.Bundle.Size < 0 {
		return facts, nil, refuse(CodeNotAManifest, "manifest is missing a non-negative bundle.size")
	}
	if !hexHash.MatchString(doc.Bundle.Hash) {
		return facts, nil, refuse(CodeNotAManifest, "bundle.hash is not a lowercase hex sha-256")
	}
	if !hexHash.MatchString(doc.Wasm.Hash) {
		return facts, nil, refuse(CodeNotAManifest, "wasm.hash is not a lowercase hex sha-256")
	}
	if doc.Signing.Algorithm != SignatureAlgorithm {
		return facts, nil, refuse(CodeNotAManifest,
			"signing.algorithm is %q, want %q — both verifiers refuse anything else", doc.Signing.Algorithm, SignatureAlgorithm)
	}

	// The domain must be stated by the caller and must be EXACTLY the reserved
	// one. Note the deliberate asymmetry with sigdomain.Registered(): this is
	// an equality check against a single constant, not a registry lookup, so
	// this endpoint can never be talked into signing a module-publication
	// statement by naming that domain in a manifest.
	if doc.Signing.StatementDomain != sigdomain.DomainUpdateManifestV1 {
		if strings.TrimSpace(doc.Signing.StatementDomain) == "" {
			return facts, nil, refuse(CodeBadStatementScope,
				"manifest is missing signing.statement_domain: set it to %q before submitting, so the domain is covered by the signature it authorizes",
				sigdomain.DomainUpdateManifestV1)
		}
		return facts, nil, refuse(CodeBadStatementScope,
			"signing.statement_domain is %q; this endpoint signs %q only",
			doc.Signing.StatementDomain, sigdomain.DomainUpdateManifestV1)
	}

	canonical, err := update.CanonicalManifestBytes(body)
	if err != nil {
		return facts, nil, refuse(CodeNotAManifest, "manifest could not be canonicalized: %v", err)
	}

	facts = manifestFacts{
		UpdateID: doc.UpdateID,
		Version:  doc.Version,
		Sequence: *doc.Sequence,
		Channel:  doc.Channel,
		Target:   doc.Target.Kind + "/" + doc.Target.Platform + "/" + doc.Target.Arch,
		Resigned: strings.TrimSpace(doc.Signing.Signature) != "",
	}
	return facts, canonical, nil
}
