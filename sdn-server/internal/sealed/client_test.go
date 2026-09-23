package sealed

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// An in-process node: its public descriptor plus the real sealed handler.
func serveNode(t *testing.T, f *fixture, advertSigner ed25519.PublicKey) *httptest.Server {
	t.Helper()
	f.h.Now = time.Now
	mux := http.NewServeMux()
	mux.HandleFunc("/api/node/info", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"sealed_transport": map[string]string{
			"encryption_key": hex.EncodeToString(f.nodeEnc),
			"signing_key":    hex.EncodeToString(advertSigner),
			"key_exchange":   "Secp256k1",
			"fingerprint":    fingerprint(f.nodeEnc),
		}})
	})
	mux.Handle(Route, f.h)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestRemoteCLICall(t *testing.T) {
	f := newFixture(t)
	srv := serveNode(t, f, f.nodeSign)
	ctx := context.Background()

	if _, err := ReadNodeKeys(ctx, srv.Client(), srv.URL, ""); err == nil {
		t.Fatal("read node keys without an expected fingerprint")
	}
	if _, err := ReadNodeKeys(ctx, srv.Client(), srv.URL, "0000:0000:0000:0000"); err == nil || !strings.Contains(err.Error(), "refusing") {
		t.Fatalf("wrong fingerprint: %v", err)
	}
	node, err := ReadNodeKeys(ctx, srv.Client(), srv.URL, fingerprint(f.nodeEnc))
	if err != nil {
		t.Fatal(err)
	}

	res, err := Call(ctx, srv.Client(), srv.URL, node, f.admin, "PUT", "/api/echo", []byte("from the cli"), "text/plain")
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != 200 || string(res.Body) != "PUT xpub-admin admin from the cli" {
		t.Fatalf("result %d %q", res.Status, res.Body)
	}

	// A signer that is not an admin gets the node's plain refusal back.
	res, err = Call(ctx, srv.Client(), srv.URL, node, f.viewer, "GET", "/api/echo", nil, "")
	if err != nil || res.Status != http.StatusForbidden {
		t.Fatalf("viewer: %d %v", res.Status, err)
	}
}

func TestRemoteCLIRefusesAReplyFromAnotherKey(t *testing.T) {
	f := newFixture(t)
	impostor, _, _ := ed25519.GenerateKey(rand.Reader)
	// The descriptor advertises a signing key the handler does not hold, so
	// every reply looks forged to the client.
	srv := serveNode(t, f, impostor)
	node, err := ReadNodeKeys(context.Background(), srv.Client(), srv.URL, fingerprint(f.nodeEnc))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Call(context.Background(), srv.Client(), srv.URL, node, f.admin, "GET", "/api/echo", nil, ""); err == nil || !strings.Contains(err.Error(), "not signed by this node") {
		t.Fatalf("forged reply accepted: %v", err)
	}
}
