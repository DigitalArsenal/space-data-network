package flowrt

// Gateway loop G.3 integration: the REAL compiled pnm-history flow bundle
// (space-data-network-modules/flows/discovery/dist/pnm, bridge linkage) is
// mounted at its PRODUCTION mux pattern (/api/v1/peers/{peerId}/pnm)
// ALONGSIDE the peers-discovery subtree mount, proving Go 1.22 pattern
// precedence routes the pnm surface to its own flow. Fixtures carry REAL
// Ed25519 signatures over the dataset-publication payload
// ("SDN-DPM-PNM\x00" + FILE_ID + "\x00" + CID) so the test verifies the
// whole provenance chain a cold client walks: fetch the newest $PNM frame
// verbatim, extract SIGNATURE/FILE_ID/CID, verify against the publisher's
// publication key.

import (
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	flatbuffers "github.com/google/flatbuffers/go"

	PNM "github.com/DigitalArsenal/spacedatastandards.org/lib/go/PNM"

	"github.com/spacedatanetwork/sdn-server/internal/config"
	"github.com/spacedatanetwork/sdn-server/internal/modulert"
	"github.com/spacedatanetwork/sdn-server/internal/modulert/caps"
)

// pnmPublicationSignaturePayload mirrors storage.BuildDatasetPublicationPNM /
// channels.datasetPublicationPNMSignaturePayload.
func pnmPublicationSignaturePayload(manifestCID, fileID string) []byte {
	payload := make([]byte, 0, len(manifestCID)+len(fileID)+16)
	payload = append(payload, []byte("SDN-DPM-PNM\x00")...)
	payload = append(payload, fileID...)
	payload = append(payload, 0)
	payload = append(payload, manifestCID...)
	return payload
}

func buildSignedDiscoveryPNM(t *testing.T, key ed25519.PrivateKey, fileID, cid, ts string) []byte {
	t.Helper()
	signature := ed25519.Sign(key, pnmPublicationSignaturePayload(cid, fileID))
	b := flatbuffers.NewBuilder(512)
	fileIDOff := b.CreateString(fileID)
	cidOff := b.CreateString(cid)
	tsOff := b.CreateString(ts)
	fileNameOff := b.CreateString("dataset.fsql")
	sigOff := b.CreateString(hex.EncodeToString(signature))
	sigTypeOff := b.CreateString("Ed25519")
	PNM.PNMStart(b)
	PNM.PNMAddFILE_ID(b, fileIDOff)
	PNM.PNMAddCID(b, cidOff)
	PNM.PNMAddPUBLISH_TIMESTAMP(b, tsOff)
	PNM.PNMAddFILE_NAME(b, fileNameOff)
	PNM.PNMAddSIGNATURE(b, sigOff)
	PNM.PNMAddSIGNATURE_TYPE(b, sigTypeOff)
	b.FinishSizePrefixedWithFileIdentifier(PNM.PNMEnd(b), []byte(PNM.PNMIdentifier))
	return b.FinishedBytes()
}

func TestHTTPMountedPNMHistoryFlow(t *testing.T) {
	peersDist := discoveryFlowDist(t, "peers")
	pnmDist := discoveryFlowDist(t, "pnm")

	celestrakPub, celestrakPriv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}

	// 120 signed publications, newest first in store order, so the api-block
	// clamp (limit <= 100) is observable end-to-end.
	const publicationCount = 120
	pnms := make([]caps.P2PPNMRecord, 0, publicationCount+2)
	for i := 0; i < publicationCount; i++ {
		ts := fmt.Sprintf("2026-07-%02dT%02d:00:00Z", 6-(i/24), 23-(i%24))
		frame := buildSignedDiscoveryPNM(t, celestrakPriv,
			"celestrak:gp:OMM.fbs:"+ts, fmt.Sprintf("bafy-omm-%03d", i), ts)
		// Half the records are gossip-attributed to the RELAYING self peer:
		// signature attribution must reclaim them for the publisher.
		gossipID := discoveryCelestrakID
		if i%2 == 1 {
			gossipID = discoverySelfID
		}
		pnms = append(pnms, caps.P2PPNMRecord{PeerID: gossipID, Data: frame})
	}
	// An unsigned PNM and a foreign-signed PNM gossip-attributed to
	// celestrak: neither may appear on the provenance surface.
	pnms = append(pnms, caps.P2PPNMRecord{
		PeerID: discoveryCelestrakID,
		Data:   buildDiscoveryPNM(t, "celestrak:gp:OMM.fbs:2026-07-06T23:30:00Z", "bafy-unsigned", "2026-07-06T23:30:00Z"),
	})
	_, foreignPriv, _ := ed25519.GenerateKey(nil)
	pnms = append(pnms, caps.P2PPNMRecord{
		PeerID: discoveryCelestrakID,
		Data:   buildSignedDiscoveryPNM(t, foreignPriv, "impostor:gp:OMM.fbs:2026-07-06T23:45:00Z", "bafy-impostor", "2026-07-06T23:45:00Z"),
	})

	reg := modulert.NewCapabilityRegistry()
	reg.Register("p2p_read", caps.NewP2PCapFactory(caps.P2PCapOptions{
		SelfID: discoverySelfID,
		Peers: func() []caps.P2PPeerInfo {
			return []caps.P2PPeerInfo{{ID: discoveryCelestrakID, Connected: true}}
		},
		RecentPNMs: func(limit int) []caps.P2PPNMRecord {
			if limit < len(pnms) {
				return pnms[:limit]
			}
			return pnms
		},
		PublisherKeys: func(peerID string) []caps.P2PPublisherKey {
			if peerID == discoveryCelestrakID {
				return []caps.P2PPublisherKey{{PublicKey: celestrakPub, Source: "epm-directory"}}
			}
			return nil
		},
	}))

	mux := http.NewServeMux()
	mounted, err := RegisterFlowMounts(mux,
		[]config.FlowMount{
			// PRODUCTION topology: the peers subtree AND the more specific
			// pnm pattern coexist; Go 1.22 mux precedence sends
			// /peers/{peerId}/pnm to the pnm flow.
			{Path: "/api/v1/peers/", Flow: peersDist, Pool: 1},
			{Path: "/api/v1/peers/{peerId}/pnm", Flow: pnmDist, Pool: 1},
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
	if len(mounted) != 2 {
		t.Fatalf("mounted %d flows, want 2", len(mounted))
	}

	pnmDoc := mounted[1].APIDoc()
	if pnmDoc == nil || pnmDoc.BasePath != "/api/v1/peers/{peerId}/pnm" ||
		len(pnmDoc.Routes) != 1 || !pnmDoc.Routes[0].Anonymous {
		t.Fatalf("pnm bundle api block wrong: %+v", pnmDoc)
	}

	srv := httptest.NewServer(mux)
	defer srv.Close()
	base := srv.URL + "/api/v1/peers/" + discoveryCelestrakID + "/pnm"

	var newestEtag string

	t.Run("default limit=1 serves the newest signed $PNM verbatim + verifiable", func(t *testing.T) {
		resp, body := discoveryGET(t, base, nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d body %q", resp.StatusCode, body)
		}
		if ct := resp.Header.Get("Content-Type"); ct != "application/vnd.sdn.flatbuffers.stream" {
			t.Fatalf("content-type = %q", ct)
		}
		if rc := resp.Header.Get("X-Sdn-Record-Count"); rc != "1" {
			t.Fatalf("x-sdn-record-count = %q, want 1", rc)
		}
		newestEtag = resp.Header.Get("ETag")
		if !strings.HasPrefix(newestEtag, `W/"fnv1a64-`) {
			t.Fatalf("etag = %q", newestEtag)
		}
		frames := splitSizePrefixedFrames(t, body)
		if len(frames) != 1 {
			t.Fatalf("frames = %d, want 1", len(frames))
		}
		// Byte-verbatim: the newest publication is store record 0.
		if string(frames[0]) != string(pnms[0].Data) {
			t.Fatalf("newest frame not spliced verbatim")
		}
		// Cold-client verification chain over the response bytes ONLY.
		if !PNM.SizePrefixedPNMBufferHasIdentifier(frames[0]) {
			t.Fatalf("frame is not $PNM")
		}
		pnm := PNM.GetSizePrefixedRootAsPNM(frames[0], 0)
		if got := string(pnm.SIGNATURE_TYPE()); got != "Ed25519" {
			t.Fatalf("SIGNATURE_TYPE = %q", got)
		}
		signature, err := hex.DecodeString(string(pnm.SIGNATURE()))
		if err != nil {
			t.Fatalf("decode signature: %v", err)
		}
		payload := pnmPublicationSignaturePayload(string(pnm.CID()), string(pnm.FILE_ID()))
		if !ed25519.Verify(celestrakPub, payload, signature) {
			t.Fatalf("served PNM signature does not verify against the publisher key")
		}
	})

	t.Run("limit forwards and clamps at 100; json exposes provenance; shared etag + 304", func(t *testing.T) {
		resp, body := discoveryGET(t, base+"?limit=5", nil)
		if rc := resp.Header.Get("X-Sdn-Record-Count"); rc != "5" {
			t.Fatalf("limit=5 count = %q body %d bytes", rc, len(body))
		}
		frames := splitSizePrefixedFrames(t, body)
		for i, frame := range frames {
			pnm := PNM.GetSizePrefixedRootAsPNM(frame, 0)
			if got := string(pnm.CID()); got != fmt.Sprintf("bafy-omm-%03d", i) {
				t.Fatalf("frame %d = %q: not newest-first", i, got)
			}
		}

		// The api-block clamp: 120 verified publications stored, limit=5000
		// answers exactly 100.
		resp, _ = discoveryGET(t, base+"?limit=5000", nil)
		if rc := resp.Header.Get("X-Sdn-Record-Count"); rc != "100" {
			t.Fatalf("limit=5000 count = %q, want 100 (in-wasm clamp)", rc)
		}

		respJSON, bodyJSON := discoveryGET(t, base+"?format=json", nil)
		if respJSON.StatusCode != http.StatusOK {
			t.Fatalf("json status = %d", respJSON.StatusCode)
		}
		if respJSON.Header.Get("ETag") != newestEtag {
			t.Fatalf("json etag %q != fb etag %q", respJSON.Header.Get("ETag"), newestEtag)
		}
		var records []map[string]interface{}
		if err := json.Unmarshal(bodyJSON, &records); err != nil {
			t.Fatalf("bare array: %v (%q)", err, bodyJSON)
		}
		if len(records) != 1 {
			t.Fatalf("records = %d", len(records))
		}
		entry := records[0]
		if entry["publisher_peer_id"] != discoveryCelestrakID ||
			entry["signature_verified"] != true ||
			entry["attribution"] != "signature" {
			t.Fatalf("provenance fields: %v", entry)
		}
		if entry["publisher_key"] != hex.EncodeToString(celestrakPub) ||
			entry["publisher_key_source"] != "epm-directory" {
			t.Fatalf("publisher key fields: %v", entry)
		}
		if entry["cid"] != "bafy-omm-000" {
			t.Fatalf("newest cid: %v", entry["cid"])
		}
		// The json "signature" must be the PNM's Ed25519 signature hex —
		// regression guard for the key-vs-value collision (Go marshals
		// entry keys alphabetically, so "attribution":"signature" precedes
		// the "signature" key; a substring key match served the cid here).
		sigHex, _ := entry["signature"].(string)
		sig, err := hex.DecodeString(sigHex)
		if err != nil || len(sig) != ed25519.SignatureSize {
			t.Fatalf("json signature is not a 64-byte hex signature: %q", sigHex)
		}
		if !ed25519.Verify(celestrakPub,
			pnmPublicationSignaturePayload(entry["cid"].(string), entry["file_id"].(string)), sig) {
			t.Fatalf("json signature does not verify: %v", entry)
		}

		resp, _ = discoveryGET(t, base, map[string]string{"If-None-Match": newestEtag})
		if resp.StatusCode != http.StatusNotModified {
			t.Fatalf("conditional GET = %d, want 304", resp.StatusCode)
		}
	})

	t.Run("impostor and unsigned frames never appear even at the full clamp", func(t *testing.T) {
		_, body := discoveryGET(t, base+"?limit=100&format=json", nil)
		var records []map[string]interface{}
		if err := json.Unmarshal(body, &records); err != nil {
			t.Fatalf("bare array: %v", err)
		}
		for _, record := range records {
			cid, _ := record["cid"].(string)
			if cid == "bafy-impostor" || cid == "bafy-unsigned" {
				t.Fatalf("unattributable frame served: %v", record)
			}
			if record["signature_verified"] != true {
				t.Fatalf("unverified entry on a keyed surface: %v", record)
			}
		}
	})

	t.Run("unknown publisher 404s; POST 404s; peers list still served by its own mount", func(t *testing.T) {
		resp, _ := discoveryGET(t, srv.URL+"/api/v1/peers/16Uiu2NobodyKnown/pnm", nil)
		if resp.StatusCode != http.StatusNotFound {
			t.Fatalf("unknown publisher = %d, want 404", resp.StatusCode)
		}

		req, _ := http.NewRequest(http.MethodPost, base, strings.NewReader("{}"))
		postResp, err := noRedirectClient().Do(req)
		if err != nil {
			t.Fatalf("POST: %v", err)
		}
		postResp.Body.Close()
		if postResp.StatusCode != http.StatusNotFound {
			t.Fatalf("POST = %d, want 404", postResp.StatusCode)
		}

		// Mux precedence sanity: the subtree mount still answers the list
		// and the single-peer read.
		resp, _ = discoveryGET(t, srv.URL+"/api/v1/peers", nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("peers list = %d", resp.StatusCode)
		}
		resp, _ = discoveryGET(t, srv.URL+"/api/v1/peers/"+discoveryCelestrakID, nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("peer get = %d", resp.StatusCode)
		}
	})
}
