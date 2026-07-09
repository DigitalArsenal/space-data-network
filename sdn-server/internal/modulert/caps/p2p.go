// Package caps: p2p_read discovery capability (gateway loop G.2).
//
// Read-only, policy-mediated snapshots of the host's network view for the
// discovery gateway flows (hostcap/p2p-discovery guest node):
//
//   - p2p.peers_snapshot   — the merged peerstore/registry/PNM view, where
//     "DHT peers" means only peers verified via the SDN advertisement flag
//     rendezvous namespace (the node's DHT joins the public IPFS/Amino
//     swarm, so raw DHT routing-table membership is NOT evidence of SDN
//     membership — see sdnAdvertisementDiscoveryNamespace in
//     internal/node/advertisement_discovery.go): one entry per known peer
//     (self included, listed first) with addrs, connectedness, agent
//     version, the standards the peer publishes (derived from stored signed
//     PNMs), and the stored $EPM profile as a size-prefixed frame in the
//     binary stream segment ({"$bin":0}).
//   - p2p.standards_snapshot — the newest stored signed $PNM per
//     (publishing peer, standard), FILE_ID-derived standard names, frames
//     verbatim in the stream segment (entry order).
//   - p2p.pnm_history — the stored signed $PNM frames PUBLISHED BY one
//     peer, newest first (gateway loop G.3). Attribution honesty: the store
//     records the GOSSIP-DELIVERING peer id, which is not necessarily the
//     publisher, so this op attributes by SIGNATURE — a PNM belongs to the
//     requested peer iff its Ed25519 signature verifies under one of the
//     peer's publication keys (identity key extracted from the peer id, or
//     the signing key of the peer's EPM directory record — the exact key
//     path the host's own dataset-publication ingest uses). Only when NO
//     key is resolvable does the op fall back to the stored gossip
//     attribution, and every entry carries signature_verified +
//     attribution so the surface never overstates provenance.
//   - p2p.latest_dataset — the provider's newest published dataset for one
//     standard (gateway loop G.4): the newest publication BATCH whose PNM
//     verifies under the peer's publication keys (same attribution rules as
//     pnm_history) AND whose full shard content is materialized locally.
//     Serving is gated by the host's OPT-IN gateway.pin config (a node's
//     own publications are always servable); an unpinned or not-yet-
//     materialized dataset returns MATERIALS for the honest 404/503 —
//     known/pinned flags plus the newest PNM pointer — never a silent
//     proxy fetch. Stream delivery is by hostcall-bridge body reference
//     (deliver="ref") or an inline binary segment (json shaping path).
//
// The host supplies MATERIALS only (which peers exist, which records are
// stored); all response shaping (record selection, format handling, ETag)
// lives in the wasm flow. All ops are deterministic for a fixed network
// state: peers and entries are sorted so identical state yields identical
// streams (content-derived ETags stay stable).
package caps

import (
	"crypto/ed25519"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	PNM "github.com/DigitalArsenal/spacedatastandards.org/lib/go/PNM"
	"github.com/spacedatanetwork/sdn-server/internal/channels"
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

// P2PPublisherKey is one candidate Ed25519 dataset-publication public key
// for a peer, with the provenance of how the host learned it.
type P2PPublisherKey struct {
	PublicKey ed25519.PublicKey
	Source    string // "peer-id" (identity multihash) | "epm-directory"
}

// P2PDatasetBatch is one locally materialized publication batch supplied by
// the host store (storage.MaterializedDatasetBatch) for the latest-dataset
// surface.
type P2PDatasetBatch struct {
	ProviderID  string
	SourceName  string
	BatchID     string
	RecordCount int
	// Bytes is the batch's aligned size-prefixed record stream (the shard
	// files spliced in window order, SHA-verified by the store).
	Bytes []byte
	// FNV1a64 is the word-folded FNV-1a 64 hash of Bytes (entity-tag
	// identity, matching the flow-side algorithm).
	FNV1a64     uint64
	Parts       int
	PublishedAt string // RFC3339, newest part
}

// P2PBatchCandidate is one publication batch attributed to a peer via its
// stored PNMs, newest first (the shared selection rule for serving AND the
// supersede evaluation).
type P2PBatchCandidate struct {
	BatchID          string
	FileID           string
	FileName         string
	CID              string
	PublishTimestamp string
	Verified         bool
	Attribution      string // "signature" | "gossip"
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
	// Peers returns the merged network view (connected + peerstore +
	// SDN-advertisement-flag-verified DHT peers + trust registry), excluding
	// self.
	Peers func() []P2PPeerInfo
	// PeerEPM returns the stored size-prefixed $EPM for a peer (nil = none).
	PeerEPM func(peerID string) []byte
	// RecentPNMs returns up to limit stored PNM records, newest first.
	RecentPNMs func(limit int) []P2PPNMRecord
	// PublisherKeys resolves a peer's candidate Ed25519 dataset-publication
	// public keys (peer-id identity key and/or EPM-directory signing key).
	// Read-only and local: NO live network fetch (deterministic materials).
	PublisherKeys func(peerID string) []P2PPublisherKey
	// PNMScanLimit bounds the standards-derivation scan (default 4096).
	PNMScanLimit int

	// SchemaForStandard maps a URL standard segment ("omm", "OMM") to the
	// node's canonical schema name ("OMM.fbs"); "" = unknown standard
	// (latest-dataset surface, gateway loop G.4).
	SchemaForStandard func(standard string) string
	// PinnedDataset reports the host's OPT-IN gateway.pin decision for a
	// (peer, schema) pair. Never defaults to true.
	PinnedDataset func(peerID, schemaName string) bool
	// LatestDatasetBatch materializes one publication batch from the local
	// store (nil/false = not materialized here). includeBytes=false is the
	// cheap servability probe.
	LatestDatasetBatch func(schemaName, batchID string, includeBytes bool) (*P2PDatasetBatch, bool)
	// LatestBatchScan bounds how many newest batch candidates are probed for
	// local materialization before answering unavailable (default 8).
	LatestBatchScan int
}

const (
	defaultPNMScanLimit    = 4096
	defaultLatestBatchScan = 8
)

// NewP2PCapFactory builds the p2p_read capability handler factory. It is
// bridge-aware since G.4: p2p.latest_dataset delivers large dataset streams
// as hostcall-bridge body references (deliver="ref") so the bytes never
// enter the flow's linear memory.
func NewP2PCapFactory(opts P2PCapOptions) modulert.BridgeCapFactory {
	if opts.PNMScanLimit <= 0 {
		opts.PNMScanLimit = defaultPNMScanLimit
	}
	if opts.LatestBatchScan <= 0 {
		opts.LatestBatchScan = defaultLatestBatchScan
	}
	adapter := &p2pCapAdapter{opts: opts}
	return func(mod *modulert.Module, bridge *modulert.HostBridge) modulert.CapHandler {
		return func(operation string, payload []byte) ([]byte, error) {
			return adapter.handle(operation, payload, bridge)
		}
	}
}

type p2pCapAdapter struct {
	opts P2PCapOptions
}

func (a *p2pCapAdapter) handle(operation string, payload []byte, bridge *modulert.HostBridge) ([]byte, error) {
	var params struct {
		PeerID   string `json:"peer_id"`
		Limit    int    `json:"limit"`
		Standard string `json:"standard"`
		Deliver  string `json:"deliver"`
	}
	if len(payload) > 0 {
		_ = json.Unmarshal(payload, &params) // tolerate empty/omitted payloads
	}
	switch operation {
	case "p2p.peers_snapshot":
		return a.peersSnapshot(strings.TrimSpace(params.PeerID)), nil
	case "p2p.standards_snapshot":
		return a.standardsSnapshot(strings.TrimSpace(params.PeerID)), nil
	case "p2p.pnm_history":
		return a.pnmHistory(strings.TrimSpace(params.PeerID), params.Limit), nil
	case "p2p.latest_dataset":
		return a.latestDataset(strings.TrimSpace(params.PeerID), strings.TrimSpace(params.Standard), strings.TrimSpace(params.Deliver), bridge), nil
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

// ---------------------------------------------------------------------------
// p2p.pnm_history (gateway loop G.3)
// ---------------------------------------------------------------------------

// pnmHistory returns the stored signed $PNM frames published by peerID,
// newest first (PUBLISH_TIMESTAMP descending, store arrival order as the
// tiebreak), truncated to limit (limit <= 0 = no request bound; the scan is
// always bounded by PNMScanLimit).
//
// Publisher attribution (docs/gateway-api.md §"attribution honesty"): the
// store's recorded peer id is the GOSSIP-DELIVERING peer, so when the host
// can resolve publication keys for peerID, an entry is included iff its
// Ed25519 signature verifies under one of those keys (attribution
// "signature", signature_verified true) — gossip-attributed frames that do
// NOT verify are excluded and counted in gossip_only_excluded. When no key
// resolves, entries fall back to the stored gossip attribution
// (attribution "gossip", signature_verified false). Frames that do not
// carry a well-formed Ed25519 signature never appear on this surface.
func (a *p2pCapAdapter) pnmHistory(peerID string, limit int) []byte {
	if peerID == "" {
		return errCapJSON("p2p.pnm_history requires peer_id")
	}

	entries, keysAvailable, gossipOnlyExcluded := a.scanAttributedPNMs(peerID)
	if limit > 0 && len(entries) > limit {
		entries = entries[:limit]
	}

	stream := make([]byte, 0, 4096)
	entriesOut := make([]map[string]interface{}, 0, len(entries))
	for i, entry := range entries {
		stream = append(stream, entry.frame...)
		schema := pnmFileIDSchema(entry.evidence.FileID)
		attribution := "gossip"
		if entry.verified {
			attribution = "signature"
		}
		out := map[string]interface{}{
			"publisher_peer_id":  peerID,
			"gossip_peer_id":     entry.gossipID,
			"standard":           strings.TrimSuffix(schema, ".fbs"),
			"schema":             schema,
			"file_id":            entry.evidence.FileID,
			"file_name":          entry.fileName,
			"cid":                entry.evidence.CID,
			"publish_timestamp":  entry.publishTS,
			"signature_type":     entry.evidence.SignatureType,
			"signature":          hex.EncodeToString(entry.evidence.Signature),
			"signature_verified": entry.verified,
			"attribution":        attribution,
			"pnm_index":          i,
		}
		if entry.keyHex != "" {
			out["publisher_key"] = entry.keyHex
			out["publisher_key_source"] = entry.keySource
		}
		entriesOut = append(entriesOut, out)
	}

	return modulert.PreEncodedEnvelope(map[string]interface{}{
		"ok": true,
		"result": map[string]interface{}{
			"peer_id":                 peerID,
			"publisher_key_available": keysAvailable,
			"gossip_only_excluded":    gossipOnlyExcluded,
			"entries":                 entriesOut,
			"records":                 map[string]interface{}{"$bin": 0},
		},
	}, [][]byte{stream})
}

// attributedPNMEntry is one stored PNM attributed to a publisher (the shared
// selection core of pnm_history and latest_dataset).
type attributedPNMEntry struct {
	evidence  channels.PNMTrustEvidence
	frame     []byte
	gossipID  string
	verified  bool
	keyHex    string
	keySource string
	publishTS string
	fileName  string
	scanOrder int
}

// scanAttributedPNMs walks the newest-first stored PNMs and returns the ones
// attributable to peerID, newest first (PUBLISH_TIMESTAMP descending, store
// arrival as tiebreak), deduplicated per publication. Attribution rules per
// the package comment: signature verification when publication keys resolve
// (failures excluded + counted), gossip attribution only when NO key
// resolves. Unsigned/malformed frames never appear.
func (a *p2pCapAdapter) scanAttributedPNMs(peerID string) (entries []attributedPNMEntry, keysAvailable bool, gossipOnlyExcluded int) {
	var keys []P2PPublisherKey
	if a.opts.PublisherKeys != nil {
		keys = a.opts.PublisherKeys(peerID)
	}
	keysAvailable = len(keys) > 0

	entries = make([]attributedPNMEntry, 0, 16)
	scanOrder := 0
	seen := make(map[string]bool)
	if a.opts.RecentPNMs != nil {
		for _, record := range a.opts.RecentPNMs(a.opts.PNMScanLimit) {
			scanOrder++
			if len(record.Data) < 8 || !PNM.SizePrefixedPNMBufferHasIdentifier(record.Data) {
				continue
			}
			evidence, err := channels.VerifySignedPNMEnvelope(record.Data)
			if err != nil {
				continue // unsigned / malformed: no provenance value here
			}
			entry := attributedPNMEntry{
				evidence:  evidence,
				frame:     record.Data,
				gossipID:  record.PeerID,
				scanOrder: scanOrder,
			}
			if keysAvailable {
				for _, key := range keys {
					if len(key.PublicKey) != ed25519.PublicKeySize {
						continue
					}
					if _, err := channels.VerifySignedPNMEnvelopeWithProviderKey(record.Data, key.PublicKey); err == nil {
						entry.verified = true
						entry.keyHex = hex.EncodeToString(key.PublicKey)
						entry.keySource = key.Source
						break
					}
				}
				if !entry.verified {
					if record.PeerID == peerID {
						gossipOnlyExcluded++
					}
					continue
				}
			} else if record.PeerID != peerID {
				continue
			}
			dedupKey := evidence.FileID + "\x00" + evidence.CID + "\x00" + hex.EncodeToString(evidence.Signature)
			if seen[dedupKey] {
				continue
			}
			seen[dedupKey] = true
			pnm := PNM.GetSizePrefixedRootAsPNM(record.Data, 0)
			entry.publishTS = strings.TrimSpace(string(pnm.PUBLISH_TIMESTAMP()))
			entry.fileName = strings.TrimSpace(string(pnm.FILE_NAME()))
			entries = append(entries, entry)
		}
	}

	// Newest first: PUBLISH_TIMESTAMP descending (RFC3339 strings compare
	// lexicographically; empty sorts last), store arrival order as tiebreak.
	sort.SliceStable(entries, func(i, j int) bool {
		ti, tj := entries[i].publishTS, entries[j].publishTS
		if ti != tj {
			if ti == "" {
				return false
			}
			if tj == "" {
				return true
			}
			return ti > tj
		}
		return entries[i].scanOrder < entries[j].scanOrder
	})
	return entries, keysAvailable, gossipOnlyExcluded
}

// ---------------------------------------------------------------------------
// p2p.latest_dataset (gateway loop G.4)
// ---------------------------------------------------------------------------

// pnmFileIDBatch extracts the batch id of a dataset-publication FILE_ID
// ("<datasetID>:<schema>:<batchID>[:part-N]" — the segment right after the
// schema; mirrors the host's datasetPublicationFileIDParts rule).
func pnmFileIDBatch(fileID, schema string) string {
	parts := strings.Split(fileID, ":")
	for i, part := range parts {
		if strings.TrimSpace(part) == schema && i+1 < len(parts) {
			return strings.TrimSpace(parts[i+1])
		}
	}
	return ""
}

// LatestBatchCandidates lists a peer's publication batches for one schema,
// newest first by PNM publish timestamp — the SHARED batch selection rule
// for the latest-dataset serving path and the node's supersede evaluation.
// max <= 0 uses the options' LatestBatchScan default.
func LatestBatchCandidates(opts P2PCapOptions, peerID, schemaName string, max int) []P2PBatchCandidate {
	if opts.PNMScanLimit <= 0 {
		opts.PNMScanLimit = defaultPNMScanLimit
	}
	if max <= 0 {
		max = opts.LatestBatchScan
	}
	if max <= 0 {
		max = defaultLatestBatchScan
	}
	adapter := &p2pCapAdapter{opts: opts}
	return adapter.latestBatchCandidates(peerID, schemaName, max)
}

func (a *p2pCapAdapter) latestBatchCandidates(peerID, schemaName string, max int) []P2PBatchCandidate {
	entries, _, _ := a.scanAttributedPNMs(peerID)
	candidates := make([]P2PBatchCandidate, 0, 4)
	seen := make(map[string]bool)
	for _, entry := range entries {
		if pnmFileIDSchema(entry.evidence.FileID) != schemaName {
			continue
		}
		batchID := pnmFileIDBatch(entry.evidence.FileID, schemaName)
		if batchID == "" || seen[batchID] {
			continue
		}
		seen[batchID] = true
		attribution := "gossip"
		if entry.verified {
			attribution = "signature"
		}
		candidates = append(candidates, P2PBatchCandidate{
			BatchID:          batchID,
			FileID:           entry.evidence.FileID,
			FileName:         entry.fileName,
			CID:              entry.evidence.CID,
			PublishTimestamp: entry.publishTS,
			Verified:         entry.verified,
			Attribution:      attribution,
		})
		if len(candidates) >= max {
			break
		}
	}
	return candidates
}

// pnmPointerJSON is the honest-error / provenance pointer for a candidate:
// enough for a client to fetch the publication itself over p2p.
//
// This object travels VERBATIM into the public 503/406 error bodies (the
// discovery-shape flow node embeds the "pnm" slice unmodified), so it is a
// public JSON rendering of PNM record fields. HARD RULE (user 2026-07-06,
// json-schema-capitalization-rule): schema-derived properties carry the
// schema/PNM/main.fbs field names EXACTLY (CID, FILE_ID, PUBLISH_TIMESTAMP);
// synthesized fields (batch_id, standard, schema, signature_verified,
// attribution) stay lowercase snake_case.
func pnmPointerJSON(c P2PBatchCandidate, schemaName string) map[string]interface{} {
	return map[string]interface{}{
		"CID":                c.CID,
		"FILE_ID":            c.FileID,
		"PUBLISH_TIMESTAMP":  c.PublishTimestamp,
		"batch_id":           c.BatchID,
		"standard":           strings.TrimSuffix(schemaName, ".fbs"),
		"schema":             schemaName,
		"signature_verified": c.Verified,
		"attribution":        c.Attribution,
	}
}

// latestDataset assembles the materials for GET
// /api/v1/peers/{peerId}/{standard}/latest. The wasm flow makes every
// response decision; this op reports flags + materials only:
//
//	known:    the peer has attributable publications for the standard
//	pinned:   the host's gateway.pin opts this (peer, standard) in
//	self:     the peer is this node (own publications are always servable)
//	pnm:      the NEWEST publication pointer (known = true)
//	serving:  the served batch metadata + stream (pinned/self and a batch
//	          is fully materialized locally)
//	fresh:    the served batch IS the newest published batch
func (a *p2pCapAdapter) latestDataset(peerID, standard, deliver string, bridge *modulert.HostBridge) []byte {
	if peerID == "" {
		return errCapJSON("p2p.latest_dataset requires peer_id")
	}
	if standard == "" {
		return errCapJSON("p2p.latest_dataset requires standard")
	}

	schemaName := ""
	if a.opts.SchemaForStandard != nil {
		schemaName = strings.TrimSpace(a.opts.SchemaForStandard(standard))
	}
	if schemaName == "" {
		return modulert.PreEncodedEnvelope(map[string]interface{}{
			"ok": true,
			"result": map[string]interface{}{
				"known":  false,
				"reason": "unknown-standard",
			},
		}, nil)
	}

	candidates := a.latestBatchCandidates(peerID, schemaName, a.opts.LatestBatchScan)
	if len(candidates) == 0 {
		return modulert.PreEncodedEnvelope(map[string]interface{}{
			"ok": true,
			"result": map[string]interface{}{
				"known":    false,
				"reason":   "no-publications",
				"standard": strings.TrimSuffix(schemaName, ".fbs"),
				"schema":   schemaName,
			},
		}, nil)
	}
	newest := candidates[0]

	self := a.opts.SelfID != "" && peerID == a.opts.SelfID
	pinned := false
	if a.opts.PinnedDataset != nil {
		pinned = a.opts.PinnedDataset(peerID, schemaName)
	}
	base := map[string]interface{}{
		"known":    true,
		"pinned":   pinned,
		"self":     self,
		"standard": strings.TrimSuffix(schemaName, ".fbs"),
		"schema":   schemaName,
		"pnm":      pnmPointerJSON(newest, schemaName),
	}
	if !pinned && !self {
		base["reason"] = "not-pinned"
		return modulert.PreEncodedEnvelope(map[string]interface{}{"ok": true, "result": base}, nil)
	}

	if a.opts.LatestDatasetBatch != nil {
		for i, candidate := range candidates {
			batch, ok := a.opts.LatestDatasetBatch(schemaName, candidate.BatchID, true)
			if !ok || batch == nil {
				continue
			}
			serving := map[string]interface{}{
				"batch_id":     batch.BatchID,
				"provider_id":  batch.ProviderID,
				"source_name":  batch.SourceName,
				"record_count": batch.RecordCount,
				"byte_count":   len(batch.Bytes),
				"parts":        batch.Parts,
				"published_at": batch.PublishedAt,
				"pnm":          pnmPointerJSON(candidate, schemaName),
				"etag_fnv1a64": fmt.Sprintf("%016x", batch.FNV1a64),
			}
			base["serving"] = serving
			base["fresh"] = i == 0
			if deliver == "ref" && bridge != nil {
				serving["ref"] = map[string]interface{}{
					"token":   bridge.PutBodyRef(batch.Bytes),
					"size":    len(batch.Bytes),
					"frames":  batch.RecordCount,
					"fnv1a64": fmt.Sprintf("%016x", batch.FNV1a64),
				}
				return modulert.PreEncodedEnvelope(map[string]interface{}{"ok": true, "result": base}, nil)
			}
			serving["stream"] = map[string]interface{}{"$bin": 0}
			return modulert.PreEncodedEnvelope(map[string]interface{}{"ok": true, "result": base}, [][]byte{batch.Bytes})
		}
	}

	base["reason"] = "not-materialized"
	return modulert.PreEncodedEnvelope(map[string]interface{}{"ok": true, "result": base}, nil)
}
