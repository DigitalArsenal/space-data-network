package api

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/DPM"
	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/WXF"
	flatbuffers "github.com/google/flatbuffers/go"
	"github.com/spacedatanetwork/sdn-server/internal/channels"
	"github.com/spacedatanetwork/sdn-server/internal/config"
	"github.com/spacedatanetwork/sdn-server/internal/sds"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
)

// Exercise the real schema gate, source-tagged store, complete-batch export,
// auto-publisher and signed manifests together. The HTTP server stands in for
// Kubo only; no provider is contacted and no live publication occurs.
func TestWXFValidatedBatchAutoPublishesWithAttribution(t *testing.T) {
	v, err := sds.NewValidator(nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := channels.AssertStandardCode("WXF"); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	store, err := storage.NewFlatSQLStore(filepath.Join(dir, "store"), v)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	tags := storage.SourceTags{ProviderID: "weather-fixture", SourceName: "forecast/52.52,13.41", BatchID: "weather-batch", ContentKeyID: "public", ProducerPeerID: "weather-producer", License: "CC-BY-4.0", LicenseURL: "https://creativecommons.org/licenses/by/4.0/", Citation: "Weather fixture attribution"}
	want := map[string][]byte{}
	for i := 0; i < 3; i++ {
		b := flatbuffers.NewBuilder(512)
		id := b.CreateString(fmt.Sprintf("weather-%d", i))
		units := b.CreateString("K")
		license := b.CreateString(tags.LicenseURL)
		citation := b.CreateString(tags.Citation)
		producer := b.CreateString(tags.ProducerPeerID)
		WXF.WXFGridStart(b)
		WXF.WXFGridAddLAT0(b, 52.52)
		WXF.WXFGridAddLON0(b, 13.41)
		WXF.WXFGridAddNLAT(b, 1)
		WXF.WXFGridAddNLON(b, 1)
		grid := WXF.WXFGridEnd(b)
		WXF.WXFStartVALUESVector(b, 1)
		b.PrependFloat32(288.15 + float32(i))
		values := b.EndVector(1)
		WXF.WXFStart(b)
		WXF.WXFAddFIELD_ID(b, id)
		WXF.WXFAddGRID(b, grid)
		WXF.WXFAddVALUES(b, values)
		WXF.WXFAddUNITS(b, units)
		WXF.WXFAddVARIABLE(b, WXF.EnumValueswxfVariable["Temperature2m"])
		WXF.WXFAddVALID_TIME_MS(b, 1788912000000+uint64(i)*3600000)
		WXF.WXFAddTIME_BASIS(b, WXF.EnumValueswxfTimeBasis["ValidTimeOnly"])
		WXF.WXFAddMEMBER_KIND(b, WXF.EnumValueswxfMemberKind["Unspecified"])
		WXF.WXFAddLICENSE_CLASS(b, WXF.EnumValueswxfLicenseClass["OpenAttribution"])
		WXF.WXFAddLICENSE_URL(b, license)
		WXF.WXFAddCITATION(b, citation)
		WXF.WXFAddPRODUCER_PEER_ID(b, producer)
		WXF.FinishWXFBuffer(b, WXF.WXFEnd(b))
		record := append([]byte(nil), b.FinishedBytes()...)
		cid, err := store.StoreWithSourceTags("WXF.fbs", record, tags.ProducerPeerID, nil, tags)
		if err != nil {
			t.Fatal(err)
		}
		want[cid] = record
	}
	for offset := 0; offset < 3; offset += 2 {
		rows, err := store.QueryIndexedRecords(storage.IndexedRecordQuery{SchemaName: "WXF.fbs", ProviderID: tags.ProviderID, SourceName: tags.SourceName, BatchID: tags.BatchID, Limit: 2, Offset: offset, OrderByCID: true})
		if err != nil {
			t.Fatal(err)
		}
		for _, row := range rows {
			if !bytes.Equal(row.Data, want[row.CID]) {
				t.Fatal("paged WXF bytes changed")
			}
			decoded := WXF.GetRootAsWXF(row.Data, 0)
			if decoded.TIME_BASIS() != WXF.EnumValueswxfTimeBasis["ValidTimeOnly"] || decoded.INIT_TIME_MS() != 0 || decoded.LEAD_HOURS() != 0 || decoded.LICENSE_CLASS() != WXF.EnumValueswxfLicenseClass["OpenAttribution"] || string(decoded.PRODUCER_PEER_ID()) != tags.ProducerPeerID {
				t.Fatal("weather metadata changed")
			}
		}
	}
	pinned := map[string][]byte{}
	kubo := newDatasetPublicationKuboTestServer(t, pinned)
	defer kubo.Close()
	publicKey, signingKey, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	publisher := &fakeDatasetUpdatePublisher{}
	service := NewConcreteDatasetPublicationService(store, publisher, signingKey, tags.ProducerPeerID, "bafy-provider-epm", kubo.URL, filepath.Join(dir, "publications"))
	auto := NewAutoPublisher(service, []config.AutoPublishLane{{Schema: "WXF.fbs", ProviderID: tags.ProviderID, SourceName: tags.SourceName, MinInterval: time.Hour}})
	type outcome struct {
		result *DatasetPublicationResult
		err    error
	}
	done := make(chan outcome, 1)
	auto.onPublished = func(_ DatasetPublicationRequest, result *DatasetPublicationResult, err error) {
		done <- outcome{result, err}
	}
	auto.Start(context.Background())
	defer auto.Stop()
	auto.ObserveIngest(IngestedBatch{Schema: "WXF.fbs", ProviderID: tags.ProviderID, SourceName: tags.SourceName, BatchID: tags.BatchID, Inserted: 3})
	var result *DatasetPublicationResult
	select {
	case got := <-done:
		if got.err != nil {
			t.Fatal(got.err)
		}
		result = got.result
	case <-time.After(30 * time.Second):
		t.Fatal("weather publication did not finish")
	}
	if result == nil || result.RecordCount != 3 || !publisher.called {
		t.Fatalf("incomplete publication: %+v", result)
	}
	manifestBytes := pinned[result.ManifestCID]
	pnm, err := channels.VerifySignedPNMEnvelope(publisher.announcement.PNM)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := channels.VerifySignedDPMManifestWithProviderKey(manifestBytes, pnm.FileID, publicKey); err != nil {
		t.Fatal(err)
	}
	manifest := DPM.GetRootAsDPM(manifestBytes, 0)
	var source DPM.DPMSourceBatch
	if !manifest.SOURCES(&source, 0) || string(source.LICENSE()) != tags.License || string(source.LICENSE_URL()) != tags.LicenseURL || string(source.CITATION()) != tags.Citation {
		t.Fatal("signed manifest lost source licence")
	}
	var asset DPM.DPMAsset
	if !manifest.ASSETS(&asset, 0) || len(asset.SCHEMA_HASH()) == 0 {
		t.Fatal("manifest lacks embedded WXF schema identity")
	}
	if len(pinned[result.ShardCID]) == 0 || len(pinned[result.IndexCID]) == 0 {
		t.Fatal("weather shard or index was not pinned")
	}
	consumer, err := storage.NewFlatSQLStore(filepath.Join(dir, "consumer"), v)
	if err != nil {
		t.Fatal(err)
	}
	defer consumer.Close()
	replayOptions := storage.DatasetPublicationReplayOptions{
		PNM: publisher.announcement.PNM, ProviderPublicKey: publicKey, WorkDir: filepath.Join(dir, "replay"),
		FetchByCID: func(_ context.Context, cid string) ([]byte, error) {
			body, ok := pinned[cid]
			if !ok {
				return nil, fmt.Errorf("unpublished CID %s", cid)
			}
			return body, nil
		},
	}
	materialized, err := storage.MaterializeDatasetPublication(context.Background(), consumer, replayOptions)
	if err != nil {
		t.Fatal(err)
	}
	if materialized.Imported != 3 {
		t.Fatalf("weather consumer imported %d records", materialized.Imported)
	}
	replay, err := storage.VerifyDatasetPublicationReplay(context.Background(), consumer, replayOptions)
	if err != nil {
		t.Fatal(err)
	}
	if replay.RecordCount != 3 || replay.SchemaName != "WXF.fbs" || replay.ShardCID != result.ShardCID {
		t.Fatalf("WXF replay changed publication: %+v", replay)
	}
	consumerLicense, found, err := consumer.SourceBatchLicenseFor("WXF.fbs", tags.ProviderID, tags.SourceName, tags.BatchID)
	if err != nil || !found || consumerLicense.License != tags.License {
		t.Fatalf("consumer lost licence: %+v found=%v err=%v", consumerLicense, found, err)
	}
}
