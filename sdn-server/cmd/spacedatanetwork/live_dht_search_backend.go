package main

import (
	"context"
	"fmt"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/api"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
)

type liveDHTSearchNode interface {
	Store() *storage.FlatSQLStore
	DiscoverSDNAdvertisementPeers(context.Context) (int, error)
}

type liveDHTSearchBackend struct {
	node liveDHTSearchNode
}

func newLiveDHTSearchBackend(node liveDHTSearchNode) api.LiveSearchBackend {
	return &liveDHTSearchBackend{node: node}
}

func (b *liveDHTSearchBackend) SearchProviders(ctx context.Context, req api.SearchRequest) ([]map[string]interface{}, error) {
	store, err := b.refreshSearchIndex(ctx)
	if err != nil {
		return nil, err
	}
	return api.SearchProviderRows(store, req)
}

func (b *liveDHTSearchBackend) SearchData(ctx context.Context, req api.SearchRequest) ([]map[string]interface{}, error) {
	store, err := b.refreshSearchIndex(ctx)
	if err != nil {
		return nil, err
	}
	return api.SearchDataRows(store, req)
}

func (b *liveDHTSearchBackend) refreshSearchIndex(ctx context.Context) (*storage.FlatSQLStore, error) {
	if b == nil || b.node == nil {
		return nil, fmt.Errorf("live DHT node is unavailable")
	}
	store := b.node.Store()
	if store == nil {
		return nil, fmt.Errorf("live DHT storage is unavailable")
	}
	discoveryCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	if _, err := b.node.DiscoverSDNAdvertisementPeers(discoveryCtx); err != nil {
		return nil, fmt.Errorf("live DHT discovery: %w", err)
	}
	return store, nil
}
