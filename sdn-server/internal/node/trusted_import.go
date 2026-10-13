package node

import (
	"strings"
	"sync"

	"github.com/libp2p/go-libp2p/core/peer"

	"github.com/spacedatanetwork/sdn-server/internal/config"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
)

// TRUSTED-PEER IMPORT, SCOPED BY LANE (multi-body round 2, coordinator D2,
// 2026-10-13).
//
// A node used to materialize every dataset publication of every schema from
// every trusted peer. host-02 trusts host-01, so the moment host-01 starts
// publishing the beyond-Earth retrieval lanes (895,910 asteroid $SBD small-body records
// every 72 hours, the NAIF kernel and body-imagery $PNM files), host-02 (7 GB
// of RAM, its disk 94% full) would import all of them on every cycle.
//
// The rule now, per lane (schema, provider, source) and peer:
//   - peers.trusted_import.deny names it          -> skipped, always;
//   - this node already holds a publication of it, or records of it (a lane
//     received only as gossiped PNMs has no publication row) -> imported (the
//     lanes the fleet syncs today keep syncing, with no config change);
//   - peers.trusted_import.allow names it         -> imported;
//   - otherwise (a NEW lane)                      -> skipped, unless
//     peers.trusted_import.new_lanes is "allow".
// The rule is asked on every import path: the shard-publication catch-up and
// the feed head (before anything is fetched), and the PNM replay that the
// gossiped PNM, the TipQueue tip and the stored-PNM catch-up all share (after
// the signed DPM names the lane, before its shard or index is fetched). Each
// skipped lane is logged once per process.

// trustedImportDecision is why a lane is or is not imported from a peer.
type trustedImportDecision string

const (
	trustedImportDenied   trustedImportDecision = "denied by peers.trusted_import.deny"
	trustedImportHeld     trustedImportDecision = "held here"
	trustedImportAllowed  trustedImportDecision = "listed in peers.trusted_import.allow"
	trustedImportNewLanes trustedImportDecision = "peers.trusted_import.new_lanes is allow"
	trustedImportNew      trustedImportDecision = "a new lane, not listed in peers.trusted_import.allow"
)

// trustedImportLanePolicy decides one lane from one peer. held reports
// whether this node already holds a publication of the lane; it is asked
// only when the configuration does not decide alone.
func trustedImportLanePolicy(cfg config.TrustedImportConfig, from, schema, providerID, sourceName string, held func() bool) (bool, trustedImportDecision) {
	if config.AnyLaneMatches(cfg.Deny, from, schema, providerID, sourceName) {
		return false, trustedImportDenied
	}
	if config.AnyLaneMatches(cfg.Allow, from, schema, providerID, sourceName) {
		return true, trustedImportAllowed
	}
	if held != nil && held() {
		return true, trustedImportHeld
	}
	if cfg.NewLanesAllowed() {
		return true, trustedImportNewLanes
	}
	return false, trustedImportNew
}

// trustedImportSkipLog remembers the lanes already reported as skipped.
var trustedImportSkipLog sync.Map

// trustedPeerLaneImportAllowed applies the lane policy to a publication a
// trusted peer offers. A node without a config or store keeps the old rule.
func (n *Node) trustedPeerLaneImportAllowed(from peer.ID, schema, providerID, sourceName, queryProfile string) bool {
	if n == nil || n.config == nil {
		return true
	}
	schema, providerID, sourceName = strings.TrimSpace(schema), strings.TrimSpace(providerID), strings.TrimSpace(sourceName)
	held := func() bool {
		if n.store == nil || schema == "" || providerID == "" || sourceName == "" {
			return false
		}
		if profile := strings.TrimSpace(queryProfile); profile != "" {
			pubs, err := n.store.ListDatasetShardPublications(storage.DatasetShardPublicationQuery{
				SchemaName:   schema,
				ProviderID:   providerID,
				SourceName:   sourceName,
				QueryProfile: profile,
			})
			if err == nil && len(pubs) > 0 {
				return true
			}
		}
		// The PNM replay path writes no shard-publication row, so a lane this
		// node has only ever received that way is held when its records are:
		// the per-lane source summary (tens of rows) answers that.
		progress, err := n.store.SourceBatchProgress()
		if err != nil {
			return false
		}
		for _, p := range progress {
			if p.Count > 0 && strings.EqualFold(p.SchemaName, schema) && strings.EqualFold(p.ProviderID, providerID) && strings.EqualFold(p.SourceName, sourceName) {
				return true
			}
		}
		return false
	}
	ok, why := trustedImportLanePolicy(n.config.Peers.TrustedImport, from.String(), schema, providerID, sourceName, held)
	if !ok {
		key := from.String() + "|" + schema + "|" + providerID + "|" + sourceName
		if _, seen := trustedImportSkipLog.LoadOrStore(key, true); !seen {
			log.Infof("Not importing %s %s/%s from trusted peer %s: %s", schema, providerID, sourceName, from.ShortString(), why)
		}
	}
	return ok
}
