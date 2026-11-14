package api

import (
	"context"
	"encoding/json"
	"github.com/spacedatanetwork/sdn-server/internal/config"
	"os"
	"path/filepath"
	"testing"
	"time"
)

type catalogNoopPublication struct {
	*ConcreteDatasetPublicationService
}

func (catalogNoopPublication) PublishDatasetUpdate(context.Context, DatasetPublicationRequest) (*DatasetPublicationResult, error) {
	panic("publication is forbidden in the scheduling gate")
}

// Exercise scheduling only: no publisher, network request, signature, or key.
func TestCatalogPublicationContract(t *testing.T) {
	dir := t.TempDir()
	store := pairedStore(t, filepath.Join(dir, "store"))
	pairedCommit(t, store, "changed-one", 900001)
	pairedCommit(t, store, "changed-two", 900002)
	service := catalogNoopPublication{&ConcreteDatasetPublicationService{store: store}}

	batch := config.AutoPublishLane{PairedPublication: true, Schema: "CAT.fbs", ProviderID: "synthetic-node", SourceName: "catalog-index", PublishScope: "batch", MinInterval: 5 * time.Minute}
	snapshot := batch
	snapshot.PublishScope = "source"
	snapshot.MinInterval = 24 * time.Hour
	p := NewAutoPublisher(service, []config.AutoPublishLane{batch, snapshot})
	p.seedSourcePublicationsLocked()
	one := IngestedBatch{Schema: "CAT.fbs", ProviderID: "synthetic-node", SourceName: "catalog-index", BatchID: "changed-one", Inserted: 1}
	two := one
	two.BatchID = "changed-two"
	p.ObserveIngest(one)
	p.ObserveIngest(two)
	reversed := NewAutoPublisher(service, []config.AutoPublishLane{snapshot, batch})
	reversed.seedSourcePublicationsLocked()
	reversed.ObserveIngest(one)
	reversed.ObserveIngest(two)
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store = pairedStore(t, filepath.Join(dir, "store"))
	defer store.Close()
	service = catalogNoopPublication{&ConcreteDatasetPublicationService{store: store}}
	restarted := NewAutoPublisher(service, []config.AutoPublishLane{batch, snapshot})
	restarted.seedSourcePublicationsLocked()
	restarted.recoverPairedBatches(context.Background())
	evidence := map[string]int{"changedBatches": 2, "batchFirstQueued": len(p.queue), "batchFirstSnapshotSources": len(p.sources), "sourceFirstQueued": len(reversed.queue), "sourceFirstSnapshotSources": len(reversed.sources), "restartedBatchQueue": len(restarted.queue)}
	raw, _ := json.MarshalIndent(evidence, "", "  ")
	if root := os.Getenv("CATALOG_DIAGNOSTICS"); root != "" {
		os.WriteFile(filepath.Join(root, "session5-publication-gate.json"), raw, 0600)
	}
	if len(p.sources) != 1 {
		t.Errorf("batch-first configuration did not arm the requested daily full-source snapshot")
	}
	if len(p.queue) != 2 {
		t.Errorf("two changed batches yielded %d pending batch publications", len(p.queue))
	}
	if len(reversed.queue) != 2 {
		t.Errorf("source-first configuration yielded %d pending changed-batch publications", len(reversed.queue))
	}
	if len(restarted.queue) != 2 {
		t.Errorf("new publisher has %d pending changed batches; startup seeds source snapshots only", len(restarted.queue))
	}
}
