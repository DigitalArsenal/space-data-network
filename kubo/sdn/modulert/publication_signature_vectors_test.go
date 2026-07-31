package modulert

// KUBO TWIN of sdn-server/internal/modulert/publication_signature_vectors_test.go.
// The kubo fork is a SEPARATE GO MODULE and cannot import the original, so the
// verifier, the sigdomain registry AND this vector assertion are maintained
// copies. Both binaries run on the same host: a vector one copy accepts and the
// other refuses is a module that loads in one and dies in the other. The vector
// FILE is byte-identical across all three homes (this one, sdn-server's, and
// the module SDK's) and its sha256 is pinned in each — see the header of the
// sdn-server copy for the two-stage regeneration procedure, which lives THERE
// and only there.

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
// DATA, in one file that both language suites read:
//
//	sdn-server/internal/modulert/testdata/statement-domain-vectors.json
//	space-data-module-sdk/test/support/statement-domain-vectors.json   (identical bytes)
//
// The two copies are held identical by vectorsSHA256 below, which is asserted
// on both sides: edit one copy and the OTHER suite fails, which is the only
// arrangement that makes drift loud.
//
// REGENERATION is two-stage and Go is authoritative for everything signed:
//
//  1. go test ./internal/modulert -run TestWriteStatementDomainSignatureMaterial \
//     -args   (with SDN_STATEMENT_DOMAIN_MATERIAL=/path/material.json set)
//     emits the signature entries — every signature made by crypto/ed25519 over
//     a preimage built by sigdomain.Statement, never by hand.
//  2. node space-data-module-sdk/test/support/statement-domain-vectors.mjs \
//     <material.json> <out.json>
//     wraps each entry in a REAL SDK publication trailer and writes the final
//     vector file. Copy it to BOTH paths above and update vectorsSHA256.
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
	"testing"

	"github.com/ipfs/kubo/sdn/sigdomain"
)

// vectorsPath is the Go-side copy of the shared vector file.
const vectorsPath = "testdata/statement-domain-vectors.json"

// vectorsSHA256 pins the exact bytes of the shared vector file. The JS suite
// pins the SAME constant against ITS copy (space-data-module-sdk
// test/statement-domain-parity.test.js), so the two copies cannot diverge
// without a red suite on at least one side.
const vectorsSHA256 = "00432b9115f49f6f15cf7e6bd0296f2f9eca0428d7ccdd98036b32469698000d"

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
	Expect               struct {
		Verified        bool   `json:"verified"`
		Reason          string `json:"reason"`
		StatementDomain string `json:"statementDomain"`
		ContentHashHex  string `json:"contentHashHex"`
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

			goBuilt := appendPublicationTrailer(portable, buildRECTrailerWithMBLSignature(t, vec.SignatureEntry))
			checkStatementDomainVector(t, "go-built trailer", vec, goBuilt, portable, trusted)

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
	if vec.Expect.Verified && err != nil {
		t.Fatalf("%s: verification succeeded but returned err = %v", label, err)
	}
	if !vec.Expect.Verified && err == nil {
		t.Fatalf("%s: verification failed (reason=%q) but returned a nil error — every refusal must be reportable — %s", label, status.Reason, vec.Why)
	}
}
