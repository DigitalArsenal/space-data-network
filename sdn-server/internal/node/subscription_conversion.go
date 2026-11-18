package node

// THE UPGRADE FROM THE REMOVED IMPORT GATE (1.2.0).
//
// Up to 1.1.9 a node imported a trusted peer's dataset publications lane by
// lane under peers.trusted_import (an allow list, a deny list and new_lanes)
// and a lane ledger, <storage.path>/trusted-import-lanes.json, holding every
// lane it had held. From 1.2.0 a node imports only the lanes it subscribes
// to. The production hosts update themselves from the update signal with
// nobody logged in, so a node started on a 1.1.9 data folder converts what it
// imported into subscriptions on its first start:
//
//   - the lanes are the ledger's rows and the allow list's selectors that
//     name a lane in full (standard, provider and source);
//   - each lane's producer is the peer the store names as the producer of
//     its records, when this node trusts that peer and it is not this node;
//     an allow selector that names a peer gives that peer. A lane this node
//     produces is not converted; a lane with no known producer is reported
//     (log and the alert subscription_conversion), not guessed;
//   - every converted lane is a subscription that starts at the producer's
//     current batch, whatever its rule: the old path imported each
//     publication as it came, never the history before it, so nothing older
//     than the current batch is read, and records already held are tagged to
//     the batch, not sent again (follow.go). Its bound (the batch's records
//     and bytes) is logged when the producer's statement arrives;
//   - then the ledger is renamed trusted-import-lanes.json.converted and
//     trusted-import-converted (plain text, one line per lane) records the
//     conversion. It runs once.
//
// A later start with peers.trusted_import still in the config logs that the
// key is ignored and raises subscription_conversion. A new node (an empty
// storage path) whose config carries the key refuses to start.

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"

	"github.com/spacedatanetwork/sdn-server/internal/channels"
	"github.com/spacedatanetwork/sdn-server/internal/config"
	"github.com/spacedatanetwork/sdn-server/internal/ops"
	"github.com/spacedatanetwork/sdn-server/internal/syncwire"
)

const (
	// removedImportLedgerFile is the removed lane ledger under storage.path.
	removedImportLedgerFile = "trusted-import-lanes.json"
	// importConversionFile records the one conversion under storage.path.
	importConversionFile = "trusted-import-converted"
)

// errRemovedTrustedImport is a new node configured with the removed key.
var errRemovedTrustedImport = errors.New("peers.trusted_import is removed in 1.2.0: a node imports only the lanes it subscribes to. Remove the key from the config and subscribe to each lane with `spacedatanetwork sync subscribe --schema <standard> --provider-id <provider> --source-name <source> --from <producer peer id>`")

// storagePath is the configured storage path ("" without one).
func (n *Node) storagePath() string {
	if n == nil || n.config == nil {
		return ""
	}
	return strings.TrimSpace(n.config.Storage.Path)
}

// checkRemovedTrustedImport refuses the removed key on a new node: one
// whose storage path holds nothing yet. A node with data is converted at
// its start instead.
func (n *Node) checkRemovedTrustedImport() error {
	if n == nil || n.config == nil || n.config.Peers.RemovedTrustedImport == nil {
		return nil
	}
	path := n.storagePath()
	if path == "" {
		return errRemovedTrustedImport
	}
	entries, err := os.ReadDir(path)
	if err != nil || len(entries) == 0 {
		return errRemovedTrustedImport
	}
	return nil
}

// ConvertedLane is one lane of the removed import gate and what became of
// it.
type ConvertedLane struct {
	Schema, ProviderID, SourceName string
	// Producers are the peers it is subscribed from; Reason why not, when
	// there are none.
	Producers []string
	Reason    string
	// FromLedger and FromAllow say where the lane was named.
	FromLedger, FromAllow bool
}

// removedImportLanes reads the ledger and the allow list and resolves each
// lane's producers.
func (n *Node) removedImportLanes() ([]ConvertedLane, error) {
	type laneKey struct{ code, provider, source string }
	lanes := map[laneKey]*ConvertedLane{}
	var order []laneKey
	add := func(schema, provider, source string) *ConvertedLane {
		schema, provider, source = syncwire.NormalizeSchema(strings.TrimSpace(schema)), strings.TrimSpace(provider), strings.TrimSpace(source)
		k := laneKey{channels.RetentionStandardCode(schema), strings.ToLower(provider), strings.ToLower(source)}
		if l := lanes[k]; l != nil {
			return l
		}
		l := &ConvertedLane{Schema: schema, ProviderID: provider, SourceName: source}
		lanes[k] = l
		order = append(order, k)
		return l
	}
	var ledgerErr error
	if raw, err := os.ReadFile(filepath.Join(n.storagePath(), removedImportLedgerFile)); err == nil {
		var rows []struct {
			Schema     string `json:"schema"`
			ProviderID string `json:"provider_id"`
			SourceName string `json:"source_name"`
		}
		if err := json.Unmarshal(raw, &rows); err != nil {
			ledgerErr = fmt.Errorf("%s: %w", removedImportLedgerFile, err)
		}
		for _, r := range rows {
			if strings.TrimSpace(r.Schema) != "" && strings.TrimSpace(r.ProviderID) != "" && strings.TrimSpace(r.SourceName) != "" {
				add(r.Schema, r.ProviderID, r.SourceName).FromLedger = true
			}
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		ledgerErr = err
	}
	var allow []config.LaneSelector
	if removed := n.config.Peers.RemovedTrustedImport; removed != nil {
		allow = removed.Allow
	}
	var wildcards []string
	for _, s := range allow {
		named := func(v string) bool { v = strings.TrimSpace(v); return v != "" && v != "*" }
		if !named(s.Schema) || !named(s.ProviderID) || !named(s.SourceName) {
			wildcards = append(wildcards, fmt.Sprintf("%s %s/%s", s.Schema, s.ProviderID, s.SourceName))
			continue
		}
		add(s.Schema, s.ProviderID, s.SourceName).FromAllow = true
	}

	self := ""
	if n.host != nil {
		self = n.host.ID().String()
	}
	progress, err := n.store.ProducerSourceProgress()
	if err != nil {
		return nil, fmt.Errorf("the store's producers: %w", err)
	}
	for _, k := range order {
		l := lanes[k]
		producers := map[string]bool{}
		producedHere := false
		for _, row := range progress {
			if row.Count <= 0 || channels.RetentionStandardCode(row.SchemaName) != k.code || !strings.EqualFold(row.ProviderID, l.ProviderID) || !strings.EqualFold(row.SourceName, l.SourceName) {
				continue
			}
			id := strings.TrimSpace(row.ProducerPeerID)
			if id == self || id == "local-node" {
				producedHere = true
				continue
			}
			if pid, err := peer.Decode(id); err == nil && n.peerRegistry != nil && n.peerRegistry.IsTrusted(pid) {
				producers[pid.String()] = true
			}
		}
		for _, s := range allow {
			if p := strings.TrimSpace(s.Peer); p != "" && p != "*" && s.Matches(p, l.Schema, l.ProviderID, l.SourceName) {
				if pid, err := peer.Decode(p); err == nil && pid.String() != self {
					producers[pid.String()] = true
				}
			}
		}
		for p := range producers {
			l.Producers = append(l.Producers, p)
		}
		sort.Strings(l.Producers)
		switch {
		case len(l.Producers) > 0:
		case producedHere:
			l.Reason = "this node produces the lane"
		default:
			l.Reason = "no trusted peer is known as its producer: subscribe with `spacedatanetwork sync subscribe --from <producer peer id>`"
		}
	}
	out := make([]ConvertedLane, 0, len(order))
	for _, k := range order {
		out = append(out, *lanes[k])
	}
	for _, w := range wildcards {
		out = append(out, ConvertedLane{Schema: w, FromAllow: true, Reason: "an allow selector that does not name one lane (standard, provider and source)"})
	}
	return out, ledgerErr
}

// RemovedImportPlan is what converting the removed import gate subscribes
// to (sync subscribe --from-ledger).
func (n *Node) RemovedImportPlan() ([]ConvertedLane, error) {
	if n == nil || n.store == nil || n.config == nil {
		return nil, errNoFollows
	}
	return n.removedImportLanes()
}

// convertRemovedTrustedImport runs the conversion once (see the file
// comment), or reports the key a converted node still carries.
func (n *Node) convertRemovedTrustedImport() {
	if n == nil || n.store == nil || n.config == nil || n.follows == nil || n.storagePath() == "" {
		return
	}
	base := n.storagePath()
	ledger := filepath.Join(base, removedImportLedgerFile)
	marker := filepath.Join(base, importConversionFile)
	_, ledgerErr := os.Stat(ledger)
	_, markerErr := os.Stat(marker)
	hasKey := n.config.Peers.RemovedTrustedImport != nil
	if markerErr == nil || (!hasKey && ledgerErr != nil) {
		if hasKey {
			log.Warnf("peers.trusted_import is ignored: it was converted to subscriptions (%s). Remove it from the config; subscriptions are managed with `spacedatanetwork sync subscribe`", marker)
			n.alerts.Raise(ops.KindSubscriptionConversion, "", ops.SeverityWarning,
				"peers.trusted_import is still in the config and is ignored (converted to subscriptions); remove it")
		}
		return
	}
	lanes, err := n.removedImportLanes()
	if err != nil {
		log.Errorf("Converting the import ledger and allow list to subscriptions: %v", err)
		if lanes == nil {
			n.alerts.Raise(ops.KindSubscriptionConversion, "", ops.SeverityError, "the import ledger could not be converted to subscriptions: "+err.Error())
			return
		}
	}
	var report strings.Builder
	fmt.Fprintf(&report, "# peers.trusted_import and %s converted to subscriptions at %s\n", removedImportLedgerFile, time.Now().UTC().Format(time.RFC3339))
	converted := 0
	for _, l := range lanes {
		if len(l.Producers) == 0 {
			fmt.Fprintf(&report, "%s %s/%s: not converted: %s\n", l.Schema, l.ProviderID, l.SourceName, l.Reason)
			if l.Reason != "this node produces the lane" {
				log.Warnf("Import ledger lane %s %s/%s not converted to a subscription: %s", l.Schema, l.ProviderID, l.SourceName, l.Reason)
				n.alerts.Raise(ops.KindSubscriptionConversion, l.Schema+" "+l.ProviderID+"/"+l.SourceName, ops.SeverityWarning, "not converted to a subscription: "+l.Reason)
			}
			continue
		}
		for _, p := range l.Producers {
			key := FollowKey{Schema: l.Schema, ProviderID: l.ProviderID, SourceName: l.SourceName, Producer: p}
			if n.follows.adopt(key) {
				converted++
			}
			fmt.Fprintf(&report, "%s %s/%s from %s: subscribed, starting at the producer's current batch\n", l.Schema, l.ProviderID, l.SourceName, p)
			log.Infof("Converted import ledger lane %s %s/%s to a subscription from %s; its first read is bounded by the producer's current batch (logged when the producer states it)",
				l.Schema, l.ProviderID, l.SourceName, p)
		}
	}
	if err := n.follows.save(); err != nil {
		log.Errorf("Converted subscriptions not saved: %v", err)
		n.alerts.Raise(ops.KindSubscriptionConversion, "", ops.SeverityError, "converted subscriptions could not be saved: "+err.Error())
		return
	}
	if err := os.WriteFile(marker, []byte(report.String()), 0o600); err != nil {
		log.Errorf("Conversion record %s not written: %v", marker, err)
		return
	}
	if ledgerErr == nil {
		if err := os.Rename(ledger, ledger+".converted"); err != nil {
			log.Warnf("Import ledger not renamed: %v", err)
		}
	}
	log.Infof("Converted the removed import gate to %d subscriptions (%d lanes named); record in %s", converted, len(lanes), marker)
	if hasKey {
		log.Warnf("peers.trusted_import is ignored from now on: remove it from the config")
	}
}
