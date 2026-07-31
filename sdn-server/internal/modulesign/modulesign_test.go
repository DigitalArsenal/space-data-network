package modulesign

import (
	"bufio"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spacedatanetwork/sdn-server/internal/sigdomain"
)

var wasmHeader = []byte{0x00, 0x61, 0x73, 0x6d, 0x01, 0x00, 0x00, 0x00}

func testSigner(t *testing.T) (*Signer, ed25519.PublicKey, string) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	auditPath := filepath.Join(t.TempDir(), "audit", "module-signing.audit.jsonl")
	signer, err := NewSigner(priv, "12D3KooTestPeer", NewAuditLog(auditPath))
	if err != nil {
		t.Fatalf("NewSigner: %v", err)
	}
	return signer, pub, auditPath
}

func readAudit(t *testing.T, path string) []Entry {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open audit log: %v", err)
	}
	defer f.Close()
	var out []Entry
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(strings.TrimSpace(string(line))) == 0 {
			continue
		}
		var entry Entry
		if err := json.Unmarshal(line, &entry); err != nil {
			t.Fatalf("audit line is not valid JSON: %v (%s)", err, line)
		}
		out = append(out, entry)
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan audit log: %v", err)
	}
	return out
}

// --- the core property: the node hashes, and signs a domain-separated statement

func TestSignProducesDomainSeparatedSignature(t *testing.T) {
	signer, pub, _ := testSigner(t)

	result, err := signer.Sign(Request{Artifact: wasmHeader, Requester: "abc123def456"})
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}

	sum := sha256.Sum256(wasmHeader)
	if result.ContentHash != hex.EncodeToString(sum[:]) {
		t.Fatalf("ContentHash = %s, want %s", result.ContentHash, hex.EncodeToString(sum[:]))
	}
	if result.StatementDomain != sigdomain.DomainModulePublicationV1 {
		t.Fatalf("StatementDomain = %q, want %q", result.StatementDomain, sigdomain.DomainModulePublicationV1)
	}

	sig, err := hex.DecodeString(result.SignatureHex)
	if err != nil || len(sig) != ed25519.SignatureSize {
		t.Fatalf("SignatureHex is not a 64-byte hex signature: %q (%v)", result.SignatureHex, err)
	}
	statement, err := sigdomain.Statement(sigdomain.DomainModulePublicationV1, sum[:])
	if err != nil {
		t.Fatalf("Statement: %v", err)
	}
	if !ed25519.Verify(pub, statement, sig) {
		t.Fatal("signature does not verify over the domain-separated statement")
	}
	// The property the whole design exists for.
	if ed25519.Verify(pub, sum[:], sig) {
		t.Fatal("signature ALSO verifies over the bare digest — the endpoint is a cross-protocol oracle")
	}
}

// TestSignedEntryIsSDKShaped pins the emitted trailer entry against the module
// SDK's field names (space-data-module-sdk/src/bundle/signing.js:362-369): a
// rename here silently breaks the build box's trailer writer.
func TestSignedEntryIsSDKShaped(t *testing.T) {
	signer, _, _ := testSigner(t)
	result, err := signer.Sign(Request{Artifact: wasmHeader})
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}

	raw, err := json.Marshal(result.Entry)
	if err != nil {
		t.Fatalf("marshal entry: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("unmarshal entry: %v", err)
	}
	for _, key := range []string{"algorithm", "keyId", "publicKeyHex", "signatureHex", "signedHashHex", "signedHashAlgorithm", "statementDomain"} {
		if _, ok := decoded[key]; !ok {
			t.Fatalf("signature entry is missing SDK field %q; got %v", key, decoded)
		}
	}
	if decoded["algorithm"] != SignatureAlgorithm {
		t.Fatalf("algorithm = %v, want %q", decoded["algorithm"], SignatureAlgorithm)
	}
	if decoded["signedHashAlgorithm"] != SignedHashAlgorithm {
		t.Fatalf("signedHashAlgorithm = %v, want %q", decoded["signedHashAlgorithm"], SignedHashAlgorithm)
	}
	if decoded["keyId"] != "12D3KooTestPeer" {
		t.Fatalf("keyId = %v, want the node key id", decoded["keyId"])
	}
	if decoded["signedHashHex"] != result.ContentHash {
		t.Fatalf("signedHashHex = %v, want the recomputed content hash %s", decoded["signedHashHex"], result.ContentHash)
	}
}

// --- "never sign a caller-supplied digest", structurally

func TestSignRefusesDigestShapedInput(t *testing.T) {
	signer, _, auditPath := testSigner(t)
	sum := sha256.Sum256(wasmHeader)

	cases := map[string][]byte{
		"raw 32-byte sha256 digest": sum[:],
		"64-char hex digest text":   []byte(hex.EncodeToString(sum[:])),
		"empty":                     nil,
		"short":                     {0x00, 0x61, 0x73},
		"not wasm at all":           []byte("{\"cid\":\"bafy...\",\"file_id\":\"x\"}"),
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			result, err := signer.Sign(Request{Artifact: body})
			if err == nil {
				t.Fatalf("Sign(%s) returned a signature %+v; the node must refuse anything that is not module bytes", name, result)
			}
			var refusal *Refusal
			if !errors.As(err, &refusal) {
				t.Fatalf("Sign(%s) error = %v, want a *Refusal", name, err)
			}
			if refusal.Code != CodeNotWasmModule && refusal.Code != CodeEmptyPayload {
				t.Fatalf("Sign(%s) refusal code = %q, want %q or %q", name, refusal.Code, CodeNotWasmModule, CodeEmptyPayload)
			}
		})
	}

	// Every refusal is on the record, and none of them minted a signature.
	entries := readAudit(t, auditPath)
	if len(entries) != len(cases) {
		t.Fatalf("audit has %d lines, want %d (one per refused request)", len(entries), len(cases))
	}
	for _, e := range entries {
		if e.Event != EventRefused {
			t.Fatalf("audit event = %q, want %q", e.Event, EventRefused)
		}
		if e.SignatureHex != "" {
			t.Fatalf("a refused request recorded a signature: %+v", e)
		}
	}
}

// A DPM manifest's unsigned bytes are the concrete cross-protocol payload the
// domain separation exists to defeat; they are also not a wasm module, so they
// never even reach the signer. Both defenses, independently.
func TestSignRefusesDatasetPublicationManifestBytes(t *testing.T) {
	signer, _, _ := testSigner(t)
	// A size-prefixed FlatBuffer, as buildDatasetPublicationManifestBytes emits.
	fake := make([]byte, 4)
	binary.LittleEndian.PutUint32(fake, 128)
	fake = append(fake, []byte("$DPM-like-flatbuffer-bytes")...)

	if _, err := signer.Sign(Request{Artifact: fake}); err == nil {
		t.Fatal("the signer accepted non-wasm bytes; a dataset-publication payload must never reach the signing key")
	}
}

func TestSignRefusesOversizedArtifact(t *testing.T) {
	signer, _, _ := testSigner(t)
	oversized := make([]byte, MaxArtifactBytes+1)
	copy(oversized, wasmHeader)

	_, err := signer.Sign(Request{Artifact: oversized})
	var refusal *Refusal
	if !errors.As(err, &refusal) || refusal.Code != CodePayloadTooLarge {
		t.Fatalf("Sign(oversized) error = %v, want %s", err, CodePayloadTooLarge)
	}
}

// --- the trailer-stripping contract

func TestSignHashesPortablePayloadOfAnAlreadyPublishedArtifact(t *testing.T) {
	signer, _, _ := testSigner(t)

	// "payload || REC bytes || uint32le(len) || $REC" — the layout
	// modulert.StripPublicationTrailer expects.
	rec := []byte("pretend-REC-record-collection-bytes")
	protected := append([]byte(nil), wasmHeader...)
	protected = append(protected, rec...)
	protected = binary.LittleEndian.AppendUint32(protected, uint32(len(rec)))
	protected = append(protected, []byte("$REC")...)

	result, err := signer.Sign(Request{Artifact: protected})
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	if !result.TrailerStripped {
		t.Fatal("TrailerStripped = false, want true for an artifact carrying a publication trailer")
	}
	sum := sha256.Sum256(wasmHeader)
	if result.ContentHash != hex.EncodeToString(sum[:]) {
		t.Fatalf("ContentHash = %s, want the PORTABLE payload hash %s — a signature over the envelope would never match at load time",
			result.ContentHash, hex.EncodeToString(sum[:]))
	}
	if result.PortableBytes != len(wasmHeader) {
		t.Fatalf("PortableBytes = %d, want %d", result.PortableBytes, len(wasmHeader))
	}
}

// --- the audit is a gate, not a log

func TestSignerRequiresAuditLog(t *testing.T) {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	if _, err := NewSigner(priv, "peer", nil); err == nil {
		t.Fatal("NewSigner accepted a nil audit log; an unauditable signer must not exist")
	}
	if _, err := NewSigner([]byte{1, 2, 3}, "peer", NewAuditLog("x")); err == nil {
		t.Fatal("NewSigner accepted a malformed key")
	}
}

func TestSignDiscardsSignatureWhenAuditCannotBeWritten(t *testing.T) {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	// A path whose parent is a FILE: MkdirAll must fail, so the append must
	// fail, so the signature must not be returned.
	dir := t.TempDir()
	blocker := filepath.Join(dir, "not-a-directory")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatalf("write blocker: %v", err)
	}
	signer, err := NewSigner(priv, "peer", NewAuditLog(filepath.Join(blocker, "audit.jsonl")))
	if err != nil {
		t.Fatalf("NewSigner: %v", err)
	}

	result, err := signer.Sign(Request{Artifact: wasmHeader})
	if err == nil {
		t.Fatalf("Sign returned %+v with an unwritable audit log; the signature must be discarded", result)
	}
	if result != nil {
		t.Fatal("Sign returned a non-nil result alongside an audit failure")
	}
	if !strings.Contains(err.Error(), "audit") {
		t.Fatalf("error = %v, want it to name the audit failure", err)
	}
}

func TestAuditLineContentAndPermissions(t *testing.T) {
	signer, _, auditPath := testSigner(t)
	fingerprint := FingerprintPrincipal("xpub6RawSecretLookingValue")

	result, err := signer.Sign(Request{Artifact: wasmHeader, Requester: fingerprint, RemoteIP: "203.0.113.7"})
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}

	entries := readAudit(t, auditPath)
	if len(entries) != 1 {
		t.Fatalf("audit has %d lines, want exactly 1 per signature", len(entries))
	}
	e := entries[0]
	if e.Event != EventIssued {
		t.Fatalf("event = %q, want %q", e.Event, EventIssued)
	}
	if e.ContentHash != result.ContentHash || e.SignatureHex != result.SignatureHex {
		t.Fatalf("audit line does not match the issued signature: %+v", e)
	}
	if e.StatementDomain != sigdomain.DomainModulePublicationV1 {
		t.Fatalf("audit statement_domain = %q", e.StatementDomain)
	}
	if e.Requester != fingerprint {
		t.Fatalf("audit requester = %q, want the fingerprint %q", e.Requester, fingerprint)
	}
	if strings.Contains(e.Requester, "xpub") {
		t.Fatalf("audit recorded a raw xpub: %q", e.Requester)
	}
	if e.RemoteIP != "203.0.113.7" {
		t.Fatalf("audit remote_ip = %q", e.RemoteIP)
	}
	if e.Timestamp.IsZero() {
		t.Fatal("audit line has no timestamp")
	}

	info, err := os.Stat(auditPath)
	if err != nil {
		t.Fatalf("stat audit log: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("audit log mode = %o, want 0600", perm)
	}
	dirInfo, err := os.Stat(filepath.Dir(auditPath))
	if err != nil {
		t.Fatalf("stat audit dir: %v", err)
	}
	if perm := dirInfo.Mode().Perm(); perm != 0o700 {
		t.Fatalf("audit dir mode = %o, want 0700", perm)
	}
}

// The audit is APPEND-ONLY: a second signature must not truncate the first.
func TestAuditIsAppendOnly(t *testing.T) {
	signer, _, auditPath := testSigner(t)
	for i := 0; i < 3; i++ {
		body := append([]byte(nil), wasmHeader...)
		body = append(body, byte(i))
		if _, err := signer.Sign(Request{Artifact: body}); err != nil {
			t.Fatalf("Sign #%d: %v", i, err)
		}
	}
	entries := readAudit(t, auditPath)
	if len(entries) != 3 {
		t.Fatalf("audit has %d lines, want 3", len(entries))
	}
	seen := map[string]bool{}
	for _, e := range entries {
		if seen[e.ContentHash] {
			t.Fatalf("duplicate content hash in audit: %s", e.ContentHash)
		}
		seen[e.ContentHash] = true
	}
}

func TestFingerprintPrincipal(t *testing.T) {
	if got := FingerprintPrincipal(""); got != "" {
		t.Fatalf("FingerprintPrincipal(\"\") = %q, want empty", got)
	}
	got := FingerprintPrincipal("xpub6Something")
	if len(got) != PrincipalFingerprintLen {
		t.Fatalf("fingerprint = %q (len %d), want len %d to match deployment/signing.json", got, len(got), PrincipalFingerprintLen)
	}
	if strings.Contains(got, "xpub") {
		t.Fatalf("fingerprint leaks the input: %q", got)
	}
	if FingerprintPrincipal("xpub6Something") != got {
		t.Fatal("fingerprint is not deterministic")
	}
	if FingerprintPrincipal("xpub6SomethingElse") == got {
		t.Fatal("fingerprint collided across distinct principals")
	}
}

func TestDefaultAuditPathHonoursOverride(t *testing.T) {
	t.Setenv(auditLogEnv, "/custom/module-signing.jsonl")
	if got := DefaultAuditPath(); got != "/custom/module-signing.jsonl" {
		t.Fatalf("DefaultAuditPath() = %q, want the override", got)
	}
	t.Setenv(auditLogEnv, "")
	got := DefaultAuditPath()
	if got == "" {
		t.Skip("no home directory in this environment")
	}
	if !strings.HasSuffix(got, filepath.Join(".spacedatanetwork", "logs", "module-signing.audit.jsonl")) {
		t.Fatalf("DefaultAuditPath() = %q, want it under ~/.spacedatanetwork/logs", got)
	}
}
