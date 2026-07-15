package appmanifest

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"testing"

	blockstore "github.com/ipfs/boxo/blockstore"
	ds "github.com/ipfs/go-datastore"
	dssync "github.com/ipfs/go-datastore/sync"
)

// moduleFixture reuses the vendored FlatSQL WASI module as "real module bytes"
// for the content-hash resolve test. It is a genuine .wasm artifact shipped in
// this tree, so the test exercises real bytes, not a synthetic blob.
const moduleFixture = "../flatsqlrt/flatsql-wasi-noeh.wasm"

// TestAppRecordWithDataflowRoundtrips constructs an $APP record carrying a
// page-targeted module ref (with a CONTENT_HASH + RUNTIME_TARGET=PAGE) and an
// APPDataflow contract entry, round-trips it through the $APP FlatBuffer
// (ToAPP -> bytes -> FromAPP), and asserts the dataflow entry and module ref
// survive with every field intact, then re-runs the manifest cross-ref Validate.
func TestAppRecordWithDataflowRoundtrips(t *testing.T) {
	// A realistic 64-hex CONTENT_HASH (sha-256 of some portable wasm bytes).
	sum := sha256.Sum256([]byte("portable-wasm-bytes"))
	contentHash := hex.EncodeToString(sum[:])

	app := &AppManifest{
		ID:          "spaceaware-conjunction",
		Name:        "Conjunction",
		Version:     "1.0.0",
		Description: "Conjunction screening app",
		Modules: []ModuleRef{{
			ID:            "screener",
			PluginID:      "org.sdn.conjunction-screener",
			ContentHash:   contentHash,
			Version:       "2.3.1",
			Role:          "primary",
			Description:   "conjunction screening module",
			RuntimeTarget: RuntimeTargetPage,
		}},
		Dataflow: []DataflowEntry{{
			Name:            "omm-in",
			Direction:       FlowDirectionToPage,
			SDSSchema:       "OMM",
			Transport:       FlowTransportPubsubTopic,
			Locator:         "/sdn/omm/live",
			ModuleID:        "screener",
			MethodID:        "ingest",
			PortId:          "omm",
			ContentEncoding: EncodingUTF8,
			Description:     "live OMM feed into the page",
		}},
	}

	if err := app.Validate(); err != nil {
		t.Fatalf("Validate() on constructed app: %v", err)
	}

	buf, err := app.ToAPP()
	if err != nil {
		t.Fatalf("ToAPP: %v", err)
	}
	if len(buf) == 0 {
		t.Fatal("ToAPP returned empty buffer")
	}

	got, err := FromAPP(buf)
	if err != nil {
		t.Fatalf("FromAPP: %v", err)
	}

	// Module ref survives with all fields, including CONTENT_HASH and the
	// RUNTIME_TARGET=PAGE enum.
	if n := len(got.Modules); n != 1 {
		t.Fatalf("modules: got %d, want 1", n)
	}
	gm, wm := got.Modules[0], app.Modules[0]
	if gm.ID != wm.ID {
		t.Errorf("module ID: got %q, want %q", gm.ID, wm.ID)
	}
	if gm.PluginID != wm.PluginID {
		t.Errorf("module PluginID: got %q, want %q", gm.PluginID, wm.PluginID)
	}
	if gm.ContentHash != wm.ContentHash {
		t.Errorf("module ContentHash: got %q, want %q", gm.ContentHash, wm.ContentHash)
	}
	if gm.Version != wm.Version {
		t.Errorf("module Version: got %q, want %q", gm.Version, wm.Version)
	}
	if gm.Role != wm.Role {
		t.Errorf("module Role: got %q, want %q", gm.Role, wm.Role)
	}
	if gm.Description != wm.Description {
		t.Errorf("module Description: got %q, want %q", gm.Description, wm.Description)
	}
	if gm.RuntimeTarget != RuntimeTargetPage {
		t.Errorf("module RuntimeTarget: got %q, want %q", gm.RuntimeTarget, RuntimeTargetPage)
	}

	// Dataflow entry survives with all fields, including the direction /
	// transport / content-encoding enums.
	if n := len(got.Dataflow); n != 1 {
		t.Fatalf("dataflow: got %d, want 1", n)
	}
	gf, wf := got.Dataflow[0], app.Dataflow[0]
	if gf.Name != wf.Name {
		t.Errorf("dataflow Name: got %q, want %q", gf.Name, wf.Name)
	}
	if gf.Direction != wf.Direction {
		t.Errorf("dataflow Direction: got %q, want %q", gf.Direction, wf.Direction)
	}
	if gf.SDSSchema != wf.SDSSchema {
		t.Errorf("dataflow SDSSchema: got %q, want %q", gf.SDSSchema, wf.SDSSchema)
	}
	if gf.Transport != wf.Transport {
		t.Errorf("dataflow Transport: got %q, want %q", gf.Transport, wf.Transport)
	}
	if gf.Locator != wf.Locator {
		t.Errorf("dataflow Locator: got %q, want %q", gf.Locator, wf.Locator)
	}
	if gf.ModuleID != wf.ModuleID {
		t.Errorf("dataflow ModuleID: got %q, want %q", gf.ModuleID, wf.ModuleID)
	}
	if gf.MethodID != wf.MethodID {
		t.Errorf("dataflow MethodID: got %q, want %q", gf.MethodID, wf.MethodID)
	}
	if gf.PortId != wf.PortId {
		t.Errorf("dataflow PortId: got %q, want %q", gf.PortId, wf.PortId)
	}
	if gf.ContentEncoding != wf.ContentEncoding {
		t.Errorf("dataflow ContentEncoding: got %q, want %q", gf.ContentEncoding, wf.ContentEncoding)
	}
	if gf.Description != wf.Description {
		t.Errorf("dataflow Description: got %q, want %q", gf.Description, wf.Description)
	}

	// The round-tripped record still passes the manifest cross-ref Validate:
	// the dataflow entry's ModuleID resolves into MODULES.
	if err := got.Validate(); err != nil {
		t.Fatalf("Validate() on round-tripped app: %v", err)
	}

	// A non-default transport (PUBSUB_TOPIC) actually persisted through the
	// buffer rather than defaulting — proves the enum is really carried.
	if got.Dataflow[0].Transport != FlowTransportPubsubTopic {
		t.Fatalf("transport did not survive as PUBSUB_TOPIC: got %q", got.Dataflow[0].Transport)
	}
}

// TestResolveModuleByContentHash stores real module bytes in a blockstore,
// computes their CONTENT_HASH (sha-256), builds an $APP referencing that hash,
// and resolves the bytes back by CONTENT_HASH — asserting byte-identity and a
// matching hash on the fetched block.
func TestResolveModuleByContentHash(t *testing.T) {
	ctx := context.Background()

	wasm, err := os.ReadFile(moduleFixture)
	if err != nil {
		t.Fatalf("read module fixture %s: %v", moduleFixture, err)
	}
	if len(wasm) == 0 {
		t.Fatalf("module fixture %s is empty", moduleFixture)
	}

	// In-memory blockstore, same construction the sdnstore tests use.
	mds := dssync.MutexWrap(ds.NewMapDatastore())
	bs := blockstore.NewBlockstore(mds)

	contentHash, storedCID, err := StoreModuleBytes(ctx, bs, wasm)
	if err != nil {
		t.Fatalf("StoreModuleBytes: %v", err)
	}

	// CONTENT_HASH must be the lowercase hex sha-256 of the module bytes.
	sum := sha256.Sum256(wasm)
	if want := hex.EncodeToString(sum[:]); contentHash != want {
		t.Fatalf("content hash: got %q, want %q", contentHash, want)
	}

	// ContentHashToCID (bytes-free, from the hash advertised in an $APP) must
	// address the very block StoreModuleBytes wrote.
	derivedCID, err := ContentHashToCID(contentHash)
	if err != nil {
		t.Fatalf("ContentHashToCID: %v", err)
	}
	if !derivedCID.Equals(storedCID) {
		t.Fatalf("CID mismatch: ContentHashToCID=%s, StoreModuleBytes=%s", derivedCID, storedCID)
	}

	// Build an $APP referencing the module by CONTENT_HASH, RUNTIME_TARGET=PAGE.
	app := &AppManifest{
		ID:      "flatsql-host",
		Name:    "FlatSQL Host",
		Version: "1.0.0",
		Modules: []ModuleRef{{
			ID:            "engine",
			PluginID:      "org.sdn.flatsql",
			ContentHash:   contentHash,
			RuntimeTarget: RuntimeTargetPage,
		}},
	}
	if err := app.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}

	// Resolve the bytes back by CONTENT_HASH — assert byte-identity.
	got, err := ResolveModuleByContentHash(ctx, bs, contentHash)
	if err != nil {
		t.Fatalf("ResolveModuleByContentHash: %v", err)
	}
	if !bytes.Equal(got, wasm) {
		t.Fatalf("resolved bytes differ from stored module: got %d bytes, want %d", len(got), len(wasm))
	}
	// Hash of the resolved bytes matches the requested CONTENT_HASH.
	gotSum := sha256.Sum256(got)
	if h := hex.EncodeToString(gotSum[:]); h != contentHash {
		t.Fatalf("resolved-bytes hash: got %q, want %q", h, contentHash)
	}

	// Node-side app serving: resolve every member module of the $APP by hash.
	resolved, unresolved, err := ResolveAppModules(ctx, bs, app)
	if err != nil {
		t.Fatalf("ResolveAppModules: %v", err)
	}
	if len(unresolved) != 0 {
		t.Fatalf("unresolved modules: %v", unresolved)
	}
	engineBytes, ok := resolved["engine"]
	if !ok {
		t.Fatal("ResolveAppModules did not resolve module \"engine\"")
	}
	if !bytes.Equal(engineBytes, wasm) {
		t.Fatal("ResolveAppModules bytes differ from stored module")
	}

	// A CONTENT_HASH not present in the blockstore is a hard miss, not silent
	// empty bytes.
	absent := sha256.Sum256([]byte("never-stored"))
	if _, err := ResolveModuleByContentHash(ctx, bs, hex.EncodeToString(absent[:])); err == nil {
		t.Fatal("expected error resolving an absent content hash, got nil")
	}
}
