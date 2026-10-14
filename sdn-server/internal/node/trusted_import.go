package node

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

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
//
// "Held here" survives a store wipe: every lane the store holds, and every
// lane imported, is written to the lane ledger <storage.path>/
// trusted-import-lanes.json, which `spacedatanetwork store-wipe` keeps (it is
// not a record layer). A wiped or rebuilt store therefore re-imports the
// lanes it synced before (host-01 materializes the satellite catalog lane
// space-data-network-02/celestrak-gp from host-02), and still imports no lane
// it never held. The ledger is seeded from the store at the first decision a
// process makes, so a node that ran this build once before a wipe keeps
// its lanes.

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
	ledger := n.trustedImportLedger()
	held := func() bool {
		if schema == "" || providerID == "" || sourceName == "" {
			return false
		}
		if ledger.holds(schema, providerID, sourceName) {
			return true
		}
		if n.store == nil {
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
	if ok && schema != "" && providerID != "" && sourceName != "" {
		ledger.record(schema, providerID, sourceName)
	}
	if !ok {
		key := from.String() + "|" + schema + "|" + providerID + "|" + sourceName
		if _, seen := trustedImportSkipLog.LoadOrStore(key, true); !seen {
			log.Infof("Not importing %s %s/%s from trusted peer %s: %s", schema, providerID, sourceName, from.ShortString(), why)
		}
	}
	return ok
}

// trustedImportLaneLedgerFile is the lane ledger's name under storage.path.
// store-wipe keeps it (cmd/spacedatanetwork/store_wipe.go).
const trustedImportLaneLedgerFile = "trusted-import-lanes.json"

// trustedImportLane is one ledger row.
type trustedImportLane struct {
	Schema     string `json:"schema"`
	ProviderID string `json:"provider_id"`
	SourceName string `json:"source_name"`
	Since      string `json:"since"`
}

// trustedImportLedger is the set of lanes this node has held, kept on disk
// so a store wipe does not turn the lanes it synced into new lanes.
type trustedImportLedger struct {
	mu    sync.Mutex
	path  string
	lanes map[string]trustedImportLane
}

func trustedImportLaneKey(schema, providerID, sourceName string) string {
	return strings.ToLower(schema) + "\x00" + strings.ToLower(providerID) + "\x00" + strings.ToLower(sourceName)
}

var trustedImportLedgers sync.Map // storage path -> *trustedImportLedger

// trustedImportLedger returns this node's ledger, loading it and seeding it
// from the store's lanes the first time a process asks.
func (n *Node) trustedImportLedger() *trustedImportLedger {
	base := ""
	if n.config != nil {
		base = strings.TrimSpace(n.config.Storage.Path)
	}
	if base == "" {
		return &trustedImportLedger{lanes: map[string]trustedImportLane{}}
	}
	path := filepath.Join(base, trustedImportLaneLedgerFile)
	if v, ok := trustedImportLedgers.Load(path); ok {
		return v.(*trustedImportLedger)
	}
	l := loadTrustedImportLedger(path)
	if n.store != nil {
		if progress, err := n.store.SourceBatchProgress(); err == nil {
			for _, p := range progress {
				if p.Count > 0 {
					l.add(p.SchemaName, p.ProviderID, p.SourceName)
				}
			}
			l.save()
		}
	}
	v, _ := trustedImportLedgers.LoadOrStore(path, l)
	return v.(*trustedImportLedger)
}

func loadTrustedImportLedger(path string) *trustedImportLedger {
	l := &trustedImportLedger{path: path, lanes: map[string]trustedImportLane{}}
	data, err := os.ReadFile(path)
	if err != nil {
		return l
	}
	var rows []trustedImportLane
	if err := json.Unmarshal(data, &rows); err != nil {
		log.Warnf("trusted import lane ledger %s is unreadable (%v); starting it again from the store", path, err)
		return l
	}
	for _, r := range rows {
		l.lanes[trustedImportLaneKey(r.Schema, r.ProviderID, r.SourceName)] = r
	}
	return l
}

func (l *trustedImportLedger) holds(schema, providerID, sourceName string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	_, ok := l.lanes[trustedImportLaneKey(schema, providerID, sourceName)]
	return ok
}

// add records a lane in memory; it reports whether the lane is new.
func (l *trustedImportLedger) add(schema, providerID, sourceName string) bool {
	schema, providerID, sourceName = strings.TrimSpace(schema), strings.TrimSpace(providerID), strings.TrimSpace(sourceName)
	if schema == "" || providerID == "" || sourceName == "" {
		return false
	}
	key := trustedImportLaneKey(schema, providerID, sourceName)
	l.mu.Lock()
	defer l.mu.Unlock()
	if _, ok := l.lanes[key]; ok {
		return false
	}
	l.lanes[key] = trustedImportLane{Schema: schema, ProviderID: providerID, SourceName: sourceName, Since: time.Now().UTC().Format(time.RFC3339)}
	return true
}

func (l *trustedImportLedger) record(schema, providerID, sourceName string) {
	if l.add(schema, providerID, sourceName) {
		l.save()
	}
}

// save writes the ledger atomically (a temporary file renamed over it).
func (l *trustedImportLedger) save() {
	if l.path == "" {
		return
	}
	l.mu.Lock()
	rows := make([]trustedImportLane, 0, len(l.lanes))
	for _, r := range l.lanes {
		rows = append(rows, r)
	}
	l.mu.Unlock()
	sort.Slice(rows, func(i, j int) bool {
		return trustedImportLaneKey(rows[i].Schema, rows[i].ProviderID, rows[i].SourceName) < trustedImportLaneKey(rows[j].Schema, rows[j].ProviderID, rows[j].SourceName)
	})
	data, err := json.MarshalIndent(rows, "", " ")
	if err != nil {
		return
	}
	tmp := l.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		log.Warnf("trusted import lane ledger: %v", err)
		return
	}
	if err := os.Rename(tmp, l.path); err != nil {
		log.Warnf("trusted import lane ledger: %v", err)
	}
}
