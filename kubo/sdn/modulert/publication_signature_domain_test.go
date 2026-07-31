package modulert

// DOMAIN SEPARATION acceptance tests for the publication-signature verifier
// (Seal Council 2026-07-30, graph/tasks/sdn-module-signing-endpoint.md,
// Hephaestus non-optional property 1).
//
// MIRROR of sdn-server/internal/modulert/publication_signature_domain_test.go.
// The kubo fork is a separate Go module and cannot import sdn-server, so the
// verifier — and therefore its acceptance tests — exist twice. Both binaries
// run on host-01; if these two files ever disagree, one of them is admitting an
// artifact the other refuses.
//
// These pin four properties, in this order of importance:
//
//  1. A node-signed artifact — signature over
//     sigdomain.Statement(DomainModulePublicationV1, contentHash) — verifies.
//  2. A signature made over the BARE DIGEST but LABELLED with the module domain
//     is REFUSED. This is the test that would fail if someone "simplified"
//     signedMessageForPayload back to verifying over sum[:].
//  3. A signature under a DIFFERENT REGISTERED domain (the reserved update
//     domain) is REFUSED even when the signer is trusted — cross-domain replay
//     into a module trailer is impossible.
//  4. Legacy SDK-signed artifacts (no statementDomain field) still verify
//     byte-for-byte as before. Zero blast radius on the live admit path.

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"testing"

	"github.com/ipfs/kubo/sdn/sigdomain"
)

// buildDomainSignedArtifact produces the artifact shape the node's signing
// endpoint + the SDK trailer writer produce together: portable payload, plus a
// trailer whose signature entry carries statementDomain and a signature over
// the domain-separated statement.
//
// signedMessage lets a test deliberately sign the WRONG bytes while keeping the
// declared domain, which is how property 2 above is exercised.
func buildDomainSignedArtifact(t *testing.T, portable []byte, signer ed25519.PrivateKey, domain string, signedMessage []byte) []byte {
	t.Helper()

	pub, ok := signer.Public().(ed25519.PublicKey)
	if !ok {
		t.Fatalf("signer.Public() did not return an ed25519.PublicKey")
	}
	sum := sha256.Sum256(portable)
	sig := ed25519.Sign(signer, signedMessage)

	payload := struct {
		Algorithm           string `json:"algorithm"`
		KeyID               string `json:"keyId"`
		PublicKeyHex        string `json:"publicKeyHex"`
		SignatureHex        string `json:"signatureHex"`
		SignedHashHex       string `json:"signedHashHex"`
		SignedHashAlgorithm string `json:"signedHashAlgorithm"`
		StatementDomain     string `json:"statementDomain"`
	}{
		Algorithm:           moduleSignatureAlgorithm,
		KeyID:               "node-key",
		PublicKeyHex:        hex.EncodeToString(pub),
		SignatureHex:        hex.EncodeToString(sig),
		SignedHashHex:       hex.EncodeToString(sum[:]),
		SignedHashAlgorithm: "sha256-canonical-module-hash",
		StatementDomain:     domain,
	}
	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal signature payload: %v", err)
	}
	return appendPublicationTrailer(portable, buildRECTrailerWithMBLSignature(t, payloadJSON))
}

func mustStatement(t *testing.T, domain string, portable []byte) []byte {
	t.Helper()
	sum := sha256.Sum256(portable)
	stmt, err := sigdomain.Statement(domain, sum[:])
	if err != nil {
		t.Fatalf("sigdomain.Statement(%q): %v", domain, err)
	}
	return stmt
}

// Property 1.
func TestVerifyAcceptsDomainSeparatedSignature(t *testing.T) {
	pub, priv := mustGenerateEd25519Key(t)
	artifact := buildDomainSignedArtifact(t, wasmHeader, priv,
		sigdomain.DomainModulePublicationV1,
		mustStatement(t, sigdomain.DomainModulePublicationV1, wasmHeader))

	portable, status, err := verifyPublicationSignature(artifact, []ed25519.PublicKey{pub})
	if err != nil {
		t.Fatalf("verifyPublicationSignature() error = %v", err)
	}
	if !status.Verified {
		t.Fatalf("status = %+v, want Verified=true", status)
	}
	if status.Reason != "ok" {
		t.Fatalf("status.Reason = %q, want %q", status.Reason, "ok")
	}
	if status.StatementDomain != sigdomain.DomainModulePublicationV1 {
		t.Fatalf("status.StatementDomain = %q, want %q", status.StatementDomain, sigdomain.DomainModulePublicationV1)
	}
	if !bytesEqual(portable, wasmHeader) {
		t.Fatalf("portable payload was not returned intact")
	}
}

// Property 2 — the regression gate on the whole design.
func TestVerifyRefusesBareDigestSignatureLabelledWithDomain(t *testing.T) {
	pub, priv := mustGenerateEd25519Key(t)
	sum := sha256.Sum256(wasmHeader)

	artifact := buildDomainSignedArtifact(t, wasmHeader, priv,
		sigdomain.DomainModulePublicationV1,
		sum[:]) // signed the BARE digest while claiming the domain

	_, status, err := verifyPublicationSignature(artifact, []ed25519.PublicKey{pub})
	if err == nil {
		t.Fatal("verifyPublicationSignature() error = nil, want a signature failure: a bare-digest signature must not pass as a domain-separated one")
	}
	if status.Verified {
		t.Fatalf("status = %+v, want Verified=false", status)
	}
	if status.Reason != "invalid_signature" {
		t.Fatalf("status.Reason = %q, want %q", status.Reason, "invalid_signature")
	}
}

// Property 3 — cross-domain replay, with a fully trusted signer.
func TestVerifyRefusesForeignRegisteredDomain(t *testing.T) {
	pub, priv := mustGenerateEd25519Key(t)

	artifact := buildDomainSignedArtifact(t, wasmHeader, priv,
		sigdomain.DomainUpdateManifestV1,
		mustStatement(t, sigdomain.DomainUpdateManifestV1, wasmHeader))

	_, status, err := verifyPublicationSignature(artifact, []ed25519.PublicKey{pub})
	if err == nil {
		t.Fatal("verifyPublicationSignature() error = nil, want refusal: an update-manifest signature must not be admissible as a module signature")
	}
	if status.Verified {
		t.Fatalf("status = %+v, want Verified=false", status)
	}
	if status.Reason != "unsupported_statement_domain" {
		t.Fatalf("status.Reason = %q, want %q", status.Reason, "unsupported_statement_domain")
	}
	if status.StatementDomain != sigdomain.DomainUpdateManifestV1 {
		t.Fatalf("status.StatementDomain = %q, want the domain as declared", status.StatementDomain)
	}
}

func TestVerifyRefusesUnregisteredDomain(t *testing.T) {
	pub, priv := mustGenerateEd25519Key(t)
	sum := sha256.Sum256(wasmHeader)
	forged := append(append([]byte("SDN-MADE-UP-DOMAIN"), 0), sum[:]...)

	artifact := buildDomainSignedArtifact(t, wasmHeader, priv, "SDN-MADE-UP-DOMAIN", forged)

	_, status, err := verifyPublicationSignature(artifact, []ed25519.PublicKey{pub})
	if err == nil {
		t.Fatal("verifyPublicationSignature() error = nil, want refusal of an unregistered statement domain")
	}
	if status.Reason != "unsupported_statement_domain" {
		t.Fatalf("status.Reason = %q, want %q", status.Reason, "unsupported_statement_domain")
	}
}

// Property 4 — backward compatibility. buildSignedModuleArtifact is the
// pre-existing fixture: bare-digest signature, no statementDomain field.
func TestVerifyStillAcceptsLegacySDKSignature(t *testing.T) {
	pub, priv := mustGenerateEd25519Key(t)
	artifact := buildSignedModuleArtifact(t, wasmHeader, priv, "legacy-sdk-key")

	_, status, err := verifyPublicationSignature(artifact, []ed25519.PublicKey{pub})
	if err != nil {
		t.Fatalf("verifyPublicationSignature() error = %v — legacy SDK-signed artifacts must keep verifying", err)
	}
	if !status.Verified {
		t.Fatalf("status = %+v, want Verified=true", status)
	}
	if status.StatementDomain != "" {
		t.Fatalf("status.StatementDomain = %q, want empty for the legacy bare-digest form", status.StatementDomain)
	}
}

// A tampered payload must fail even with a valid domain-separated signature over
// the ORIGINAL bytes (acceptance 3, second half).
func TestVerifyRefusesTamperedPayloadUnderDomainSeparation(t *testing.T) {
	pub, priv := mustGenerateEd25519Key(t)
	artifact := buildDomainSignedArtifact(t, wasmHeader, priv,
		sigdomain.DomainModulePublicationV1,
		mustStatement(t, sigdomain.DomainModulePublicationV1, wasmHeader))

	// Flip one byte of the portable payload, leaving the trailer intact.
	tampered := append([]byte(nil), artifact...)
	tampered[len(wasmHeader)-1] ^= 0xff

	_, status, err := verifyPublicationSignature(tampered, []ed25519.PublicKey{pub})
	if err == nil {
		t.Fatal("verifyPublicationSignature() error = nil, want failure on a tampered payload")
	}
	if status.Verified {
		t.Fatalf("status = %+v, want Verified=false", status)
	}
	// The declared signedHashHex no longer matches the recomputed content hash,
	// so this is caught before the signature check even runs.
	if status.Reason != "hash_mismatch" && status.Reason != "invalid_signature" {
		t.Fatalf("status.Reason = %q, want hash_mismatch or invalid_signature", status.Reason)
	}
}
