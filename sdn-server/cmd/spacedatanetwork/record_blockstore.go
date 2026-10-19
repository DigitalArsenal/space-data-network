package main

// ipfsRecordBlockstore binds internal/api's blockstore ports (the account EPM
// lane's AccountEPMBlockstore and the publish route's RecordBlockstore) to the
// node pin policy and the Kubo RPC API this node already runs.
//
// THE BOUNDARY THIS FILE KEEPS: internal/api gains no knowledge of Kubo, and
// the pin policy gains no knowledge of accounts or records. Bytes in, content
// identifier out; an announcement through Kubo's own provide call.

import (
	"context"
	"strings"

	"github.com/spacedatanetwork/sdn-server/internal/api"
	"github.com/spacedatanetwork/sdn-server/internal/config"
	"github.com/spacedatanetwork/sdn-server/internal/node"
	"github.com/spacedatanetwork/sdn-server/internal/pinpolicy"
)

type ipfsRecordBlockstore struct {
	apiURL string
	pins   *pinpolicy.Policy
}

// PutRecordBlock stores a published record as a CIDv1 raw block (sha2-256,
// the record store's own CID) under SHARE, feed = its standard.
func (s ipfsRecordBlockstore) PutRecordBlock(ctx context.Context, schema string, data []byte) (string, bool, error) {
	scope := s.pins.Scope(pinpolicy.Share, pinpolicy.StandardFeed(schema))
	cid, err := scope.PutBlock(ctx, data)
	if err != nil {
		return "", false, err
	}
	return cid, scope.Allowed(), nil
}

// ProvideOnce announces a block the blockstore holds to the routing system.
func (s ipfsRecordBlockstore) ProvideOnce(ctx context.Context, cid string) error {
	return node.ProvideOnce(ctx, s.apiURL, cid)
}

// recordBlockstore is the publish route's blockstore: the node's own Kubo, or
// nil when no Kubo RPC API is configured (records then stay in the store).
func recordBlockstore(cfg *config.Config, pins *pinpolicy.Policy) api.RecordBlockstore {
	if cfg == nil {
		return nil
	}
	apiURL := strings.TrimSpace(cfg.Admin.IPFSAPIURL)
	if apiURL == "" {
		return nil
	}
	return ipfsRecordBlockstore{apiURL: apiURL, pins: pins}
}
