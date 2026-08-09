package modulert

// SHARED CROSS-RUNTIME TEST VECTORS for the domain-separated module publication
// signature (Seal Council 2026-07-30; graph task sdn-sdk-statement-domain-parity,
// Janus).
//
// WHY THIS FILE EXISTS. The statement contract
//
//	"SDN-MODULE-PUBLICATION-V1" || 0x00 || sha256(portable)
//
// is now implemented FOUR times: here (sdn-server/internal/sigdomain), in the
// kubo fork's maintained twin (kubo/sdn/sigdomain), and in the module SDK's JS
// mirror (space-data-module-sdk/src/bundle/sigdomain.js) which serves both the
// Node/WasmEdge and the browser loader. Four implementations of one preimage is
// exactly the shape that drifts silently: a divergence does not fail a build,
// it just makes an artifact that verifies on the node refuse to load in the
// browser (or worse, the reverse).
//
// So the preimages, the signature bytes, and the REFUSAL REASONS are pinned as
// DATA, in one file that both language suites read — THREE homes, identical
// bytes:
//
//	sdn-server/internal/modulert/testdata/statement-domain-vectors.json
//	kubo/sdn/modulert/testdata/statement-domain-vectors.json
//	space-data-module-sdk/test/support/statement-domain-vectors.json
//
// The copies are held identical by vectorsSHA256 below, which is asserted in
// all three suites: edit one copy and the OTHERS fail, which is the only
// arrangement that makes drift loud.
//
// REGENERATION is THREE-stage and Go is authoritative for everything signed
// except the bundle-scope vectors (stage 3), whose preimage is a canonical
// statement only the SDK writer defines:
//
//  1. go test ./internal/modulert -run TestWriteStatementDomainSignatureMaterial \
//     -args   (with SDN_STATEMENT_DOMAIN_MATERIAL=/path/material.json set)
//     emits the signature entries — every signature made by crypto/ed25519 over
//     a preimage built by sigdomain.Statement, never by hand.
//  2. node space-data-module-sdk/test/support/statement-domain-vectors.mjs \
//     <material.json> <stage2.json>
//     wraps each entry in a REAL SDK publication trailer.
//  3. node space-data-module-sdk/test/support/statement-domain-vectors-bundle.mjs \
//     <stage2.json> <out.json>
//     adds the bundle-scope and scope-ambiguity vectors (SDK-signed; see that
//     file's header for why the authority inverts) and backfills signatureScope
//     / signedHashHex / flowNodeAdmissible on every earlier vector. Copy the
//     result to ALL THREE paths above and update vectorsSHA256 in all three
//     suites, in ONE change.
//
// Stage 2 lives in JS on purpose: sdkArtifactHex must be bytes the production
// SDK writer actually produces, so that TestStatementDomainVectors below proves
// the GO verifier accepts the real SDK envelope — not just a Go-built lookalike.

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spacedatanetwork/sdn-server/internal/sigdomain"
)

// vectorsPath is the Go-side copy of the shared vector file.
const vectorsPath = "testdata/statement-domain-vectors.json"

// vectorsSHA256 pins the exact bytes of the shared vector file. The JS suite
// pins the SAME constant against ITS copy (space-data-module-sdk
// test/statement-domain-parity.test.js), so the two copies cannot diverge
// without a red suite on at least one side.
const vectorsSHA256 = "3f750e0def2b0d673578e91e7ec89c8917b98c84aa9ef2488618ae51677c312c"

type statementDomainVectors struct {
	SchemaVersion int    `json:"schemaVersion"`
	Note          string `json:"note"`
	PortableHex   string `json:"portableHex"`
	SignerSeedHex string `json:"signerSeedHex"`
	SignerPubHex  string `json:"signerPublicKeyHex"`

	Registry []struct {
		Domain      string `json:"domain"`
		Description string `json:"description"`
	} `json:"registry"`

	Statements []struct {
		Name           string `json:"name"`
		Domain         string `json:"domain"`
		ContentHashHex string `json:"contentHashHex"`
		StatementHex   string `json:"statementHex"`
		Registered     bool   `json:"registered"`
	} `json:"statements"`

	Artifacts []statementDomainArtifactVector `json:"artifacts"`
}

type statementDomainArtifactVector struct {
	Name                 string          `json:"name"`
	Why                  string          `json:"why"`
	PortableHex          string          `json:"portableHex"`
	SignatureEntry       json.RawMessage `json:"signatureEntry"`
	TrustedPublicKeysHex []string        `json:"trustedPublicKeysHex"`
	SDKArtifactHex       string          `json:"sdkArtifactHex"`
	// SDKEnvelopeOnly marks a vector that cannot be re-wrapped in the minimal
	// Go-built trailer: a bundle-scope signature covers the WHOLE MBL, and the
	// hand-built trailer carries no canonicalization rule and no canonical
	// module hash, so decoding fails before any signature is examined. Those
	// vectors are checked through sdkArtifactHex alone — which is the stronger
	// check anyway, since it is the byte-for-byte envelope the SDK emits.
	SDKEnvelopeOnly bool `json:"sdkEnvelopeOnly"`
	Expect          struct {
		Verified        bool   `json:"verified"`
		Reason          string `json:"reason"`
		StatementDomain string `json:"statementDomain"`
		ContentHashHex  string `json:"contentHashHex"`
		// SignatureScope and SignedHashHex pin WHAT the signature covered, not
		// merely whether it passed. Without them "verified" says nothing about
		// which basis each implementation used to reach that verdict — which is
		// exactly how sdn-server and kubo came to disagree about bundle scope.
		SignatureScope string `json:"signatureScope"`
		SignedHashHex  string `json:"signedHashHex"`
		// FlowNodeAdmissible is the module SDK's flow-composition gate. Go does
		// not implement that gate; the field is pinned here so the vector file
		// stays one contract rather than two, and the SDK suite asserts it.
		FlowNodeAdmissible bool `json:"flowNodeAdmissible"`
	} `json:"expect"`
}

func loadStatementDomainVectors(t *testing.T) (statementDomainVectors, []byte) {
	t.Helper()
	raw, err := os.ReadFile(filepath.FromSlash(vectorsPath))
	if err != nil {
		t.Fatalf("read %s: %v", vectorsPath, err)
	}
	var v statementDomainVectors
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatalf("parse %s: %v", vectorsPath, err)
	}
	return v, raw
}

func mustHex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatalf("decode hex %q: %v", s, err)
	}
	return b
}

// TestStatementDomainVectorFileIsPinned is the anti-drift gate: if this fails,
// somebody edited the vectors without updating BOTH copies and the constant.
func TestStatementDomainVectorFileIsPinned(t *testing.T) {
	_, raw := loadStatementDomainVectors(t)
	sum := sha256.Sum256(raw)
	got := hex.EncodeToString(sum[:])
	if got != vectorsSHA256 {
		t.Fatalf("shared vector file sha256 = %s, pinned %s — regenerate BOTH copies (Go testdata + module SDK test/support) and update the constant in BOTH suites", got, vectorsSHA256)
	}
}

// TestStatementDomainRegistryMatchesVectors pins the CLOSED registry. A new
// domain must be added deliberately, in the Go registry AND the SDK mirror AND
// this file — never as a side effect.
func TestStatementDomainRegistryMatchesVectors(t *testing.T) {
	v, _ := loadStatementDomainVectors(t)
	got := sigdomain.Domains()
	if len(got) != len(v.Registry) {
		t.Fatalf("sigdomain.Domains() = %v (%d), vectors pin %d domains", got, len(got), len(v.Registry))
	}
	for i, want := range v.Registry {
		if got[i] != want.Domain {
			t.Fatalf("sigdomain.Domains()[%d] = %q, vectors pin %q", i, got[i], want.Domain)
		}
		if desc := sigdomain.Describe(want.Domain); desc != want.Description {
			t.Fatalf("sigdomain.Describe(%q) = %q, vectors pin %q", want.Domain, desc, want.Description)
		}
	}
}

// TestStatementDomainPreimageVectors pins the exact signed preimage bytes. This
// is the single most important assertion in the file: it is the byte string the
// JS mirror must produce for the same (domain, hash) pair.
func TestStatementDomainPreimageVectors(t *testing.T) {
	v, _ := loadStatementDomainVectors(t)
	for _, vec := range v.Statements {
		t.Run(vec.Name, func(t *testing.T) {
			stmt, err := sigdomain.Statement(vec.Domain, mustHex(t, vec.ContentHashHex))
			if !vec.Registered {
				if err == nil {
					t.Fatalf("sigdomain.Statement(%q, ...) succeeded; an unregistered domain must be refused", vec.Domain)
				}
				return
			}
			if err != nil {
				t.Fatalf("sigdomain.Statement(%q, ...): %v", vec.Domain, err)
			}
			if got := hex.EncodeToString(stmt); got != vec.StatementHex {
				t.Fatalf("statement = %s, vector pins %s", got, vec.StatementHex)
			}
		})
	}
}

// TestStatementDomainVectors runs every shared artifact vector through the real
// verifier, TWICE: once against a Go-built trailer around the shared signature
// entry, and once against sdkArtifactHex — the byte-for-byte envelope the
// module SDK's own writer produced for that same entry.
func TestStatementDomainVectors(t *testing.T) {
	v, _ := loadStatementDomainVectors(t)
	if len(v.Artifacts) == 0 {
		t.Fatal("vector file carries no artifact vectors")
	}
	for _, vec := range v.Artifacts {
		vec := vec
		t.Run(vec.Name, func(t *testing.T) {
			trusted := make([]ed25519.PublicKey, 0, len(vec.TrustedPublicKeysHex))
			for _, keyHex := range vec.TrustedPublicKeysHex {
				trusted = append(trusted, ed25519.PublicKey(mustHex(t, keyHex)))
			}
			portable := mustHex(t, vec.PortableHex)

			if !vec.SDKEnvelopeOnly {
				goBuilt := appendPublicationTrailer(portable, buildRECTrailerWithMBLSignature(t, vec.SignatureEntry))
				checkStatementDomainVector(t, "go-built trailer", vec, goBuilt, portable, trusted)
			}

			if vec.SDKArtifactHex != "" {
				sdkBuilt := mustHex(t, vec.SDKArtifactHex)
				checkStatementDomainVector(t, "sdk-written trailer", vec, sdkBuilt, portable, trusted)
			}
		})
	}
}

func checkStatementDomainVector(
	t *testing.T,
	label string,
	vec statementDomainArtifactVector,
	artifact []byte,
	portable []byte,
	trusted []ed25519.PublicKey,
) {
	t.Helper()
	gotPortable, status, err := verifyPublicationSignature(artifact, trusted)

	if !bytesEqual(gotPortable, portable) {
		t.Fatalf("%s: portable payload was not returned intact (%d bytes, want %d) — %s", label, len(gotPortable), len(portable), vec.Why)
	}
	if status.ContentHash != vec.Expect.ContentHashHex {
		t.Fatalf("%s: status.ContentHash = %s, vector pins %s", label, status.ContentHash, vec.Expect.ContentHashHex)
	}
	if status.Verified != vec.Expect.Verified {
		t.Fatalf("%s: status.Verified = %t, want %t (reason=%q err=%v) — %s", label, status.Verified, vec.Expect.Verified, status.Reason, err, vec.Why)
	}
	if status.Reason != vec.Expect.Reason {
		t.Fatalf("%s: status.Reason = %q, vector pins %q (err=%v) — %s", label, status.Reason, vec.Expect.Reason, err, vec.Why)
	}
	if status.StatementDomain != vec.Expect.StatementDomain {
		t.Fatalf("%s: status.StatementDomain = %q, vector pins %q", label, status.StatementDomain, vec.Expect.StatementDomain)
	}
	if status.SignatureScope != vec.Expect.SignatureScope {
		t.Fatalf("%s: status.SignatureScope = %q, vector pins %q — the verdict may match while the BASIS does not, which is the divergence this field exists to catch", label, status.SignatureScope, vec.Expect.SignatureScope)
	}
	if status.SignedHash != vec.Expect.SignedHashHex {
		t.Fatalf("%s: status.SignedHash = %q, vector pins %q", label, status.SignedHash, vec.Expect.SignedHashHex)
	}
	if vec.Expect.Verified && err != nil {
		t.Fatalf("%s: verification succeeded but returned err = %v", label, err)
	}
	if !vec.Expect.Verified && err == nil {
		t.Fatalf("%s: verification failed (reason=%q) but returned a nil error — every refusal must be reportable — %s", label, status.Reason, vec.Why)
	}
}

// TestWriteStatementDomainSignatureMaterial is the stage-1 GENERATOR. It is a
// no-op unless SDN_STATEMENT_DOMAIN_MATERIAL names an output path, so it costs
// nothing in CI while keeping the generator in the tree next to the assertions
// it feeds. See this file's header for the two-stage procedure.
func TestWriteStatementDomainSignatureMaterial(t *testing.T) {
	out := strings.TrimSpace(os.Getenv("SDN_STATEMENT_DOMAIN_MATERIAL"))
	if out == "" {
		t.Skip("set SDN_STATEMENT_DOMAIN_MATERIAL=<path> to regenerate the shared vector signature material")
	}

	// A FIXED seed, so the vectors are reproducible byte-for-byte on any box
	// and in any language: Ed25519 signing is deterministic, so the SDK mirror
	// signing the same statement with the same seed must produce the same 64
	// bytes — which the JS suite asserts as its own round-trip check.
	seedHex := "4a414e55532d5354415445-4d454e542d444f4d41494e2d5631"
	seedHex = strings.ReplaceAll(seedHex, "-", "")
	for len(seedHex) < 64 {
		seedHex += "00"
	}
	seed := mustHex(t, seedHex[:64])
	priv := ed25519.NewKeyFromSeed(seed)
	pub := priv.Public().(ed25519.PublicKey)
	pubHex := hex.EncodeToString(pub)

	// A minimal but real wasm module: preamble plus one non-sds custom section,
	// so a payload tamper is expressible without breaking wasm parsing on the
	// JS side (which must parse the module to write a bundle around it).
	portable := []byte{
		0x00, 0x61, 0x73, 0x6d, 0x01, 0x00, 0x00, 0x00,
		0x00, 0x05, 0x01, 0x78, 0x01, 0x02, 0x03,
	}
	sum := sha256.Sum256(portable)
	contentHash := hex.EncodeToString(sum[:])

	tampered := append([]byte(nil), portable...)
	tampered[len(tampered)-1] ^= 0xff
	tamperedSum := sha256.Sum256(tampered)

	statement := func(domain string) []byte {
		stmt, err := sigdomain.Statement(domain, sum[:])
		if err != nil {
			t.Fatalf("sigdomain.Statement(%q): %v", domain, err)
		}
		return stmt
	}
	// Unregistered domains cannot go through sigdomain.Statement (that is the
	// point of the registry), so the forgery is assembled by hand — exactly as
	// an attacker would have to.
	forged := append(append([]byte("SDN-MADE-UP-DOMAIN"), 0), sum[:]...)

	entry := func(domain string, signedMessage []byte, signedHashHex string) map[string]any {
		return entryWithAlgorithm(moduleSignatureAlgorithm, domain, signedMessage, signedHashHex, pubHex, priv)
	}
	entryAlg := func(algorithm, domain string, signedMessage []byte, signedHashHex string) map[string]any {
		return entryWithAlgorithm(algorithm, domain, signedMessage, signedHashHex, pubHex, priv)
	}
	_ = func(domain string, signedMessage []byte, signedHashHex string) map[string]any {
		e := map[string]any{
			"algorithm":           moduleSignatureAlgorithm,
			"keyId":               "janus-vector-key",
			"publicKeyHex":        pubHex,
			"signatureHex":        hex.EncodeToString(ed25519.Sign(priv, signedMessage)),
			"signedHashHex":       signedHashHex,
			"signedHashAlgorithm": "sha256-canonical-module-hash",
		}
		if domain != "" {
			e["statementDomain"] = domain
		}
		return e
	}

	type expect struct {
		Verified        bool   `json:"verified"`
		Reason          string `json:"reason"`
		StatementDomain string `json:"statementDomain"`
		ContentHashHex  string `json:"contentHashHex"`
	}
	type artifact struct {
		Name                 string         `json:"name"`
		Why                  string         `json:"why"`
		PortableHex          string         `json:"portableHex"`
		SignatureEntry       map[string]any `json:"signatureEntry"`
		TrustedPublicKeysHex []string       `json:"trustedPublicKeysHex"`
		Expect               expect         `json:"expect"`
	}
	trusted := []string{pubHex}
	mod := sigdomain.DomainModulePublicationV1
	upd := sigdomain.DomainUpdateManifestV1
	sig := sigdomain.DomainUpdateSignalV1

	artifacts := []artifact{
		{
			Name:                 "domain-signed-ok",
			Why:                  "the node-signed form: signature covers domain||0x00||sha256(portable)",
			PortableHex:          hex.EncodeToString(portable),
			SignatureEntry:       entry(mod, statement(mod), contentHash),
			TrustedPublicKeysHex: trusted,
			Expect:               expect{Verified: true, Reason: "ok", StatementDomain: mod, ContentHashHex: contentHash},
		},
		{
			Name:                 "domain-signed-ok-whitespace-padded-domain",
			Why:                  "declared domain is whitespace-padded; both sides TrimSpace before comparing",
			PortableHex:          hex.EncodeToString(portable),
			SignatureEntry:       entry("  "+mod+"  ", statement(mod), contentHash),
			TrustedPublicKeysHex: trusted,
			Expect:               expect{Verified: true, Reason: "ok", StatementDomain: mod, ContentHashHex: contentHash},
		},
		{
			Name:                 "domain-signed-ok-nel-padded-domain",
			Why:                  "U+0085 NEL pads the domain: Go's strings.TrimSpace strips it and JS's String.trim does NOT, so this vector is the whole reason the SDK trims Go's whitespace set explicitly",
			PortableHex:          hex.EncodeToString(portable),
			SignatureEntry:       entry("\u0085"+mod+"\u0085", statement(mod), contentHash),
			TrustedPublicKeysHex: trusted,
			Expect:               expect{Verified: true, Reason: "ok", StatementDomain: mod, ContentHashHex: contentHash},
		},
		{
			Name:                 "domain-bom-padded-is-refused",
			Why:                  "U+FEFF BOM pads the domain: JS's String.trim strips it and Go's does NOT, so a naive JS verifier would ACCEPT what the node refuses",
			PortableHex:          hex.EncodeToString(portable),
			SignatureEntry:       entry("\ufeff"+mod, statement(mod), contentHash),
			TrustedPublicKeysHex: trusted,
			Expect:               expect{Verified: false, Reason: "unsupported_statement_domain", StatementDomain: "\ufeff" + mod, ContentHashHex: contentHash},
		},
		{
			Name:                 "domain-signed-ok-uppercase-algorithm",
			Why:                  "the node compares the algorithm with EqualFold after TrimSpace; an exact-match verifier would refuse what the node admits",
			PortableHex:          hex.EncodeToString(portable),
			SignatureEntry:       entryAlg("  ED25519 ", mod, statement(mod), contentHash),
			TrustedPublicKeysHex: trusted,
			Expect:               expect{Verified: true, Reason: "ok", StatementDomain: mod, ContentHashHex: contentHash},
		},
		{
			Name:                 "bare-digest-labelled-with-domain",
			Why:                  "regression gate on the whole design: a bare-digest signature must not pass as a domain-separated one",
			PortableHex:          hex.EncodeToString(portable),
			SignatureEntry:       entry(mod, sum[:], contentHash),
			TrustedPublicKeysHex: trusted,
			Expect:               expect{Verified: false, Reason: "invalid_signature", StatementDomain: mod, ContentHashHex: contentHash},
		},
		{
			Name:                 "foreign-registered-domain",
			Why:                  "cross-domain replay: an update-manifest signature must not be admissible as a module signature",
			PortableHex:          hex.EncodeToString(portable),
			SignatureEntry:       entry(upd, statement(upd), contentHash),
			TrustedPublicKeysHex: trusted,
			Expect:               expect{Verified: false, Reason: "unsupported_statement_domain", StatementDomain: upd, ContentHashHex: contentHash},
		},
		{
			Name:                 "foreign-registered-domain-untrusted-signer",
			Why:                  "ORDER pin: what the signature covers is resolved BEFORE who signed it, so the reason is the domain, not the signer",
			PortableHex:          hex.EncodeToString(portable),
			SignatureEntry:       entry(upd, statement(upd), contentHash),
			TrustedPublicKeysHex: []string{},
			Expect:               expect{Verified: false, Reason: "unsupported_statement_domain", StatementDomain: upd, ContentHashHex: contentHash},
		},
		{
			Name:                 "update-signal-domain-is-refused-for-a-module",
			Why:                  "the update SIGNAL domain (owner ruling 2026-08-09) is a POINTER space, not an authorization space: a signature minted over a signal is cheap, frequent and broadcast to anyone listening, so if it were admissible here a listener could staple it onto BYTES. Registered, well-formed, correctly signed by a trusted key — and still refused, because the module verifier admits exactly one domain",
			PortableHex:          hex.EncodeToString(portable),
			SignatureEntry:       entry(sig, statement(sig), contentHash),
			TrustedPublicKeysHex: trusted,
			Expect:               expect{Verified: false, Reason: "unsupported_statement_domain", StatementDomain: sig, ContentHashHex: contentHash},
		},
		{
			Name:                 "update-signal-domain-untrusted-signer",
			Why:                  "ORDER pin for the signal domain, and it matters MORE here than for the reserved manifest domain because the signal has a live producer: what the signature covers is resolved BEFORE who signed it, so a replayed signal is refused as the wrong STATEMENT KIND even when the signer would also have failed. A verifier that checked the signer first would report untrusted_signer and hide the replay",
			PortableHex:          hex.EncodeToString(portable),
			SignatureEntry:       entry(sig, statement(sig), contentHash),
			TrustedPublicKeysHex: []string{},
			Expect:               expect{Verified: false, Reason: "unsupported_statement_domain", StatementDomain: sig, ContentHashHex: contentHash},
		},
		{
			Name:                 "unregistered-domain",
			Why:                  "the registry is CLOSED: an unknown label is refused even though its preimage is well-formed",
			PortableHex:          hex.EncodeToString(portable),
			SignatureEntry:       entry("SDN-MADE-UP-DOMAIN", forged, contentHash),
			TrustedPublicKeysHex: trusted,
			Expect:               expect{Verified: false, Reason: "unsupported_statement_domain", StatementDomain: "SDN-MADE-UP-DOMAIN", ContentHashHex: contentHash},
		},
		{
			Name:                 "domain-signed-untrusted-signer",
			Why:                  "a perfectly formed domain signature from a key nobody trusts is still refused",
			PortableHex:          hex.EncodeToString(portable),
			SignatureEntry:       entry(mod, statement(mod), contentHash),
			TrustedPublicKeysHex: []string{},
			Expect:               expect{Verified: false, Reason: "untrusted_signer", StatementDomain: mod, ContentHashHex: contentHash},
		},
		{
			Name:                 "domain-signed-tampered-payload",
			Why:                  "payload mutated after signing; the declared content hash no longer matches the bytes",
			PortableHex:          hex.EncodeToString(tampered),
			SignatureEntry:       entry(mod, statement(mod), contentHash),
			TrustedPublicKeysHex: trusted,
			Expect:               expect{Verified: false, Reason: "hash_mismatch", StatementDomain: "", ContentHashHex: hex.EncodeToString(tamperedSum[:])},
		},
		{
			Name:                 "legacy-bare-digest-no-domain",
			Why:                  "pre-domain SDK artifacts keep verifying byte-for-byte: zero blast radius on the live admit path",
			PortableHex:          hex.EncodeToString(portable),
			SignatureEntry:       entry("", sum[:], contentHash),
			TrustedPublicKeysHex: trusted,
			Expect:               expect{Verified: true, Reason: "ok", StatementDomain: "", ContentHashHex: contentHash},
		},
	}

	registry := make([]map[string]string, 0, len(sigdomain.Domains()))
	for _, d := range sigdomain.Domains() {
		registry = append(registry, map[string]string{"domain": d, "description": sigdomain.Describe(d)})
	}

	statements := []map[string]any{
		{"name": "module-publication-over-portable-hash", "domain": mod, "contentHashHex": contentHash, "statementHex": hex.EncodeToString(statement(mod)), "registered": true},
		{"name": "update-manifest-over-portable-hash", "domain": upd, "contentHashHex": contentHash, "statementHex": hex.EncodeToString(statement(upd)), "registered": true},
		{"name": "update-signal-over-portable-hash", "domain": sig, "contentHashHex": contentHash, "statementHex": hex.EncodeToString(statement(sig)), "registered": true},
		{"name": "unregistered-domain-has-no-statement", "domain": "SDN-MADE-UP-DOMAIN", "contentHashHex": contentHash, "statementHex": "", "registered": false},
	}

	material := map[string]any{
		"schemaVersion":      1,
		"note":               "Generated by TestWriteStatementDomainSignatureMaterial (sdn-server/internal/modulert). Go is authoritative for every signature and preimage here; stage 2 (the SDK writer) only wraps them in a publication trailer.",
		"portableHex":        hex.EncodeToString(portable),
		"signerSeedHex":      hex.EncodeToString(seed),
		"signerPublicKeyHex": pubHex,
		"registry":           registry,
		"statements":         statements,
		"artifacts":          artifacts,
	}
	encoded, err := json.MarshalIndent(material, "", "  ")
	if err != nil {
		t.Fatalf("marshal material: %v", err)
	}
	encoded = append(encoded, '\n')
	if err := os.WriteFile(out, encoded, 0o644); err != nil {
		t.Fatalf("write %s: %v", out, err)
	}
	t.Logf("wrote %d bytes of statement-domain signature material to %s", len(encoded), out)
}

// entryWithAlgorithm builds one signature-entry payload. The algorithm is a
// parameter so a vector can pin the node's EqualFold/TrimSpace tolerance rather
// than assume it.
func entryWithAlgorithm(algorithm, domain string, signedMessage []byte, signedHashHex, pubHex string, priv ed25519.PrivateKey) map[string]any {
	e := map[string]any{
		"algorithm":           algorithm,
		"keyId":               "janus-vector-key",
		"publicKeyHex":        pubHex,
		"signatureHex":        hex.EncodeToString(ed25519.Sign(priv, signedMessage)),
		"signedHashHex":       signedHashHex,
		"signedHashAlgorithm": "sha256-canonical-module-hash",
	}
	if domain != "" {
		e["statementDomain"] = domain
	}
	return e
}
