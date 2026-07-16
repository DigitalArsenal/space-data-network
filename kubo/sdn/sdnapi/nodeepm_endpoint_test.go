package sdnapi_test

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ipfs/kubo/sdn/nodeepm"
	sdnapi "github.com/ipfs/kubo/sdn/sdnapi"
)

func testNodeEPM(t *testing.T) []byte {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	b, err := nodeepm.BuildNodeEPM(nodeepm.Identity{
		PeerID:     "12D3KooWEndpointTestPeerID0000000000000000000000000",
		SigningPub: pub,
		Sign:       func(payload []byte) ([]byte, error) { return ed25519.Sign(priv, payload), nil },
	})
	if err != nil {
		t.Fatalf("build node EPM: %v", err)
	}
	return b
}

func nodeEPMHandlerWith(epm []byte) http.Handler {
	return sdnapi.NewNodeEPMHandler(sdnapi.NodeEPMDeps{
		EPM: func() ([]byte, error) { return epm, nil },
	})
}

func TestNodeEPMEndpointJSON(t *testing.T) {
	h := nodeEPMHandlerWith(testNodeEPM(t))
	req := httptest.NewRequest(http.MethodGet, "/sdn/v1/node/epm", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "application/json") {
		t.Fatalf("content-type = %q, want json", ct)
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode json: %v", err)
	}
	if body["entity_type"] != "Node" {
		t.Fatalf("entity_type = %v, want Node", body["entity_type"])
	}
	if _, ok := body["signature"].(string); !ok {
		t.Fatal("json missing signature")
	}
	if _, ok := body["peer_id"].(string); !ok {
		t.Fatal("json missing peer_id")
	}
}

func TestNodeEPMEndpointFlatBuffer(t *testing.T) {
	epm := testNodeEPM(t)
	h := nodeEPMHandlerWith(epm)
	req := httptest.NewRequest(http.MethodGet, "/sdn/v1/node/epm?format=fb", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/octet-stream" {
		t.Fatalf("content-type = %q, want octet-stream", ct)
	}
	if len(rec.Body.Bytes()) != len(epm) {
		t.Fatalf("fb body len = %d, want %d", len(rec.Body.Bytes()), len(epm))
	}
	// The raw bytes must verify as a signed node EPM.
	if err := nodeepm.VerifyEPMSignature(rec.Body.Bytes()); err != nil {
		t.Fatalf("served fb bytes do not verify: %v", err)
	}
}

func TestNodeEPMEndpointVCard(t *testing.T) {
	h := nodeEPMHandlerWith(testNodeEPM(t))
	req := httptest.NewRequest(http.MethodGet, "/sdn/v1/node/vcard", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "text/vcard") {
		t.Fatalf("content-type = %q, want text/vcard", ct)
	}
	if cd := rec.Header().Get("Content-Disposition"); !strings.Contains(cd, "attachment") || !strings.Contains(cd, ".vcf") {
		t.Fatalf("content-disposition = %q, want .vcf attachment", cd)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "BEGIN:VCARD") || !strings.Contains(body, "12D3KooWEndpointTestPeerID0000000000000000000000000") {
		t.Fatalf("vcard body missing header or peer id:\n%s", body)
	}
}

func TestNodeEPMEndpointQR(t *testing.T) {
	h := nodeEPMHandlerWith(testNodeEPM(t))
	req := httptest.NewRequest(http.MethodGet, "/sdn/v1/node/qr", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "image/png" {
		t.Fatalf("content-type = %q, want image/png", ct)
	}
	// PNG magic number.
	if got := rec.Body.Bytes(); len(got) < 8 || string(got[1:4]) != "PNG" {
		t.Fatalf("body is not a PNG (len %d)", len(got))
	}
}

func TestNodeEPMEndpointUnavailable(t *testing.T) {
	// Nil EPM accessor -> 503 on every route, never a panic.
	h := sdnapi.NewNodeEPMHandler(sdnapi.NodeEPMDeps{EPM: nil})
	for _, path := range []string{"/sdn/v1/node/epm", "/sdn/v1/node/vcard", "/sdn/v1/node/qr"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("GET %s status = %d, want 503", path, rec.Code)
		}
	}
}
