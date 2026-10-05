package main

// $CQR (conjunction query and result, with SDS 1.231's uncertainty
// provenance) is a standard on store formats 1 and 4: a format-1 store holds
// and serves it, store-migrate --to 4 carries it (no unregistered table) and
// verifies, and format 4 serves the migrated records and stores new ones.

import (
	"bytes"
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/CQR"
	flatbuffers "github.com/google/flatbuffers/go"

	"github.com/spacedatanetwork/sdn-server/internal/flatsqlrt"
	"github.com/spacedatanetwork/sdn-server/internal/sds"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
	"github.com/spacedatanetwork/sdn-server/internal/storage/format4"
)

// cqrTestEvent builds one $CQR screening event with the pinned binding: a
// probability from a supplied covariance and each object's hard-body radius
// and covariance basis.
func cqrTestEvent(i int) []byte {
	b := flatbuffers.NewBuilder(512)
	primary := b.CreateString(fmt.Sprintf("2026-%03dA", 100+i))
	secondary := b.CreateString(fmt.Sprintf("1999-025%c", 'A'+i))
	calibration := b.CreateString("leo-600-800-coverage")
	CQR.TIMInstantStart(b)
	CQR.TIMInstantAddJULIAN_DATE(b, 2461318.5+float64(i)/24)
	tca := CQR.TIMInstantEnd(b)
	CQR.CQRProbabilityResultStart(b)
	CQR.CQRProbabilityResultAddPROBABILITY(b, 1.5e-5*float64(i+1))
	CQR.CQRProbabilityResultAddALGORITHM(b, 1) // FOSTER
	CQR.CQRProbabilityResultAddCONVERGED(b, true)
	CQR.CQRProbabilityResultAddUNCERTAINTY_SOURCE(b, 1) // SUPPLIED_COVARIANCE
	CQR.CQRProbabilityResultAddCALIBRATION_REFERENCE(b, calibration)
	CQR.CQRProbabilityResultAddCROSS_CORRELATION(b, 1) // INDEPENDENT
	probability := CQR.CQRProbabilityResultEnd(b)
	CQR.CQREventStart(b)
	CQR.CQREventAddPRIMARY_ID(b, primary)
	CQR.CQREventAddSECONDARY_ID(b, secondary)
	CQR.CQREventAddTCA(b, tca)
	CQR.CQREventAddMISS_DISTANCE_M(b, 180.5+float64(i))
	CQR.CQREventAddRELATIVE_SPEED_M_S(b, 14120)
	CQR.CQREventAddPROBABILITY(b, probability)
	CQR.CQREventAddPRIMARY_HARD_BODY_RADIUS_M(b, 6.5)
	CQR.CQREventAddHAS_PRIMARY_HARD_BODY_RADIUS_M(b, true)
	CQR.CQREventAddSECONDARY_HARD_BODY_RADIUS_M(b, 0.9)
	CQR.CQREventAddHAS_SECONDARY_HARD_BODY_RADIUS_M(b, true)
	CQR.CQREventAddPRIMARY_RADIUS_BASIS(b, 2)       // CATALOG_SIZE
	CQR.CQREventAddSECONDARY_RADIUS_BASIS(b, 3)     // RADAR_CROSS_SECTION
	CQR.CQREventAddPRIMARY_COVARIANCE_BASIS(b, 3)   // CONJUNCTION_MESSAGE
	CQR.CQREventAddSECONDARY_COVARIANCE_BASIS(b, 5) // EMPIRICAL_MODEL
	event := CQR.CQREventEnd(b)
	CQR.CQRStart(b)
	CQR.CQRAddEVENT_RESULT(b, event)
	CQR.FinishCQRBuffer(b, CQR.CQREnd(b))
	return append([]byte(nil), b.FinishedBytes()...)
}

// assertCQRServed reads every record back from s, byte for byte with its
// provenance, and counts them on the public SQL surface.
func assertCQRServed(t *testing.T, s *storage.FlatSQLStore, recs [][]byte) {
	t.Helper()
	for i, rec := range recs {
		got, err := s.GetRecord("CQR.fbs", storage.ComputeCID(rec))
		if err != nil || !bytes.Equal(got.Data, rec) {
			t.Fatalf("format4=%v: GetRecord of $CQR %d: %v", s.Format4(), i, err)
		}
		ev := CQR.GetRootAsCQR(got.Data, 0).EVENT_RESULT(nil)
		if ev == nil || uint8(ev.SECONDARY_COVARIANCE_BASIS()) != 5 || uint8(ev.SECONDARY_RADIUS_BASIS()) != 3 ||
			uint8(ev.PROBABILITY(nil).UNCERTAINTY_SOURCE()) != 1 {
			t.Fatalf("format4=%v: $CQR %d lost its uncertainty provenance", s.Format4(), i)
		}
	}
	payload, _, _, err := s.QuerySandboxedJSON(`SELECT COUNT(*) AS n FROM "CQR"`, flatsqlrt.SandboxCaps{Timeout: time.Minute})
	if want := fmt.Sprintf(`[{"n":%d}]`, len(recs)); err != nil || string(payload) != want {
		t.Fatalf("format4=%v: SELECT COUNT(*) FROM \"CQR\" = %s, %v; want %s", s.Format4(), payload, err, want)
	}
}

func TestStoreMigrateFormat4CarriesCQR(t *testing.T) {
	requireFormat4Engine(t)
	ctx := context.Background()
	v, err := sds.NewValidator(nil)
	if err != nil {
		t.Fatal(err)
	}
	var recs [][]byte
	for i := 0; i < 3; i++ {
		recs = append(recs, cqrTestEvent(i))
	}
	tags := storage.SourceTags{ProviderID: "space-data-network-02", SourceName: "conjunction-screening", BatchID: "cqr-1"}

	// Format 1.
	t.Setenv(format4.FormatEnv, "1")
	dir := t.TempDir()
	s, err := storage.NewFlatSQLStore(dir, v)
	if err != nil {
		t.Fatal(err)
	}
	if n, err := s.StoreBatchWithSourceTags("CQR.fbs", recs, "source:screening", nil, tags); err != nil || n != len(recs) {
		t.Fatalf("format 1: stored %d $CQR records: %v", n, err)
	}
	assertCQRServed(t, s, recs)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	// store-migrate --to 4: no unregistered table, every record carried,
	// the migration's check and a --verify-only rerun clean.
	inv, err := migrate4Inventory(dir)
	if err != nil {
		t.Fatal(err)
	}
	if u := inv.Extra["unregistered_tables"]; u != nil {
		t.Fatalf("the inventory lists unregistered tables: %v", u)
	}
	legacy := cloneStore(t, dir)
	rep, err := migrateStore4(ctx, engineOptions(t, dir), nil)
	if err != nil || !rep.Activated || len(rep.Unregistered) != 0 || rep.Records != int64(len(recs)) ||
		rep.Check == nil || rep.Check.MismatchCount != 0 {
		t.Fatalf("store-migrate --to 4: %v; report %+v", err, rep)
	}
	verify := engineOptions(t, dir)
	verify.VerifyOnly = true
	if rep, err := migrateStore4(ctx, verify, nil); err != nil || rep.Check == nil || rep.Check.MismatchCount != 0 ||
		rep.Check.Records != int64(len(recs)) {
		t.Fatalf("store-migrate --to 4 --verify-only: %v; report %+v", err, rep)
	}
	api := openEngineAt(t, dir, "CQR.fbs")
	assertFormat4EqualsFormat1(t, api, legacy, 0)
	if err := api.Close(ctx); err != nil {
		t.Fatal(err)
	}

	// Format 4: the daemon on the migrated store, and on a fresh store.
	if _, _, err := flatsqlrt.PrewarmP4ThreadsAOT(storage.EngineAOTCacheDir()); err != nil {
		t.Fatalf("prewarm the format-4 engine: %v", err)
	}
	t.Setenv(format4.FormatEnv, "4")
	s4, err := storage.NewFlatSQLStore(dir, v)
	if err != nil {
		t.Fatalf("open the migrated store: %v", err)
	}
	assertCQRServed(t, s4, recs)
	if err := s4.Close(); err != nil {
		t.Fatal(err)
	}
	fresh, err := storage.NewFlatSQLStore(t.TempDir(), v)
	if err != nil {
		t.Fatal(err)
	}
	defer fresh.Close()
	if !fresh.Format4() {
		t.Fatal("SDN_STORE_FORMAT=4 opened another format")
	}
	if n, err := fresh.StoreBatchWithSourceTags("CQR.fbs", recs, "source:screening", nil, tags); err != nil || n != len(recs) {
		t.Fatalf("format 4: stored %d $CQR records: %v", n, err)
	}
	assertCQRServed(t, fresh, recs)
}
