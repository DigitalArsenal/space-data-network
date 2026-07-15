package sdnstore_test

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	blockstore "github.com/ipfs/boxo/blockstore"
	ds "github.com/ipfs/go-datastore"
	dssync "github.com/ipfs/go-datastore/sync"

	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/OMM"

	"github.com/ipfs/kubo/sdn/flatsqlrt"
	"github.com/ipfs/kubo/sdn/sdnstore"
	"github.com/ipfs/kubo/sdn/sds"
)

// ommSchema is the SDS OMM table shape (mirrors flatsqlrt's OMM test schema).
const ommSchema = `
  table OMM {
    CCSDS_OMM_VERS:double;
    CREATION_DATE:string;
    ORIGINATOR:string;
    OBJECT_NAME:string;
    OBJECT_ID:string;
    CENTER_NAME:string;
    REFERENCE_FRAME:RFM;
    REFERENCE_FRAME_EPOCH:string;
    TIME_SYSTEM:timingStandard = UTC;
    MEAN_ELEMENT_THEORY:meanElementSource = SGP4;
    COMMENT:string;
    EPOCH:string;
    SEMI_MAJOR_AXIS:double;
    MEAN_MOTION:double;
    ECCENTRICITY:double;
    INCLINATION:double;
    RA_OF_ASC_NODE:double;
    ARG_OF_PERICENTER:double;
    MEAN_ANOMALY:double;
    GM:double;
    MASS:double;
    SOLAR_RAD_AREA:double;
    SOLAR_RAD_COEFF:double;
    DRAG_AREA:double;
    DRAG_COEFF:double;
    EPHEMERIS_TYPE:ephemerisFormat = SGP4;
    CLASSIFICATION_TYPE:string;
    NORAD_CAT_ID:uint32;
    ELEMENT_SET_NO:uint32;
    REV_AT_EPOCH:double;
    BSTAR:double;
    MEAN_MOTION_DOT:double;
    MEAN_MOTION_DDOT:double;
    COV_REFERENCE_FRAME:RFM;
    COVARIANCE:[double];
    USER_DEFINED_BIP_0044_TYPE:uint;
    USER_DEFINED_OBJECT_DESIGNATOR:string;
    USER_DEFINED_EARTH_MODEL:string;
    USER_DEFINED_EPOCH_TIMESTAMP: double;
    USER_DEFINED_MICROSECONDS: double;
  }
  root_type OMM;
  file_identifier "$OMM";
`

// ommSchemas resolves the OMM 3-letter type for the store.
func ommSchemas() sdnstore.SchemaProvider {
	return sdnstore.SchemaProviderFunc(func(t string) (schema, fileID, tableName string, ok bool) {
		if t == "OMM" {
			return ommSchema, "$OMM", "OMM", true
		}
		return "", "", "", false
	})
}

// ommEpoch is an epoch-if-parsable extractor for OMM records (Unix seconds from
// USER_DEFINED_EPOCH_TIMESTAMP). It keeps the store SDS-neutral: it is supplied
// by the caller, not baked into sdnstore.
func ommEpoch(t string, fb []byte) (int64, bool) {
	if t != "OMM" {
		return 0, false
	}
	return int64(OMM.GetRootAsOMM(fb, 0).USER_DEFINED_EPOCH_TIMESTAMP()), true
}

// buildOMM produces one OMM record WITHOUT its 4-byte size prefix — the
// canonical single-FlatBuffer form the store ingests and content-addresses.
func buildOMM(t *testing.T, norad uint32, name, epoch string) []byte {
	t.Helper()
	ts, err := time.Parse("2006-01-02T15:04:05Z", epoch)
	if err != nil {
		t.Fatalf("parse epoch: %v", err)
	}
	sized := sds.NewOMMBuilder().
		WithNoradCatID(norad).
		WithObjectName(name).
		WithObjectID(fmt.Sprintf("2024-%03dA", norad%1000)).
		WithEpoch(epoch).
		WithEpochTimestamp(float64(ts.Unix())).
		WithMeanMotion(15.5).
		WithEccentricity(0.0001).
		WithInclination(53.0).
		Build()
	return sized[4:] // strip size prefix -> single FlatBuffer
}

// sharedAOTDir reuses the flatsqlrt suite's AOT cache so the engine loads at
// native speed instead of recompiling per runtime.
func sharedAOTDir(t *testing.T) string {
	t.Helper()
	base, err := os.UserCacheDir()
	if err != nil {
		return t.TempDir()
	}
	return base + "/sdn-flatsqlrt-test-aot"
}

func noradSet(t *testing.T, records [][]byte) map[uint32]string {
	t.Helper()
	out := make(map[uint32]string, len(records))
	for _, r := range records {
		o := OMM.GetRootAsOMM(r, 0)
		out[o.NORAD_CAT_ID()] = string(o.OBJECT_NAME())
	}
	return out
}

// TestStoreAndReadBySourceType_DurableAcrossReopen proves the storage model:
// real OMM FlatBuffers keyed by (source, 3-letter type) survive a full store
// close + reopen with a FRESH FlatSQL engine over the SAME durable blockstore +
// datastore, recovered with NO journal, NO boot replay, NO hydration code — and
// two sources of the same type stay isolated.
func TestStoreAndReadBySourceType_DurableAcrossReopen(t *testing.T) {
	ctx := context.Background()

	// One in-memory datastore backs BOTH the content-addressed blockstore and
	// the sdnstore index; both are the durable truth that survives the reopen.
	mds := dssync.MutexWrap(ds.NewMapDatastore())
	bs := blockstore.NewBlockstore(mds)

	cfg := sdnstore.Config{
		Blockstore:     bs,
		Datastore:      mds,
		Schemas:        ommSchemas(),
		EpochOf:        ommEpoch,
		RuntimeOptions: []flatsqlrt.Option{flatsqlrt.WithAOTCache(sharedAOTDir(t))},
	}

	// Distinct records per source (distinct bytes => distinct CIDs).
	srcA := "celestrak-gp"
	srcB := "provider-two"
	recsA := [][]byte{
		buildOMM(t, 1001, "SAT-A1", "2026-05-10T00:00:00Z"),
		buildOMM(t, 1002, "SAT-A2", "2026-05-11T00:00:00Z"),
		buildOMM(t, 1003, "SAT-A3", "2026-05-12T00:00:00Z"),
	}
	recsB := [][]byte{
		buildOMM(t, 2001, "SAT-B1", "2026-05-10T00:00:00Z"),
		buildOMM(t, 2002, "SAT-B2", "2026-05-11T00:00:00Z"),
	}

	// --- session 1: store, then close (releases the engine) ---
	s1, err := sdnstore.Open(cfg)
	if err != nil {
		t.Fatalf("Open (session 1): %v", err)
	}
	for _, r := range recsA {
		if _, err := s1.Store(ctx, srcA, "OMM", r); err != nil {
			t.Fatalf("Store srcA: %v", err)
		}
	}
	for _, r := range recsB {
		if _, err := s1.Store(ctx, srcB, "OMM", r); err != nil {
			t.Fatalf("Store srcB: %v", err)
		}
	}
	// Byte-identical re-store is idempotent (content-addressed dedup).
	if _, err := s1.Store(ctx, srcA, "OMM", recsA[0]); err != nil {
		t.Fatalf("Store srcA duplicate: %v", err)
	}
	s1.Close()

	// --- session 2: FRESH engine, SAME durable blockstore + datastore ---
	s2, err := sdnstore.Open(cfg)
	if err != nil {
		t.Fatalf("Open (session 2, reopen): %v", err)
	}
	defer s2.Close()

	// Read back srcA purely from the durable index + blockstore (no engine).
	gotA, err := s2.ReadBySourceType(ctx, srcA, "OMM")
	if err != nil {
		t.Fatalf("ReadBySourceType srcA: %v", err)
	}
	if len(gotA) != len(recsA) {
		t.Fatalf("srcA read back %d records, want %d (dedup should have collapsed the duplicate)", len(gotA), len(recsA))
	}
	gotB, err := s2.ReadBySourceType(ctx, srcB, "OMM")
	if err != nil {
		t.Fatalf("ReadBySourceType srcB: %v", err)
	}
	if len(gotB) != len(recsB) {
		t.Fatalf("srcB read back %d records, want %d", len(gotB), len(recsB))
	}

	// Content identity: recovered bytes equal what was stored (store order).
	for i := range recsA {
		if string(gotA[i]) != string(recsA[i]) {
			t.Fatalf("srcA record %d bytes differ after reopen", i)
		}
	}

	// Source isolation: srcA's NORAD set is exactly srcA's, disjoint from srcB.
	setA := noradSet(t, gotA)
	setB := noradSet(t, gotB)
	for _, norad := range []uint32{1001, 1002, 1003} {
		if _, ok := setA[norad]; !ok {
			t.Fatalf("srcA missing NORAD %d after reopen", norad)
		}
		if _, leaked := setB[norad]; leaked {
			t.Fatalf("srcA NORAD %d leaked into srcB (isolation broken)", norad)
		}
	}
	for _, norad := range []uint32{2001, 2002} {
		if _, ok := setB[norad]; !ok {
			t.Fatalf("srcB missing NORAD %d after reopen", norad)
		}
		if _, leaked := setA[norad]; leaked {
			t.Fatalf("srcB NORAD %d leaked into srcA (isolation broken)", norad)
		}
	}

	// SQL path: the fresh engine lazily repopulates the bounded hot window from
	// the durable stores and answers a query — records come back as raw
	// FlatBuffers via the per-source shadow table.
	shadowA, err := s2.ShadowTable(srcA, "OMM")
	if err != nil {
		t.Fatalf("ShadowTable: %v", err)
	}
	stream, err := s2.Query(ctx, srcA, "OMM", fmt.Sprintf(`SELECT _data FROM "%s"`, shadowA))
	if err != nil {
		t.Fatalf("Query srcA via engine after reopen: %v", err)
	}
	frames, err := flatsqlrt.DecodeSizePrefixedStream(stream.Bytes)
	if err != nil {
		t.Fatalf("decode raw stream: %v", err)
	}
	if len(frames) != len(recsA) {
		t.Fatalf("engine returned %d records for srcA, want %d", len(frames), len(recsA))
	}
	engineSet := noradSet(t, frames)
	for _, norad := range []uint32{1001, 1002, 1003} {
		if _, ok := engineSet[norad]; !ok {
			t.Fatalf("engine query missing NORAD %d for srcA", norad)
		}
	}

	// The engine's srcA shadow table must NOT see srcB records.
	shadowB, err := s2.ShadowTable(srcB, "OMM")
	if err != nil {
		t.Fatalf("ShadowTable srcB: %v", err)
	}
	streamB, err := s2.Query(ctx, srcB, "OMM", fmt.Sprintf(`SELECT _data FROM "%s"`, shadowB))
	if err != nil {
		t.Fatalf("Query srcB via engine: %v", err)
	}
	framesB, err := flatsqlrt.DecodeSizePrefixedStream(streamB.Bytes)
	if err != nil {
		t.Fatalf("decode srcB stream: %v", err)
	}
	if len(framesB) != len(recsB) {
		t.Fatalf("engine returned %d records for srcB, want %d", len(framesB), len(recsB))
	}
}
