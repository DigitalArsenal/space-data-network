package caps

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"sync"
	"testing"

	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/IQC"
	flatbuffers "github.com/google/flatbuffers/go"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
)

// storage_ingest_identity_test.go — graph: sdn-publication-hygiene-20260928.
// The sigmf-captures flow re-runs ~20 s after every daemon start and hands the
// host the same IQEngine batch with fresh fetch stamps. Through the real
// storage.ingest_with_source op, that replay must land 0 records and report
// Inserted 0 to the ingest taps — the auto-publisher's trigger.

func buildSigmfTestIQC(seq int, stamp string) []byte {
	b := flatbuffers.NewBuilder(512)
	id := b.CreateString(fmt.Sprintf("iqengine:local/local/capture-%06d", seq))
	capture := b.CreateString(fmt.Sprintf("capture-%06d", seq))
	source := b.CreateString("IQEngine")
	retrieved := b.CreateString(stamp)
	desc := b.CreateString(fmt.Sprintf("SigMF capture %d", seq))
	IQC.IQCStart(b)
	IQC.IQCAddID(b, id)
	IQC.IQCAddCAPTURE_ID(b, capture)
	IQC.IQCAddSOURCE_NAME(b, source)
	IQC.IQCAddRETRIEVED_AT(b, retrieved)
	IQC.IQCAddDESCRIPTION(b, desc)
	IQC.IQCAddCREATED_AT(b, retrieved)
	IQC.IQCAddUPDATED_AT(b, retrieved)
	root := IQC.IQCEnd(b)
	b.FinishWithFileIdentifier(root, []byte(IQC.IQCIdentifier))
	return append([]byte(nil), b.FinishedBytes()...)
}

func TestIngestWithSourceSigmfReplayLandsNothing(t *testing.T) {
	handler, store := newIngestTestHandler(t, StorageCapOptions{MinFreeDiskBytes: 1})
	var mu sync.Mutex
	var observed []IngestObservation
	remove := AddIngestObserver(func(obs IngestObservation) {
		if obs.Schema == "IQC.fbs" {
			mu.Lock()
			observed = append(observed, obs)
			mu.Unlock()
		}
	})
	defer remove()

	const captures = 50
	const batch = "60cc968008101521f0f062a74bf83f9c7e72fcc3bcd8bc3ed84b97d12f4a29e3"
	ingest := func(stamp, reconcile string) map[string]interface{} {
		t.Helper()
		records := make([][]byte, captures)
		for i := range records {
			records[i] = buildSigmfTestIQC(i, stamp)
		}
		body, _ := json.Marshal(map[string]interface{}{
			"schema":         "IQC.fbs",
			"provider_id":    "space-data-network-02",
			"source_name":    "IQEngine",
			"source_url":     "https://www.iqengine.org/api/datasources/local/local/meta",
			"batch_id":       batch,
			"content_key_id": "public",
			"source_peer":    "source:sigmf",
			"reconcile":      reconcile,
			"records":        base64.StdEncoding.EncodeToString(sizePrefixedStream(records)),
		})
		resp, err := handler("storage.ingest_with_source", body)
		if err != nil {
			t.Fatalf("handler: %v", err)
		}
		meta := decodeCapMeta(t, resp)
		if ok, _ := meta["ok"].(bool); !ok {
			t.Fatalf("ingest failed: %v", meta)
		}
		return meta
	}

	// The module's default reconcile is "none"; "duplicates" is the host
	// default. Neither may double the batch.
	for i, run := range []struct{ stamp, reconcile string }{
		{"2026-09-15T02:11:22Z", "none"},
		{"2026-09-22T14:06:56Z", "none"},
		{"2026-09-27T16:16:48Z", "duplicates"},
	} {
		meta := ingest(run.stamp, run.reconcile)
		want := float64(0)
		if i == 0 {
			want = captures
		}
		if got, _ := capResultField(t, meta, "inserted").(float64); got != want {
			t.Fatalf("run %d (%s): inserted %v, want %v", i, run.reconcile, got, want)
		}
	}
	tagged, err := store.QuerySourceTaggedRecords(storage.SourceTagQuery{
		SchemaName: "IQC.fbs", ProviderID: "space-data-network-02", SourceName: "IQEngine", BatchID: batch, Limit: 10 * captures,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(tagged) != captures {
		t.Fatalf("batch holds %d records after 3 runs, want %d", len(tagged), captures)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(observed) != 3 || observed[0].Inserted != captures || observed[1].Inserted != 0 || observed[2].Inserted != 0 {
		t.Fatalf("ingest taps saw %+v, want Inserted %d then 0, 0", observed, captures)
	}
}
