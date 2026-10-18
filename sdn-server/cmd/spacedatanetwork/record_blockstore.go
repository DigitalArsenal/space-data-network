package main

// ipfsRecordBlockstore binds internal/api's blockstore ports (the account EPM
// lane's AccountEPMBlockstore and the publish route's RecordBlockstore) to the
// Kubo RPC API this node already runs.
//
// THE BOUNDARY THIS FILE KEEPS, exactly as profile_photo_store.go keeps it for
// pictures: internal/api gains no knowledge of Kubo, and internal/node's
// blockstore connector gains no knowledge of accounts or records. Bytes in,
// content identifier out, through the same block/put + pin call the node's own
// $EPM takes; an announcement through Kubo's own provide call.

import (
	"context"
	"strings"

	"github.com/spacedatanetwork/sdn-server/internal/api"
	"github.com/spacedatanetwork/sdn-server/internal/config"
	"github.com/spacedatanetwork/sdn-server/internal/node"
)

type ipfsRecordBlockstore struct {
	apiURL string
}

// PutPinnedRawBlock stores bytes as a pinned CIDv1 raw block (sha2-256) — the
// same codec and hash the record store's own CID uses, so the two agree.
func (s ipfsRecordBlockstore) PutPinnedRawBlock(ctx context.Context, data []byte) (string, error) {
	return node.PutPinnedRawBlock(ctx, s.apiURL, data)
}

// ProvideOnce announces a block the blockstore holds to the routing system.
func (s ipfsRecordBlockstore) ProvideOnce(ctx context.Context, cid string) error {
	return node.ProvideOnce(ctx, s.apiURL, cid)
}

// recordBlockstore is the publish route's blockstore: the node's own Kubo, or
// nil when no Kubo RPC API is configured (records then stay in the store).
func recordBlockstore(cfg *config.Config) api.RecordBlockstore {
	if cfg == nil {
		return nil
	}
	apiURL := strings.TrimSpace(cfg.Admin.IPFSAPIURL)
	if apiURL == "" {
		return nil
	}
	return ipfsRecordBlockstore{apiURL: apiURL}
}
