package update

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/sigdomain"
)

// The two accepted signature forms, and the absence of a path between them.
//
// Seal Council 2026-07-31: the update lane gained a domain-separated signature
// form so the bonded node key can sign releases through the same content-bound
// endpoint that signs modules. The requirement is ADDITIVE — every manifest
// that verified before must still verify — and the requirement is that there is
// NO DOWNGRADE: a manifest naming a domain is never retried in legacy mode.

func domainTestKey(t *testing.T) (ed25519.PrivateKey, string) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	der, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		t.Fatalf("MarshalPKIXPublicKey: %v", err)
	}
	return priv, base64.StdEncoding.EncodeToString(der)
}

func domainTestManifest(t *testing.T, statementDomain string) map[string]any {
	t.Helper()
	bundleSum := sha256.Sum256([]byte("bundle"))
	wasmSum := sha256.Sum256([]byte("carrier"))
	signing := map[string]any{
		"key_id":    "testroot",
		"algorithm": "Ed25519",
	}
	if statementDomain != "" {
		signing["statement_domain"] = statementDomain
	}
	return map[string]any{
		"schema":     ManifestSchema,
		"update_id":  "u-1",
		"version":    "1.0.0",
		"sequence":   json.Number("5"),
		"channel":    "beta",
		"created_at": "2026-07-31T00:00:00Z",
		"expires_at": "2027-07-31T00:00:00Z",
		"target":     map[string]any{"platform": "linux", "arch": "amd64", "kind": "cli-bundle"},
		"bundle":     map[string]any{"hash": hex.EncodeToString(bundleSum[:]), "size": json.Number("6"), "format": "tar.gz"},
		"wasm":       map[string]any{"hash": hex.EncodeToString(wasmSum[:])},
		"signing":    signing,
	}
}

func signDomainTestManifest(t *testing.T, doc map[string]any, key ed25519.PrivateKey, domain string) []byte {
	t.Helper()
	unsigned, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	canonical, err := CanonicalManifestBytes(unsigned)
	if err != nil {
		t.Fatalf("CanonicalManifestBytes: %v", err)
	}
	preimage := canonical
	if domain != "" {
		sum := sha256.Sum256(canonical)
		statement, err := sigdomain.Statement(domain, sum[:])
		if err != nil {
			t.Fatalf("Statement: %v", err)
		}
		preimage = statement
	}
	doc["signing"].(map[string]any)["signature"] = base64.StdEncoding.EncodeToString(ed25519.Sign(key, preimage))
	signed, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("marshal signed: %v", err)
	}
	return signed
}

func validateDomainTestManifest(t *testing.T, raw []byte, root string) error {
	t.Helper()
	manifest, err := ParseManifest(raw)
	if err != nil {
		t.Fatalf("ParseManifest: %v", err)
	}
	bundleSum := sha256.Sum256([]byte("bundle"))
	_, err = manifest.Validate(hex.EncodeToString(bundleSum[:]), VerifyOptions{
		Platform:        "linux",
		Arch:            "amd64",
		CurrentSequence: 1,
		TrustedRoots:    TrustedRoots{"testroot": root},
		Now:             time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC),
	})
	return err
}

// A manifest with no statement_domain verifies exactly as it did before the
// second form existed. Nothing previously admitted is now refused.
func TestLegacyRawSignatureStillVerifies(t *testing.T) {
	key, root := domainTestKey(t)
	raw := signDomainTestManifest(t, domainTestManifest(t, ""), key, "")
	if err := validateDomainTestManifest(t, raw, root); err != nil {
		t.Fatalf("legacy manifest rejected: %v", err)
	}
}

func TestDomainSeparatedSignatureVerifies(t *testing.T) {
	key, root := domainTestKey(t)
	doc := domainTestManifest(t, sigdomain.DomainUpdateManifestV1)
	raw := signDomainTestManifest(t, doc, key, sigdomain.DomainUpdateManifestV1)
	if err := validateDomainTestManifest(t, raw, root); err != nil {
		t.Fatalf("domain-separated manifest rejected: %v", err)
	}
}

// NO DOWNGRADE, from either direction: a legacy signature presented under a
// domain label fails, and a domain signature presented without the label fails.
func TestNoDowngradeBetweenForms(t *testing.T) {
	key, root := domainTestKey(t)

	// Legacy-signed bytes, then the label bolted on.
	legacy := domainTestManifest(t, "")
	signDomainTestManifest(t, legacy, key, "")
	legacy["signing"].(map[string]any)["statement_domain"] = sigdomain.DomainUpdateManifestV1
	relabelled, err := json.Marshal(legacy)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := validateDomainTestManifest(t, relabelled, root); err == nil {
		t.Fatal("a legacy signature verified after a statement domain was added")
	}

	// Domain-signed bytes, then the label stripped.
	doc := domainTestManifest(t, sigdomain.DomainUpdateManifestV1)
	signDomainTestManifest(t, doc, key, sigdomain.DomainUpdateManifestV1)
	delete(doc["signing"].(map[string]any), "statement_domain")
	stripped, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := validateDomainTestManifest(t, stripped, root); err == nil {
		t.Fatal("a domain-separated signature verified after the statement domain was stripped")
	}
}

// The domain is matched by EQUALITY, never by registry membership: naming the
// module domain in a manifest must be refused, not resolved.
func TestForeignStatementDomainRefused(t *testing.T) {
	key, root := domainTestKey(t)
	for _, domain := range []string{
		sigdomain.DomainModulePublicationV1,
		"SDN-UPDATE-MANIFEST-V2",
		"sdn-update-manifest-v1",
	} {
		doc := domainTestManifest(t, domain)
		// Sign it as honestly as the attacker could: with the real key, over
		// that domain's own statement where the domain is registered at all.
		unsigned, err := json.Marshal(doc)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		canonical, err := CanonicalManifestBytes(unsigned)
		if err != nil {
			t.Fatalf("canonical: %v", err)
		}
		sum := sha256.Sum256(canonical)
		preimage := canonical
		if statement, err := sigdomain.Statement(domain, sum[:]); err == nil {
			preimage = statement
		}
		doc["signing"].(map[string]any)["signature"] = base64.StdEncoding.EncodeToString(ed25519.Sign(key, preimage))
		raw, err := json.Marshal(doc)
		if err != nil {
			t.Fatalf("marshal signed: %v", err)
		}

		err = validateDomainTestManifest(t, raw, root)
		if err == nil {
			t.Fatalf("statement domain %q was accepted by the update verifier", domain)
		}
		if !strings.Contains(err.Error(), "statement domain") {
			t.Fatalf("statement domain %q: error = %v, want a statement-domain refusal", domain, err)
		}
	}
}
