package epm

import (
	"crypto/rand"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/protocol"
	ma "github.com/multiformats/go-multiaddr"
	manet "github.com/multiformats/go-multiaddr/net"

	"github.com/spacedatanetwork/sdn-server/internal/peers"
	"github.com/spacedatanetwork/sdn-server/internal/syncwire"
)

const PeerDirectoryProtocolID protocol.ID = "/space-data-network/peer-directory/1.0.0"

const (
	directoryPageRows       = 128
	directoryPageBytes      = 1 << 20
	directorySnapshotRows   = 4096
	directorySnapshotBytes  = 2 << 20
	directorySnapshots      = 16
	directoryReadsPerMinute = 32
	directoryAddrCount      = 8
	directoryProtocolCount  = 16
)

// The public projection is an allowlist. In particular it never serializes a
// TrustedPeer, graph node, metadata map, pin, name override, or trust level.
// Repeated typed columns bound each row without introducing a text encoding.
var directoryColumns = func() []syncwire.QRPColumnField {
	c := []syncwire.QRPColumnField{{Name: "PEER_ID", Type: "string"}, {Name: "AGENT", Type: "string"}, {Name: "PROFILE_CID", Type: "string"}, {Name: "CONNECTED", Type: "int64"}, {Name: "LAST_SEEN_MS", Type: "int64"}, {Name: "EVIDENCE", Type: "string"}}
	for i := 0; i < directoryAddrCount; i++ {
		c = append(c, syncwire.QRPColumnField{Name: fmt.Sprintf("MULTIADDR_%d", i), Type: "string"})
	}
	for i := 0; i < directoryProtocolCount; i++ {
		c = append(c, syncwire.QRPColumnField{Name: fmt.Sprintf("SDN_PROTOCOL_%d", i), Type: "string"})
	}
	return c
}()

type directorySnapshot struct {
	owner     peer.ID
	created   time.Time
	rows      [][]syncwire.QRPCell
	truncated bool
}
type directoryCursor struct {
	snapshot *directorySnapshot
	offset   int
}
type directoryQuota struct {
	since time.Time
	reads int
}

// PeerDirectory serves immutable, peer-bound snapshots for two minutes. Global
// snapshot and quota caps bound memory even when requesters change identities.
type PeerDirectory struct {
	host      host.Host
	registry  *peers.Registry
	mu        sync.Mutex
	cursors   map[string]directoryCursor
	snapshots []*directorySnapshot
	quotas    map[peer.ID]directoryQuota
}

func RegisterPeerDirectory(h host.Host, registry *peers.Registry) *PeerDirectory {
	d := &PeerDirectory{host: h, registry: registry, cursors: make(map[string]directoryCursor), quotas: make(map[peer.ID]directoryQuota)}
	h.SetStreamHandler(PeerDirectoryProtocolID, d.handle)
	return d
}

func (d *PeerDirectory) handle(s network.Stream) {
	defer s.Close()
	// FlatBuffers accessors panic on invalid offsets. Malformed remote input
	// resets the stream; it must not terminate the node or become an empty page.
	defer func() {
		if failure := recover(); failure != nil {
			_ = s.Reset()
			log.Warnf("peer-directory request refused: invalid buffer: %v", failure)
		}
	}()
	_ = s.SetDeadline(time.Now().Add(10 * time.Second))
	d.mu.Lock()
	now := time.Now()
	d.expire(now)
	owner := s.Conn().RemotePeer()
	q, known := d.quotas[owner]
	if (!known && len(d.quotas) >= 1024) || q.reads >= directoryReadsPerMinute {
		d.mu.Unlock()
		_, _ = s.Write(syncwire.ErrorFrame(syncwire.QRPStatusBusy, "rate_limited", "Directory read limit reached", 429, time.Minute))
		return
	}
	if !known {
		q.since = now
	}
	q.reads++
	d.quotas[owner] = q
	d.mu.Unlock()
	frame, err := syncwire.ReadFrame(s, 4096)
	if err != nil {
		_ = s.Reset()
		return
	}
	req, err := syncwire.ParseQRP(frame)
	if err != nil {
		_ = s.Reset()
		return
	}
	if int8(req.KIND()) != syncwire.QRPKindRequest || req.LIMIT() > directoryPageRows || len(req.CURSOR()) > 64 {
		_, _ = s.Write(syncwire.ErrorFrameFor(fmt.Errorf("invalid directory request")))
		return
	}
	limit := int(req.LIMIT())
	if limit == 0 {
		limit = directoryPageRows
	}
	page, err := d.page(owner, now, limit, string(req.CURSOR()))
	if err != nil {
		_ = s.Reset()
		return
	}
	_, _ = s.Write(page)
}

func (d *PeerDirectory) page(owner peer.ID, now time.Time, limit int, cursor string) ([]byte, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.expire(now)
	var snap *directorySnapshot
	offset := 0
	if cursor != "" {
		c, ok := d.cursors[cursor]
		if !ok || c.snapshot.owner != owner {
			return syncwire.ErrorFrameFor(fmt.Errorf("invalid or expired directory cursor")), nil
		}
		snap, offset = c.snapshot, c.offset
	} else {
		if len(d.snapshots) >= directorySnapshots {
			return syncwire.ErrorFrame(syncwire.QRPStatusBusy, "snapshot_limit", "Directory snapshot limit reached", 503, 2*time.Minute), nil
		}
		snap = d.project(owner, now)
		d.snapshots = append(d.snapshots, snap)
	}
	end := min(offset+limit, len(snap.rows))
	next := ""
	if end < len(snap.rows) {
		next = rand.Text()
	}
	for {
		page := syncwire.BuildQRP(syncwire.QRPFields{Kind: syncwire.QRPKindPage, ProducerPeerID: d.host.ID().String(), AsOfMs: snap.created.UnixMilli(), NextCursor: next, RecordCount: uint32(end - offset), TotalCount: uint64(len(snap.rows)), Truncated: snap.truncated, Columns: directoryColumns, Rows: snap.rows[offset:end]})
		if len(page) <= directoryPageBytes {
			if next != "" {
				d.cursors[next] = directoryCursor{snap, end}
			}
			return page, nil
		}
		end--
		if end <= offset {
			return nil, fmt.Errorf("directory row exceeds page bound")
		}
		if next == "" {
			next = rand.Text()
		}
	}
}

func (d *PeerDirectory) expire(now time.Time) {
	kept := d.snapshots[:0]
	for _, s := range d.snapshots {
		if now.Sub(s.created) < 2*time.Minute {
			kept = append(kept, s)
		}
	}
	clear(d.snapshots[len(kept):])
	d.snapshots = kept
	for token, c := range d.cursors {
		if now.Sub(c.snapshot.created) >= 2*time.Minute {
			delete(d.cursors, token)
		}
	}
	for id, q := range d.quotas {
		if now.Sub(q.since) >= time.Minute {
			delete(d.quotas, id)
		}
	}
}

func (d *PeerDirectory) project(owner peer.ID, now time.Time) *directorySnapshot {
	graph := BuildGraphSnapshot(d.host, d.registry)
	observed := BuildObservedSDNPeers(graph, d.registry.ListPeers(), nil, nil)
	sort.Slice(observed, func(i, j int) bool { return observed[i].ID.String() < observed[j].ID.String() })
	snap := &directorySnapshot{owner: owner, created: now}
	snapshotBytes := 0
	for _, p := range observed {
		connected := d.host.Network().Connectedness(p.ID) == network.Connected
		// Pins alone are private operator knowledge, not observed evidence.
		if !connected && p.LastSeen.IsZero() && p.LastConnected.IsZero() {
			continue
		}
		addresses := mergePeerAddrs(nil, p, nil)
		addresses = append(addresses, d.host.Peerstore().Addrs(p.ID)...)
		public := []string{}
		for _, a := range addresses {
			if publicBrowserAddr(a, p.ID) {
				public = append(public, a.String())
			}
		}
		public = uniqueStrings(public)
		if len(public) == 0 {
			continue
		}
		if len(snap.rows) == directorySnapshotRows {
			snap.truncated = true
			break
		}
		protocols, _ := d.host.Peerstore().GetProtocols(p.ID)
		protos := strings.Split(p.Metadata["protocols"], ",")
		for _, p := range protocols {
			protos = append(protos, string(p))
		}
		selected := []string{}
		for _, p := range uniqueStrings(protos) {
			if len(p) <= 128 && (strings.HasPrefix(p, "/space-data-network/") || strings.HasPrefix(p, "/spacedatanetwork/")) {
				selected = append(selected, p)
			}
		}
		agent := p.Metadata["agent_version"]
		if len(agent) > 256 {
			agent = ""
		}
		if !isSDNAgentVersion(agent) && len(selected) == 0 {
			continue
		}
		profile := ""
		if len(p.EPMData) > 0 {
			profile, _ = ComputeEPMCID(p.EPMData)
		}
		last := p.LastSeen
		if p.LastConnected.After(last) {
			last = p.LastConnected
		}
		flag := int64(0)
		evidence := "seen"
		if connected {
			flag = 1
			evidence = "connected"
			last = now
		}
		lastMs := int64(0)
		if !last.IsZero() {
			lastMs = last.UnixMilli()
		}
		row := []syncwire.QRPCell{syncwire.TextCell(p.ID.String()), syncwire.TextCell(agent), syncwire.TextCell(profile), syncwire.IntCell(flag), syncwire.IntCell(lastMs), syncwire.TextCell(evidence)}
		for i := 0; i < directoryAddrCount; i++ {
			v := ""
			if i < len(public) {
				v = public[i]
			}
			row = append(row, syncwire.TextCell(v))
		}
		for i := 0; i < directoryProtocolCount; i++ {
			v := ""
			if i < len(selected) {
				v = selected[i]
			}
			row = append(row, syncwire.TextCell(v))
		}
		// Include conservative cell/slice overhead as well as retained text.
		rowBytes := len(row) * 128
		for _, cell := range row {
			rowBytes += len(cell.Text)
		}
		if snapshotBytes+rowBytes > directorySnapshotBytes {
			snap.truncated = true
			break
		}
		snapshotBytes += rowBytes
		snap.rows = append(snap.rows, row)
	}
	return snap
}

func publicBrowserAddr(a ma.Multiaddr, id peer.ID) bool {
	if len(a.String()) > 512 || !manet.IsPublicAddr(a) {
		return false
	}
	secure, ws, webtransport, webrtc, certHash := false, false, false, false, false
	valid := true
	ma.ForEach(a, func(c ma.Component) bool {
		switch c.Protocol().Code {
		case ma.P_DNS, ma.P_DNS4, ma.P_DNS6, ma.P_SNI:
			name := strings.ToLower(strings.TrimSuffix(c.Value(), "."))
			if !strings.Contains(name, ".") || !manet.IsPublicAddr(ma.StringCast("/dns4/"+name)) || strings.HasSuffix(name, ".local") || strings.HasSuffix(name, ".internal") || strings.HasSuffix(name, ".localhost") {
				valid = false
			}
		case ma.P_IP4, ma.P_IP6:
			if !manet.IsPublicAddr(ma.Multiaddr{c}) {
				valid = false
			}
		case ma.P_TLS:
			secure = true
		case ma.P_WSS:
			secure = true
			ws = true
		case ma.P_WS:
			ws = true
		case ma.P_WEBTRANSPORT:
			webtransport = true
		case ma.P_WEBRTC_DIRECT:
			webrtc = true
		case ma.P_CERTHASH:
			certHash = true
		case ma.P_P2P:
			if c.Value() != id.String() {
				valid = false
			}
		case ma.P_CIRCUIT, ma.P_DNSADDR, ma.P_IP6ZONE:
			valid = false
		}
		return valid
	})
	return valid && ((ws && secure) || ((webtransport || webrtc) && certHash))
}
