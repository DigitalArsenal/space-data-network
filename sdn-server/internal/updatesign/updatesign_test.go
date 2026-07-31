package updatesign

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/sigdomain"
	"github.com/spacedatanetwork/sdn-server/internal/update"
)

func newTestSigner(t *testing.T) (*Signer, ed25519.PublicKey, string) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	auditPath := filepath.Join(t.TempDir(), "audit", "update-signing.audit.jsonl")
	signer, err := NewSigner(priv, NewAuditLog(auditPath))
	if err != nil {
		t.Fatalf("NewSigner: %v", err)
	}
	return signer, pub, auditPath
}

// manifestDoc is the minimal well-formed manifest the producer submits: every
// verifier-required field present, statement_domain stated, signature absent.
func manifestDoc(t *testing.T, keyID string, mutate func(map[string]any)) []byte {
	t.Helper()
	bundle := []byte("bundle payload")
	carrier := []byte("carrier payload")
	bundleSum := sha256.Sum256(bundle)
	wasmSum := sha256.Sum256(carrier)

	doc := map[string]any{
		"schema":     update.ManifestSchema,
		"update_id":  "sdn-cli-2026-07-31",
		"version":    "1.2.3",
		"sequence":   json.Number("7"),
		"channel":    "beta",
		"created_at": "2026-07-31T00:00:00Z",
		"expires_at": "2027-07-31T00:00:00Z",
		"target": map[string]any{
			"platform": "linux",
			"arch":     "amd64",
			"kind":     "cli-bundle",
		},
		"bundle": map[string]any{
			"hash":   hex.EncodeToString(bundleSum[:]),
			"size":   json.Number("14"),
			"format": "tar.gz",
		},
		"wasm": map[string]any{
			"hash": hex.EncodeToString(wasmSum[:]),
		},
		"signing": map[string]any{
			"key_id":           keyID,
			"algorithm":        SignatureAlgorithm,
			"statement_domain": sigdomain.DomainUpdateManifestV1,
		},
	}
	if mutate != nil {
		mutate(doc)
	}
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("marshal manifest: %v", err)
	}
	return raw
}

// TestIssuedSignatureVerifiesThroughTheRealVerifier is the acceptance test that
// matters: a signature this package issues must be accepted by the SAME code
// path the fleet runs, with the node key installed as a trust root. If this
// passes, a release signed by the node installs on a node.
func TestIssuedSignatureVerifiesThroughTheRealVerifier(t *testing.T) {
	signer, pub, _ := newTestSigner(t)
	raw := manifestDoc(t, signer.KeyID(), nil)

	result, err := signer.Sign(Request{Manifest: raw})
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	if result.StatementDomain != sigdomain.DomainUpdateManifestV1 {
		t.Fatalf("StatementDomain = %q", result.StatementDomain)
	}

	signed := withSignature(t, raw, result.SignatureB64)
	manifest, err := update.ParseManifest(signed)
	if err != nil {
		t.Fatalf("ParseManifest: %v", err)
	}

	bundleSum := sha256.Sum256([]byte("bundle payload"))
	if _, err := manifest.Validate(hex.EncodeToString(bundleSum[:]), update.VerifyOptions{
		Platform:        "linux",
		Arch:            "amd64",
		CurrentSequence: 1,
		TrustedRoots:    update.TrustedRoots{signer.KeyID(): result.PublicKeyB64},
		Now:             time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC),
	}); err != nil {
		t.Fatalf("Validate rejected a node-signed manifest: %v", err)
	}

	// And the raw math, so a failure above is attributable.
	canonical, err := update.CanonicalManifestBytes(signed)
	if err != nil {
		t.Fatalf("CanonicalManifestBytes: %v", err)
	}
	sum := sha256.Sum256(canonical)
	statement, err := sigdomain.Statement(sigdomain.DomainUpdateManifestV1, sum[:])
	if err != nil {
		t.Fatalf("Statement: %v", err)
	}
	sig, err := base64.StdEncoding.DecodeString(result.SignatureB64)
	if err != nil {
		t.Fatalf("decode signature: %v", err)
	}
	if !ed25519.Verify(pub, statement, sig) {
		t.Fatal("issued signature does not verify over its own domain statement")
	}
	if ed25519.Verify(pub, canonical, sig) {
		t.Fatal("issued signature ALSO verifies over the bare canonical bytes — domain separation is not binding")
	}
}

// TestTamperedPayloadFails: the signature covers the whole document, so moving
// the bundle hash invalidates it.
func TestTamperedPayloadFails(t *testing.T) {
	signer, _, _ := newTestSigner(t)
	raw := manifestDoc(t, signer.KeyID(), nil)
	result, err := signer.Sign(Request{Manifest: raw})
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}

	tampered := manifestDoc(t, signer.KeyID(), func(doc map[string]any) {
		doc["bundle"].(map[string]any)["hash"] = strings.Repeat("a", 64)
	})
	signed := withSignature(t, tampered, result.SignatureB64)
	manifest, err := update.ParseManifest(signed)
	if err != nil {
		t.Fatalf("ParseManifest: %v", err)
	}
	if _, err := manifest.Validate(strings.Repeat("a", 64), update.VerifyOptions{
		Platform:        "linux",
		Arch:            "amd64",
		CurrentSequence: 1,
		TrustedRoots:    update.TrustedRoots{signer.KeyID(): result.PublicKeyB64},
		Now:             time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC),
	}); err == nil {
		t.Fatal("a tampered manifest verified")
	}
}

// TestRefusesDigestAndNonManifestBodies is the structural half of "never sign a
// caller-supplied digest": the things a caller might POST INSTEAD of a manifest
// are unsignable, not merely unwanted.
func TestRefusesDigestAndNonManifestBodies(t *testing.T) {
	signer, _, _ := newTestSigner(t)
	sum := sha256.Sum256([]byte("some artifact"))

	bodies := map[string][]byte{
		"raw hex digest":     []byte(hex.EncodeToString(sum[:])),
		"raw digest bytes":   sum[:],
		"json string digest": []byte(`"` + hex.EncodeToString(sum[:]) + `"`),
		"digest object":      []byte(`{"sha256":"` + hex.EncodeToString(sum[:]) + `"}`),
		"empty":              nil,
		"wasm module":        {0x00, 0x61, 0x73, 0x6d, 0x01, 0x00, 0x00, 0x00},
		"wrong schema":       []byte(`{"schema":"org.spacedatanetwork.bundle.v1"}`),
	}
	for name, body := range bodies {
		if _, err := signer.Sign(Request{Manifest: body}); err == nil {
			t.Fatalf("Sign(%s) succeeded; a non-manifest must be unsignable", name)
		} else {
			var refusal *Refusal
			if !errors.As(err, &refusal) {
				t.Fatalf("Sign(%s) error = %v, want a Refusal", name, err)
			}
		}
	}
}

// TestRefusesForeignAndMissingStatementDomain closes the cross-protocol path:
// this endpoint must never be talked into signing under the module domain, and
// must never sign a document that does not say what it is.
func TestRefusesForeignAndMissingStatementDomain(t *testing.T) {
	signer, _, _ := newTestSigner(t)

	cases := map[string]any{
		"module domain":  sigdomain.DomainModulePublicationV1,
		"unregistered":   "SDN-UPDATE-MANIFEST-V2",
		"lowercase":      "sdn-update-manifest-v1",
		"empty":          "",
		"whitespace":     "   ",
		"dpm pnm domain": "SDN-DPM-PNM",
	}
	for name, domain := range cases {
		raw := manifestDoc(t, signer.KeyID(), func(doc map[string]any) {
			doc["signing"].(map[string]any)["statement_domain"] = domain
		})
		_, err := signer.Sign(Request{Manifest: raw})
		if err == nil {
			t.Fatalf("Sign with %s statement domain succeeded", name)
		}
		var refusal *Refusal
		if !errors.As(err, &refusal) || refusal.Code != CodeBadStatementScope {
			t.Fatalf("Sign with %s domain: error = %v, want %s", name, err, CodeBadStatementScope)
		}
	}

	// The field missing entirely is the same refusal, not a legacy fallback.
	raw := manifestDoc(t, signer.KeyID(), func(doc map[string]any) {
		delete(doc["signing"].(map[string]any), "statement_domain")
	})
	if _, err := signer.Sign(Request{Manifest: raw}); err == nil {
		t.Fatal("Sign without a statement domain succeeded; this signer never issues legacy-form signatures")
	}
}

// TestUnwritableAuditDiscardsSignature — property 3 is a GATE.
func TestUnwritableAuditDiscardsSignature(t *testing.T) {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	dir := t.TempDir()
	// A regular FILE where the audit's parent directory must be: MkdirAll fails,
	// so the append fails, so the signature must never be returned.
	blocker := filepath.Join(dir, "logs")
	if err := os.WriteFile(blocker, []byte("not a directory"), 0o600); err != nil {
		t.Fatalf("write blocker: %v", err)
	}
	signer, err := NewSigner(priv, NewAuditLog(filepath.Join(blocker, "update-signing.audit.jsonl")))
	if err != nil {
		t.Fatalf("NewSigner: %v", err)
	}
	result, err := signer.Sign(Request{Manifest: manifestDoc(t, signer.KeyID(), nil)})
	if err == nil {
		t.Fatal("Sign succeeded with an unwritable audit log")
	}
	if result != nil {
		t.Fatal("Sign returned a signature it could not audit")
	}
	if !strings.Contains(err.Error(), "discarded") {
		t.Fatalf("error = %v, want it to say the signature was discarded", err)
	}
}

func TestAuditLineRecordsTheRelease(t *testing.T) {
	signer, _, auditPath := newTestSigner(t)
	if _, err := signer.Sign(Request{Manifest: manifestDoc(t, signer.KeyID(), nil), Requester: FingerprintPrincipal("xpub-of-the-operator")}); err != nil {
		t.Fatalf("Sign: %v", err)
	}
	data, err := os.ReadFile(auditPath)
	if err != nil {
		t.Fatalf("read audit: %v", err)
	}
	var entry Entry
	if err := json.Unmarshal([]byte(strings.TrimSpace(string(data))), &entry); err != nil {
		t.Fatalf("parse audit line: %v", err)
	}
	if entry.Event != EventIssued || entry.UpdateID != "sdn-cli-2026-07-31" || entry.Version != "1.2.3" || entry.Sequence != 7 {
		t.Fatalf("audit line does not identify the release: %+v", entry)
	}
	if entry.StatementDomain != sigdomain.DomainUpdateManifestV1 {
		t.Fatalf("audit statement domain = %q", entry.StatementDomain)
	}
	if strings.Contains(string(data), "xpub-of-the-operator") {
		t.Fatal("audit line contains a raw principal; it must be fingerprinted")
	}
	if len(entry.Requester) != PrincipalFingerprintLen {
		t.Fatalf("requester fingerprint width = %d, want %d", len(entry.Requester), PrincipalFingerprintLen)
	}
}

// TestKeyIDAndTrustRootFormAgree: the value the endpoint logs for the trust
// store must be the value the desktop verifier can actually load. The desktop
// side constructs its key with type:'spki', format:'der', so a raw-hex root
// silently works on the fleet and fails on the desktop.
func TestKeyIDAndTrustRootFormAgree(t *testing.T) {
	signer, pub, _ := newTestSigner(t)
	der, err := base64.StdEncoding.DecodeString(signer.PublicKeyB64())
	if err != nil {
		t.Fatalf("trust root is not base64: %v", err)
	}
	if len(der) != 44 {
		t.Fatalf("SPKI DER length = %d, want 44 for Ed25519", len(der))
	}
	if !strings.HasSuffix(hex.EncodeToString(der), hex.EncodeToString(pub)) {
		t.Fatal("SPKI DER does not end with the raw public key")
	}
	if len(signer.KeyID()) != PrincipalFingerprintLen {
		t.Fatalf("KeyID width = %d", len(signer.KeyID()))
	}
}

// withSignature inserts the detached signature into signing.signature, which is
// exactly what the producer does after the endpoint answers.
func withSignature(t *testing.T, raw []byte, signature string) []byte {
	t.Helper()
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.UseNumber()
	var doc map[string]any
	if err := decoder.Decode(&doc); err != nil {
		t.Fatalf("decode manifest: %v", err)
	}
	doc["signing"].(map[string]any)["signature"] = signature
	out, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("marshal signed manifest: %v", err)
	}
	return out
}
