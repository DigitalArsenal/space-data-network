package modulert

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/ipfs/go-cid"
	mh "github.com/multiformats/go-multihash"

	"github.com/spacedatanetwork/sdn-server/internal/testsupport"
)

// ── Fake upstream fixtures (kept tiny; the module-side test at
// space-data-network-modules/data-source/spacex-starlink-source/test/module.test.mjs
// covers the full 3-entry / 12-state fixtures). A single MANIFEST entry keeps the
// per-object fetch loop (and the recorded host-cap sequence) crisp and
// deterministic while staying well under the default objectCap of 25. ────────────

const starlinkMemeFileName = "MEME_67850_STARLINK-36840_1340142_Operational_1463017380_UNCLASSIFIED.txt"

// starlinkManifestBody is the discover-endpoint MANIFEST.txt listing: one MEME
// filename per line (the module keeps only lines containing "MEME_").
const starlinkManifestBody = starlinkMemeFileName + "\n"

// starlinkMemeBody is a trimmed-but-real-shaped SpaceX MEME operator-ephemeris
// file: 4 header lines (created / start+stop+step / source / covariance-frame
// label) followed by two state rows. The module parses this into a canonical
// CCSDS OEM record and binds these exact bytes into signed provenance by SHA-256.
const starlinkMemeBody = "created:2026-05-14 02:02:54 UTC\n" +
	"ephemeris_start:2026-05-14 01:42:42 UTC ephemeris_stop:2026-05-17 01:42:42 UTC step_size:60\n" +
	"ephemeris_source:blend\n" +
	"UVW\n" +
	"2026134014242.000 -2877.5130811997 4075.0989745008 -4706.3410523462 -3.4551274033 -6.0345187465 -3.1147385352\n" +
	"2026134014342.000 -3080.1234567890 3800.9876543210 -4500.1112223330 -3.5000000000 -5.9000000000 -3.2000000000\n"

const (
	starlinkBaseURL     = "https://api.starlink.com/public-files/ephemerides/"
	starlinkManifestURL = "https://api.starlink.com/public-files/ephemerides/MANIFEST.txt"
	starlinkPublishTopic = "sdn/data-source/spacex-starlink"
)

// starlinkFakeSignature is the detached signature the wallet_sign (keyslot.sign)
// oracle returns. Distinctive bytes so the published PNM's SIGNATURE can be bound
// back to exactly what the fake host returned.
var starlinkFakeSignature = bytes.Repeat([]byte{0x2b}, 64)

type starlinkStorageWrite struct {
	schema       string
	record       []byte // decoded record bytes from the size-prefixed ingest stream
	cid          string // CIDv1 (raw, sha2-256) mirroring the host store's go-cid computation
	providerID   string
	sourceName   string
	batchID      string
	contentKeyID string
	reconcile    string
}

// mustMultihashSum mirrors the host store's multihash computation (sha2-256).
func mustMultihashSum(data []byte) mh.Multihash {
	m, err := mh.Sum(data, mh.SHA2_256, -1)
	if err != nil {
		panic(err)
	}
	return m
}

type starlinkPublish struct {
	topic string
	raw   []byte // the {"PNM":..,"provenance":..} message the module published
}

// TestStarlinkSourcePullDrivesHostCapSequence loads the built spacex-starlink-source
// data-source module under the real module runtime and asserts that invoking its
// TIMERS-driven `pull` method (what the node cron re-invokes) drives the full
// A2.2b data-source host-capability sequence and emits schema-exact records.
//
// The A2.2b rewrite (modules c7821d3; rebuilt byte-identical by A2.2c af42708)
// changed pull from a single fetch→store→sign→publish to:
//
//	http.request  (fetch the MANIFEST listing)
//	  then, per planned object (capped by objectCap, default 25):
//	http.request  (fetch that object's raw MEME ephemeris file)
//	storage.ingest_with_source (canonical CCSDS OEM record + SourceTags, schema "OEM")
//	keyslot.sign  (host-side signing oracle over the record's CID)
//	pubsub.publish(schema-exact PNM pointer + provenance sidecar)
//
// Capabilities are only granted because the embedded manifest declares them, so
// this also exercises the real-manifest embedding end-to-end. The assertions are
// structural (decoded record/PNM/provenance shape), not brittle byte-for-byte
// JSON equality: Go map marshaling is alphabetical while the C++ emission order
// is fixed, so equality would be over-specified.
func TestStarlinkSourcePullDrivesHostCapSequence(t *testing.T) {
	t.Parallel()

	wasmPath := testsupport.SkipIfNoStarlinkSourceWasm(t)
	// Make the resolved artifact loud: a silent skip (missing artifact) would hide
	// a real host-cap contract regression, and logging the path surfaces a stale
	// or wrong-checkout artifact when this runs from a worktree.
	t.Logf("resolved spacex-starlink-source WASM artifact: %s", wasmPath)
	wasmBytes, err := os.ReadFile(wasmPath)
	if err != nil {
		t.Fatalf("ReadFile(%q) failed: %v", wasmPath, err)
	}

	// Recording + capturing fake capabilities. Each returns the minimal valid cap
	// result the C++ module's run_pull parses so it proceeds to the next step, and
	// records the dispatched operation so we can assert ordering. Payloads for
	// storage.ingest_with_source and pubsub.publish are captured + decoded for the invariant
	// assertions below.
	var mu sync.Mutex
	var ops []string
	var storageWrites []starlinkStorageWrite
	var publishes []starlinkPublish
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
		return func(op string, payload []byte) ([]byte, error) {
			record(op)
			var req struct {
				Schema       string `json:"schema"`
				ProviderID   string `json:"provider_id"`
				SourceName   string `json:"source_name"`
				SourceURL    string `json:"source_url"`
				BatchID      string `json:"batch_id"`
				ContentKeyID string `json:"content_key_id"`
				Reconcile    string `json:"reconcile"`
				Records      string `json:"records"`
			}
			_ = json.Unmarshal(payload, &req)
			stream, _ := base64.StdEncoding.DecodeString(req.Records)
			// Single-record size-prefixed stream: [u32le len][bytes].
			var recordBytes []byte
			if len(stream) >= 4 {
				n := binary.LittleEndian.Uint32(stream[:4])
				if len(stream) >= 4+int(n) {
					recordBytes = stream[4 : 4+n]
				}
			}
			// The module computes the CID in-guest (CIDv1 raw sha2-256); the mock
			// mirrors the host store's go-cid computation so the PNM binding check
			// stays end-to-end.
			c := cid.NewCidV1(cid.Raw, mustMultihashSum(recordBytes)).String()
			mu.Lock()
			storageWrites = append(storageWrites, starlinkStorageWrite{
				schema:       req.Schema,
				record:       recordBytes,
				cid:          c,
				providerID:   req.ProviderID,
				sourceName:   req.SourceName,
				batchID:      req.BatchID,
				contentKeyID: req.ContentKeyID,
				reconcile:    req.Reconcile,
			})
			mu.Unlock()
			return okJSON(map[string]interface{}{"schema": req.Schema, "inserted": 1, "batch_id": req.BatchID}), nil
		}
	})
	reg.Register("wallet_sign", func(_ *Module) CapHandler {
		return func(op string, _ []byte) ([]byte, error) {
			record(op)
			// keyslot.sign is a host-side signing oracle that returns a detached
			// signature (never the raw key). run_pull records "signed":true and the
			// PNM SIGNATURE = base64(signature) only when it can parse this result.
			return okJSON(map[string]interface{}{
				"signature": base64.StdEncoding.EncodeToString(starlinkFakeSignature),
				"algorithm": "ed25519",
			}), nil
		}
	})
	// crypto_sign is registered defensively: the module signs host-side via
	// keyslot.sign and does not drive a separate crypto.sign step, but keeping the
	// handler avoids an unknown-capability error if a module variant signs in-guest.
	reg.Register("crypto_sign", func(_ *Module) CapHandler {
		return func(op string, _ []byte) ([]byte, error) {
			record(op)
			return okJSON(map[string]interface{}{
				"signature": base64.StdEncoding.EncodeToString(starlinkFakeSignature),
			}), nil
		}
	})
	reg.Register("pubsub", func(_ *Module) CapHandler {
		return func(op string, payload []byte) ([]byte, error) {
			record(op)
			var req struct {
				Topic string `json:"topic"`
				Data  string `json:"data"`
			}
			_ = json.Unmarshal(payload, &req)
			mu.Lock()
			publishes = append(publishes, starlinkPublish{topic: req.Topic, raw: []byte(req.Data)})
			mu.Unlock()
			return okJSON(map[string]interface{}{"published": true}), nil
		}
	})

	// The real starlink-source manifest declares sensitive capabilities (http,
	// pubsub, storage_ingest, wallet_sign) that require explicit operator approval
	// before NewModule will load it (default-deny). Pre-approve them so this test
	// keeps exercising the host-capability sequence; approval enforcement itself is
	// covered by capability_policy_test.go.
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

	// Invoke pull — exactly what the cron TIMERS scheduler re-invokes. run_pull
	// runs to completion (firing all host-caps) inside plugin_invoke_stream before
	// returning. The module still returns a raw JSON summary rather than an SDS PIV
	// frame (follow-on framing work), so we do not assert on the decoded payload
	// here — the recorded host-cap sequence + captured payloads below are the
	// assertions.
	if _, invokeErr := mod.InvokeMethod(context.Background(), "pull", nil); invokeErr != nil {
		t.Logf("pull invoke returned (module response is a raw JSON summary, not PIV yet): %v", invokeErr)
	}

	mu.Lock()
	got := append([]string(nil), ops...)
	gotStorage := append([]starlinkStorageWrite(nil), storageWrites...)
	gotPublishes := append([]starlinkPublish(nil), publishes...)
	mu.Unlock()

	// ── Invariant 1: A2.2b host-cap sequence ────────────────────────────────────
	// http (MANIFEST) → http (per-object MEME) → storage.ingest_with_source → keyslot.sign →
	// pubsub.publish. With a single-entry manifest the loop runs exactly once.
	want := []string{
		"http.request",   // MANIFEST listing
		"http.request",   // per-object MEME fetch
		"storage.ingest_with_source", // canonical OEM record + SourceTags
		"keyslot.sign",   // host-side signing oracle over the CID
		"pubsub.publish", // schema-exact PNM pointer + provenance
	}
	assertSubsequence(t, got, want)
	if n := countOp(got, "http.request"); n != 2 {
		t.Fatalf("expected exactly 2 http.request (MANIFEST + 1 object), got %d in %v", n, got)
	}
	if len(gotStorage) != 1 {
		t.Fatalf("expected exactly 1 storage.ingest_with_source, got %d", len(gotStorage))
	}
	if len(gotPublishes) != 1 {
		t.Fatalf("expected exactly 1 pubsub.publish, got %d", len(gotPublishes))
	}

	// ── Invariant 2: the ingest carries a schema-exact canonical OEM record with
	// honest SourceTags (reconcile "none" is load-bearing: indexed-duplicates
	// reconcile would delete distinct sibling objects on multi-object providers) ───
	sw := gotStorage[0]
	if sw.schema != "OEM" {
		t.Fatalf("expected ingest schema OEM (honest canonical type, not a raw-blob mislabel), got %q", sw.schema)
	}
	// Schema-exact CAPITALIZED SDS keys must be present verbatim (NORAD_CAT_ID, not
	// norad_cat_id) — Go's json decoder is case-insensitive, so assert on the raw
	// bytes, which is the load-bearing check for the JSON-capitalization rule.
	for _, key := range []string{
		"CCSDS_OEM_VERS", "CREATION_DATE", "ORIGINATOR", "CLASSIFICATION",
		"EPHEMERIS_DATA_BLOCK", "OBJECT_NAME", "OBJECT_ID", "NORAD_CAT_ID",
		"CENTER_NAME", "REFERENCE_FRAME", "TIME_SYSTEM", "START_TIME", "STOP_TIME",
		"STEP_SIZE", "STATE_VECTOR_SIZE", "EPHEMERIS_DATA",
	} {
		requireJSONKey(t, "OEM record", sw.record, key)
	}
	if bytes.Contains(sw.record, []byte("norad_cat_id")) {
		t.Fatalf("OEM record leaked a lowercase norad_cat_id key (schema capitalization rule): %s", sw.record)
	}
	if sw.providerID != "spacex-starlink" || sw.sourceName != "spacex-starlink" {
		t.Fatalf("expected SourceTags provider_id/source_name spacex-starlink, got %q/%q", sw.providerID, sw.sourceName)
	}
	if want := sha256Hex([]byte(starlinkMemeBody)); sw.batchID != want {
		t.Fatalf("expected batch_id = raw MEME sha256 %q, got %q", want, sw.batchID)
	}
	if sw.contentKeyID != "public" {
		t.Fatalf("expected content_key_id %q, got %q", "public", sw.contentKeyID)
	}
	if sw.reconcile != "none" {
		t.Fatalf("expected reconcile %q (indexed-duplicates would drop sibling objects), got %q", "none", sw.reconcile)
	}

	var oem struct {
		Version        float64 `json:"CCSDS_OEM_VERS"`
		CreationDate   string  `json:"CREATION_DATE"`
		Originator     string  `json:"ORIGINATOR"`
		Classification string  `json:"CLASSIFICATION"`
		Blocks         []struct {
			ObjectName      string    `json:"OBJECT_NAME"`
			ObjectID        string    `json:"OBJECT_ID"`
			NoradCatID      int       `json:"NORAD_CAT_ID"`
			CenterName      string    `json:"CENTER_NAME"`
			ReferenceFrame  string    `json:"REFERENCE_FRAME"`
			TimeSystem      string    `json:"TIME_SYSTEM"`
			StartTime       string    `json:"START_TIME"`
			StopTime        string    `json:"STOP_TIME"`
			StepSize        int       `json:"STEP_SIZE"`
			StateVectorSize int       `json:"STATE_VECTOR_SIZE"`
			EphemerisData   []float64 `json:"EPHEMERIS_DATA"`
		} `json:"EPHEMERIS_DATA_BLOCK"`
	}
	if err := json.Unmarshal(sw.record, &oem); err != nil {
		t.Fatalf("OEM record is not valid JSON: %v\n%s", err, sw.record)
	}
	if oem.Version != 2.0 {
		t.Fatalf("expected CCSDS_OEM_VERS 2.0, got %v", oem.Version)
	}
	if oem.Originator != "SpaceX" {
		t.Fatalf("expected ORIGINATOR SpaceX, got %q", oem.Originator)
	}
	if oem.Classification != "UNCLASSIFIED" {
		t.Fatalf("expected CLASSIFICATION UNCLASSIFIED, got %q", oem.Classification)
	}
	if len(oem.Blocks) != 1 {
		t.Fatalf("expected exactly 1 EPHEMERIS_DATA_BLOCK, got %d", len(oem.Blocks))
	}
	blk := oem.Blocks[0]
	if blk.NoradCatID != 67850 {
		t.Fatalf("expected NORAD_CAT_ID 67850, got %d", blk.NoradCatID)
	}
	if blk.ObjectName != "STARLINK-36840" {
		t.Fatalf("expected OBJECT_NAME STARLINK-36840, got %q", blk.ObjectName)
	}
	if blk.ObjectID != "" {
		t.Fatalf("expected OBJECT_ID unset (MEME COSPAR field is SpaceX-internal), got %q", blk.ObjectID)
	}
	if blk.CenterName != "EARTH" || blk.ReferenceFrame != "TEME" || blk.TimeSystem != "UTC" {
		t.Fatalf("expected CENTER_NAME/REFERENCE_FRAME/TIME_SYSTEM = EARTH/TEME/UTC, got %q/%q/%q",
			blk.CenterName, blk.ReferenceFrame, blk.TimeSystem)
	}
	if blk.StartTime != "2026-05-14T01:42:42Z" || blk.StopTime != "2026-05-17T01:42:42Z" {
		t.Fatalf("expected normalized START/STOP 2026-05-14T01:42:42Z/2026-05-17T01:42:42Z, got %q/%q",
			blk.StartTime, blk.StopTime)
	}
	if blk.StepSize != 60 || blk.StateVectorSize != 6 {
		t.Fatalf("expected STEP_SIZE 60 and STATE_VECTOR_SIZE 6, got %d/%d", blk.StepSize, blk.StateVectorSize)
	}
	if len(blk.EphemerisData) != 12 { // 2 state rows × 6 components
		t.Fatalf("expected 12 EPHEMERIS_DATA values (2 states × 6), got %d", len(blk.EphemerisData))
	}
	if math.Abs(blk.EphemerisData[0]-(-2877.5130811997)) > 1e-9 || math.Abs(blk.EphemerisData[5]-(-3.1147385352)) > 1e-9 {
		t.Fatalf("EPHEMERIS_DATA lost double precision: got [0]=%v [5]=%v", blk.EphemerisData[0], blk.EphemerisData[5])
	}

	// ── Invariant 3: schema-exact PNM + provenance sidecar on pubsub.publish ─────
	pub := gotPublishes[0]
	if pub.topic != starlinkPublishTopic {
		t.Fatalf("expected publish topic %q, got %q", starlinkPublishTopic, pub.topic)
	}
	var message struct {
		PNM        json.RawMessage `json:"PNM"`
		Provenance json.RawMessage `json:"provenance"`
	}
	if err := json.Unmarshal(pub.raw, &message); err != nil {
		t.Fatalf("published message is not valid JSON: %v\n%s", err, pub.raw)
	}

	for _, key := range []string{
		"MULTIFORMAT_ADDRESS", "PUBLISH_TIMESTAMP", "CID", "FILE_NAME",
		"FILE_ID", "SIGNATURE", "SIGNATURE_TYPE",
	} {
		requireJSONKey(t, "PNM", message.PNM, key)
	}
	var pnm struct {
		MultiformatAddress string `json:"MULTIFORMAT_ADDRESS"`
		PublishTimestamp   string `json:"PUBLISH_TIMESTAMP"`
		CID                string `json:"CID"`
		FileName           string `json:"FILE_NAME"`
		FileID             string `json:"FILE_ID"`
		Signature          string `json:"SIGNATURE"`
		SignatureType      string `json:"SIGNATURE_TYPE"`
	}
	if err := json.Unmarshal(message.PNM, &pnm); err != nil {
		t.Fatalf("PNM is not valid JSON: %v", err)
	}
	if pnm.CID != sw.cid {
		t.Fatalf("PNM.CID %q does not match the stored record CID %q", pnm.CID, sw.cid)
	}
	if pnm.MultiformatAddress != "/ipfs/"+sw.cid {
		t.Fatalf("expected MULTIFORMAT_ADDRESS /ipfs/%s, got %q", sw.cid, pnm.MultiformatAddress)
	}
	if pnm.FileName != starlinkMemeFileName {
		t.Fatalf("expected PNM.FILE_NAME %q, got %q", starlinkMemeFileName, pnm.FileName)
	}
	if pnm.FileID != "spacex-starlink:OEM:67850:2026-05-14T01:42:42Z" {
		t.Fatalf("expected FILE_ID spacex-starlink:OEM:67850:2026-05-14T01:42:42Z, got %q", pnm.FileID)
	}
	if pnm.PublishTimestamp != "2026-05-14T02:02:54Z" {
		t.Fatalf("expected PUBLISH_TIMESTAMP 2026-05-14T02:02:54Z (the MEME created stamp), got %q", pnm.PublishTimestamp)
	}
	if pnm.SignatureType != "ed25519" {
		t.Fatalf("expected SIGNATURE_TYPE ed25519, got %q", pnm.SignatureType)
	}
	// SIGNATURE must be exactly the base64 of what the keyslot.sign oracle returned
	// (the module decodes then re-encodes the detached signature into the PNM).
	if wantSig := base64.StdEncoding.EncodeToString(starlinkFakeSignature); pnm.Signature != wantSig {
		t.Fatalf("expected PNM.SIGNATURE to be the fake keyslot signature %q, got %q", wantSig, pnm.Signature)
	}

	// ── Invariant 4: provenance binds the raw MEME source by SHA-256 ─────────────
	var prov struct {
		SourceName   string `json:"SOURCE_NAME"`
		SourceURL    string `json:"SOURCE_URL"`
		SourceSHA256 string `json:"SOURCE_SHA256"`
		DataSource   string `json:"DATA_SOURCE"`
		RecordSchema string `json:"RECORD_SCHEMA"`
		NoradCatID   int    `json:"NORAD_CAT_ID"`
		ObjectName   string `json:"OBJECT_NAME"`
		ObjectStatus string `json:"OBJECT_STATUS"`
		StateCount   int    `json:"STATE_COUNT"`
	}
	if err := json.Unmarshal(message.Provenance, &prov); err != nil {
		t.Fatalf("provenance is not valid JSON: %v", err)
	}
	if wantSha := sha256Hex([]byte(starlinkMemeBody)); prov.SourceSHA256 != wantSha {
		t.Fatalf("provenance SOURCE_SHA256 must bind the raw MEME bytes: want %s, got %s", wantSha, prov.SourceSHA256)
	}
	if prov.SourceURL != starlinkBaseURL+starlinkMemeFileName {
		t.Fatalf("expected SOURCE_URL %q, got %q", starlinkBaseURL+starlinkMemeFileName, prov.SourceURL)
	}
	if prov.SourceName != "spacex-starlink" || prov.DataSource != "SpaceX-E" || prov.RecordSchema != "OEM" {
		t.Fatalf("expected provenance SOURCE_NAME/DATA_SOURCE/RECORD_SCHEMA = spacex-starlink/SpaceX-E/OEM, got %q/%q/%q",
			prov.SourceName, prov.DataSource, prov.RecordSchema)
	}
	if prov.NoradCatID != 67850 || prov.ObjectName != "STARLINK-36840" {
		t.Fatalf("expected provenance NORAD_CAT_ID 67850 / OBJECT_NAME STARLINK-36840, got %d/%q", prov.NoradCatID, prov.ObjectName)
	}
	if prov.StateCount != 2 {
		t.Fatalf("expected provenance STATE_COUNT 2 (2 parsed state rows), got %d", prov.StateCount)
	}
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

// assertSubsequence asserts that want appears as an in-order subsequence of got.
func assertSubsequence(t *testing.T, got, want []string) {
	t.Helper()
	i := 0
	for _, g := range got {
		if i < len(want) && g == want[i] {
			i++
		}
	}
	if i != len(want) {
		t.Fatalf("host-cap sequence mismatch:\n  want (in order): %v\n  got:             %v\n  matched %d/%d", want, got, i, len(want))
	}
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

// requireJSONKey asserts a schema-exact (case-sensitive) JSON key is present in
// the raw document. Used for the SDS-record capitalization invariant where Go's
// case-insensitive struct decoding would otherwise mask a lowercase-key emission.
func requireJSONKey(t *testing.T, what string, raw []byte, key string) {
	t.Helper()
	if !bytes.Contains(raw, []byte("\""+key+"\"")) {
		t.Fatalf("%s missing schema-exact key %q in: %s", what, key, raw)
	}
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
