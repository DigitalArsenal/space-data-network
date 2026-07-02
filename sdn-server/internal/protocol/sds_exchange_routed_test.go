package protocol

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/spacedatanetwork/sdn-server/internal/sds"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
)

// WS7.3c: the sds-exchange wire read paths (SDS_GET by (schema, CID) and
// SDS_QUERY all-for-schema) are backed by store.Get and store.QueryAllBounded,
// which span the (producer, standard) tables — a record that exists ONLY in a
// producer table is still served to peers. The wire format has no producer
// field, so this internal fan-out is what keeps cross-node sync working after
// the write flip.
func TestWireReadSurfacesServeRoutedOnlyRecords(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "sdn-protocol-routed-*")
	if err != nil {
		t.Fatalf("MkdirTemp failed: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	validator, err := sds.NewValidator(nil)
	if err != nil {
		t.Fatalf("NewValidator failed: %v", err)
	}
	store, err := storage.NewFlatSQLStore(filepath.Join(tmpDir, "db"), validator)
	if err != nil {
		t.Fatalf("NewFlatSQLStore failed: %v", err)
	}
	defer store.Close()

	data := sds.NewOMMBuilder().WithNoradCatID(31337).WithObjectName("ROUTED-WIRE").Build()
	cid, err := store.StoreRoutedByProducer("OMM.fbs", data, "12D3KooWRemoteProducer", nil)
	if err != nil {
		t.Fatalf("StoreRoutedByProducer failed: %v", err)
	}

	// SDS_GET path: Get(schema, cid) must fan out to producer tables.
	served, err := store.Get("OMM.fbs", cid)
	if err != nil {
		t.Fatalf("Get(routed-only) failed: %v", err)
	}
	if !bytes.Equal(served, data) {
		t.Fatalf("Get returned %d bytes, want the original %d", len(served), len(data))
	}

	// SDS_QUERY path: QueryAllBounded must include routed-only records.
	results, err := store.QueryAllBounded("OMM.fbs", DefaultQueryRecordLimit, DefaultQueryResponseMaxBytes)
	if err != nil {
		t.Fatalf("QueryAllBounded failed: %v", err)
	}
	found := false
	for _, r := range results {
		if bytes.Equal(r, data) {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("QueryAllBounded (%d results) did not include the routed-only record", len(results))
	}
}
