package main

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/spacedatanetwork/sdn-server/internal/api"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
)

func TestDaemonSearchAPIWiresLiveDHTBackend(t *testing.T) {
	source, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatalf("read main.go: %v", err)
	}
	mainSource := string(source)
	if !strings.Contains(mainSource, "api.NewSearchHandlerWithOptions(n.Store(), api.SearchHandlerOptions{") {
		t.Fatalf("daemon must use NewSearchHandlerWithOptions for live-DHT search wiring")
	}
	if !strings.Contains(mainSource, "LiveBackend: newLiveDHTSearchBackend(n)") {
		t.Fatalf("daemon search API must wire newLiveDHTSearchBackend(n)")
	}
}

func TestLiveDHTSearchBackendRefreshesDiscoveryBeforeProviderRows(t *testing.T) {
	_, store := newSyncCLITestStore(t)
	seedSyncCLITestData(t, store)
	node := &fakeLiveDHTSearchNode{store: store}
	backend := newLiveDHTSearchBackend(node)

	rows, err := backend.SearchProviders(context.Background(), api.SearchRequest{
		Query:      "catalogfixture",
		Schema:     "OMM",
		ProviderID: "space-data-network-02",
		Limit:      5,
	})
	if err != nil {
		t.Fatalf("SearchProviders failed: %v", err)
	}
	if node.discoveryCalls != 1 {
		t.Fatalf("discovery calls = %d, want 1", node.discoveryCalls)
	}
	if len(rows) != 1 {
		t.Fatalf("provider row count = %d, want 1: %#v", len(rows), rows)
	}
	row := rows[0]
	if row["peer_id"] != "16Uiu2HCatalogFixture" ||
		row["provider_id"] != "space-data-network-02" ||
		row["schema_name"] != "OMM.fbs" {
		t.Fatalf("unexpected provider row: %#v", row)
	}
}

func TestLiveDHTSearchBackendReturnsDiscoveryError(t *testing.T) {
	_, store := newSyncCLITestStore(t)
	node := &fakeLiveDHTSearchNode{
		store:        store,
		discoveryErr: errors.New("dht routing unavailable"),
	}
	backend := newLiveDHTSearchBackend(node)

	_, err := backend.SearchData(context.Background(), api.SearchRequest{Schema: "OMM"})
	if err == nil || err.Error() != "live DHT discovery: dht routing unavailable" {
		t.Fatalf("SearchData error = %v", err)
	}
	if node.discoveryCalls != 1 {
		t.Fatalf("discovery calls = %d, want 1", node.discoveryCalls)
	}
}

type fakeLiveDHTSearchNode struct {
	store          *storage.FlatSQLStore
	discoveryErr   error
	discoveryCalls int
}

func (f *fakeLiveDHTSearchNode) Store() *storage.FlatSQLStore {
	return f.store
}

func (f *fakeLiveDHTSearchNode) DiscoverSDNAdvertisementPeers(ctx context.Context) (int, error) {
	f.discoveryCalls++
	return 0, f.discoveryErr
}
