package format4proof

// TestProofSmallFixture writes the smallest format-1 fixture the coverage
// classes run on (P4PROOF_SMALL_FIXTURE=<dir>): <dir>/f1, a format-1 store
// holding the lanes benchset fixtures.t6w names (OMM batch b052 of at least
// 1,100 records, an older OMM batch, MPE batch b051, CAT from
// celestrak-satcat and celestrak-satcat-csv, IQC from IQEngine), and
// <dir>/benchset.json with R01 (16 stored CIDs per point schema) and R02
// (CIDs of records nothing stored). Records are synthetic and deterministic.
// The format-5 fixture is `spacedatanetwork store-migrate --to 5` of a copy
// of <dir>/f1.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/CAT"
	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/IQC"
	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/MPE"
	flatbuffers "github.com/google/flatbuffers/go"
	"github.com/ipfs/go-cid"
	"github.com/multiformats/go-multihash"

	"github.com/spacedatanetwork/sdn-server/internal/sds"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
	"github.com/spacedatanetwork/sdn-server/internal/storage/format4/marker"
)

const smallFixtureBase = int64(1789300000) // epochs around r16At

func smallOMM(norad uint32, at int64, name string) []byte {
	t := time.Unix(at, 0).UTC()
	data := sds.NewOMMBuilder().
		WithNoradCatID(norad).
		WithObjectName(name).
		WithObjectID(fmt.Sprintf("2026-%03dA", norad%1000)).
		WithEpoch(t.Format("2006-01-02T15:04:05.000000")).
		WithEpochTimestamp(float64(at)).
		WithCreationDate(t.Format(time.RFC3339)).
		WithMeanMotion(15.5 + float64(norad%7)/100).
		Build()
	return append([]byte(nil), data[4:]...)
}

func smallMPE(entity string, at int64) []byte {
	b := flatbuffers.NewBuilder(128)
	id := b.CreateString(entity)
	MPE.MPEStart(b)
	MPE.MPEAddENTITY_ID(b, id)
	MPE.MPEAddEPOCH(b, float64(at))
	MPE.MPEAddMEAN_MOTION(b, 15.0)
	MPE.MPEAddECCENTRICITY(b, 0.001)
	root := MPE.MPEEnd(b)
	b.FinishWithFileIdentifier(root, []byte("$MPE"))
	return append([]byte(nil), b.FinishedBytes()...)
}

func smallCAT(norad uint32, name string) []byte {
	b := flatbuffers.NewBuilder(256)
	nm := b.CreateString(name)
	id := b.CreateString(fmt.Sprintf("1990-%05dA", norad))
	CAT.CATStart(b)
	CAT.CATAddOBJECT_NAME(b, nm)
	CAT.CATAddOBJECT_ID(b, id)
	CAT.CATAddNORAD_CAT_ID(b, norad)
	root := CAT.CATEnd(b)
	b.FinishWithFileIdentifier(root, []byte("$CAT"))
	return append([]byte(nil), b.FinishedBytes()...)
}

func smallIQC(seq int, stamp string) []byte {
	b := flatbuffers.NewBuilder(512)
	id := b.CreateString(fmt.Sprintf("iqengine:local/local/capture-%06d", seq))
	capture := b.CreateString(fmt.Sprintf("capture-%06d", seq))
	source := b.CreateString("IQEngine")
	sha := b.CreateString(fmt.Sprintf("%064x", uint64(seq)*0x9e3779b97f4a7c15))
	retrieved := b.CreateString(stamp)
	desc := b.CreateString(fmt.Sprintf("synthetic capture %d", seq))
	datatype := b.CreateString("cf32_le")
	created := b.CreateString(stamp)
	IQC.IQCStart(b)
	IQC.IQCAddID(b, id)
	IQC.IQCAddCAPTURE_ID(b, capture)
	IQC.IQCAddSOURCE_NAME(b, source)
	IQC.IQCAddSOURCE_SHA256(b, sha)
	IQC.IQCAddRETRIEVED_AT(b, retrieved)
	IQC.IQCAddDESCRIPTION(b, desc)
	IQC.IQCAddDATATYPE(b, datatype)
	IQC.IQCAddSAMPLE_RATE_HZ(b, 2.4e6)
	IQC.IQCAddCENTER_FREQ_HZ(b, 1.0e8+float64(seq)*1e3)
	IQC.IQCAddCREATED_AT(b, created)
	IQC.IQCAddUPDATED_AT(b, created)
	root := IQC.IQCEnd(b)
	b.FinishWithFileIdentifier(root, []byte(IQC.IQCIdentifier))
	return append([]byte(nil), b.FinishedBytes()...)
}

// smallCID is the raw CIDv1 (sha2-256) the node names a record by.
func smallCID(t *testing.T, data []byte) string {
	h, err := multihash.Sum(data, multihash.SHA2_256, -1)
	if err != nil {
		t.Fatal(err)
	}
	return cid.NewCidV1(cid.Raw, h).String()
}

func TestProofSmallFixture(t *testing.T) {
	dir := os.Getenv("P4PROOF_SMALL_FIXTURE")
	if dir == "" {
		t.Skip("P4PROOF_SMALL_FIXTURE not set")
	}
	f1 := filepath.Join(dir, "f1")
	if _, err := os.Stat(f1); err == nil {
		t.Fatalf("%s exists", f1)
	}
	s, _, err := OpenArm(ArmF1, f1)
	if err != nil {
		t.Fatal(err)
	}
	hits := map[string][]string{}
	put := func(schema string, data []byte, peer string, tags storage.SourceTags) {
		t.Helper()
		got, err := s.StoreWithSourceTags(schema, data, peer, nil, tags)
		if err != nil {
			t.Fatalf("store %s: %v", schema, err)
		}
		if want := smallCID(t, data); got != want {
			t.Fatalf("%s: the store named the record %s, the fixture computes %s", schema, got, want)
		}
		if len(hits[schema]) < 16 {
			hits[schema] = append(hits[schema], got)
		}
	}
	gp := func(batch string) storage.SourceTags {
		return storage.SourceTags{ProviderID: FixtureProvider, SourceName: "celestrak-gp", BatchID: batch}
	}
	// An older OMM batch (superseded objects), then b052: 1,200 objects
	// including 25544.
	for i := 0; i < 100; i++ {
		norad := uint32(25000 + i*12)
		put("OMM.fbs", smallOMM(norad, smallFixtureBase-86400+int64(i)*60, fmt.Sprintf("OBJECT %d", norad)), gpPeer, gp("OMM-celestrak-gp-b051"))
	}
	for i := 0; i < 1200; i++ {
		norad := uint32(25000 + i)
		put("OMM.fbs", smallOMM(norad, smallFixtureBase+int64(i)*60, fmt.Sprintf("OBJECT %d", norad)), gpPeer, gp(OMMLatestBatch))
	}
	for i := 0; i < 300; i++ {
		put("MPE.fbs", smallMPE(fmt.Sprintf("%d", 22400+i), smallFixtureBase+int64(i)*30), gpPeer, gp(MPELatestBatch))
	}
	for i := 0; i < 200; i++ {
		norad := uint32(40400 + i)
		put("CAT.fbs", smallCAT(norad, fmt.Sprintf("SATCAT %d", norad)), gpPeer,
			storage.SourceTags{ProviderID: FixtureProvider, SourceName: "celestrak-satcat"})
	}
	for i := 0; i < 50; i++ {
		norad := uint32(40440 + i)
		put("CAT.fbs", smallCAT(norad, fmt.Sprintf("CSV %d", norad)), gpPeer,
			storage.SourceTags{ProviderID: FixtureProvider, SourceName: "celestrak-satcat-csv"})
	}
	stamp := time.Unix(smallFixtureBase, 0).UTC().Format(time.RFC3339)
	for i := 0; i < 60; i++ {
		peer := iqcPeer
		if i%2 == 1 {
			peer = iqcSigmfPeer
		}
		put("IQC.fbs", smallIQC(i, stamp), peer, storage.SourceTags{ProviderID: "iqengine", SourceName: "IQEngine", BatchID: iqcBatch})
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	miss := map[string][]string{
		"OMM.fbs": {smallCID(t, smallOMM(1, smallFixtureBase, "NOT STORED"))},
		"MPE.fbs": {smallCID(t, smallMPE("not-stored", smallFixtureBase))},
		"CAT.fbs": {smallCID(t, smallCAT(1, "NOT STORED"))},
		"IQC.fbs": {smallCID(t, smallIQC(999999, stamp))},
	}
	r01, _ := json.Marshal(hits)
	r02, _ := json.Marshal(miss)
	bs := Benchset{Version: 1, Fixtures: map[string]BenchFixture{FixtureT6W: {UniqueRecords: 1910, RecordCopies: 1910}},
		Reads: []BenchOp{{ID: "R01", Name: "point hits", Fn: "GetRecord", Params: r01}, {ID: "R02", Name: "point misses", Fn: "GetRecord", Params: r02}}}
	b, err := json.MarshalIndent(bs, "", " ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "benchset.json"), b, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Logf("small fixture: %s (OMM 1,300, MPE 300, CAT 250, IQC 60), benchset %s", f1, filepath.Join(dir, "benchset.json"))
}

// TestProofSmallFixturePromote promotes a copy of the format-5 fixture
// (P4PROOF_PROMOTE=<store>) to layout 2: unlimited SQL visibility, so the
// coverage classes compare format 1 with a promoted store too (the s arm
// opens a layout-2 store with the setting true).
func TestProofSmallFixturePromote(t *testing.T) {
	dir := os.Getenv("P4PROOF_PROMOTE")
	if dir == "" {
		t.Skip("P4PROOF_PROMOTE not set")
	}
	s, _, err := OpenArm(ArmS, dir, storage.WithUnlimitedSQLVisibility(true))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	m, err := marker.Read(dir)
	if err != nil || m.Layout != 2 || m.RequiredLayout != 2 {
		t.Fatalf("promoted fixture: %+v %v", m, err)
	}
}
