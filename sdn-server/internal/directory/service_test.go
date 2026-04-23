package directory

import (
	"os"
	"testing"

	"github.com/spacedatanetwork/sdn-server/internal/sds"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
)

func mustNewDirectoryStore(t *testing.T) *storage.FlatSQLStore {
	t.Helper()

	tmpDir, err := os.MkdirTemp("", "directory-test-*")
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

	store, err := storage.NewFlatSQLStore(tmpDir, validator)
	if err != nil {
		t.Fatalf("NewFlatSQLStore failed: %v", err)
	}
	t.Cleanup(func() {
		_ = store.Close()
	})

	return store
}

func TestDirectoryService_IndexesNodeEPMJSON(t *testing.T) {
	store := mustNewDirectoryStore(t)
	svc := NewService(store)

	info := map[string]any{
		"peer_id":         "16Uiu2HAmExample",
		"dn":              "SDN Node Example",
		"legal_name":      "Space Data Node Example LLC",
		"bitcoin_address": "bc1qexample",
	}

	if err := svc.UpsertNodeEPMJSON(info, "bafyexample", "local"); err != nil {
		t.Fatalf("UpsertNodeEPMJSON failed: %v", err)
	}

	nodes, err := svc.SearchNodes("bc1qexample")
	if err != nil {
		t.Fatalf("SearchNodes failed: %v", err)
	}
	if len(nodes) != 1 {
		t.Fatalf("SearchNodes returned %d records, want 1", len(nodes))
	}

	got := nodes[0]
	if got.Kind != "node" {
		t.Fatalf("Kind = %q, want %q", got.Kind, "node")
	}
	if got.PeerID != "16Uiu2HAmExample" {
		t.Fatalf("PeerID = %q, want %q", got.PeerID, "16Uiu2HAmExample")
	}
	if got.BitcoinAddress != "bc1qexample" {
		t.Fatalf("BitcoinAddress = %q, want %q", got.BitcoinAddress, "bc1qexample")
	}
}
