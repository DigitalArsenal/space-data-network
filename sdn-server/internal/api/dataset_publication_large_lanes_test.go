package api

import (
	"context"
	"crypto/ed25519"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	MPEFB "github.com/DigitalArsenal/spacedatastandards.org/lib/go/MPE"
	flatbuffers "github.com/google/flatbuffers/go"

	"github.com/spacedatanetwork/sdn-server/internal/sds"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
	"github.com/spacedatanetwork/sdn-server/internal/storage/format4"
)

// The scheduled element-set run on a format-5 store holding production lane
// sizes (stack issue 818): an OMM lane of 9,611,180 records and an MPE lane
// of 12,112,720 records over 33,000 objects, filled in runs of 32,915
// records. The store is closed and opened again, as on a node restart, and
// one run follows: one batch into both lanes, the OMM batch published, then
// the MPE batch. FlatSQL 3.12.10 ends its engine on the checkpoint thread
// (bad_alloc in the feed's DAG cache) once a lane passes 8,388,608 records.
//
// Closing the store stops the engine, which checkpoints every lane
// synchronously, so both lanes' DAG caches are built in full whatever the
// timing of the background checkpoints. The batch is then read back from the
// reopened store.
//
// It needs about 15 GB of disk in the temporary directory and about ten
// minutes; set SDN_E2E_LARGE_LANES=1 to run it.
func TestElementSetRunAtProductionLaneSizes(t *testing.T) {
	if os.Getenv("SDN_E2E_LARGE_LANES") != "1" {
		t.Skip("set SDN_E2E_LARGE_LANES=1 for the production-size element-set run (about 15 GB of disk, ten minutes)")
	}
	const (
		perRun     = 32915
		objects    = 33000
		ommRuns    = 292 // 9,611,180 records
		mpeRuns    = 368 // 12,112,720 records
		provider   = "space-data-network-02"
		sourceName = "celestrak-gp"
	)
	t.Setenv(format4.FormatEnv, "5")
	validator, err := sds.NewValidator(nil)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	open := func() *storage.FlatSQLStore {
		store, err := storage.NewFlatSQLStore(filepath.Join(dir, "store"), validator)
		if err != nil {
			t.Fatalf("open the store: %v", err)
		}
		return store
	}
	tags := func(run int) storage.SourceTags {
		return storage.SourceTags{ProviderID: provider, SourceName: sourceName, BatchID: fmt.Sprintf("run-%05d", run), ContentKeyID: "public"}
	}
	ingest := func(store *storage.FlatSQLStore, schema string, run int) {
		t.Helper()
		n, err := store.StoreBatchWithSourceTags(schema, largeLaneRun(schema, run, perRun, objects), "source:celestrak", nil, tags(run))
		if err != nil {
			t.Fatalf("store %s run %d: %v", schema, run, err)
		}
		if n != perRun {
			t.Fatalf("store %s run %d stored %d records, want %d", schema, run, n, perRun)
		}
	}

	store := open()
	started := time.Now()
	for run := 0; run < ommRuns; run++ {
		ingest(store, "OMM.fbs", run)
	}
	for run := 0; run < mpeRuns; run++ {
		ingest(store, "MPE.fbs", run)
	}
	t.Logf("filled OMM %d and MPE %d records in %s", ommRuns*perRun, mpeRuns*perRun, time.Since(started).Round(time.Second))
	if err := store.Close(); err != nil {
		t.Fatalf("close the filled store: %v", err)
	}

	store = open()
	kubo := newDatasetPublicationKuboTestServer(t, make(map[string][]byte))
	defer kubo.Close()
	_, key, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	service := newPublicationServiceWithPinPolicy(t, store, &fakeDatasetUpdatePublisher{}, key,
		"16Uiu2HAmGjaPxkWFSXBbmhs9K5x1Zo6euJw95VjS6Jj2bcPpYr2U", "bafy-provider-epm", kubo.URL, filepath.Join(dir, "publications"))
	const run = mpeRuns
	for _, schema := range []string{"OMM.fbs", "MPE.fbs"} {
		ingest(store, schema, run)
	}
	for _, schema := range []string{"OMM.fbs", "MPE.fbs"} {
		result, err := service.PublishDatasetUpdate(context.Background(), DatasetPublicationRequest{
			Schema: schema, ProviderID: provider, SourceName: sourceName, BatchID: tags(run).BatchID,
		})
		if err != nil {
			t.Fatalf("publish the %s batch: %v", schema, err)
		}
		if result.RecordCount != perRun {
			t.Fatalf("the %s publication holds %d records, want %d", schema, result.RecordCount, perRun)
		}
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close after the run (the engine checkpoints both lanes): %v", err)
	}

	store = open()
	defer store.Close()
	for _, schema := range []string{"OMM.fbs", "MPE.fbs"} {
		_, n, err := store.PagedDatasetPublicationSetFingerprint(schema, provider, sourceName, tags(run).BatchID)
		if err != nil {
			t.Fatalf("read the %s batch back: %v", schema, err)
		}
		if n != perRun {
			t.Fatalf("the reopened store holds %d records of the %s batch, want %d", n, schema, perRun)
		}
	}
}

// largeLaneRun is one run's records of schema: n records over objects
// objects, their epoch one hour after the previous run's.
func largeLaneRun(schema string, run, n, objects int) [][]byte {
	out := make([][]byte, n)
	epoch := int64(1767225600 + run*3600)
	meanMotion := 15.5 + float64(run%1000)*1e-6
	for i := range out {
		norad := uint32(1 + i%objects)
		if schema == "OMM.fbs" {
			record := sds.NewOMMBuilder().WithNoradCatID(norad).WithObjectName(fmt.Sprintf("OBJ-%05d", norad)).
				WithEpoch(time.Unix(epoch, 0).UTC().Format(time.RFC3339)).WithEpochTimestamp(float64(epoch)).
				WithMeanMotion(meanMotion).Build()
			out[i] = record[4:]
			continue
		}
		b := flatbuffers.NewBuilder(256)
		id := b.CreateString(strconv.Itoa(int(norad)))
		MPEFB.MPEStart(b)
		MPEFB.MPEAddENTITY_ID(b, id)
		MPEFB.MPEAddEPOCH(b, float64(epoch))
		MPEFB.MPEAddMEAN_MOTION(b, meanMotion)
		MPEFB.MPEAddECCENTRICITY(b, 0.0001+float64(norad%97)*1e-6)
		MPEFB.MPEAddINCLINATION(b, 51.6)
		MPEFB.MPEAddRA_OF_ASC_NODE(b, float64(run%360))
		MPEFB.MPEAddARG_OF_PERICENTER(b, 90)
		MPEFB.MPEAddMEAN_ANOMALY(b, 10)
		MPEFB.MPEAddBSTAR(b, 0.0001)
		b.FinishSizePrefixedWithFileIdentifier(MPEFB.MPEEnd(b), []byte("$MPE"))
		out[i] = b.FinishedBytes()[4:]
	}
	return out
}
