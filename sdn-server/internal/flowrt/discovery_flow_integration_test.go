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

func discoveryFlowDist(t *testing.T, bundle string) string {
	t.Helper()
	root := os.Getenv("SDN_DISCOVERY_FLOW_DIST")
	if root == "" {
		root = filepath.Join("..", "..", "..", "..",
			"space-data-network-modules", "flows", "discovery", "dist")
	}
	dist := filepath.Join(root, bundle)
	if _, err := os.Stat(filepath.Join(dist, "runtime.wasm")); err != nil {
		t.Skipf("discovery flow bundle not found at %s (set SDN_DISCOVERY_FLOW_DIST): %v", dist, err)
	}
	return dist
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
	reg.Register("p2p_read", caps.NewP2PCapFactory(caps.P2PCapOptions{
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
			CapRegistry:    discoveryCapRegistry(t),
			NodeCtx:        &modulert.NodeContext{},
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

	var peersEtag string

	t.Run("GET /api/v1/peers (no trailing slash, NO redirect) streams $EPM frames", func(t *testing.T) {
		resp, body := discoveryGET(t, srv.URL+"/api/v1/peers", nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d (a 301 here means the exact-path alias regressed), body %q", resp.StatusCode, body)
		}
		if ct := resp.Header.Get("Content-Type"); ct != "application/vnd.sdn.flatbuffers.stream" {
			t.Fatalf("content-type = %q", ct)
		}
		if rc := resp.Header.Get("X-Sdn-Record-Count"); rc != "3" {
			t.Fatalf("x-sdn-record-count = %q, want 3 (self + celestrak + dht peer)", rc)
		}
		peersEtag = resp.Header.Get("ETag")
		if !strings.HasPrefix(peersEtag, `W/"fnv1a64-`) {
			t.Fatalf("etag = %q", peersEtag)
		}
		frames := splitSizePrefixedFrames(t, body)
		if len(frames) != 3 {
			t.Fatalf("frames = %d, want 3", len(frames))
		}
		for i, frame := range frames {
			if !EPM.SizePrefixedEPMBufferHasIdentifier(frame) {
				t.Fatalf("frame %d is not a size-prefixed $EPM buffer", i)
			}
		}
		// The DHT-only peer gets a synthesized profile carrying its peer id.
		synthesized := EPM.GetSizePrefixedRootAsEPM(frames[2], 0)
		if got := string(synthesized.DN()); got != discoveryDHTPeerID {
			t.Fatalf("synthesized DN = %q", got)
		}
	})

	t.Run("format=json is a bare array carrying peerID + standards", func(t *testing.T) {
		resp, body := discoveryGET(t, srv.URL+"/api/v1/peers?format=json", nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d body %q", resp.StatusCode, body)
		}
		if ct := resp.Header.Get("Content-Type"); ct != "application/json" {
			t.Fatalf("content-type = %q", ct)
		}
		if resp.Header.Get("ETag") != peersEtag {
			t.Fatalf("json etag %q != fb etag %q (must be the SAME logical stream tag)",
				resp.Header.Get("ETag"), peersEtag)
		}
		var records []map[string]interface{}
		if err := json.Unmarshal(body, &records); err != nil {
			t.Fatalf("body is not a bare JSON array: %v (%q)", err, body)
		}
		if len(records) != 3 {
			t.Fatalf("records = %d", len(records))
		}
		var celestrak map[string]interface{}
		for _, record := range records {
			if record["peer_id"] == discoveryCelestrakID {
				celestrak = record
			}
		}
		if celestrak == nil {
			t.Fatalf("celestrak peer missing from %v", records)
		}
		standards, _ := celestrak["standards"].([]interface{})
		if len(standards) != 2 || standards[0] != "CAT" || standards[1] != "OMM" {
			t.Fatalf("celestrak standards = %v", standards)
		}
		epm, _ := celestrak["epm"].(map[string]interface{})
		if epm == nil {
			t.Fatalf("celestrak epm missing")
		}
		names, _ := epm["alternate_names"].([]interface{})
		if len(names) != 1 || names[0] != "celestrak.eth" {
			t.Fatalf("celestrak alternate_names = %v", names)
		}
	})

	t.Run("GET /api/v1/peers/{peerId} narrows; unknown 404; If-None-Match 304", func(t *testing.T) {
		resp, body := discoveryGET(t, srv.URL+"/api/v1/peers/"+discoveryCelestrakID, nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d body %q", resp.StatusCode, body)
		}
		if rc := resp.Header.Get("X-Sdn-Record-Count"); rc != "1" {
			t.Fatalf("x-sdn-record-count = %q, want 1", rc)
		}
		frames := splitSizePrefixedFrames(t, body)
		if len(frames) != 1 {
			t.Fatalf("frames = %d", len(frames))
		}
		profile := EPM.GetSizePrefixedRootAsEPM(frames[0], 0)
		if got := string(profile.ALTERNATE_NAMES(0)); got != "celestrak.eth" {
			t.Fatalf("stored profile not spliced verbatim: alternate name %q", got)
		}
		etag := resp.Header.Get("ETag")

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

	t.Run("GET /api/v1/standards streams the newest $PNM per (peer, standard)", func(t *testing.T) {
		resp, body := discoveryGET(t, srv.URL+"/api/v1/standards", nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d body %q", resp.StatusCode, body)
		}
		if rc := resp.Header.Get("X-Sdn-Record-Count"); rc != "2" {
			t.Fatalf("x-sdn-record-count = %q, want 2 (CAT + newest OMM)", rc)
		}
		frames := splitSizePrefixedFrames(t, body)
		if len(frames) != 2 {
			t.Fatalf("frames = %d", len(frames))
		}
		for i, frame := range frames {
			if !PNM.SizePrefixedPNMBufferHasIdentifier(frame) {
				t.Fatalf("frame %d is not $PNM", i)
			}
		}
		// Newest-per-pair: the OMM frame must be the 2026-07-06 publication.
		omm := PNM.GetSizePrefixedRootAsPNM(frames[1], 0)
		if got := string(omm.CID()); got != "bafy-omm-new" {
			t.Fatalf("expected the newest OMM PNM, got CID %q", got)
		}

		respJSON, bodyJSON := discoveryGET(t, srv.URL+"/api/v1/standards?format=json", nil)
		if respJSON.StatusCode != http.StatusOK {
			t.Fatalf("json status = %d", respJSON.StatusCode)
		}
		var entries []map[string]interface{}
		if err := json.Unmarshal(bodyJSON, &entries); err != nil {
			t.Fatalf("bare array: %v (%q)", err, bodyJSON)
		}
		if len(entries) != 2 || entries[0]["standard"] != "CAT" || entries[1]["standard"] != "OMM" {
			t.Fatalf("entries = %v", entries)
		}
		if entries[1]["peer_id"] != discoveryCelestrakID {
			t.Fatalf("publisher attribution missing: %v", entries[1])
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
