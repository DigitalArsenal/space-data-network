package node

import (
	"sort"
	"strings"

	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/spacedatanetwork/sdn-server/internal/config"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
)

// Discovery uses a conservative schema filter. The complete import policy
// still checks each publication's provider and source before fetching bytes.
func mayDiscoverTrustedSchema(cfg config.TrustedImportConfig, from, schema string, held bool) bool {
	if config.AnyLaneMatches(cfg.Deny, from, schema, "", "") {
		return false
	}
	if cfg.NewLanesAllowed() || held {
		return true
	}
	for _, selector := range cfg.Allow {
		selector.ProviderID, selector.SourceName = "", ""
		if selector.Matches(from, schema, "", "") {
			return true
		}
	}
	return false
}

func (n *Node) datasetCatchupSchemas(from peer.ID) ([]string, error) {
	schemas := n.validator.Schemas()
	if n.config == nil {
		return n.datasetCatchupOrder(from, schemas), nil
	}
	held := map[string]bool{}
	policy := n.config.Peers.TrustedImport
	needsHeld := false
	for _, schema := range schemas {
		if !mayDiscoverTrustedSchema(policy, from.String(), schema, false) && mayDiscoverTrustedSchema(policy, from.String(), schema, true) {
			needsHeld = true
			break
		}
	}
	// Read local state only when holding a schema can change discovery policy.
	// Scoped discovery reports read errors rather than silently losing held data.
	if needsHeld {
		ledger := n.trustedImportLedger()
		ledger.mu.Lock()
		for _, entry := range ledger.lanes {
			held[strings.ToLower(entry.Schema)] = true
		}
		ledger.mu.Unlock()
		progress, err := n.store.SourceBatchProgress()
		if err != nil {
			return nil, err
		}
		for _, entry := range progress {
			if entry.Count > 0 {
				held[strings.ToLower(entry.SchemaName)] = true
			}
		}
		publications, err := n.store.ListDatasetShardPublicationsForProfile(storage.DatasetPublicationQueryProfile, "")
		if err != nil {
			return nil, err
		}
		for _, entry := range publications {
			held[strings.ToLower(entry.SchemaName)] = true
		}
	}
	candidates := make([]string, 0, len(schemas))
	for _, schema := range schemas {
		if mayDiscoverTrustedSchema(n.config.Peers.TrustedImport, from.String(), schema, held[strings.ToLower(schema)]) {
			candidates = append(candidates, schema)
		}
	}
	return n.datasetCatchupOrder(from, candidates), nil
}

// datasetCatchupOrder is the order one catch-up pass lists a peer's schemas
// in: by name, starting at the schema the peer's last pass was refused at
// (rate_limited) and wrapping round. A peer's listing budget then covers
// every schema in consecutive passes; the map order the validator answers in
// is not random enough for that (a pass could miss the same schema again).
func (n *Node) datasetCatchupOrder(from peer.ID, schemas []string) []string {
	sort.Strings(schemas)
	n.datasetCatchupResumeMu.Lock()
	resume := n.datasetCatchupResume[from]
	n.datasetCatchupResumeMu.Unlock()
	if resume == "" {
		return schemas
	}
	at := sort.SearchStrings(schemas, resume)
	return append(append(make([]string, 0, len(schemas)), schemas[at:]...), schemas[:at]...)
}

// noteDatasetCatchupRefused records the schema a peer's catch-up pass was
// refused at; "" clears it.
func (n *Node) noteDatasetCatchupRefused(from peer.ID, schema string) {
	n.datasetCatchupResumeMu.Lock()
	defer n.datasetCatchupResumeMu.Unlock()
	if schema == "" {
		delete(n.datasetCatchupResume, from)
		return
	}
	if n.datasetCatchupResume == nil {
		n.datasetCatchupResume = map[peer.ID]string{}
	}
	n.datasetCatchupResume[from] = schema
}
