package api

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/DPM"
	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/NCD"
	flatbuffers "github.com/google/flatbuffers/go"
	"github.com/spacedatanetwork/sdn-server/internal/config"
	"github.com/spacedatanetwork/sdn-server/internal/sds"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
)

func TestAutoPublisherSourceReplaysAllRawHashBatchesAfterStartup(t *testing.T) {
	validator, err := sds.NewValidator(nil)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	store, err := storage.NewFlatSQLStore(filepath.Join(dir, "store"), validator)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	provider, source := "resource-fixture", "native-containers"
	wantHashes := map[string]bool{}
	for _, resource := range []string{"first resource bytes", "second resource bytes"} {
		digest := fmt.Sprintf("%x", sha256.Sum256([]byte(resource)))
		wantHashes[digest] = true
		builder := flatbuffers.NewBuilder(256)
		format := builder.CreateString("resource-fixture")
		hash := builder.CreateString(digest)
		NCD.NCDStart(builder)
		NCD.NCDAddFORMAT(builder, NCD.EnumValuesncdContainerFormat["PROVIDER_DEFINED"])
		NCD.NCDAddPROVIDER_DEFINED_FORMAT_NAME(builder, format)
		NCD.NCDAddSOURCE_SHA256(builder, hash)
		NCD.NCDAddSOURCE_BYTE_LENGTH(builder, uint64(len(resource)))
		NCD.FinishNCDBuffer(builder, NCD.NCDEnd(builder))
		tags := storage.SourceTags{ProviderID: provider, SourceName: source, BatchID: digest, ContentKeyID: "public", ProducerPeerID: "fixture-producer", License: "CC0-1.0"}
		if _, err := store.StoreWithSourceTags("NCD.fbs", builder.FinishedBytes(), tags.ProducerPeerID, nil, tags); err != nil {
			t.Fatal(err)
		}
	}
	pinned := map[string][]byte{}
	kubo := newDatasetPublicationKuboTestServer(t, pinned)
	defer kubo.Close()
	publicKey, privateKey, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	announcer := &fakeDatasetUpdatePublisher{}
	service := NewConcreteDatasetPublicationService(store, announcer, privateKey, "fixture-producer", "bafy-provider-epm", kubo.URL, filepath.Join(dir, "publications"))
	publisher := NewAutoPublisher(service, []config.AutoPublishLane{{Schema: "NCD.fbs", ProviderID: provider, SourceName: source, PublishScope: "source", MinInterval: time.Hour}})
	type outcome struct {
		result *DatasetPublicationResult
		err    error
	}
	done := make(chan outcome, 1)
	publisher.onPublished = func(_ DatasetPublicationRequest, result *DatasetPublicationResult, err error) {
		done <- outcome{result, err}
	}
	publisher.Start(context.Background()) // no new ingest: durable source catchup
	defer publisher.Stop()
	var result *DatasetPublicationResult
	select {
	case got := <-done:
		if got.err != nil {
			t.Fatal(got.err)
		}
		result = got.result
	case <-time.After(30 * time.Second):
		t.Fatal("source catchup did not publish")
	}
	if result == nil || result.RecordCount != 2 || !announcer.called {
		t.Fatalf("source publication omitted stored batches: %+v", result)
	}
	manifest := DPM.GetRootAsDPM(pinned[result.ManifestCID], 0)
	if manifest.SOURCESLength() != len(wantHashes) {
		t.Fatalf("manifest source batches=%d, want %d", manifest.SOURCESLength(), len(wantHashes))
	}
	seenHashes := map[string]bool{}
	for i := 0; i < manifest.SOURCESLength(); i++ {
		var batch DPM.DPMSourceBatch
		if !manifest.SOURCES(&batch, i) || !wantHashes[string(batch.SOURCE_SHA256())] || string(batch.LICENSE()) != "CC0-1.0" {
			t.Fatal("manifest replaced raw-hash batch provenance")
		}
		seenHashes[string(batch.SOURCE_SHA256())] = true
	}
	if len(seenHashes) != len(wantHashes) {
		t.Fatal("manifest duplicated a source batch")
	}
	consumer, err := storage.NewFlatSQLStore(filepath.Join(dir, "consumer"), validator)
	if err != nil {
		t.Fatal(err)
	}
	defer consumer.Close()
	options := storage.DatasetPublicationReplayOptions{
		PNM: announcer.announcement.PNM, ProviderPublicKey: publicKey, WorkDir: filepath.Join(dir, "replay"),
		FetchByCID: func(_ context.Context, cid string) ([]byte, error) {
			if body, ok := pinned[cid]; ok {
				return body, nil
			}
			return nil, fmt.Errorf("unpublished CID %s", cid)
		},
	}
	materialized, err := storage.MaterializeDatasetPublication(context.Background(), consumer, options)
	if err != nil || materialized.Imported != 2 {
		t.Fatalf("source materialization: %+v err=%v", materialized, err)
	}
	if replay, err := storage.VerifyDatasetPublicationReplay(context.Background(), consumer, options); err != nil || replay.RecordCount != 2 {
		t.Fatalf("source replay: %+v err=%v", replay, err)
	}
	rows, err := consumer.QueryIndexedRecords(storage.IndexedRecordQuery{SchemaName: "NCD.fbs", ProviderID: provider, SourceName: source, Limit: 10})
	if err != nil || len(rows) != 2 {
		t.Fatalf("source rows=%d err=%v", len(rows), err)
	}
	for _, row := range rows {
		if hash := string(NCD.GetRootAsNCD(row.Data, 0).SOURCE_SHA256()); !wantHashes[hash] {
			t.Fatalf("source descriptor digest changed: %s", hash)
		}
	}
}
