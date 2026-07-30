package api

// Acceptance tests for POST /api/v1/admin/modules/sign
// (graph/tasks/sdn-module-signing-endpoint.md, Seal Council 2026-07-30).
//
// The end-to-end test at the bottom walks the REAL LANE's node-side, in
// process: POST artifact bytes -> node signs -> the returned signature_entry is
// written into an MBL publication trailer exactly as the module SDK writer does
// -> the resulting artifact is admitted by modulert's load-time gate as
// Verified against the node's own advertised publisher key. That is acceptance
// items 1, 3 and 5 minus the network hop and the JS writer.

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/MBL"
	flatbuffers "github.com/google/flatbuffers/go"
	"github.com/libp2p/go-libp2p/core/peer"

	"github.com/spacedatanetwork/sdn-server/internal/auth"
	"github.com/spacedatanetwork/sdn-server/internal/modulert"
	"github.com/spacedatanetwork/sdn-server/internal/modulesign"
	"github.com/spacedatanetwork/sdn-server/internal/peers"
	"github.com/spacedatanetwork/sdn-server/internal/sigdomain"
)

var signingTestWasm = []byte{
	0x00, 0x61, 0x73, 0x6d, 0x01, 0x00, 0x00, 0x00, // \0asm, version 1
	0x00, 0x09, 0x04, 'n', 'a', 'm', 'e', 0x01, 0x02, 0x03, 0x04, // a custom section, so the module has a body
}

// signingNode is a node handle that can sign — the shape *node.Node presents to
// internal/api.
type signingNode struct{ key ed25519.PrivateKey }

func (n *signingNode) PublishToTopic(context.Context, string, []byte) error { return nil }
func (n *signingNode) SigningKey() []byte {
	if n.key == nil {
		return nil
	}
	return append([]byte(nil), n.key...)
}

// mutePublisher can publish but holds no signing key.
type mutePublisher struct{}

func (mutePublisher) PublishToTopic(context.Context, string, []byte) error { return nil }

func newSigningTestHandler(t *testing.T) (*CoreAPIHandler, ed25519.PublicKey, string) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	auditPath := filepath.Join(t.TempDir(), "module-signing.audit.jsonl")
	t.Setenv("SDN_MODULE_SIGNING_AUDIT_LOG", auditPath)

	h := NewCoreAPIHandler(peer.ID("12D3KooTestNode"), nil, nil, &signingNode{key: priv}, nil, nil, nil, nil, nil)
	h.registerModuleSigningRoutes(http.NewServeMux())
	if h.moduleSigner == nil {
		t.Fatal("module signer was not constructed")
	}
	return h, pub, auditPath
}

// adminRequest builds a POST carrying an authenticated Admin session, bypassing
// the middleware the way auth.ContextWithSession is documented for.
func adminRequest(body []byte) *http.Request {
	r := httptest.NewRequest(http.MethodPost, ModuleSigningRoute, bytes.NewReader(body))
	r.Header.Set("Content-Type", "application/wasm")
	r.RemoteAddr = "203.0.113.9:54321"
	return r.WithContext(auth.ContextWithSession(r.Context(), &auth.Session{
		XPub:       "xpub6TestAdminPrincipal",
		TrustLevel: peers.Admin,
	}))
}

func decodeSignResponse(t *testing.T, rec *httptest.ResponseRecorder) moduleSignResponse {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	var resp moduleSignResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v (%s)", err, rec.Body.String())
	}
	return resp
}

// --- acceptance 1: signs bytes it hashed itself, under a dedicated domain ----

func TestModuleSignEndpointSignsPortableBytes(t *testing.T) {
	h, pub, _ := newSigningTestHandler(t)

	rec := httptest.NewRecorder()
	h.handleModuleSign(rec, adminRequest(signingTestWasm))
	resp := decodeSignResponse(t, rec)

	if resp.StatementDomain != sigdomain.DomainModulePublicationV1 {
		t.Fatalf("statement_domain = %q, want %q", resp.StatementDomain, sigdomain.DomainModulePublicationV1)
	}
	if resp.PublicKeyHex != hex.EncodeToString(pub) {
		t.Fatalf("public_key_hex = %q, want the node signing key %q", resp.PublicKeyHex, hex.EncodeToString(pub))
	}
	if resp.PortableBytes != len(signingTestWasm) || resp.TrailerStripped {
		t.Fatalf("portable_bytes = %d / trailer_stripped = %v, want %d / false", resp.PortableBytes, resp.TrailerStripped, len(signingTestWasm))
	}

	hash, err := hex.DecodeString(resp.ContentHash)
	if err != nil {
		t.Fatalf("content_hash is not hex: %v", err)
	}
	statement, err := sigdomain.Statement(sigdomain.DomainModulePublicationV1, hash)
	if err != nil {
		t.Fatalf("Statement: %v", err)
	}
	sig, err := hex.DecodeString(resp.SignatureHex)
	if err != nil {
		t.Fatalf("signature_hex is not hex: %v", err)
	}
	if !ed25519.Verify(pub, statement, sig) {
		t.Fatal("returned signature does not verify over the domain-separated statement")
	}
}

// TestModuleSignResponseKeyCasing pins the JSON contract: node-synthesized
// top-level fields are lowercase snake_case, and the nested signature entry
// keeps the module SDK's camelCase verbatim.
func TestModuleSignResponseKeyCasing(t *testing.T) {
	h, _, _ := newSigningTestHandler(t)

	rec := httptest.NewRecorder()
	h.handleModuleSign(rec, adminRequest(signingTestWasm))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatalf("decode: %v", err)
	}
	for _, key := range []string{"content_hash", "statement_domain", "algorithm", "public_key_hex", "signature_hex", "portable_bytes", "trailer_stripped", "signed_at", "signature_entry"} {
		if _, ok := raw[key]; !ok {
			t.Fatalf("response is missing %q; got keys %v", key, mapKeys(raw))
		}
	}
	var entry map[string]json.RawMessage
	if err := json.Unmarshal(raw["signature_entry"], &entry); err != nil {
		t.Fatalf("decode signature_entry: %v", err)
	}
	for _, key := range []string{"publicKeyHex", "signatureHex", "signedHashHex", "signedHashAlgorithm", "statementDomain"} {
		if _, ok := entry[key]; !ok {
			t.Fatalf("signature_entry is missing SDK field %q; got %v", key, mapKeys(entry))
		}
	}
}

func mapKeys(m map[string]json.RawMessage) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// --- acceptance 2: a caller-supplied digest is REFUSED ----------------------

func TestModuleSignRefusesCallerSuppliedDigest(t *testing.T) {
	h, _, auditPath := newSigningTestHandler(t)

	build := func(mutate func(*http.Request)) *httptest.ResponseRecorder {
		r := adminRequest(signingTestWasm)
		mutate(r)
		rec := httptest.NewRecorder()
		h.handleModuleSign(rec, r)
		return rec
	}

	digestHex := "8f434346648f6b96df89dda901c5176b10a6d83961dd3c1ac88b59b2dc327aa4"

	cases := map[string]func(*http.Request){
		"query content_hash": func(r *http.Request) { r.URL.RawQuery = "content_hash=" + digestHex },
		"query digest":       func(r *http.Request) { r.URL.RawQuery = "digest=" + digestHex },
		"query sha256":       func(r *http.Request) { r.URL.RawQuery = "sha256=" + digestHex },
		"empty-valued query": func(r *http.Request) { r.URL.RawQuery = "hash=" },
		"header X-Sdn-Content-Hash": func(r *http.Request) {
			r.Header.Set("X-Sdn-Content-Hash", digestHex)
		},
		"header Digest":     func(r *http.Request) { r.Header.Set("Digest", "sha-256="+digestHex) },
		"JSON body wrapper": func(r *http.Request) { r.Header.Set("Content-Type", "application/json") },
		"JSON+suffix type":  func(r *http.Request) { r.Header.Set("Content-Type", "application/vnd.sdn+json") },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			rec := build(mutate)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400 — the node must never accept a caller-supplied digest (body %s)", rec.Code, rec.Body.String())
			}
			if !bytes.Contains(rec.Body.Bytes(), []byte(modulesign.CodeDigestNotAccepted)) {
				t.Fatalf("body = %s, want error code %s", rec.Body.String(), modulesign.CodeDigestNotAccepted)
			}
		})
	}

	// Refused at the transport layer means the signer was never invoked, so
	// nothing reached the audit — and, crucially, nothing was signed.
	if data, err := os.ReadFile(auditPath); err == nil && len(bytes.TrimSpace(data)) != 0 {
		t.Fatalf("digest-bearing requests reached the signer; audit contains: %s", data)
	}
}

// The body itself being a digest is refused too — the structural half of the
// same rule (a digest is not a wasm module).
func TestModuleSignRefusesDigestAsBody(t *testing.T) {
	h, _, _ := newSigningTestHandler(t)
	digest, err := hex.DecodeString("8f434346648f6b96df89dda901c5176b10a6d83961dd3c1ac88b59b2dc327aa4")
	if err != nil {
		t.Fatalf("decode: %v", err)
	}

	rec := httptest.NewRecorder()
	h.handleModuleSign(rec, adminRequest(digest))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for a 32-byte digest body", rec.Code)
	}
	if !bytes.Contains(rec.Body.Bytes(), []byte(modulesign.CodeNotWasmModule)) {
		t.Fatalf("body = %s, want %s", rec.Body.String(), modulesign.CodeNotWasmModule)
	}
}

// --- gates ------------------------------------------------------------------

func TestModuleSignRejectsNonPost(t *testing.T) {
	h, _, _ := newSigningTestHandler(t)
	for _, method := range []string{http.MethodGet, http.MethodPut, http.MethodDelete} {
		r := httptest.NewRequest(method, ModuleSigningRoute, nil)
		rec := httptest.NewRecorder()
		h.handleModuleSign(rec, r)
		if rec.Code != http.StatusMethodNotAllowed {
			t.Fatalf("%s status = %d, want 405", method, rec.Code)
		}
	}
}

// The endpoint must NOT inherit CoreAPIHandler.requireAuth's fallthrough: with
// no auth handler the door is closed, not open.
func TestModuleSignFailsClosedWithoutAuthHandler(t *testing.T) {
	h, _, _ := newSigningTestHandler(t)
	mux := http.NewServeMux()
	h.registerModuleSigningRoutes(mux)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, ModuleSigningRoute, bytes.NewReader(signingTestWasm)))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 — an unauthenticated daemon must refuse to sign, never fall through", rec.Code)
	}
	if !bytes.Contains(rec.Body.Bytes(), []byte("AUTH_NOT_CONFIGURED")) {
		t.Fatalf("body = %s, want AUTH_NOT_CONFIGURED", rec.Body.String())
	}
}

// A node with no publisher key mounts no route at all: 404, not a stub that
// answers 500 later.
func TestModuleSignRouteAbsentWithoutSigningKey(t *testing.T) {
	t.Setenv("SDN_MODULE_SIGNING_AUDIT_LOG", filepath.Join(t.TempDir(), "audit.jsonl"))

	for name, publisher := range map[string]topicPublisher{
		"no signing-key method": mutePublisher{},
		"empty signing key":     &signingNode{},
	} {
		t.Run(name, func(t *testing.T) {
			h := NewCoreAPIHandler(peer.ID("12D3KooTestNode"), nil, nil, publisher, nil, nil, nil, nil, nil)
			mux := http.NewServeMux()
			h.registerModuleSigningRoutes(mux)
			if h.moduleSigner != nil {
				t.Fatal("a signer was constructed for a node with no publisher key")
			}
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, ModuleSigningRoute, nil))
			if rec.Code != http.StatusNotFound {
				t.Fatalf("status = %d, want 404 (route not mounted)", rec.Code)
			}
		})
	}
}

func TestModuleSignRouteIsRegisteredByRegisterRoutes(t *testing.T) {
	h, _, _ := newSigningTestHandler(t)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, ModuleSigningRoute, bytes.NewReader(signingTestWasm)))
	if rec.Code == http.StatusNotFound {
		t.Fatal("RegisterRoutes did not mount the module signing endpoint")
	}
}

// --- acceptance 3 + 5: the real lane, end to end ----------------------------

// appendSDKPublicationTrailer writes the artifact shape
// space-data-module-sdk/src/bundle/signing.js produces: the portable payload,
// then an SDS $REC collection carrying one MBL record whose "signature"-role
// entry is the JSON object the endpoint returned, then the
// "uint32le(len) || $REC" footer.
func appendSDKPublicationTrailer(t *testing.T, portable []byte, entry modulesign.SignatureEntry) []byte {
	t.Helper()

	entryJSON, err := json.Marshal(entry)
	if err != nil {
		t.Fatalf("marshal signature entry: %v", err)
	}

	b := flatbuffers.NewBuilder(512)
	entryIDOff := b.CreateString("signature")
	payloadOff := b.CreateByteVector(entryJSON)

	MBL.ModuleBundleEntryStart(b)
	MBL.ModuleBundleEntryAddEntryId(b, entryIDOff)
	MBL.ModuleBundleEntryAddRole(b, MBL.ModuleBundleEntryRoleSIGNATURE)
	MBL.ModuleBundleEntryAddPayloadEncoding(b, MBL.ModulePayloadEncodingJSON_UTF8)
	MBL.ModuleBundleEntryAddPayload(b, payloadOff)
	entryOff := MBL.ModuleBundleEntryEnd(b)

	MBL.MBLStartEntriesVector(b, 1)
	b.PrependUOffsetT(entryOff)
	entriesVecOff := b.EndVector(1)

	MBL.MBLStart(b)
	MBL.MBLAddEntries(b, entriesVecOff)
	mblOff := MBL.MBLEnd(b)

	standardOff := b.CreateString("MBL")

	// REC.fbs Record wrapper: value_type=MBL(80), value, standard.
	b.StartObject(3)
	b.PrependUOffsetTSlot(2, standardOff, 0)
	b.PrependUOffsetTSlot(1, mblOff, 0)
	b.PrependByteSlot(0, 80, 0)
	recordOff := b.EndObject()

	b.StartVector(4, 1, 4)
	b.PrependUOffsetT(recordOff)
	recordsVecOff := b.EndVector(1)

	versionOff := b.CreateString("1.0.0")

	b.StartObject(2)
	b.PrependUOffsetTSlot(1, recordsVecOff, 0)
	b.PrependUOffsetTSlot(0, versionOff, 0)
	recOff := b.EndObject()
	b.FinishWithFileIdentifier(recOff, []byte("$REC"))

	rec := b.FinishedBytes()
	out := append([]byte(nil), portable...)
	out = append(out, rec...)
	out = binary.LittleEndian.AppendUint32(out, uint32(len(rec)))
	return append(out, []byte("$REC")...)
}

func TestEndToEndSignedModuleLoadsAsVerified(t *testing.T) {
	h, nodePub, auditPath := newSigningTestHandler(t)

	// 1. The build box POSTs the portable artifact bytes.
	rec := httptest.NewRecorder()
	h.handleModuleSign(rec, adminRequest(signingTestWasm))
	resp := decodeSignResponse(t, rec)

	// 2. The build box appends the publication trailer with the SDK writer,
	//    using the entry the node handed back verbatim.
	artifact := appendSDKPublicationTrailer(t, signingTestWasm, resp.SignatureEntry)

	// 3. The node loads it. The policy is the one the daemon actually builds:
	//    the node SELF-TRUSTS its own publisher key
	//    (internal/node/module_signature_policy.go:86-91).
	policy := &modulert.ModuleSignaturePolicy{
		TrustedSigners:             []ed25519.PublicKey{nodePub},
		AllowUnsignedByContentHash: map[string]bool{},
	}
	portable, status, err := modulert.EnforceModuleSignaturePolicy(policy, artifact)
	if err != nil {
		t.Fatalf("EnforceModuleSignaturePolicy() error = %v — the node-signed artifact must load as Verified", err)
	}
	if !status.Signed || !status.Verified {
		t.Fatalf("status = %+v, want Signed=true Verified=true", status)
	}
	if status.Reason != "ok" {
		t.Fatalf("status.Reason = %q, want ok", status.Reason)
	}
	if status.StatementDomain != sigdomain.DomainModulePublicationV1 {
		t.Fatalf("status.StatementDomain = %q, want %q", status.StatementDomain, sigdomain.DomainModulePublicationV1)
	}
	if status.ContentHash != resp.ContentHash {
		t.Fatalf("loader content hash %s != signed content hash %s", status.ContentHash, resp.ContentHash)
	}
	if !bytes.Equal(portable, signingTestWasm) {
		t.Fatal("the loader's portable payload is not the payload that was signed")
	}

	// 4. A tampered payload fails, under the same trusted signer.
	tampered := append([]byte(nil), artifact...)
	tampered[len(signingTestWasm)-1] ^= 0xff
	if _, tamperStatus, tamperErr := modulert.EnforceModuleSignaturePolicy(policy, tampered); tamperErr == nil || tamperStatus.Verified {
		t.Fatalf("a tampered artifact was admitted: status=%+v err=%v", tamperStatus, tamperErr)
	}

	// 5. Exactly one audit line, naming what was signed.
	data, err := os.ReadFile(auditPath)
	if err != nil {
		t.Fatalf("read audit: %v", err)
	}
	lines := bytes.Split(bytes.TrimSpace(data), []byte("\n"))
	if len(lines) != 1 {
		t.Fatalf("audit has %d lines, want exactly 1", len(lines))
	}
	var entry modulesign.Entry
	if err := json.Unmarshal(lines[0], &entry); err != nil {
		t.Fatalf("audit line is not JSON: %v", err)
	}
	if entry.Event != modulesign.EventIssued || entry.ContentHash != resp.ContentHash {
		t.Fatalf("audit line = %+v, want an issued line for %s", entry, resp.ContentHash)
	}
	if entry.Requester == "" || entry.Requester == "xpub6TestAdminPrincipal" {
		t.Fatalf("audit requester = %q, want a fingerprint of the session xpub, never the raw value", entry.Requester)
	}
	if entry.RemoteIP != "203.0.113.9" {
		t.Fatalf("audit remote_ip = %q, want the caller IP without the port", entry.RemoteIP)
	}
}

// A trailered artifact submitted for RE-signing is hashed portable-first, so the
// signature matches what the loader recomputes — the re-signing lane
// saw-module-signing-enforcement step 3 depends on.
func TestEndToEndResigningAnAlreadyPublishedArtifact(t *testing.T) {
	h, nodePub, _ := newSigningTestHandler(t)

	// Start from an artifact that already carries a (stale) trailer.
	stale := appendSDKPublicationTrailer(t, signingTestWasm, modulesign.SignatureEntry{
		Algorithm:           "ed25519",
		KeyID:               "retired-dev-key",
		PublicKeyHex:        hex.EncodeToString(make([]byte, ed25519.PublicKeySize)),
		SignatureHex:        hex.EncodeToString(make([]byte, ed25519.SignatureSize)),
		SignedHashHex:       "",
		SignedHashAlgorithm: "sha256-canonical-module-hash",
	})

	rec := httptest.NewRecorder()
	h.handleModuleSign(rec, adminRequest(stale))
	resp := decodeSignResponse(t, rec)
	if !resp.TrailerStripped {
		t.Fatal("trailer_stripped = false; the node must strip the old trailer before hashing")
	}

	resigned := appendSDKPublicationTrailer(t, signingTestWasm, resp.SignatureEntry)
	_, status, err := modulert.EnforceModuleSignaturePolicy(&modulert.ModuleSignaturePolicy{
		TrustedSigners: []ed25519.PublicKey{nodePub},
	}, resigned)
	if err != nil || !status.Verified {
		t.Fatalf("re-signed artifact did not verify: status=%+v err=%v", status, err)
	}
}
