package api

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/NCD"
	flatbuffers "github.com/google/flatbuffers/go"
	"github.com/spacedatanetwork/sdn-server/internal/channels"
	"github.com/spacedatanetwork/sdn-server/internal/sds"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
)

func TestNCDValidatedDescriptorPublicationReplay(t *testing.T) {
	v, err := sds.NewValidator(nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := channels.AssertStandardCode("NCD"); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	store, err := storage.NewFlatSQLStore(filepath.Join(dir, "store"), v)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	resource := []byte("native resource fixture\n")
	digest := fmt.Sprintf("%x", sha256.Sum256(resource))
	b := flatbuffers.NewBuilder(256)
	format := b.CreateString("resource-fixture")
	digestOffset := b.CreateString(digest)
	producer := b.CreateString("resource-producer")
	NCD.NCDStart(b)
	NCD.NCDAddFORMAT(b, NCD.EnumValuesncdContainerFormat["PROVIDER_DEFINED"])
	NCD.NCDAddPROVIDER_DEFINED_FORMAT_NAME(b, format)
	NCD.NCDAddPRODUCER(b, producer)
	NCD.NCDAddSOURCE_BYTE_LENGTH(b, uint64(len(resource)))
	NCD.NCDAddSOURCE_SHA256(b, digestOffset)
	NCD.FinishNCDBuffer(b, NCD.NCDEnd(b))
	record := append([]byte(nil), b.FinishedBytes()...)
	tags := storage.SourceTags{ProviderID: "resource-fixture", SourceName: "native-container", BatchID: digest, ContentKeyID: "public", ProducerPeerID: "resource-producer", License: "CC0-1.0"}
	if _, err := store.StoreWithSourceTags("NCD.fbs", record, tags.ProducerPeerID, nil, tags); err != nil {
		t.Fatal(err)
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
	result, err := service.PublishDatasetUpdate(context.Background(), DatasetPublicationRequest{Schema: "NCD.fbs", ProviderID: tags.ProviderID, SourceName: tags.SourceName, BatchID: tags.BatchID})
	if err != nil {
		t.Fatal(err)
	}
	if result.RecordCount != 1 || !publisher.called {
		t.Fatalf("incomplete descriptor publication: %+v", result)
	}
	consumer, err := storage.NewFlatSQLStore(filepath.Join(dir, "consumer"), v)
	if err != nil {
		t.Fatal(err)
	}
	defer consumer.Close()
	options := storage.DatasetPublicationReplayOptions{
		PNM: publisher.announcement.PNM, ProviderPublicKey: publicKey, WorkDir: filepath.Join(dir, "replay"),
		FetchByCID: func(_ context.Context, cid string) ([]byte, error) {
			body, ok := pinned[cid]
			if !ok {
				return nil, fmt.Errorf("unpublished CID %s", cid)
			}
			return body, nil
		},
	}
	materialized, err := storage.MaterializeDatasetPublication(context.Background(), consumer, options)
	if err != nil {
		t.Fatal(err)
	}
	if materialized.Imported != 1 {
		t.Fatalf("descriptor consumer imported %d records", materialized.Imported)
	}
	replay, err := storage.VerifyDatasetPublicationReplay(context.Background(), consumer, options)
	if err != nil {
		t.Fatal(err)
	}
	if replay.RecordCount != 1 || replay.SchemaName != "NCD.fbs" || replay.ShardCID != result.ShardCID {
		t.Fatalf("descriptor replay changed publication: %+v", replay)
	}
	rows, err := consumer.QueryIndexedRecords(storage.IndexedRecordQuery{SchemaName: "NCD.fbs", ProviderID: tags.ProviderID, SourceName: tags.SourceName, BatchID: tags.BatchID, Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || !bytes.Equal(rows[0].Data, record) {
		t.Fatal("published descriptor bytes changed")
	}
	decoded := NCD.GetRootAsNCD(rows[0].Data, 0)
	if decoded.SOURCE_BYTE_LENGTH() != uint64(len(resource)) || string(decoded.SOURCE_SHA256()) != digest {
		t.Fatal("published descriptor lost its resource identity")
	}
}
