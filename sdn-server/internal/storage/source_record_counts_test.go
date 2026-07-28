package storage

import (
	"path/filepath"
	"testing"

	"github.com/spacedatanetwork/sdn-server/internal/sds"
)

// SourceRecordCounts is the evidence a retrieval-ledger reconciliation acts on,
// so it has to answer the two questions honestly: a source with records reports
// them under the ledger's own key, and a source with none is simply absent.
func TestSourceRecordCountsAnswersPerProvenancePair(t *testing.T) {
	validator, err := sds.NewValidator(nil)
	if err != nil {
		t.Fatalf("NewValidator: %v", err)
	}
	store, err := NewFlatSQLStore(filepath.Join(t.TempDir(), "store"), validator)
	if err != nil {
		t.Fatalf("NewFlatSQLStore: %v", err)
	}
	defer store.Close()

	// An empty store claims nothing for anyone.
	counts, err := store.SourceRecordCounts()
	if err != nil {
		t.Fatalf("SourceRecordCounts on an empty store: %v", err)
	}
	if len(counts) != 0 {
		t.Fatalf("empty store reported %d source(s): %+v", len(counts), counts)
	}

	tags := SourceTags{
		ProviderID: "space-data-network-02",
		SourceName: "celestrak-gp",
		SourceURL:  "https://celestrak.org/NORAD/elements/gp.php",
		BatchID:    "batch-gp",
	}
	records := [][]byte{
		sds.NewOMMBuilder().
			WithNoradCatID(25544).
			WithObjectID("1998-067A").
			WithEpoch("2024-01-16T11:51:22Z").
			Build(),
		sds.NewOMMBuilder().
			WithNoradCatID(1).
			WithObjectID("1957-001A").
			WithEpoch("1959-01-11T01:49:23Z").
			Build(),
	}
	if _, err := store.StoreBatchWithSourceTags("OMM.fbs", records, "peer-self", nil, tags); err != nil {
		t.Fatalf("StoreBatchWithSourceTags: %v", err)
	}

	counts, err = store.SourceRecordCounts()
	if err != nil {
		t.Fatalf("SourceRecordCounts: %v", err)
	}
	// The key is the ledger's own "<provider_id>/<source_name>" pair, so the two
	// databases join without inventing a second identity space.
	if got := counts["space-data-network-02/celestrak-gp"]; got != 2 {
		t.Fatalf("counts[space-data-network-02/celestrak-gp] = %d, want 2 (all: %+v)", got, counts)
	}
	// A source the store has never held must be ABSENT, not zero-valued by
	// accident of some other row.
	if _, ok := counts["space-data-network-02/celestrak-satcat-csv"]; ok {
		t.Fatalf("a source with no records was reported: %+v", counts)
	}
}
