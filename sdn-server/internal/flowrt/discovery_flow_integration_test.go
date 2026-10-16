package flowrt

// Gateway loop G.2 integration: the REAL compiled discovery flow bundles
// (space-data-network-modules/flows/discovery/dist/{peers,standards}, bridge
// linkage) are mounted at their PRODUCTION paths (/api/v1/peers/ +
// /api/v1/standards) through the config mount table and served by a real
// HTTP listener. The Go host is pure socket plumbing plus the policy-mediated
// p2p_read capability; every routing / format / ETag / 404 decision is made
// INSIDE the wasm flows. Fixtures mirror the production topology: a self
// node, the celestrak.eth provider peer with a stored signed EPM and PNMs
// for OMM/CAT, and an anonymous DHT peer without a profile.

import (
	"crypto/ed25519"
	"encoding/binary"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	flatbuffers "github.com/google/flatbuffers/go"
	libp2pcrypto "github.com/libp2p/go-libp2p/core/crypto"

	EPM "github.com/DigitalArsenal/spacedatastandards.org/lib/go/EPM"
	PNM "github.com/DigitalArsenal/spacedatastandards.org/lib/go/PNM"

	"github.com/spacedatanetwork/sdn-server/internal/config"
	"github.com/spacedatanetwork/sdn-server/internal/modulert"
	"github.com/spacedatanetwork/sdn-server/internal/modulert/caps"
)

const (
	discoverySelfID      = "16Uiu2HAm1LbvwjEHW2GDP2ZQZvwHLZrz2jbYoRLQmJEQ3wZ5Fm45"
	discoveryCelestrakID = "16Uiu2HAm9oK2jAeVC2RMESFcYfq7BKGp2K2CCDxzoKhB5s9vpbj3"
	discoveryDHTPeerID   = "16Uiu2HAmDHTonlyPeerWithoutProfile"
)

// discoveryFlowDist locates one compiled discovery bundle. Roots are tried
// most specific first: the env override, the canonical sibling layout, and
// then modulesCheckoutCandidates — the same resolver the public-query suite
// uses, which follows a LINKED WORKTREE's gitdir back to the stack it belongs
// to. Without that last step every discovery test silently SKIPPED inside a
// worktree, which is how the publisher-key fixtures below went stale unnoticed
// after 982fc25d changed the wire format.
func discoveryFlowDist(t *testing.T, bundle string) string {
	t.Helper()
	// AN EXPLICIT OVERRIDE IS AN INSTRUCTION, NOT A HINT. When
	// SDN_DISCOVERY_FLOW_DIST is set, it is the ONLY root considered: whoever set
	// it is pointing at a specific build, and silently searching past a typo to
	// run a DIFFERENT bundle answers a question nobody asked. Before this, a
	// wrong path fell through to a discovered checkout and the run passed green
	// — demonstrated with SDN_DISCOVERY_FLOW_DIST=/nonexistent/typo.
	var roots []string
	if env := strings.TrimSpace(os.Getenv("SDN_DISCOVERY_FLOW_DIST")); env != "" {
		dist := filepath.Join(env, bundle)
		if _, err := os.Stat(filepath.Join(dist, "runtime.wasm")); err != nil {
			t.Fatalf("SDN_DISCOVERY_FLOW_DIST=%s has no %s/runtime.wasm (%v); "+
				"an explicit override is not searched past", env, bundle, err)
		}
		return dist
	}
	roots = append(roots, filepath.Join("..", "..", "..", "..",
		"space-data-network-modules", "flows", "discovery", "dist"))
	for _, checkout := range modulesCheckoutCandidates() {
		roots = append(roots, filepath.Join(checkout, "flows", "discovery", "dist"))
	}
	for _, root := range roots {
		dist := filepath.Join(root, bundle)
		if _, err := os.Stat(filepath.Join(dist, "runtime.wasm")); err == nil {
			return dist
		}
	}
	t.Skipf("discovery flow %s bundle not found (set SDN_DISCOVERY_FLOW_DIST); checked %v", bundle, roots)
	return ""
}

// discoveryPublisherKey encodes an Ed25519 public key the way the NODE hands
// publisher keys to the p2p_read capability: libp2p protobuf
// (crypto.MarshalPublicKey), never the raw 32 bytes. Node.buildP2PCapOptions
// marshals every candidate key through appendKey (internal/node/node.go), and
// since 982fc25d the capability unmarshals it with crypto.UnmarshalPublicKey so
// the key carries its ALGORITHM — a raw Ed25519 key is invalid protobuf and
// every attribution attempt against it fails, which reads on the wire as an
// empty publication history.
func discoveryPublisherKey(t *testing.T, pub ed25519.PublicKey) []byte {
	t.Helper()
	key, err := libp2pcrypto.UnmarshalEd25519PublicKey(pub)
	if err != nil {
		t.Fatalf("unmarshal ed25519 public key: %v", err)
	}
	raw, err := libp2pcrypto.MarshalPublicKey(key)
	if err != nil {
		t.Fatalf("marshal publisher key: %v", err)
	}
	return raw
}

func buildDiscoveryEPM(t *testing.T, dn string, alternateNames, addrs []string) []byte {
	t.Helper()
	b := flatbuffers.NewBuilder(512)
	dnOff := b.CreateString(dn)
	var altVec, addrVec flatbuffers.UOffsetT
	if len(alternateNames) > 0 {
		offs := make([]flatbuffers.UOffsetT, len(alternateNames))
		for i, name := range alternateNames {
			offs[i] = b.CreateString(name)
		}
		EPM.EPMStartALTERNATE_NAMESVector(b, len(offs))
		for i := len(offs) - 1; i >= 0; i-- {
			b.PrependUOffsetT(offs[i])
		}
		altVec = b.EndVector(len(offs))
	}
	if len(addrs) > 0 {
		offs := make([]flatbuffers.UOffsetT, len(addrs))
		for i, addr := range addrs {
			offs[i] = b.CreateString(addr)
		}
		EPM.EPMStartMULTIFORMAT_ADDRESSVector(b, len(offs))
		for i := len(offs) - 1; i >= 0; i-- {
			b.PrependUOffsetT(offs[i])
		}
		addrVec = b.EndVector(len(offs))
	}
	EPM.EPMStart(b)
	EPM.EPMAddDN(b, dnOff)
	if altVec != 0 {
		EPM.EPMAddALTERNATE_NAMES(b, altVec)
	}
	if addrVec != 0 {
		EPM.EPMAddMULTIFORMAT_ADDRESS(b, addrVec)
	}
	// SIGNATURE_TIMESTAMP (int64): 8-byte scalars are aligned relative to
	// the size-PREFIXED buffer start — regression guard for the shape
	// node's size-prefixed frame verification (real signed EPMs carry it).
	EPM.EPMAddSIGNATURE_TIMESTAMP(b, 1751793600)
	EPM.EPMAddENTITY_TYPE(b, EPM.EntityTypeNode)
	b.FinishSizePrefixedWithFileIdentifier(EPM.EPMEnd(b), []byte(EPM.EPMIdentifier))
	return b.FinishedBytes()
}

func buildDiscoveryPNM(t *testing.T, fileID, cid, ts string) []byte {
	t.Helper()
	b := flatbuffers.NewBuilder(512)
	fileIDOff := b.CreateString(fileID)
	cidOff := b.CreateString(cid)
	tsOff := b.CreateString(ts)
	PNM.PNMStart(b)
	PNM.PNMAddFILE_ID(b, fileIDOff)
	PNM.PNMAddCID(b, cidOff)
	PNM.PNMAddPUBLISH_TIMESTAMP(b, tsOff)
	b.FinishSizePrefixedWithFileIdentifier(PNM.PNMEnd(b), []byte(PNM.PNMIdentifier))
	return b.FinishedBytes()
}

// discoveryCapRegistry wires the p2p_read capability exactly like
// Node.buildCapRegistry, over fixture closures.
func discoveryCapRegistry(t *testing.T) *modulert.CapabilityRegistry {
	t.Helper()
	selfEPM := buildDiscoveryEPM(t, "sdn.spaceaware.io", nil, []string{"/p2p/" + discoverySelfID})
	celestrakEPM := buildDiscoveryEPM(t, "celestrak", []string{"celestrak.eth"},
		[]string{"/p2p/" + discoveryCelestrakID, "/ip4/167.172.219.213/tcp/4001"})
	pnms := []caps.P2PPNMRecord{
		{PeerID: discoveryCelestrakID, Data: buildDiscoveryPNM(t, "celestrak:gp:OMM.fbs:2026-07-06T03:00:00Z", "bafy-omm-new", "2026-07-06T03:00:00Z")},
		{PeerID: discoveryCelestrakID, Data: buildDiscoveryPNM(t, "celestrak:gp:OMM.fbs:2026-07-05T03:00:00Z", "bafy-omm-old", "2026-07-05T03:00:00Z")},
		{PeerID: discoveryCelestrakID, Data: buildDiscoveryPNM(t, "celestrak:satcat:CAT.fbs:2026-07-06T02:00:00Z", "bafy-cat", "2026-07-06T02:00:00Z")},
	}
	reg := modulert.NewCapabilityRegistry()
	reg.RegisterBridgeAware("p2p_read", caps.NewP2PCapFactory(caps.P2PCapOptions{
		SelfID:           discoverySelfID,
		SelfAgentVersion: "spacedatanetwork/test",
		SelfAddrs:        func() []string { return []string{"/ip4/104.131.11.220/tcp/4001"} },
		SelfEPM:          func() []byte { return selfEPM },
		Peers: func() []caps.P2PPeerInfo {
			return []caps.P2PPeerInfo{
				{ID: discoveryCelestrakID, Addrs: []string{"/ip4/167.172.219.213/tcp/4001"}, Connected: true, AgentVersion: "spacedatanetwork/1.0.4"},
				{ID: discoveryDHTPeerID, Connected: false},
			}
		},
		PeerEPM: func(peerID string) []byte {
			if peerID == discoveryCelestrakID {
				return celestrakEPM
			}
			return nil
		},
		RecentPNMs: func(limit int) []caps.P2PPNMRecord { return pnms },
	}))
	return reg
}

// noRedirectClient fails the test on ANY redirect: API paths never redirect
// (docs/gateway-api.md §4.1).
func noRedirectClient() *http.Client {
	return &http.Client{
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

func discoveryGET(t *testing.T, url string, headers map[string]string) (*http.Response, []byte) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := noRedirectClient().Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	body, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return resp, body
}

func splitSizePrefixedFrames(t *testing.T, body []byte) [][]byte {
	t.Helper()
	frames := make([][]byte, 0, 8)
	for off := 0; off+4 <= len(body); {
		n := int(binary.LittleEndian.Uint32(body[off:]))
		off += 4
		if n == 0 {
			continue
		}
		if off+n > len(body) {
			t.Fatalf("malformed stream: frame overruns body at offset %d", off)
		}
		frames = append(frames, body[off-4:off+n])
		off += n
	}
	return frames
}

func TestHTTPMountedDiscoveryFlows(t *testing.T) {
	peersDist := discoveryFlowDist(t, "peers")
	standardsDist := discoveryFlowDist(t, "standards")

	mux := http.NewServeMux()
	mounted, err := RegisterFlowMounts(mux,
		[]config.FlowMount{
			{Path: "/api/v1/peers/", Flow: peersDist, Pool: 1},
			{Path: "/api/v1/standards", Flow: standardsDist, Pool: 1},
		},
		FlowMountDeps{
			CapRegistry: discoveryCapRegistry(t),
			// The node's own peer id answers node.peerId (the $PNV
			// VERIFIER_PEER_ID), as Node hands every mount.
			NodeCtx:        &modulert.NodeContext{PeerID: discoverySelfID},
			MaxMemoryPages: 2048,
		})
	if err != nil {
		t.Fatalf("RegisterFlowMounts: %v", err)
	}
	defer func() {
		for _, mf := range mounted {
			mf.Close()
		}
	}()
	if len(mounted) != 2 {
		t.Fatalf("mounted %d flows, want 2", len(mounted))
	}

	// The api blocks travel with the bundles (OpenAPI auto-shadow input).
	peersDoc := mounted[0].APIDoc()
	if peersDoc == nil || len(peersDoc.Routes) != 2 || !peersDoc.Routes[0].Anonymous {
		t.Fatalf("peers bundle api block wrong: %+v", peersDoc)
	}
	standardsDoc := mounted[1].APIDoc()
	if standardsDoc == nil || len(standardsDoc.Routes) != 1 || !standardsDoc.Routes[0].Anonymous {
		t.Fatalf("standards bundle api block wrong: %+v", standardsDoc)
	}

	srv := httptest.NewServer(mux)
	defer srv.Close()

	nodesByID := func(nst sdsTable) map[string]sdsTable {
		out := map[string]sdsTable{}
		for _, node := range nst.tables(nstNODES) {
			out[node.str(nodePEER_ID)] = node
		}
		return out
	}

	var peersEtag string

	t.Run("GET /api/v1/peers (no trailing slash, NO redirect) answers one $NST", func(t *testing.T) {
		resp, body := discoveryGET(t, srv.URL+"/api/v1/peers", nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d (a 301 here means the exact-path alias regressed), body %q", resp.StatusCode, body)
		}
		if ct, id := resp.Header.Get("Content-Type"), resp.Header.Get("X-Sdn-File-Identifier"); ct != "application/x-flatbuffers" || id != "$NST" {
			t.Fatalf("content-type = %q, x-sdn-file-identifier = %q", ct, id)
		}
		nst := readSDSRecord(t, body, "$NST")
		peersEtag = resp.Header.Get("ETag")
		if !strings.HasPrefix(peersEtag, `W/"fnv1a64-`) || peersEtag != nst.str(nstETAG) {
			t.Fatalf("etag = %q, $NST ETAG = %q (the record's ETAG is the entity tag)", peersEtag, nst.str(nstETAG))
		}
		if got := nst.str(nstSOURCE_PEER_ID); got != discoverySelfID {
			t.Fatalf("SOURCE_PEER_ID = %q", got)
		}
		nodes := nodesByID(nst)
		if len(nst.tables(nstNODES)) != 3 || len(nodes) != 3 {
			t.Fatalf("NODES = %d, want 3 (self + celestrak + dht peer)", len(nst.tables(nstNODES)))
		}
		if self := nodes[discoverySelfID]; !self.flag(nodeIS_SELF) || self.str(nodeDN) != "sdn.spaceaware.io" {
			t.Fatalf("self node: is_self %v dn %q", self.flag(nodeIS_SELF), self.str(nodeDN))
		}
		celestrak := nodes[discoveryCelestrakID]
		if celestrak.str(nodeDN) != "celestrak" || !celestrak.flag(nodeIS_ONLINE) {
			t.Fatalf("celestrak node: dn %q online %v (identity from its stored $EPM)", celestrak.str(nodeDN), celestrak.flag(nodeIS_ONLINE))
		}
		if got := strings.Join(celestrak.strs(nodeSTANDARDS), ","); got != "CAT,OMM" {
			t.Fatalf("celestrak STANDARDS = %q", got)
		}
		// The DHT-only peer has no stored profile: present by peer id alone.
		if dht, ok := nodes[discoveryDHTPeerID]; !ok || dht.str(nodeDN) != "" {
			t.Fatalf("dht peer missing or carries a DN it never published")
		}
	})

	t.Run("format=json renders the same $NST with its IDL field names and the same etag", func(t *testing.T) {
		resp, body := discoveryGET(t, srv.URL+"/api/v1/peers?format=json", nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d body %q", resp.StatusCode, body)
		}
		if ct := resp.Header.Get("Content-Type"); ct != "application/json" {
			t.Fatalf("content-type = %q", ct)
		}
		if resp.Header.Get("ETag") != peersEtag {
			t.Fatalf("json etag %q != fb etag %q (one record, one entity tag)", resp.Header.Get("ETag"), peersEtag)
		}
		var nst struct {
			SourcePeerID string `json:"SOURCE_PEER_ID"`
			ETag         string `json:"ETAG"`
			Nodes        []struct {
				PeerID    string   `json:"PEER_ID"`
				DN        *string  `json:"DN"`
				Standards []string `json:"STANDARDS"`
			} `json:"NODES"`
		}
		if err := json.Unmarshal(body, &nst); err != nil {
			t.Fatalf("body is not the $NST as JSON: %v (%q)", err, body)
		}
		if nst.SourcePeerID != discoverySelfID || nst.ETag != peersEtag || len(nst.Nodes) != 3 {
			t.Fatalf("json $NST: source %q etag %q nodes %d", nst.SourcePeerID, nst.ETag, len(nst.Nodes))
		}
		for _, node := range nst.Nodes {
			if node.PeerID == discoveryCelestrakID && (node.DN == nil || *node.DN != "celestrak" || strings.Join(node.Standards, ",") != "CAT,OMM") {
				t.Fatalf("celestrak node: %+v", node)
			}
		}
	})

	t.Run("GET /api/v1/peers/{peerId} narrows; unknown 404; If-None-Match 304", func(t *testing.T) {
		resp, body := discoveryGET(t, srv.URL+"/api/v1/peers/"+discoveryCelestrakID, nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d body %q", resp.StatusCode, body)
		}
		nst := readSDSRecord(t, body, "$NST")
		nodes := nst.tables(nstNODES)
		if len(nodes) != 1 || nodes[0].str(nodePEER_ID) != discoveryCelestrakID || nodes[0].str(nodeDN) != "celestrak" {
			t.Fatalf("narrowed $NST: %d nodes", len(nodes))
		}
		etag := resp.Header.Get("ETag")
		if etag == "" || etag != nst.str(nstETAG) {
			t.Fatalf("etag = %q, $NST ETAG = %q", etag, nst.str(nstETAG))
		}

		resp, _ = discoveryGET(t, srv.URL+"/api/v1/peers/"+discoveryCelestrakID,
			map[string]string{"If-None-Match": etag})
		if resp.StatusCode != http.StatusNotModified {
			t.Fatalf("conditional GET status = %d, want 304", resp.StatusCode)
		}

		resp, body = discoveryGET(t, srv.URL+"/api/v1/peers/16Uiu2NobodyKnown", nil)
		if resp.StatusCode != http.StatusNotFound {
			t.Fatalf("unknown peer status = %d body %q", resp.StatusCode, body)
		}
	})

	t.Run("GET /api/v1/standards answers one $PNV of the newest notice per (peer, standard)", func(t *testing.T) {
		resp, body := discoveryGET(t, srv.URL+"/api/v1/standards", nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d body %q", resp.StatusCode, body)
		}
		if id := resp.Header.Get("X-Sdn-File-Identifier"); id != "$PNV" {
			t.Fatalf("x-sdn-file-identifier = %q", id)
		}
		pnv := readSDSRecord(t, body, "$PNV")
		if got := pnv.str(pnvVERIFIER_PEER_ID); got != discoverySelfID {
			t.Fatalf("VERIFIER_PEER_ID = %q", got)
		}
		if etag := resp.Header.Get("ETag"); etag == "" || etag != pnv.str(pnvETAG) {
			t.Fatalf("etag = %q, $PNV ETAG = %q", etag, pnv.str(pnvETAG))
		}
		entries := pnv.tables(pnvENTRIES)
		if len(entries) != 2 || entries[0].str(entrySTANDARD) != "CAT" || entries[1].str(entrySTANDARD) != "OMM" {
			t.Fatalf("entries = %d, want 2 (CAT + newest OMM)", len(entries))
		}
		// Newest-per-pair: the OMM entry is the 2026-07-06 publication, its
		// received $PNM carried verbatim so a reader can verify it.
		omm := entries[1]
		if got := omm.str(entryCID); got != "bafy-omm-new" {
			t.Fatalf("expected the newest OMM notice, got CID %q", got)
		}
		if got := omm.str(entryPUBLISHER_PEER_ID); got != discoveryCelestrakID {
			t.Fatalf("publisher attribution = %q", got)
		}
		if raw := omm.bytes(entryPNM_BYTES); !PNM.SizePrefixedPNMBufferHasIdentifier(raw) ||
			string(PNM.GetSizePrefixedRootAsPNM(raw, 0).CID()) != "bafy-omm-new" {
			t.Fatalf("PNM_BYTES is not the received $PNM")
		}

		respJSON, bodyJSON := discoveryGET(t, srv.URL+"/api/v1/standards?format=json", nil)
		if respJSON.StatusCode != http.StatusOK {
			t.Fatalf("json status = %d", respJSON.StatusCode)
		}
		var pnvJSON struct {
			Entries []struct {
				Standard  string `json:"STANDARD"`
				Publisher string `json:"PUBLISHER_PEER_ID"`
			} `json:"ENTRIES"`
		}
		if err := json.Unmarshal(bodyJSON, &pnvJSON); err != nil {
			t.Fatalf("json $PNV: %v (%q)", err, bodyJSON)
		}
		if len(pnvJSON.Entries) != 2 || pnvJSON.Entries[0].Standard != "CAT" || pnvJSON.Entries[1].Publisher != discoveryCelestrakID {
			t.Fatalf("json entries = %+v", pnvJSON.Entries)
		}
		if respJSON.Header.Get("ETag") != resp.Header.Get("ETag") {
			t.Fatalf("standards etag differs across encodings")
		}
	})
	t.Run("POST and deeper per-peer paths answer 404 (no redirects anywhere)", func(t *testing.T) {
		req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/v1/peers", strings.NewReader("{}"))
		resp, err := noRedirectClient().Do(req)
		if err != nil {
			t.Fatalf("POST: %v", err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Fatalf("POST peers status = %d", resp.StatusCode)
		}
		respDeep, _ := discoveryGET(t, srv.URL+"/api/v1/peers/"+discoveryCelestrakID+"/pnm", nil)
		if respDeep.StatusCode != http.StatusNotFound {
			t.Fatalf("deep path status = %d (G.3 surface must 404 for now)", respDeep.StatusCode)
		}
	})
}
