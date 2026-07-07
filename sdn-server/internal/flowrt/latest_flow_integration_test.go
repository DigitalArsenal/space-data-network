package flowrt

// Gateway loop G.4 integration: the REAL compiled latest-dataset flow bundle
// (space-data-network-modules/flows/discovery/dist/latest, bridge linkage)
// is mounted at its PRODUCTION mux pattern
// (/api/v1/peers/{peerId}/{standard}/latest) ALONGSIDE the peers-discovery
// subtree, over the real p2p_read capability handler and the real hostcall
// bridge — so the deliver="ref" path exercises the whole C.5c chain:
// cap PutBodyRef -> wasm $sdnbodyref descriptor -> $HTR BODY_REF ->
// httpmount TakeBodyRef -> HTTP body bytes.

import (
	"crypto/ed25519"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/spacedatanetwork/sdn-server/internal/config"
	"github.com/spacedatanetwork/sdn-server/internal/flatsqlrt"
	"github.com/spacedatanetwork/sdn-server/internal/modulert"
	"github.com/spacedatanetwork/sdn-server/internal/modulert/caps"
	"github.com/spacedatanetwork/sdn-server/internal/sds"
)

// latestOMMStream builds a "published batch" fixture with the LIVE shard
// shape: STORED records are size-prefixed $OMM FlatBuffers (the ingest wire
// form), and the shard wraps each in an OUTER stream frame — so shard
// frames are double-prefixed ([u32 outer][u32 inner][buffer]). The fb path
// serves these bytes verbatim; the json path unwraps the redundant outer
// layer before omm-json.
func latestOMMStream(t *testing.T) []byte {
	t.Helper()
	stream := make([]byte, 0, 2048)
	var prefix [4]byte
	for i, name := range []string{"ISS (ZARYA)", "NOAA 19", "HST"} {
		record := sds.NewOMMBuilder().
			WithObjectName(name).
			WithNoradCatID(uint32(25544 + i)).
			WithMeanMotion(15.49 - float64(i)).
			WithEpochTimestamp(1783300000 + float64(i)*100).
			Build() // size-prefixed (the stored form)
		binary.LittleEndian.PutUint32(prefix[:], uint32(len(record)))
		stream = append(stream, prefix[:]...)
		stream = append(stream, record...)
	}
	return stream
}

func TestHTTPMountedLatestDatasetFlow(t *testing.T) {
	peersDist := discoveryFlowDist(t, "peers")
	latestDist := discoveryFlowDist(t, "latest")

	celestrakPub, celestrakPriv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}

	// Two signed publication batches (newest first by publish timestamp),
	// plus a CAT publication so /cat/latest is "known" but unpinned.
	const (
		newBatch = "f00dfeedbatchnew"
		oldBatch = "0ddba11batcholder"
	)
	newestPNM := buildSignedDiscoveryPNM(t, celestrakPriv,
		"sdn-OMM-full:OMM.fbs:"+newBatch+":part-000001", "bafy-manifest-new", "2026-07-06T06:00:00Z")
	olderPNM := buildSignedDiscoveryPNM(t, celestrakPriv,
		"sdn-OMM-full:OMM.fbs:"+oldBatch+":part-000001", "bafy-manifest-old", "2026-07-06T03:00:00Z")
	catPNM := buildSignedDiscoveryPNM(t, celestrakPriv,
		"sdn-CAT-full:CAT.fbs:"+newBatch+":part-000001", "bafy-manifest-cat", "2026-07-06T05:00:00Z")
	pnms := []caps.P2PPNMRecord{
		{PeerID: discoveryCelestrakID, Data: olderPNM},
		{PeerID: discoverySelfID, Data: newestPNM}, // relayed: signature reclaims it
		{PeerID: discoveryCelestrakID, Data: catPNM},
	}

	stream := latestOMMStream(t)
	fnv := flatsqlrt.FNV1a64WordFolded(stream)
	wantEtag := fmt.Sprintf("W/\"fnv1a64-%016x\"", fnv)

	// gateway.pin fixture: EXACTLY (celestrak, OMM) — the real config
	// predicate, so CAT stays honestly unpinned (opt-in proven).
	pinCfg := config.GatewayConfig{Pin: []config.GatewayPinEntry{
		{Peer: discoveryCelestrakID, Standard: "OMM"},
	}}

	reg := modulert.NewCapabilityRegistry()
	reg.RegisterBridgeAware("p2p_read", caps.NewP2PCapFactory(caps.P2PCapOptions{
		SelfID: discoverySelfID,
		Peers: func() []caps.P2PPeerInfo {
			return []caps.P2PPeerInfo{{ID: discoveryCelestrakID, Connected: true}}
		},
		RecentPNMs: func(limit int) []caps.P2PPNMRecord { return pnms },
		PublisherKeys: func(peerID string) []caps.P2PPublisherKey {
			if peerID == discoveryCelestrakID {
				return []caps.P2PPublisherKey{{PublicKey: celestrakPub, Source: "epm-directory"}}
			}
			return nil
		},
		SchemaForStandard: func(standard string) string {
			switch strings.ToUpper(strings.TrimSuffix(standard, ".fbs")) {
			case "OMM":
				return "OMM.fbs"
			case "CAT":
				return "CAT.fbs"
			}
			return ""
		},
		PinnedDataset: pinCfg.PinnedStandard,
		LatestDatasetBatch: func(schemaName, batchID string, includeBytes bool) (*caps.P2PDatasetBatch, bool) {
			// Only the NEWEST OMM batch is materialized: the older batch is
			// superseded (files evicted), CAT was never pinned.
			if schemaName != "OMM.fbs" || batchID != newBatch {
				return nil, false
			}
			batch := &caps.P2PDatasetBatch{
				ProviderID:  "space-data-network-02",
				SourceName:  "celestrak-gp",
				BatchID:     batchID,
				RecordCount: 3,
				FNV1a64:     fnv,
				Parts:       1,
				PublishedAt: "2026-07-06T06:00:00Z",
			}
			if includeBytes {
				batch.Bytes = stream
			}
			return batch, true
		},
	}))

	mux := http.NewServeMux()
	mounted, err := RegisterFlowMounts(mux,
		[]config.FlowMount{
			// PRODUCTION topology: the peers subtree AND the more specific
			// latest pattern coexist; Go 1.22 mux precedence routes
			// /peers/{peerId}/{standard}/latest to the latest flow.
			{Path: "/api/v1/peers/", Flow: peersDist, Pool: 1},
			{Path: "/api/v1/peers/{peerId}/{standard}/latest", Flow: latestDist, Pool: 1},
		},
		FlowMountDeps{
			CapRegistry:    reg,
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

	latestDoc := mounted[1].APIDoc()
	if latestDoc == nil || latestDoc.BasePath != "/api/v1/peers/{peerId}/{standard}/latest" ||
		len(latestDoc.Routes) != 1 || !latestDoc.Routes[0].Anonymous {
		t.Fatalf("latest bundle api block wrong: %+v", latestDoc)
	}

	srv := httptest.NewServer(mux)
	defer srv.Close()
	base := srv.URL + "/api/v1/peers/" + discoveryCelestrakID

	t.Run("pinned OMM serves the published batch bytes verbatim via BODY_REF", func(t *testing.T) {
		resp, body := discoveryGET(t, base+"/omm/latest", nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d body %q", resp.StatusCode, body)
		}
		if ct := resp.Header.Get("Content-Type"); ct != "application/vnd.sdn.flatbuffers.stream" {
			t.Fatalf("content-type = %q", ct)
		}
		if rc := resp.Header.Get("X-Sdn-Record-Count"); rc != "3" {
			t.Fatalf("x-sdn-record-count = %q, want 3", rc)
		}
		if etag := resp.Header.Get("ETag"); etag != wantEtag {
			t.Fatalf("etag = %q, want %q", etag, wantEtag)
		}
		if string(body) != string(stream) {
			t.Fatalf("body != published batch stream (%d vs %d bytes)", len(body), len(stream))
		}
		frames := splitSizePrefixedFrames(t, body)
		if len(frames) != 3 {
			t.Fatalf("frames = %d, want 3", len(frames))
		}

		// Conditional GET: If-None-Match answers 304, empty body.
		resp304, body304 := discoveryGET(t, base+"/omm/latest", map[string]string{"If-None-Match": wantEtag})
		if resp304.StatusCode != http.StatusNotModified || len(body304) != 0 {
			t.Fatalf("304 = %d (%d bytes)", resp304.StatusCode, len(body304))
		}
	})

	t.Run("format=json is the bare-array presentation with the shared etag", func(t *testing.T) {
		resp, body := discoveryGET(t, base+"/omm/latest?format=json", nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d body %q", resp.StatusCode, body)
		}
		if ct := resp.Header.Get("Content-Type"); ct != "application/json" {
			t.Fatalf("content-type = %q", ct)
		}
		if etag := resp.Header.Get("ETag"); etag != wantEtag {
			t.Fatalf("json etag = %q, want the shared %q", etag, wantEtag)
		}
		var records []map[string]interface{}
		if err := json.Unmarshal(body, &records); err != nil {
			t.Fatalf("bare array: %v (%q)", err, body[:min(len(body), 120)])
		}
		if len(records) != 3 || records[0]["object_name"] != "ISS (ZARYA)" {
			t.Fatalf("records: %v", records)
		}
		if rc := resp.Header.Get("X-Sdn-Record-Count"); rc != "3" {
			t.Fatalf("json x-sdn-record-count = %q", rc)
		}
	})

	t.Run("unpinned CAT answers an honest 503 carrying the newest PNM pointer", func(t *testing.T) {
		resp, body := discoveryGET(t, base+"/cat/latest", nil)
		if resp.StatusCode != http.StatusServiceUnavailable {
			t.Fatalf("status = %d body %q, want 503", resp.StatusCode, body)
		}
		var payload struct {
			Error string `json:"error"`
			PNM   struct {
				CID               string `json:"cid"`
				FileID            string `json:"file_id"`
				BatchID           string `json:"batch_id"`
				PublishTimestamp  string `json:"publish_timestamp"`
				SignatureVerified bool   `json:"signature_verified"`
				Attribution       string `json:"attribution"`
			} `json:"pnm"`
		}
		if err := json.Unmarshal(body, &payload); err != nil {
			t.Fatalf("503 body: %v (%q)", err, body)
		}
		if !strings.Contains(payload.Error, "not pinned") {
			t.Fatalf("503 error = %q", payload.Error)
		}
		if payload.PNM.CID != "bafy-manifest-cat" || payload.PNM.BatchID != newBatch ||
			!payload.PNM.SignatureVerified || payload.PNM.Attribution != "signature" {
			t.Fatalf("pnm pointer: %+v", payload.PNM)
		}
	})

	t.Run("unknown standard and unknown peer answer honest 404s", func(t *testing.T) {
		resp, _ := discoveryGET(t, base+"/xyz/latest", nil)
		if resp.StatusCode != http.StatusNotFound {
			t.Fatalf("unknown standard status = %d, want 404", resp.StatusCode)
		}
		resp, _ = discoveryGET(t, srv.URL+"/api/v1/peers/16Uiu2HAmNobody/omm/latest", nil)
		if resp.StatusCode != http.StatusNotFound {
			t.Fatalf("unknown peer status = %d, want 404", resp.StatusCode)
		}
	})

	t.Run("non-GET degrades to 404 (no auth wall in this harness)", func(t *testing.T) {
		resp, err := http.Post(base+"/omm/latest", "application/json", strings.NewReader("{}"))
		if err != nil {
			t.Fatalf("POST: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Fatalf("POST status = %d, want 404", resp.StatusCode)
		}
	})
}
