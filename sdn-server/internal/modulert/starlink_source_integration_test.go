package modulert

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"strings"
	"sync"
	"testing"

	OEM "github.com/DigitalArsenal/spacedatastandards.org/lib/go/OEM"

	"github.com/spacedatanetwork/sdn-server/internal/testsupport"
	"github.com/spacedatanetwork/sdn-server/internal/wasmrt"
)

// ── Fake upstream fixtures (kept tiny; the module-side test at
// space-data-network-modules/data-source/spacex-starlink-source/test/module.test.mjs
// covers the full 3-entry / 12-state fixtures). A single MANIFEST entry keeps the
// per-object fetch loop crisp and deterministic while staying well under the
// default objectCap of 25. ────────────────────────────────────────────────────

const starlinkMemeFileName = "MEME_67850_STARLINK-36840_1340142_Operational_1463017380_UNCLASSIFIED.txt"

// starlinkManifestBody is the discover-endpoint MANIFEST.txt listing: one MEME
// filename per line (the module keeps only lines containing "MEME_").
const starlinkManifestBody = starlinkMemeFileName + "\n"

// starlinkMemeBody is a trimmed-but-real-shaped SpaceX MEME operator-ephemeris
// file: 4 header lines (created / start+stop+step / source / covariance-frame
// label) followed by two state rows. The module parses this into a canonical
// $OEM ephemeris data block.
const starlinkMemeBody = "created:2026-05-14 02:02:54 UTC\n" +
	"ephemeris_start:2026-05-14 01:42:42 UTC ephemeris_stop:2026-05-17 01:42:42 UTC step_size:60\n" +
	"ephemeris_source:blend\n" +
	"UVW\n" +
	"2026134014242.000 -2877.5130811997 4075.0989745008 -4706.3410523462 -3.4551274033 -6.0345187465 -3.1147385352\n" +
	"2026134014342.000 -3080.1234567890 3800.9876543210 -4500.1112223330 -3.5000000000 -5.9000000000 -3.2000000000\n"

// TestStarlinkSourcePullReturnsRawOEM loads the built spacex-starlink-source
// data-source module under the real module runtime and asserts that invoking
// its TIMERS-driven `pull` method (what the node cron re-invokes) drives ONLY
// the http fetch chain and returns a well-formed raw $OEM FlatBuffer for
// in-wasm consumption.
//
// This supersedes the A2.2b/A2.2c "host-cap sequence" contract this test used
// to assert (http → http → storage.ingest_with_source → keyslot.sign →
// pubsub.publish): that was the Go-host orchestration the OD-run
// Go-orchestration purge repudiated (see SDN_OD_FLOW_LOOP.md's STOP block and
// coordination/tasks/kubo-fork-rewrite-loop.md) — a provider module streaming
// $OEM through the Go host's storage/sign/publish capabilities so a Go-side
// fitter could consume it. The SANCTIONED contract has pull return the $OEM
// record directly to its caller (the in-wasm OD flow) with NO Go-storage,
// Go-signing, or Go-publish step in between. The manifest still DECLARES the
// storage_ingest/wallet_sign/crypto_sign/pubsub capabilities (kept registered
// + approved below so NewModule's fail-closed gate still admits the real
// artifact), but pull must never actually drive them — Invariant 1 below is a
// regression guard against the old sequence reappearing.
//
// mod.InvokeMethod decodes every response through the $PIV envelope
// (decodePluginInvokeResponseBytes), which pull's raw $OEM response is not
// framed as (by design — see above), so this test drives the same low-level
// plugin_invoke_stream call InvokeMethodFrames makes but stops short of that
// PIV decode to recover the raw bytes for direct $OEM validation.
func TestStarlinkSourcePullReturnsRawOEM(t *testing.T) {
	t.Parallel()

	wasmPath := testsupport.SkipIfNoStarlinkSourceWasm(t)
	// Make the resolved artifact loud: a silent skip (missing artifact) would hide
	// a real contract regression, and logging the path surfaces a stale or
	// wrong-checkout artifact when this runs from a worktree.
	t.Logf("resolved spacex-starlink-source WASM artifact: %s", wasmPath)
	wasmBytes, err := os.ReadFile(wasmPath)
	if err != nil {
		t.Fatalf("ReadFile(%q) failed: %v", wasmPath, err)
	}

	// Recording fakes. The manifest still declares storage_ingest/wallet_sign/
	// crypto_sign/pubsub as sensitive capabilities (approved below), but under
	// the current contract pull must never invoke them — these handlers exist
	// only so NewModule succeeds and so a call would be recorded (and therefore
	// caught by Invariant 1) if the old sequence ever regressed back in.
	var mu sync.Mutex
	var ops []string
	record := func(op string) {
		mu.Lock()
		ops = append(ops, op)
		mu.Unlock()
	}

	reg := NewCapabilityRegistry()
	reg.Register("http", func(_ *Module) CapHandler {
		return func(op string, payload []byte) ([]byte, error) {
			record(op)
			var req struct {
				URL string `json:"url"`
			}
			_ = json.Unmarshal(payload, &req)
			status := 200
			body := ""
			switch {
			case strings.HasSuffix(req.URL, "MANIFEST.txt"):
				body = starlinkManifestBody
			case strings.HasSuffix(req.URL, starlinkMemeFileName):
				body = starlinkMemeBody
			default:
				status = 404 // unknown object → module skips it
			}
			return okJSON(map[string]interface{}{
				"status":        status,
				"headers":       map[string]string{},
				"body":          body,
				"body_encoding": "utf8",
			}), nil
		}
	})
	reg.Register("storage_ingest", func(_ *Module) CapHandler {
		return func(op string, _ []byte) ([]byte, error) {
			record(op)
			return okJSON(map[string]interface{}{"inserted": 1}), nil
		}
	})
	reg.Register("wallet_sign", func(_ *Module) CapHandler {
		return func(op string, _ []byte) ([]byte, error) {
			record(op)
			return okJSON(map[string]interface{}{"signature": "", "algorithm": "ed25519"}), nil
		}
	})
	reg.Register("crypto_sign", func(_ *Module) CapHandler {
		return func(op string, _ []byte) ([]byte, error) {
			record(op)
			return okJSON(map[string]interface{}{"signature": ""}), nil
		}
	})
	reg.Register("pubsub", func(_ *Module) CapHandler {
		return func(op string, _ []byte) ([]byte, error) {
			record(op)
			return okJSON(map[string]interface{}{"published": true}), nil
		}
	})

	// The real starlink-source manifest declares sensitive capabilities (http,
	// pubsub, storage_ingest, wallet_sign) that require explicit operator approval
	// before NewModule will load it (default-deny). Pre-approve them so the module
	// loads; approval enforcement itself is covered by capability_policy_test.go.
	policy, err := NewCapabilityPolicyStore("")
	if err != nil {
		t.Fatalf("NewCapabilityPolicyStore failed: %v", err)
	}
	moduleHash := ContentHashHex(wasmBytes)
	for _, capability := range []string{"http", "pubsub", "storage_ingest", "wallet_sign"} {
		if _, err := policy.Approve(CapabilityApproval{
			ModuleHash: moduleHash,
			Capability: capability,
			PluginID:   "com.orbpro.spacex-starlink-source",
			ApprovedBy: "test",
		}); err != nil {
			t.Fatalf("Approve(%s) failed: %v", capability, err)
		}
	}

	// NewModule reads + parses the embedded $PLG manifest; a parse failure here is
	// a real SDK-encoder-vs-Go-PLG-bindings wire-layout regression.
	mod, err := func() (m *Module, err error) {
		defer func() {
			if r := recover(); r != nil {
				err = fmt.Errorf("panic parsing manifest: %v", r)
			}
		}()
		return NewModule(wasmBytes, reg, &NodeContext{CapabilityPolicy: policy})
	}()
	if err != nil {
		t.Fatalf("NewModule failed to parse the module's $PLG manifest "+
			"(SDK encoder vs Go PLG bindings wire-layout mismatch?): %v", err)
	}
	defer func() {
		if closeErr := mod.Close(); closeErr != nil {
			t.Fatalf("Close() failed: %v", closeErr)
		}
	}()

	// The embedded manifest must expose the data-source contract that grants caps
	// and schedules the pull timer.
	manifest := mod.Manifest()
	if manifest == nil {
		t.Fatal("expected manifest to be available")
	}
	if manifest.PluginID != "com.orbpro.spacex-starlink-source" {
		t.Fatalf("expected plugin id com.orbpro.spacex-starlink-source, got %q", manifest.PluginID)
	}
	if !hasMethod(manifest, "pull") {
		t.Fatalf("expected pull method, got %+v", manifest.Methods)
	}
	for _, cap := range []string{"http", "storage_ingest", "wallet_sign", "crypto_sign", "pubsub"} {
		if !hasCapability(manifest, cap) {
			t.Fatalf("expected capability %q, got %v", cap, manifest.Capabilities)
		}
	}
	if !hasTimerForMethod(manifest, "starlink-pull", "pull") {
		t.Fatalf("expected starlink-pull timer invoking pull, got %+v", manifest.Timers)
	}

	// Invoke pull — exactly what the cron TIMERS scheduler re-invokes. Bypasses
	// mod.InvokeMethod's $PIV envelope decode (see the doc comment above) to
	// recover the raw plugin_invoke_stream response bytes directly.
	raw := invokeRawBytes(t, mod, "pull", nil)

	mu.Lock()
	got := append([]string(nil), ops...)
	mu.Unlock()

	// ── Invariant 1: ONLY the http fetch chain runs; NO Go-storage/sign/publish
	// step. http (MANIFEST) → http (per-object MEME). With a single-entry
	// manifest the loop runs exactly once. This is a regression guard: it fails
	// loudly if the purged Go-orchestration host-cap sequence ever reappears.
	if n := countOp(got, "http.request"); n != 2 {
		t.Fatalf("expected exactly 2 http.request (MANIFEST + 1 object), got %d in %v", n, got)
	}
	for _, forbidden := range []string{"storage.ingest_with_source", "keyslot.sign", "pubsub.publish"} {
		if n := countOp(got, forbidden); n != 0 {
			t.Fatalf("expected pull to NEVER drive %q (that is the purged Go-orchestration "+
				"host-cap sequence — pull must return $OEM directly for in-wasm consumption), "+
				"got %d invocation(s) in %v", forbidden, n, got)
		}
	}
	if len(got) != 2 {
		t.Fatalf("expected exactly the 2 http.request ops and nothing else, got %v", got)
	}

	// ── Invariant 2: pull's raw response is a well-formed $OEM FlatBuffer ───────
	// The current wire format is an 8-byte length header ([u32 frame
	// count/format tag = 1][u32 payload length]) followed by the embedded
	// (non-size-prefixed) $OEM buffer — lighter than the $PIV envelope pull used
	// to return before the purge, since there is no longer a Go-side decoder
	// that needs port/schema metadata.
	if len(raw) <= 8 {
		t.Fatalf("expected pull response longer than the 8-byte header, got %d bytes", len(raw))
	}
	payloadLen := decodeUint32LEOrFatal(t, raw[4:8])
	if int(payloadLen) != len(raw)-8 {
		t.Fatalf("expected the 8-byte header's payload-length word to equal len(raw)-8=%d, got %d",
			len(raw)-8, payloadLen)
	}
	oemBytes := raw[8:]
	if !OEM.OEMBufferHasIdentifier(oemBytes) {
		t.Fatalf("expected pull's response payload to carry the $OEM buffer identifier, "+
			"got (first 16 bytes) % x", oemBytes[:min(16, len(oemBytes))])
	}

	root := OEM.GetRootAsOEM(oemBytes, 0)
	if n := root.EPHEMERIS_DATA_BLOCKLength(); n != 1 {
		t.Fatalf("expected exactly 1 EPHEMERIS_DATA_BLOCK, got %d", n)
	}
	// blk is obtained via the exported GetRootAsephemerisDataBlock constructor
	// purely so its (unexported, package-private in the generated SDS bindings)
	// concrete type can be named by inference from outside package OEM; the
	// EPHEMERIS_DATA_BLOCK call immediately below re-initializes it to the real
	// nested table, so the throwaway construction argument is never read.
	blk := OEM.GetRootAsephemerisDataBlock(oemBytes, 0)
	if !root.EPHEMERIS_DATA_BLOCK(blk, 0) {
		t.Fatalf("expected to decode EPHEMERIS_DATA_BLOCK[0]")
	}

	cat := new(OEM.CAT)
	obj := blk.OBJECT(cat)
	if obj == nil {
		t.Fatal("expected the ephemeris data block to carry an OBJECT (CAT) identity")
	}
	if obj.NORAD_CAT_ID() != 67850 {
		t.Fatalf("expected NORAD_CAT_ID 67850, got %d", obj.NORAD_CAT_ID())
	}
	if string(obj.OBJECT_NAME()) != "STARLINK-36840" {
		t.Fatalf("expected OBJECT_NAME STARLINK-36840, got %q", string(obj.OBJECT_NAME()))
	}
	if string(blk.CENTER_NAME()) != "EARTH" {
		t.Fatalf("expected CENTER_NAME EARTH, got %q", string(blk.CENTER_NAME()))
	}
	if blk.TIME_SYSTEM().String() != "UTC" {
		t.Fatalf("expected TIME_SYSTEM UTC, got %v", blk.TIME_SYSTEM())
	}
	if string(blk.START_TIME()) != "2026-05-14T01:42:42Z" || string(blk.STOP_TIME()) != "2026-05-17T01:42:42Z" {
		t.Fatalf("expected normalized START/STOP 2026-05-14T01:42:42Z/2026-05-17T01:42:42Z, got %q/%q",
			string(blk.START_TIME()), string(blk.STOP_TIME()))
	}
	if blk.STEP_SIZE() != 60 || blk.STATE_VECTOR_SIZE() != 6 {
		t.Fatalf("expected STEP_SIZE 60 and STATE_VECTOR_SIZE 6, got %v/%v", blk.STEP_SIZE(), blk.STATE_VECTOR_SIZE())
	}
	if n := blk.EPHEMERIS_DATALength(); n != 12 { // 2 state rows × 6 components
		t.Fatalf("expected 12 EPHEMERIS_DATA values (2 states × 6), got %d", n)
	}
	if math.Abs(blk.EPHEMERIS_DATA(0)-(-2877.5130811997)) > 1e-9 || math.Abs(blk.EPHEMERIS_DATA(5)-(-3.1147385352)) > 1e-9 {
		t.Fatalf("EPHEMERIS_DATA lost double precision: got [0]=%v [5]=%v", blk.EPHEMERIS_DATA(0), blk.EPHEMERIS_DATA(5))
	}
}

// invokeRawBytes drives the same low-level plugin_invoke_stream call
// Module.InvokeMethodFrames makes, but returns the raw response bytes instead
// of running them through decodePluginInvokeResponseBytes (which requires a
// $PIV envelope pull no longer returns — see the test doc comment above).
// Test-only: production code (module.go) is untouched.
func invokeRawBytes(t *testing.T, m *Module, methodID string, payload []byte) []byte {
	t.Helper()

	req, err := encodePluginInvokeRequestFrames(methodID, []InvokeInputFrame{
		{PortID: "request", Payload: payload},
	})
	if err != nil {
		t.Fatalf("encode invoke request: %v", err)
	}

	reqPtr, err := m.mod.Allocate(req)
	if err != nil {
		t.Fatalf("allocate request: %v", err)
	}
	defer m.mod.SecureDeallocate(reqPtr, uint32(len(req)))

	responseLenPtr, err := m.mod.AllocateSize(4)
	if err != nil {
		t.Fatalf("allocate response length: %v", err)
	}
	defer m.mod.SecureDeallocate(responseLenPtr, 4)
	if err := m.mod.WriteMemory(responseLenPtr, []byte{0, 0, 0, 0}); err != nil {
		t.Fatalf("zero response length: %v", err)
	}

	results, err := m.mod.ExecuteContext(context.Background(), "plugin_invoke_stream",
		int32(reqPtr), int32(len(req)), int32(responseLenPtr),
	)
	if err != nil {
		t.Fatalf("plugin_invoke_stream(%s): %v", methodID, err)
	}

	responsePtr := uint32(wasmrt.ToInt32(results[0]))
	responseLenBytes, err := m.mod.ReadMemory(responseLenPtr, 4)
	if err != nil {
		t.Fatalf("read response length: %v", err)
	}
	responseLen, err := decodeUint32LE(responseLenBytes)
	if err != nil {
		t.Fatalf("decode response length: %v", err)
	}
	if responseLen == 0 || responsePtr == 0 {
		t.Fatalf("plugin_invoke_stream(%s) returned an empty/null response", methodID)
	}
	defer m.mod.SecureDeallocate(responsePtr, responseLen)

	responseBytes, err := m.mod.ReadMemory(responsePtr, responseLen)
	if err != nil {
		t.Fatalf("read invoke response: %v", err)
	}
	return append([]byte(nil), responseBytes...)
}

func decodeUint32LEOrFatal(t *testing.T, b []byte) uint32 {
	t.Helper()
	v, err := decodeUint32LE(b)
	if err != nil {
		t.Fatalf("decode uint32le: %v", err)
	}
	return v
}

func hasMethod(m *Manifest, methodID string) bool {
	for _, method := range m.Methods {
		if method.MethodID == methodID {
			return true
		}
	}
	return false
}

func hasCapability(m *Manifest, cap string) bool {
	for _, c := range m.Capabilities {
		if c == cap {
			return true
		}
	}
	return false
}

func hasTimerForMethod(m *Manifest, timerID, methodID string) bool {
	for _, timer := range m.Timers {
		if timer.TimerID == timerID && timer.MethodID == methodID {
			return true
		}
	}
	return false
}

func countOp(ops []string, op string) int {
	n := 0
	for _, o := range ops {
		if o == op {
			n++
		}
	}
	return n
}
