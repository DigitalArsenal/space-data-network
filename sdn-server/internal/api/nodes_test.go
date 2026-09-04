package api

import (
	"crypto/ed25519"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"testing"

	standardsEPM "github.com/DigitalArsenal/spacedatastandards.org/lib/go/EPM"
	flatbuffers "github.com/google/flatbuffers/go"

	"github.com/spacedatanetwork/sdn-server/internal/epm"
)

type fixedNodeProfileReader struct{ frame []byte }

func (r fixedNodeProfileReader) GetNodeEPM() []byte { return r.frame }

func signedNodeEPMFixture(t *testing.T, peerID, legalName string) ([]byte, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	build := func(signature string) []byte {
		b := flatbuffers.NewBuilder(512)
		name := b.CreateString(legalName)
		pubHex := b.CreateString(hex.EncodeToString(pub))
		algorithm := b.CreateString("ed25519")
		addressType := b.CreateString("ed25519")
		address := b.CreateString("/p2p/" + peerID)
		signatureOffset := b.CreateString(signature)

		standardsEPM.CryptoKeyStart(b)
		standardsEPM.CryptoKeyAddPUBLIC_KEY(b, pubHex)
		standardsEPM.CryptoKeyAddALGORITHM(b, algorithm)
		standardsEPM.CryptoKeyAddADDRESS_TYPE(b, addressType)
		standardsEPM.CryptoKeyAddKEY_TYPE(b, standardsEPM.KeyTypeSigning)
		key := standardsEPM.CryptoKeyEnd(b)
		standardsEPM.EPMStartKEYSVector(b, 1)
		b.PrependUOffsetT(key)
		keys := b.EndVector(1)
		standardsEPM.EPMStartMULTIFORMAT_ADDRESSVector(b, 1)
		b.PrependUOffsetT(address)
		addresses := b.EndVector(1)

		standardsEPM.EPMStart(b)
		standardsEPM.EPMAddLEGAL_NAME(b, name)
		standardsEPM.EPMAddENTITY_TYPE(b, standardsEPM.EntityTypeNode)
		standardsEPM.EPMAddKEYS(b, keys)
		standardsEPM.EPMAddMULTIFORMAT_ADDRESS(b, addresses)
		standardsEPM.EPMAddSIGNATURE_TIMESTAMP(b, 1_788_000_000)
		if signature != "" {
			standardsEPM.EPMAddSIGNATURE(b, signatureOffset)
		}
		root := standardsEPM.EPMEnd(b)
		standardsEPM.FinishSizePrefixedEPMBuffer(b, root)
		return b.FinishedBytes()
	}
	unsigned := build("")
	payload, err := epm.EPMSigningPayload(unsigned)
	if err != nil {
		t.Fatal(err)
	}
	frame := build(hex.EncodeToString(ed25519.Sign(priv, payload)))
	if err := epm.VerifyEPMSignature(frame); err != nil {
		t.Fatalf("fixture EPM does not verify: %v", err)
	}
	return frame, priv
}

func TestNodesLaneIsSelfFirstAndServesProfiles(t *testing.T) {
	self, _ := signedNodeEPMFixture(t, "self-peer", "This Node Ltd")
	peerA, _ := signedNodeEPMFixture(t, "peer-a", "Alpha Org")
	peerB, _ := signedNodeEPMFixture(t, "peer-b", "Beta Org")
	h := NewNodesHandler(NodesHandlerOptions{
		SelfPeerID: "self-peer",
		Self:       fixedNodeProfileReader{frame: self},
		Profiles: func() []NodeProfile {
			return []NodeProfile{
				{PeerID: "peer-b", Frame: peerB},
				{PeerID: "peer-a", Frame: peerA},
				{PeerID: "wrong-peer", Frame: peerA}, // peer binding rejects it
			}
		},
	})
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, NodesPath, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET nodes = %d: %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get(SelfPeerHeaderName); got != "self-peer" {
		t.Fatalf("%s = %q", SelfPeerHeaderName, got)
	}
	frames, err := SplitFrames(rec.Body.Bytes())
	if err != nil || len(frames) != 3 {
		t.Fatalf("frames = %d, err=%v", len(frames), err)
	}
	wantOrder := []string{"self-peer", "peer-a", "peer-b"}
	wantNames := []string{"This Node Ltd", "Alpha Org", "Beta Org"}
	for i, frame := range frames {
		got, err := epm.PeerIDFromEPM(frame)
		if err != nil || got != wantOrder[i] {
			t.Fatalf("frame %d peer = %q, err=%v, want %q", i, got, err, wantOrder[i])
		}
		profile := standardsEPM.GetSizePrefixedRootAsEPM(frame, 0)
		if got := string(profile.LEGAL_NAME()); got != wantNames[i] {
			t.Fatalf("frame %d LEGAL_NAME = %q, want %q", i, got, wantNames[i])
		}
	}

	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, NodesPath+"/peer-a/profile", nil))
	if rec.Code != http.StatusOK || string(rec.Body.Bytes()) != string(peerA) {
		t.Fatalf("GET peer profile = %d, %d bytes", rec.Code, rec.Body.Len())
	}

	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, NodesPath+"/missing/profile", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown profile = %d, want 404", rec.Code)
	}
	errorFrames, err := SplitFrames(rec.Body.Bytes())
	if err != nil || len(errorFrames) != 1 || FrameIdentifier(errorFrames[0]) != "$QRP" {
		t.Fatalf("unknown profile body is not one $QRP frame: frames=%d err=%v", len(errorFrames), err)
	}
}
