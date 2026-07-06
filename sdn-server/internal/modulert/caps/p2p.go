// Package caps: p2p_read discovery capability (gateway loop G.2).
//
// Read-only, policy-mediated snapshots of the host's network view for the
// discovery gateway flows (hostcap/p2p-discovery guest node):
//
//   - p2p.peers_snapshot   — the merged peerstore/DHT/registry/PNM view:
//     one entry per known peer (self included, listed first) with addrs,
//     connectedness, agent version, the standards the peer publishes
//     (derived from stored signed PNMs), and the stored $EPM profile as a
//     size-prefixed frame in the binary stream segment ({"$bin":0}).
//   - p2p.standards_snapshot — the newest stored signed $PNM per
//     (publishing peer, standard), FILE_ID-derived standard names, frames
//     verbatim in the stream segment (entry order).
//
// The host supplies MATERIALS only (which peers exist, which records are
// stored); all response shaping (record selection, format handling, ETag)
// lives in the wasm flow. Both ops are deterministic for a fixed network
// state: peers and entries are sorted so identical state yields identical
// streams (content-derived ETags stay stable).
package caps

import (
	"encoding/binary"
	"encoding/json"
	"sort"
	"strings"

	PNM "github.com/DigitalArsenal/spacedatastandards.org/lib/go/PNM"
	"github.com/spacedatanetwork/sdn-server/internal/modulert"
)

// P2PPeerInfo is one peer in the host's merged network view.
type P2PPeerInfo struct {
	ID           string
	Addrs        []string
	Connected    bool
	AgentVersion string
}

// P2PPNMRecord is one stored PNM record with its publishing-peer attribution
// (newest first, as returned by the store).
type P2PPNMRecord struct {
	PeerID string
	Data   []byte // size-prefixed $PNM FlatBuffer
}

// P2PCapOptions wires the node services into the capability as closures so
// the handler stays unit-testable without a libp2p host or a live store.
type P2PCapOptions struct {
	// SelfID is the host's own peer ID (empty = self entry omitted).
	SelfID string
	// SelfAddrs returns the host's listen/announce multiaddrs.
	SelfAddrs func() []string
	// SelfAgentVersion is the host's own agent version string.
	SelfAgentVersion string
	// SelfEPM returns the node's own size-prefixed $EPM (nil = none).
	SelfEPM func() []byte
	// Peers returns the merged network view (connected + peerstore + DHT +
	// trust registry), excluding self.
	Peers func() []P2PPeerInfo
	// PeerEPM returns the stored size-prefixed $EPM for a peer (nil = none).
	PeerEPM func(peerID string) []byte
	// RecentPNMs returns up to limit stored PNM records, newest first.
	RecentPNMs func(limit int) []P2PPNMRecord
	// PNMScanLimit bounds the standards-derivation scan (default 4096).
	PNMScanLimit int
}

const defaultPNMScanLimit = 4096

// NewP2PCapFactory builds the p2p_read capability handler factory.
func NewP2PCapFactory(opts P2PCapOptions) modulert.CapFactory {
	if opts.PNMScanLimit <= 0 {
		opts.PNMScanLimit = defaultPNMScanLimit
	}
	adapter := &p2pCapAdapter{opts: opts}
	return func(mod *modulert.Module) modulert.CapHandler {
		return adapter.handle
	}
}

type p2pCapAdapter struct {
	opts P2PCapOptions
}

func (a *p2pCapAdapter) handle(operation string, payload []byte) ([]byte, error) {
	var params struct {
		PeerID string `json:"peer_id"`
	}
	if len(payload) > 0 {
		_ = json.Unmarshal(payload, &params) // tolerate empty/omitted payloads
	}
	switch operation {
	case "p2p.peers_snapshot":
		return a.peersSnapshot(strings.TrimSpace(params.PeerID)), nil
	case "p2p.standards_snapshot":
		return a.standardsSnapshot(strings.TrimSpace(params.PeerID)), nil
	default:
		return errCapJSON("unknown p2p operation: " + operation), nil
	}
}

// ---------------------------------------------------------------------------
// PNM scan: standard derivation + newest-per-(peer, standard) selection.
// ---------------------------------------------------------------------------

// pnmFileIDSchema extracts the ".fbs" segment of a colon-delimited PNM
// FILE_ID (e.g. "celestrak:gp:OMM.fbs:2026-05-06T03:00:00Z" -> "OMM.fbs"),
// mirroring the dataset-pnms CLI rule.
func pnmFileIDSchema(fileID string) string {
	for _, part := range strings.Split(fileID, ":") {
		part = strings.TrimSpace(part)
		if strings.HasSuffix(part, ".fbs") {
			return part
		}
	}
	return ""
}

type pnmEntry struct {
	peerID           string
	standard         string
	schema           string
	fileID           string
	fileName         string
	cid              string
	publishTimestamp string
	frame            []byte // size-prefixed $PNM, verbatim
}

// scanPNMs walks the newest-first stored PNMs and keeps the first (newest)
// record per (peer, schema). peerFilter narrows to one publisher.
func (a *p2pCapAdapter) scanPNMs(peerFilter string) []pnmEntry {
	if a.opts.RecentPNMs == nil {
		return nil
	}
	seen := make(map[string]bool)
	entries := make([]pnmEntry, 0, 16)
	for _, record := range a.opts.RecentPNMs(a.opts.PNMScanLimit) {
		if record.PeerID == "" || len(record.Data) < 8 {
			continue
		}
		if peerFilter != "" && record.PeerID != peerFilter {
			continue
		}
		if !PNM.SizePrefixedPNMBufferHasIdentifier(record.Data) {
			continue
		}
		pnm := PNM.GetSizePrefixedRootAsPNM(record.Data, 0)
		fileID := strings.TrimSpace(string(pnm.FILE_ID()))
		schema := pnmFileIDSchema(fileID)
		if schema == "" {
			continue // record-level PNM without a dataset FILE_ID partition
		}
		key := record.PeerID + "\x00" + schema
		if seen[key] {
			continue // an older publication for the same (peer, standard)
		}
		seen[key] = true
		entries = append(entries, pnmEntry{
			peerID:           record.PeerID,
			standard:         strings.TrimSuffix(schema, ".fbs"),
			schema:           schema,
			fileID:           fileID,
			fileName:         strings.TrimSpace(string(pnm.FILE_NAME())),
			cid:              strings.TrimSpace(string(pnm.CID())),
			publishTimestamp: strings.TrimSpace(string(pnm.PUBLISH_TIMESTAMP())),
			frame:            record.Data,
		})
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].peerID != entries[j].peerID {
			return entries[i].peerID < entries[j].peerID
		}
		return entries[i].standard < entries[j].standard
	})
	return entries
}

// standardsByPeer inverts the PNM scan into peerID -> sorted standard names.
func (a *p2pCapAdapter) standardsByPeer() map[string][]string {
	byPeer := make(map[string][]string)
	for _, entry := range a.scanPNMs("") {
		byPeer[entry.peerID] = append(byPeer[entry.peerID], entry.standard)
	}
	return byPeer // scanPNMs sorts (peer, standard), so each list is sorted
}

// ---------------------------------------------------------------------------
// p2p.peers_snapshot
// ---------------------------------------------------------------------------

// validSizePrefixedFrame reports whether data is exactly one size-prefixed
// frame ([u32le n][n bytes]) so it can be spliced into a stream verbatim.
func validSizePrefixedFrame(data []byte) bool {
	if len(data) < 8 {
		return false
	}
	return binary.LittleEndian.Uint32(data) == uint32(len(data)-4)
}

func (a *p2pCapAdapter) peersSnapshot(peerFilter string) []byte {
	standards := a.standardsByPeer()

	type peerRow struct {
		info P2PPeerInfo
		self bool
	}
	rows := make([]peerRow, 0, 32)
	seen := make(map[string]bool)

	if a.opts.SelfID != "" {
		info := P2PPeerInfo{ID: a.opts.SelfID, Connected: true, AgentVersion: a.opts.SelfAgentVersion}
		if a.opts.SelfAddrs != nil {
			info.Addrs = a.opts.SelfAddrs()
		}
		rows = append(rows, peerRow{info: info, self: true})
		seen[a.opts.SelfID] = true
	}
	if a.opts.Peers != nil {
		peers := a.opts.Peers()
		sort.Slice(peers, func(i, j int) bool { return peers[i].ID < peers[j].ID })
		for _, info := range peers {
			if info.ID == "" || seen[info.ID] {
				continue
			}
			seen[info.ID] = true
			sort.Strings(info.Addrs)
			rows = append(rows, peerRow{info: info})
		}
	}
	// Peers only known through stored PNMs (provider synced from, then
	// disconnected) still belong to the discovery view.
	pnmPeers := make([]string, 0, len(standards))
	for peerID := range standards {
		if !seen[peerID] {
			pnmPeers = append(pnmPeers, peerID)
		}
	}
	sort.Strings(pnmPeers)
	for _, peerID := range pnmPeers {
		seen[peerID] = true
		rows = append(rows, peerRow{info: P2PPeerInfo{ID: peerID}})
	}

	if peerFilter != "" {
		filtered := rows[:0]
		for _, row := range rows {
			if row.info.ID == peerFilter {
				filtered = append(filtered, row)
				break
			}
		}
		rows = filtered
	}

	stream := make([]byte, 0, 4096)
	peersOut := make([]map[string]interface{}, 0, len(rows))
	epmCount := 0
	for _, row := range rows {
		var epm []byte
		if row.self {
			if a.opts.SelfEPM != nil {
				epm = a.opts.SelfEPM()
			}
		} else if a.opts.PeerEPM != nil {
			epm = a.opts.PeerEPM(row.info.ID)
		}
		epmIndex := -1
		if validSizePrefixedFrame(epm) {
			epmIndex = epmCount
			stream = append(stream, epm...)
			epmCount++
		}
		addrs := row.info.Addrs
		if addrs == nil {
			addrs = []string{}
		}
		peerStandards := standards[row.info.ID]
		if peerStandards == nil {
			peerStandards = []string{}
		}
		entry := map[string]interface{}{
			"peer_id":   row.info.ID,
			"addrs":     addrs,
			"connected": row.info.Connected,
			"self":      row.self,
			"standards": peerStandards,
			"epm_index": epmIndex,
		}
		if row.info.AgentVersion != "" {
			entry["agent_version"] = row.info.AgentVersion
		}
		peersOut = append(peersOut, entry)
	}

	return modulert.PreEncodedEnvelope(map[string]interface{}{
		"ok": true,
		"result": map[string]interface{}{
			"self":    a.opts.SelfID,
			"peers":   peersOut,
			"records": map[string]interface{}{"$bin": 0},
		},
	}, [][]byte{stream})
}

// ---------------------------------------------------------------------------
// p2p.standards_snapshot
// ---------------------------------------------------------------------------

func (a *p2pCapAdapter) standardsSnapshot(peerFilter string) []byte {
	entries := a.scanPNMs(peerFilter)
	stream := make([]byte, 0, 4096)
	entriesOut := make([]map[string]interface{}, 0, len(entries))
	for i, entry := range entries {
		stream = append(stream, entry.frame...)
		entriesOut = append(entriesOut, map[string]interface{}{
			"peer_id":           entry.peerID,
			"standard":          entry.standard,
			"schema":            entry.schema,
			"file_id":           entry.fileID,
			"file_name":         entry.fileName,
			"cid":               entry.cid,
			"publish_timestamp": entry.publishTimestamp,
			"pnm_index":         i,
		})
	}
	return modulert.PreEncodedEnvelope(map[string]interface{}{
		"ok": true,
		"result": map[string]interface{}{
			"entries": entriesOut,
			"records": map[string]interface{}{"$bin": 0},
		},
	}, [][]byte{stream})
}
