package main

// ipfsAccountEPMBlockstore binds internal/api's AccountEPMBlockstore port to
// the Kubo RPC API this node already runs.
//
// THE BOUNDARY THIS FILE KEEPS, exactly as profile_photo_store.go keeps it for
// pictures: internal/api gains no knowledge of Kubo, and internal/node's
// blockstore connector gains no knowledge of accounts. Bytes in, content
// identifier out, through the same block/put + pin call the node's own $EPM
// takes.

import (
	"context"

	"github.com/spacedatanetwork/sdn-server/internal/node"
)

type ipfsAccountEPMBlockstore struct {
	apiURL string
}

// PutPinnedRawBlock stores bytes as a pinned CIDv1 raw block (sha2-256) — the
// same codec and hash the record store's own CID uses, so the two agree.
func (s ipfsAccountEPMBlockstore) PutPinnedRawBlock(ctx context.Context, data []byte) (string, error) {
	return node.PutPinnedRawBlock(ctx, s.apiURL, data)
}
