package flowrt

// Loop I2: flow bundles now go through the SAME publication-trailer
// signature admit gate the module load path applies (modulert's loop I1
// instantiateWASM gate), reused here via modulert.EnforceModuleSignaturePolicy
// at every point flow-bundle wasm bytes are first read with the trailer
// intact: FlowManager.Deploy/loadAndRegister (manager.go), LoadMountedFlow
// (httpmount.go), and LoadFlowService (cronmount.go).
//
// The fixture helpers below (buildFlowRECTrailerWithMBLSignature /
// buildSignedFlowArtifact / tamperFlowArtifact) hand-build the exact on-wire
// shape internal/modulert/publication_signature_test.go's equivalent
// helpers do — duplicated here (not imported) because they reference
// modulert's unexported constants/types and this package intentionally
// reuses only the exported admit-gate entrypoint, not test internals.
//
// Tests that need a REAL compiled+linked flow bundle to prove the
// "correctly-signed artifact loads" and "nil policy loads unsigned"
// end-to-end cases reuse dataRetrievalFlowDist(t), which already skips
// gracefully when the space-data-network-modules checkout/build is not
// present (the established pattern for every other flowrt integration
// test in this package). The unsigned/tampered-rejection tests need no
// real wasm at all: the signature gate runs, and must reject, BEFORE any
// wasm bytes are ever handed to wasmrt.NewModule.

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/MBL"
	flatbuffers "github.com/google/flatbuffers/go"

	"github.com/spacedatanetwork/sdn-server/internal/config"
	"github.com/spacedatanetwork/sdn-server/internal/modulert"
	"github.com/spacedatanetwork/sdn-server/internal/modulert/caps"
	"github.com/spacedatanetwork/sdn-server/plugins"
)

// --- fixture construction (mirrors internal/modulert/publication_signature_test.go) ---

// testFlowSignatureEntryID/testFlowRECStandardMBL mirror
// modulert.moduleSignatureEntryID / modulert.mblStandard, which are unexported
// and so cannot be imported directly; findModuleSignatureEntry also matches on
// MBL.ModuleBundleEntryRoleSIGNATURE alone, but these are set for full parity
// with what the module SDK actually emits.
//
// testFlowRECRecordTypeMBLCurrent is the ordinal MBL holds TODAY and is written
// only so the fixture looks like a real publisher's output. The verifier keys on
// the STANDARD STRING (sdn-rec-ordinal-hardcoded-mbl-80): this fixture used to
// carry the ordinal alone, which meant it would have kept passing after a union
// renumber broke production — a test that agrees with the bug is worse than no
// test.
const (
	testFlowSignatureEntryID               = "signature"
	testFlowRECStandardMBL          string = "MBL"
	testFlowRECRecordTypeMBLCurrent byte   = 80
	testFlowSignatureMagic          string = "$REC"
	testFlowSignatureAlgo           string = "ed25519"
	testFlowSignatureHashAlgo       string = "sha256-canonical-module-hash"
)

// flowSignaturePayload mirrors modulert.moduleSignaturePayload's JSON shape
// exactly (field names/casing matter for interop with verifyPublicationSignature).
type flowSignaturePayload struct {
	Algorithm           string `json:"algorithm"`
	KeyID               string `json:"keyId"`
	PublicKeyHex        string `json:"publicKeyHex"`
	SignatureHex        string `json:"signatureHex"`
	SignedHashHex       string `json:"signedHashHex"`
	SignedHashAlgorithm string `json:"signedHashAlgorithm"`
}

func mustGenerateFlowSignerKey(t *testing.T) (ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("ed25519.GenerateKey failed: %v", err)
	}
	return pub, priv
}

// buildFlowRECTrailerWithMBLSignature hand-builds an SDS $REC record
// collection carrying one MBL bundle entry in the SIGNATURE role, exactly
// the shape modulert.findModuleSignatureEntry parses.
func buildFlowRECTrailerWithMBLSignature(t *testing.T, signaturePayloadJSON []byte) []byte {
	t.Helper()

	b := flatbuffers.NewBuilder(512)

	entryIDOff := b.CreateString(testFlowSignatureEntryID)
	payloadOff := b.CreateByteVector(signaturePayloadJSON)

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

	standardOff := b.CreateString(testFlowRECStandardMBL)

	// Hand-rolled REC.fbs "Record" wrapper: value_type=MBL(80), value=mblOff,
	// standard="MBL" (see modulert/publication_signature.go's recRoot/
	// recRecord doc comment for why this is hand-rolled instead of using the
	// vendored REC Go package).
	b.StartObject(3)
	b.PrependUOffsetTSlot(2, standardOff, 0)
	b.PrependUOffsetTSlot(1, mblOff, 0)
	b.PrependByteSlot(0, testFlowRECRecordTypeMBLCurrent, 0)
	recordOff := b.EndObject()

	b.StartVector(4, 1, 4)
	b.PrependUOffsetT(recordOff)
	recordsVecOff := b.EndVector(1)

	versionOff := b.CreateString("1.0.0")

	// Hand-rolled REC.fbs "REC" root wrapper: version=versionOff,
	// RECORDS=recordsVecOff.
	b.StartObject(2)
	b.PrependUOffsetTSlot(1, recordsVecOff, 0)
	b.PrependUOffsetTSlot(0, versionOff, 0)
	recOff := b.EndObject()
	b.FinishWithFileIdentifier(recOff, []byte(testFlowSignatureMagic))

	return b.FinishedBytes()
}

// appendFlowPublicationTrailer appends rec after payload in the exact
// layout modulert.StripPublicationTrailer expects:
// payload || rec || uint32le(len(rec)) || "$REC".
func appendFlowPublicationTrailer(payload, rec []byte) []byte {
	out := make([]byte, 0, len(payload)+len(rec)+8)
	out = append(out, payload...)
	out = append(out, rec...)
	var lenBuf [4]byte
	lenBuf[0] = byte(len(rec))
	lenBuf[1] = byte(len(rec) >> 8)
	lenBuf[2] = byte(len(rec) >> 16)
	lenBuf[3] = byte(len(rec) >> 24)
	out = append(out, lenBuf[:]...)
	out = append(out, []byte(testFlowSignatureMagic)...)
	return out
}

// buildSignedFlowArtifact appends a publication trailer over portable,
// signed by signer, in the same shape modulert.verifyPublicationSignature
// expects (the module SDK's signModuleArtifact output shape).
func buildSignedFlowArtifact(t *testing.T, portable []byte, signer ed25519.PrivateKey, keyID string) []byte {
	t.Helper()

	pub, ok := signer.Public().(ed25519.PublicKey)
	if !ok {
		t.Fatalf("signer.Public() did not return an ed25519.PublicKey")
	}
	sum := sha256.Sum256(portable)
	sig := ed25519.Sign(signer, sum[:])

	payload := flowSignaturePayload{
		Algorithm:           testFlowSignatureAlgo,
		KeyID:               keyID,
		PublicKeyHex:        hex.EncodeToString(pub),
		SignatureHex:        hex.EncodeToString(sig),
		SignedHashHex:       hex.EncodeToString(sum[:]),
		SignedHashAlgorithm: testFlowSignatureHashAlgo,
	}
	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal signature payload: %v", err)
	}

	recBytes := buildFlowRECTrailerWithMBLSignature(t, payloadJSON)
	return appendFlowPublicationTrailer(portable, recBytes)
}

// tamperFlowArtifact flips a byte inside the portable region of a signed
// artifact, keeping the original trailer — the signature was computed over
// the old portable bytes, so this must fail signature verification with a
// content-hash mismatch.
func tamperFlowArtifact(signed []byte) []byte {
	tampered := append([]byte(nil), signed...)
	tampered[0] ^= 0xFF
	return tampered
}

func minimalFlowJSON(programID string) []byte {
	prog := FlowProgram{ProgramID: programID, Name: "sig-admission-fixture", Version: "1.0.0"}
	b, _ := json.Marshal(prog)
	return b
}

// newTestFlowManager builds a fresh FlowManager backed by a temp-dir store,
// mirroring what internal/node wires (plugins.New(), no capability
// handlers needed for these signature-gate-only tests).
func newTestFlowManager(t *testing.T) *FlowManager {
	t.Helper()
	cfg := config.FlowsConfig{
		Enabled:        true,
		StoragePath:    filepath.Join(t.TempDir(), "flows"),
		MaxMemoryPages: 256,
	}
	mgr, err := NewFlowManager(cfg, plugins.New(), HandlerMap{})
	if err != nil {
		t.Fatalf("NewFlowManager: %v", err)
	}
	return mgr
}

// --- FlowManager.Deploy (install-time admit point) -----------------------

func TestFlowManagerDeployRejectsUnsignedWhenPolicyConfigured(t *testing.T) {
	mgr := newTestFlowManager(t)
	pub, _ := mustGenerateFlowSignerKey(t)
	mgr.SetModuleSignaturePolicy(&modulert.ModuleSignaturePolicy{TrustedSigners: []ed25519.PublicKey{pub}})

	const programID = "test.flow.deploy.reject-unsigned"
	unsigned := []byte("plain unsigned flow-bundle bytes, no publication trailer at all")

	_, err := mgr.Deploy(context.Background(), unsigned, minimalFlowJSON(programID), nil)
	if err == nil {
		t.Fatal("Deploy() with unsigned artifact under a configured policy = nil error, want rejection")
	}
	if !strings.Contains(err.Error(), "publication signature") {
		t.Fatalf("expected rejection to name the publication signature gate, got: %v", err)
	}
	if _, getErr := mgr.store.Get(programID); getErr == nil {
		t.Fatal("Deploy() must not install a signature-rejected artifact to disk")
	}
}

func TestFlowManagerDeployRejectsTamperedArtifactWhenPolicyConfigured(t *testing.T) {
	mgr := newTestFlowManager(t)
	pub, priv := mustGenerateFlowSignerKey(t)
	mgr.SetModuleSignaturePolicy(&modulert.ModuleSignaturePolicy{TrustedSigners: []ed25519.PublicKey{pub}})

	const programID = "test.flow.deploy.reject-tampered"
	portable := []byte("portable flow-bundle payload for the tamper test")
	signed := buildSignedFlowArtifact(t, portable, priv, "flow-signer-tamper")
	tampered := tamperFlowArtifact(signed)

	_, err := mgr.Deploy(context.Background(), tampered, minimalFlowJSON(programID), nil)
	if err == nil {
		t.Fatal("Deploy() with a tampered signed artifact = nil error, want rejection")
	}
	if !strings.Contains(err.Error(), "publication signature") {
		t.Fatalf("expected rejection to name the publication signature gate, got: %v", err)
	}
	if _, getErr := mgr.store.Get(programID); getErr == nil {
		t.Fatal("Deploy() must not install a tamper-rejected artifact to disk")
	}
}

func TestFlowManagerDeployAcceptsCorrectlySignedArtifact(t *testing.T) {
	mgr := newTestFlowManager(t)
	pub, priv := mustGenerateFlowSignerKey(t)
	mgr.SetModuleSignaturePolicy(&modulert.ModuleSignaturePolicy{TrustedSigners: []ed25519.PublicKey{pub}})

	const programID = "test.flow.deploy.accept-signed"
	portable := []byte("portable flow-bundle payload for the accept test")
	signed := buildSignedFlowArtifact(t, portable, priv, "flow-signer-accept")

	// Deploy tolerates a subsequent standalone-start failure (this fixture
	// is not real wasm, so loadAndRegister's later wasm-compile step fails
	// and is logged as a warning, not returned — see manager.go's Deploy
	// doc). What this test asserts is the SIGNATURE GATE specifically: a
	// trusted-signer artifact must be admitted (installed), not rejected.
	gotID, err := mgr.Deploy(context.Background(), signed, minimalFlowJSON(programID), nil)
	if err != nil {
		t.Fatalf("Deploy() with a correctly-signed, trusted-signer artifact failed: %v", err)
	}
	if gotID != programID {
		t.Fatalf("Deploy() programID = %q, want %q", gotID, programID)
	}

	atRest, err := os.ReadFile(mgr.store.WASMPath(programID))
	if err != nil {
		t.Fatalf("read installed artifact: %v", err)
	}
	if string(atRest) != string(signed) {
		t.Fatal("installed artifact must stay signed at rest (verbatim, trailer intact)")
	}
}

func TestFlowManagerDeployNilPolicyAdmitsUnsignedArtifact(t *testing.T) {
	mgr := newTestFlowManager(t) // SetModuleSignaturePolicy never called: nil, the default.

	const programID = "test.flow.deploy.nil-policy-inert"
	unsigned := []byte("plain unsigned flow-bundle bytes, nil policy must not reject this")

	gotID, err := mgr.Deploy(context.Background(), unsigned, minimalFlowJSON(programID), nil)
	if err != nil {
		t.Fatalf("Deploy() with nil signature policy (inert default) failed: %v", err)
	}
	if gotID != programID {
		t.Fatalf("Deploy() programID = %q, want %q", gotID, programID)
	}
	if _, getErr := mgr.store.Get(programID); getErr != nil {
		t.Fatalf("Deploy() under nil policy must install the artifact: %v", getErr)
	}
}

// --- FlowManager.loadAndRegister (cold-restart / LoadAll admit point) ----

func TestFlowManagerLoadAndRegisterRejectsUnsignedWhenPolicyConfigured(t *testing.T) {
	mgr := newTestFlowManager(t)
	pub, _ := mustGenerateFlowSignerKey(t)
	mgr.SetModuleSignaturePolicy(&modulert.ModuleSignaturePolicy{TrustedSigners: []ed25519.PublicKey{pub}})

	const programID = "test.flow.loadandregister.reject-unsigned"
	unsigned := []byte("plain unsigned flow-bundle bytes installed directly (bypassing Deploy)")
	if err := mgr.store.Install(programID, unsigned, minimalFlowJSON(programID), nil); err != nil {
		t.Fatalf("store.Install: %v", err)
	}
	flow, err := mgr.store.Get(programID)
	if err != nil {
		t.Fatalf("store.Get: %v", err)
	}

	err = mgr.loadAndRegister(context.Background(), flow)
	if err == nil {
		t.Fatal("loadAndRegister() with unsigned artifact under a configured policy = nil error, want rejection")
	}
	if !strings.Contains(err.Error(), "publication signature") {
		t.Fatalf("expected rejection to name the publication signature gate, got: %v", err)
	}
	mgr.mu.Lock()
	_, running := mgr.running[programID]
	mgr.mu.Unlock()
	if running {
		t.Fatal("a signature-rejected flow must not be registered as running")
	}
}

func TestFlowManagerLoadAndRegisterNilPolicyDoesNotEnforceSignature(t *testing.T) {
	mgr := newTestFlowManager(t) // nil policy, the default.

	const programID = "test.flow.loadandregister.nil-policy-inert"
	unsigned := []byte("plain unsigned flow-bundle bytes; not real wasm either")
	if err := mgr.store.Install(programID, unsigned, minimalFlowJSON(programID), nil); err != nil {
		t.Fatalf("store.Install: %v", err)
	}
	flow, err := mgr.store.Get(programID)
	if err != nil {
		t.Fatalf("store.Get: %v", err)
	}

	// This artifact is not real wasm, so loadAndRegister still fails overall
	// (at NewFlowRuntime's wasm compile step) — what this test asserts is
	// that the FAILURE IS NOT ATTRIBUTABLE TO THE SIGNATURE GATE under a nil
	// policy, proving the gate itself is inert (mirrors modulert's
	// TestEnforceModuleSignaturePolicyRejectsUnsignedUnlessAllowlisted nil-
	// policy case).
	err = mgr.loadAndRegister(context.Background(), flow)
	if err == nil {
		t.Fatal("loadAndRegister() with non-wasm bytes should still fail (invalid wasm), got nil error")
	}
	if strings.Contains(err.Error(), "publication signature") {
		t.Fatalf("nil signature policy must not enforce the gate, got: %v", err)
	}
}

// --- LoadMountedFlow (httpmount.go load-time admit point) ----------------

func TestLoadMountedFlowRejectsUnsignedWhenPolicyConfigured(t *testing.T) {
	pub, _ := mustGenerateFlowSignerKey(t)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "runtime.wasm"), []byte("unsigned garbage, no trailer"), 0644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	_, err := LoadMountedFlow(dir, FlowMountDeps{
		CapRegistry: modulert.NewCapabilityRegistry(),
		NodeCtx: &modulert.NodeContext{
			ModuleSignaturePolicy: &modulert.ModuleSignaturePolicy{TrustedSigners: []ed25519.PublicKey{pub}},
		},
	})
	if err == nil {
		t.Fatal("LoadMountedFlow() with unsigned artifact under a configured policy = nil error, want rejection")
	}
	if !strings.Contains(err.Error(), "publication signature") {
		t.Fatalf("expected rejection to name the publication signature gate, got: %v", err)
	}
}

func TestLoadMountedFlowNilPolicyDoesNotEnforceSignature(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "runtime.wasm"), []byte("unsigned garbage, no trailer, still not real wasm"), 0644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	// Nil NodeCtx.ModuleSignaturePolicy: the gate must not fire. This fixture is
	// still not real wasm, so the load fails at wasm compile — but that
	// failure must not be attributable to the signature gate.
	_, err := LoadMountedFlow(dir, FlowMountDeps{
		CapRegistry: modulert.NewCapabilityRegistry(),
		NodeCtx:     &modulert.NodeContext{},
	})
	if err == nil {
		t.Fatal("LoadMountedFlow() with non-wasm bytes should still fail, got nil error")
	}
	if strings.Contains(err.Error(), "publication signature") {
		t.Fatalf("nil signature policy must not enforce the gate, got: %v", err)
	}
}

// --- LoadFlowService (cronmount.go load-time admit point) ----------------

func TestLoadFlowServiceRejectsUnsignedWhenPolicyConfigured(t *testing.T) {
	pub, _ := mustGenerateFlowSignerKey(t)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "runtime.wasm"), []byte("unsigned garbage, no trailer"), 0644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	flowJSON := []byte(`{"programId":"test.flow.service.reject","triggers":[{"triggerId":"tick","kind":"timer","defaultIntervalMs":60000}]}`)
	if err := os.WriteFile(filepath.Join(dir, "flow.json"), flowJSON, 0644); err != nil {
		t.Fatalf("write flow.json: %v", err)
	}

	_, err := LoadFlowService(dir, nil, FlowMountDeps{
		CapRegistry: modulert.NewCapabilityRegistry(),
		NodeCtx: &modulert.NodeContext{
			ModuleSignaturePolicy: &modulert.ModuleSignaturePolicy{TrustedSigners: []ed25519.PublicKey{pub}},
		},
	})
	if err == nil {
		t.Fatal("LoadFlowService() with unsigned artifact under a configured policy = nil error, want rejection")
	}
	if !strings.Contains(err.Error(), "publication signature") {
		t.Fatalf("expected rejection to name the publication signature gate, got: %v", err)
	}
}

func TestLoadFlowServiceNilPolicyDoesNotEnforceSignature(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "runtime.wasm"), []byte("unsigned garbage, still not real wasm"), 0644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	flowJSON := []byte(`{"programId":"test.flow.service.nil-policy","triggers":[{"triggerId":"tick","kind":"timer","defaultIntervalMs":60000}]}`)
	if err := os.WriteFile(filepath.Join(dir, "flow.json"), flowJSON, 0644); err != nil {
		t.Fatalf("write flow.json: %v", err)
	}

	_, err := LoadFlowService(dir, nil, FlowMountDeps{
		CapRegistry: modulert.NewCapabilityRegistry(),
		NodeCtx:     &modulert.NodeContext{},
	})
	if err == nil {
		t.Fatal("LoadFlowService() with non-wasm bytes should still fail, got nil error")
	}
	if strings.Contains(err.Error(), "publication signature") {
		t.Fatalf("nil signature policy must not enforce the gate, got: %v", err)
	}
}

// --- Full end-to-end lifecycle against a REAL compiled+linked flow bundle -

// TestHTTPMountedFlowSignatureLifecycle exercises LoadMountedFlow's
// signature gate against the REAL data-retrieval flow bundle (same fixture
// http_mount_integration_test.go uses), proving: a correctly-signed
// artifact from a trusted signer mounts successfully; the SAME bytes
// unsigned under the same configured policy are rejected; and the SAME
// unsigned bytes load fine under a nil policy (inert default preserved).
// Skips gracefully when the space-data-network-modules checkout/build is
// not present, matching every other real-bundle test in this package.
func TestHTTPMountedFlowSignatureLifecycle(t *testing.T) {
	dist := dataRetrievalFlowDist(t)

	portable, err := readFlowArtifactForTest(dist)
	if err != nil {
		t.Fatalf("read flow artifact: %v", err)
	}
	flowJSON, err := os.ReadFile(filepath.Join(dist, "flow.json"))
	if err != nil {
		t.Fatalf("read flow.json: %v", err)
	}

	pub, priv := mustGenerateFlowSignerKey(t)
	sigPolicy := &modulert.ModuleSignaturePolicy{TrustedSigners: []ed25519.PublicKey{pub}}

	policy := approvedCapabilityPolicy(t, dist, "storage_query")
	store := newSeededMountStore(t,
		time.Date(2026, 5, 10, 0, 0, 0, 0, time.UTC).Unix(),
		time.Date(2026, 5, 12, 0, 0, 0, 0, time.UTC).Unix())
	reg := modulert.NewCapabilityRegistry()
	reg.RegisterBridgeAware("storage_query", caps.NewStorageCapFactory(store))

	// depsFor builds a fresh FlowMountDeps (with its OWN *NodeContext, never
	// shared/mutated across the three scenarios below — mutating a shared
	// NodeContext pointer's ModuleSignaturePolicy field would silently leak
	// the policy into the "nil policy" scenario too).
	depsFor := func(sigPolicy *modulert.ModuleSignaturePolicy) FlowMountDeps {
		return FlowMountDeps{
			CapRegistry: reg,
			NodeCtx: &modulert.NodeContext{
				CapabilityPolicy:      policy,
				ModuleSignaturePolicy: sigPolicy,
			},
			MaxMemoryPages: 2048,
			EngineLink:     store,
		}
	}

	// (a) Correctly-signed artifact from a trusted signer: mounts.
	signedDir := t.TempDir()
	signedArtifact := buildSignedFlowArtifact(t, portable, priv, "flow-signer-lifecycle")
	if err := os.WriteFile(filepath.Join(signedDir, "runtime.wasm"), signedArtifact, 0644); err != nil {
		t.Fatalf("write signed artifact: %v", err)
	}
	if err := os.WriteFile(filepath.Join(signedDir, "flow.json"), flowJSON, 0644); err != nil {
		t.Fatalf("write flow.json: %v", err)
	}
	mfSigned, err := LoadMountedFlow(signedDir, depsFor(sigPolicy))
	if err != nil {
		t.Fatalf("LoadMountedFlow(signed artifact, trusted policy) failed: %v", err)
	}
	defer mfSigned.Close()

	// (b) The SAME real bytes, unsigned, under the SAME configured policy: rejected.
	unsignedDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(unsignedDir, "runtime.wasm"), portable, 0644); err != nil {
		t.Fatalf("write unsigned artifact: %v", err)
	}
	if err := os.WriteFile(filepath.Join(unsignedDir, "flow.json"), flowJSON, 0644); err != nil {
		t.Fatalf("write flow.json: %v", err)
	}
	if _, err := LoadMountedFlow(unsignedDir, depsFor(sigPolicy)); err == nil {
		t.Fatal("LoadMountedFlow(unsigned artifact, configured policy) = nil error, want rejection")
	} else if !strings.Contains(err.Error(), "publication signature") {
		t.Fatalf("expected rejection to name the publication signature gate, got: %v", err)
	}

	// (c) The SAME unsigned bytes under a nil policy: loads (inert default).
	mfNil, err := LoadMountedFlow(unsignedDir, depsFor(nil))
	if err != nil {
		t.Fatalf("LoadMountedFlow(unsigned artifact, nil policy) failed: %v", err)
	}
	defer mfNil.Close()
}
