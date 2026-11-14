package api

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"path/filepath"
	"testing"

	dpm "github.com/DigitalArsenal/spacedatastandards.org/lib/go/DPM"
	"github.com/spacedatanetwork/sdn-server/internal/pinpolicy"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
	"github.com/spacedatanetwork/sdn-server/internal/syncwire"
)

func TestPairedSnapshotListingCutoffsMatchSignedWindows(t *testing.T) {
	root := t.TempDir()
	store := pairedStore(t, filepath.Join(root, "producer"))
	defer store.Close()
	catalogEnableRetention(t, store)
	catalogPutDay(t, store, 0, "OCM", "CAT")
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	trusted := mustLibp2pEd25519(t, public)
	pinned := map[string][]byte{}
	kubo := newDatasetPublicationKuboTestServer(t, pinned)
	defer kubo.Close()
	pins, err := pinpolicy.New(kubo.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	service := NewConcreteDatasetPublicationService(store, &fakeDatasetUpdatePublisher{}, private, "synthetic-node", "synthetic-epm", kubo.URL, filepath.Join(root, "exports"))
	service.SetPinPolicy(pins)
	ctx := context.Background()
	if _, err := service.PublishDatasetUpdate(ctx, DatasetPublicationRequest{Schema: "CAT.fbs", ProviderID: "synthetic-node", SourceName: "catalog-index", FullCatalog: true, ChunkSize: 8}); err != nil {
		t.Fatal(err)
	}
	// Reusing the original service must not leak the paired-snapshot flag.
	if _, err := service.PublishDatasetUpdate(ctx, DatasetPublicationRequest{Schema: "CAT.fbs", ProviderID: "synthetic-node", SourceName: "catalog-index", BatchID: "day0"}); err != nil {
		t.Fatal(err)
	}
	var segments []syncwire.Segment
	for _, lane := range []struct{ schema, source string }{{"OCM.fbs", "catalog-products"}, {"CAT.fbs", "catalog-index"}} {
		rows, err := store.ListDatasetShardPublications(storage.DatasetShardPublicationQuery{SchemaName: lane.schema, ProviderID: "synthetic-node", SourceName: lane.source, QueryProfile: storage.DatasetPublicationQueryProfile})
		if err != nil {
			t.Fatal(err)
		}
		for _, row := range rows {
			segments = append(segments, syncwire.SegmentFromPublication(row))
		}
	}
	page, err := syncwire.ParseQRP(syncwire.SegmentPage(syncwire.QRPFields{}, segments))
	if err != nil {
		t.Fatal(err)
	}
	rows, err := syncwire.Segments(page)
	if err != nil {
		t.Fatal(err)
	}
	columns, cells := syncwire.Rows(page)
	windows := map[string]int{}
	ordinary := 0
	for i, row := range rows {
		if row.BatchID != "" {
			ordinary++
			if row.PublicationCutoff != nil || row.PublicationSourceCutoff != nil {
				t.Fatal("ordinary row acquired snapshot cutoffs")
			}
			for _, name := range []string{"PUBLICATION_CUTOFF", "PUBLICATION_SOURCE_CUTOFF"} {
				cell := cells[i][columns[name]]
				if cell.IsInt || cell.IsText || cell.IsBytes {
					t.Fatal("ordinary cutoff cell is not Null")
				}
			}
			continue
		}
		windows[row.SchemaName]++
		if row.PublicationCutoff == nil || row.PublicationSourceCutoff == nil || *row.PublicationCutoff <= 0 {
			t.Fatal("snapshot row has no typed cutoffs")
		}
		manifestBytes := pinned[row.ManifestCID]
		if _, err := storage.VerifySignedDatasetPublicationManifest(manifestBytes, trusted); err != nil {
			t.Fatalf("verify listed manifest: %v", err)
		}
		manifest := dpm.GetRootAsDPM(manifestBytes, 0)
		binding := manifest.QUERY(nil)
		if binding == nil {
			t.Fatal("signed manifest has no query binding")
		}
		// Only this test reads the signed standard's canonical query as JSON.
		var query struct {
			PublicationCutoff       uint64 `json:"publicationCutoff"`
			PublicationSourceCutoff uint64 `json:"publicationSourceCutoff"`
			BatchID                 string `json:"batchId"`
			Offset                  int64  `json:"offset"`
		}
		if err := json.Unmarshal(binding.CANONICAL_QUERY(), &query); err != nil {
			t.Fatal(err)
		}
		if uint64(*row.PublicationCutoff) != query.PublicationCutoff || uint64(*row.PublicationSourceCutoff) != query.PublicationSourceCutoff || row.BatchID != query.BatchID || row.Offset != query.Offset {
			t.Fatal("listed cutoff, batch or offset differs from signed window query")
		}
	}
	if windows["OCM.fbs"] != 4 || windows["CAT.fbs"] != 4 || ordinary != 1 {
		t.Fatalf("window coverage: products=%d indexes=%d ordinary=%d", windows["OCM.fbs"], windows["CAT.fbs"], ordinary)
	}
}
