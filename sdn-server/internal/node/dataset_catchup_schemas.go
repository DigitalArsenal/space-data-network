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
	sort.Strings(schemas)
	if n.config == nil {
		return schemas, nil
	}
	held := map[string]bool{}
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
	candidates := make([]string, 0, len(schemas))
	for _, schema := range schemas {
		if mayDiscoverTrustedSchema(n.config.Peers.TrustedImport, from.String(), schema, held[strings.ToLower(schema)]) {
			candidates = append(candidates, schema)
		}
	}
	return candidates, nil
}
