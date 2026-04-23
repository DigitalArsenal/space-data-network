package storage

import (
	"os"
	"testing"

	"github.com/spacedatanetwork/sdn-server/internal/sds"
)

func mustNewFlatSQLStore(t *testing.T) *FlatSQLStore {
	t.Helper()

	tmpDir, err := os.MkdirTemp("", "flatsql-directory-test-*")
	if err != nil {
		t.Fatalf("MkdirTemp failed: %v", err)
	}
	t.Cleanup(func() {
		_ = os.RemoveAll(tmpDir)
	})

	validator, err := sds.NewValidator(nil)
	if err != nil {
		t.Fatalf("NewValidator failed: %v", err)
	}

	store, err := NewFlatSQLStore(tmpDir, validator)
	if err != nil {
		t.Fatalf("NewFlatSQLStore failed: %v", err)
	}
	t.Cleanup(func() {
		_ = store.Close()
	})

	return store
}

func TestFlatSQLStore_UpsertAndQueryDirectoryRecord(t *testing.T) {
	store := mustNewFlatSQLStore(t)

	record := DirectoryRecord{
		Kind:           "node",
		PeerID:         "16Uiu2HAmExample",
		DN:             "SDN Node Example",
		BitcoinAddress: "bc1qexample",
		EPMCID:         "bafyexample",
		Source:         "local",
		EPMJSON:        `{"bitcoin_address":"bc1qexample","dn":"SDN Node Example","peer_id":"16Uiu2HAmExample"}`,
		UpdatedAt:      1700000000,
	}

	if err := store.UpsertDirectoryRecord(record); err != nil {
		t.Fatalf("UpsertDirectoryRecord failed: %v", err)
	}

	updated := record
	updated.DN = "SDN Node Example v2"
	updated.BitcoinAddress = "bc1qupdated"
	updated.EPMCID = "bafyupdated"
	updated.Source = "remote"
	updated.EPMJSON = `{"bitcoin_address":"bc1qupdated","dn":"SDN Node Example v2","peer_id":"16Uiu2HAmExample"}`
	updated.UpdatedAt = 1700000100

	if err := store.UpsertDirectoryRecord(updated); err != nil {
		t.Fatalf("second UpsertDirectoryRecord failed: %v", err)
	}

	results, err := store.QueryDirectory(DirectoryQuery{
		Kind:   "node",
		Search: "bc1qupdated",
	})
	if err != nil {
		t.Fatalf("QueryDirectory failed: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("QueryDirectory returned %d records, want 1", len(results))
	}
	got := results[0]
	if got.PeerID != updated.PeerID || got.EPMCID != updated.EPMCID || got.Source != updated.Source {
		t.Fatalf("unexpected record: %+v", got)
	}
	if got.DN != updated.DN {
		t.Fatalf("DN = %q, want %q", got.DN, updated.DN)
	}
	if got.BitcoinAddress != updated.BitcoinAddress {
		t.Fatalf("BitcoinAddress = %q, want %q", got.BitcoinAddress, updated.BitcoinAddress)
	}
	if got.UpdatedAt != updated.UpdatedAt {
		t.Fatalf("UpdatedAt = %d, want %d", got.UpdatedAt, updated.UpdatedAt)
	}
}
