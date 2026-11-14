package api

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/spacedatanetwork/sdn-server/internal/pinpolicy"
	sdnpubsub "github.com/spacedatanetwork/sdn-server/internal/pubsub"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
)

// signedCatalogLoopback delivers the production signed announcement bytes to
// a consumer over HTTP. All identities and servers belong to this test.
type signedCatalogLoopback struct {
	url           string
	announcements [][]byte
}

func (p *signedCatalogLoopback) PublishDatasetUpdatePNM(ctx context.Context, ann sdnpubsub.DatasetUpdateAnnouncement) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.url, bytes.NewReader(ann.PNM))
	if err != nil {
		return err
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	body, err := io.ReadAll(res.Body)
	if err != nil {
		return err
	}
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("consumer rejected publication: %s", body)
	}
	p.announcements = append(p.announcements, bytes.Clone(ann.PNM))
	return nil
}

func TestRetainedCatalogSignedPublicationLoopback(t *testing.T) {
	t.Setenv("SDN_STORE_FORMAT", "4")
	ctx := context.Background()
	root := t.TempDir()
	producer := pairedStore(t, filepath.Join(root, "producer"))
	defer producer.Close()
	catalogEnableRetention(t, producer)
	consumer := pairedStore(t, filepath.Join(root, "consumer"))
	defer consumer.Close()
	public, private, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	trusted := mustLibp2pEd25519(t, public)
	pinned := map[string][]byte{}
	kubo := newDatasetPublicationKuboTestServer(t, pinned)
	defer kubo.Close()
	fetch := func(ctx context.Context, id string) ([]byte, error) {
		return storage.FetchIPFSBlockByCID(ctx, kubo.URL, id)
	}
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err == nil {
			_, err = storage.MaterializeDatasetPublication(r.Context(), consumer, storage.DatasetPublicationReplayOptions{
				PNM: body, ProviderPublicKey: trusted, FetchByCID: fetch, WorkDir: filepath.Join(root, "replay"),
			})
		}
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer receiver.Close()
	announcer := &signedCatalogLoopback{url: receiver.URL}
	service := NewConcreteDatasetPublicationService(producer, announcer, private, "synthetic-node", "bafy-provider-epm", kubo.URL, filepath.Join(root, "exports"))
	pins, err := pinpolicy.New(kubo.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	service.SetPinPolicy(pins)
	catalogPutDay(t, producer, 0, "OCM", "CAT")
	publisher := NewAutoPublisher(service, catalogRules())
	publisher.onPublished = func(_ DatasetPublicationRequest, _ *DatasetPublicationResult, err error) {
		if err != nil {
			t.Fatal(err)
		}
	}
	catalogDrain(t, publisher)
	if len(announcer.announcements) != 2 {
		t.Fatalf("batch announcements = %d, want 2", len(announcer.announcements))
	}
	assertSelected := func() {
		t.Helper()
		expected, _ := catalogSnapshotConsumer(t, producer, 0)
		actual := newCatalogConsumer()
		for _, item := range []struct{ schema, source string }{{"OCM.fbs", "catalog-products"}, {"CAT.fbs", "catalog-index"}} {
			// The consumer can retain both 32-product generations.
			ex, err := consumer.ExportDatasetWindow(t.TempDir(), storage.IndexedRecordQuery{SchemaName: item.schema, ProviderID: "synthetic-node", SourceName: item.source, Limit: 100})
			if err != nil {
				t.Fatal(err)
			}

			actual.apply(t, ex)
		}
		actual.validate(t)
		if !reflect.DeepEqual(actual.selected, expected.selected) {
			t.Fatal("verified consumer selection differs from retained source")
		}
		for id, data := range expected.products {
			if !bytes.Equal(actual.products[id], data) {
				t.Fatal("verified consumer product bytes differ")
			}
		}
	}
	assertSelected()
	catalogPutDay(t, producer, 1, "OCM", "CAT")
	result, err := service.PublishDatasetUpdate(ctx, DatasetPublicationRequest{Schema: "CAT.fbs", ProviderID: "synthetic-node", SourceName: "catalog-index", FullCatalog: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(announcer.announcements) != 4 {
		t.Fatalf("batch and snapshot announcements = %d, want 4", len(announcer.announcements))
	}
	assertSelected()
	// Duplicate signed delivery must leave the selected catalog unchanged.
	good := announcer.announcements[len(announcer.announcements)-1]
	if err := announcer.PublishDatasetUpdatePNM(ctx, sdnpubsub.DatasetUpdateAnnouncement{PNM: good}); err != nil {
		t.Fatal(err)
	}
	assertSelected()
	// A different real test signer cannot pass the consumer's pinned trust key.
	_, untrusted, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	attackerStore := pairedStore(t, filepath.Join(root, "untrusted-store"))
	defer attackerStore.Close()
	catalogEnableRetention(t, attackerStore)
	catalogPutDay(t, attackerStore, 0, "OCM", "CAT")
	attacker := NewConcreteDatasetPublicationService(attackerStore, announcer, untrusted, "synthetic-node", "bafy-provider-epm", kubo.URL, filepath.Join(root, "untrusted"))
	attacker.SetPinPolicy(pins)
	_, err = attacker.PublishDatasetUpdate(ctx, DatasetPublicationRequest{Schema: "CAT.fbs", ProviderID: "synthetic-node", SourceName: "catalog-index", FullCatalog: true})
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "signature") {
		t.Fatalf("untrusted announcement rejection: %v", err)
	}
	assertSelected()
	// Keep the signed manifest and announcement intact, change only fetched bytes.
	rejected := pairedStore(t, filepath.Join(root, "rejected"))
	defer rejected.Close()
	tampered := false
	_, err = storage.MaterializeDatasetPublication(ctx, rejected, storage.DatasetPublicationReplayOptions{
		PNM: good, ProviderPublicKey: trusted, WorkDir: filepath.Join(root, "tampered"),
		FetchByCID: func(ctx context.Context, id string) ([]byte, error) {
			data, err := fetch(ctx, id)
			if err == nil && id == result.ShardCID {
				data = bytes.Clone(data)
				data[len(data)-1] ^= 1
				tampered = true
			}
			return data, err
		},
	})
	if !tampered || err == nil {
		t.Fatalf("tampered shard accepted: modified=%v err=%v", tampered, err)
	}
	rows, err := rejected.QueryIndexedRecords(storage.IndexedRecordQuery{SchemaName: "CAT.fbs", ProviderID: "synthetic-node", SourceName: "catalog-index", Limit: 100})
	if err != nil || len(rows) != 0 {
		t.Fatalf("rejected shard changed consumer: count=%d err=%v", len(rows), err)
	}
}
