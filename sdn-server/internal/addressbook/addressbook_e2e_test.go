package addressbook

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/peer"

	"github.com/spacedatanetwork/sdn-server/internal/accessgate"
)

// The flow that must hold (owner 2026-10-06): a private entry never reaches
// the public route, every public entry verifies from the node's peer ID alone,
// and a revoked entry stays revoked when the node restarts.
func TestPrivateEntriesNeverReachThePublicRouteAndRevocationLasts(t *testing.T) {
	key, _, err := crypto.GenerateSecp256k1Key(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	self, _ := peer.IDFromPrivateKey(key)
	dir := t.TempDir()
	serve := func() *httptest.Server {
		h := &Handler{Book: NewBook(Options{Dir: dir, Key: func() crypto.PrivKey { return key }})}
		mux := http.NewServeMux()
		mux.HandleFunc(PublicPath, h.ServePublic)
		mux.Handle(OperatorPath, h)
		return httptest.NewServer(mux)
	}
	call := func(srv *httptest.Server, method, path string, body any, want int) map[string]any {
		t.Helper()
		var payload []byte
		if body != nil {
			payload, _ = json.Marshal(body)
		}
		req, _ := http.NewRequest(method, srv.URL+path, bytes.NewReader(payload))
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		if res.StatusCode != want {
			t.Fatalf("%s %s: status %d, want %d", method, path, res.StatusCode, want)
		}
		out := map[string]any{}
		_ = json.NewDecoder(res.Body).Decode(&out)
		return out
	}
	publicEntries := func(srv *httptest.Server) []any {
		t.Helper()
		entries, _ := call(srv, http.MethodGet, PublicPath, nil, http.StatusOK)["entries"].([]any)
		return entries
	}
	card := func(name string) string {
		return "BEGIN:VCARD\r\nVERSION:3.0\r\nFN:" + name + "\r\nORG:Example Space\r\nEMAIL:" + strings.ToLower(strings.Fields(name)[0]) + "@example.org\r\nEND:VCARD\r\n"
	}

	// Only the public route is open to anyone.
	gate, err := accessgate.New(accessgate.DefaultPublic, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !gate.Public(http.MethodGet, PublicPath) || gate.Public(http.MethodGet, OperatorPath+"entries") || gate.Public(http.MethodGet, OperatorPath+"settings") {
		t.Fatal("the access gate must open the public entries and nothing else of the address book")
	}

	srv := serve()
	ada := call(srv, http.MethodPost, OperatorPath+"entries", map[string]any{"vcard": card("Ada Lovelace")}, http.StatusCreated)["entry"].(map[string]any)
	if ada["visibility"] != "private" || len(publicEntries(srv)) != 0 {
		t.Fatal("a new entry is private by default and the public route must not show it")
	}
	call(srv, http.MethodPost, OperatorPath+"entries", map[string]any{"vcard": card("Grace Hopper"), "visibility": "private"}, http.StatusCreated)
	call(srv, http.MethodPatch, OperatorPath+"entries/"+ada["id"].(string), map[string]any{"visibility": "public"}, http.StatusOK)

	shown := publicEntries(srv)
	if len(shown) != 1 || !strings.Contains(shown[0].(map[string]any)["vcard"].(string), "Ada Lovelace") {
		t.Fatalf("the public route must show exactly the public entry, got %v", shown)
	}
	// Verify it the way a stranger would: from the record and the peer ID.
	attestation := shown[0].(map[string]any)["attestation"].(map[string]any)
	frame, _ := base64.StdEncoding.DecodeString(attestation["record"].(string))
	record, err := DecodeFrame(frame)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := peer.Decode(attestation["signer"].(string))
	if err != nil || signer != self {
		t.Fatal("the signer must be this node")
	}
	embedded, _ := signer.ExtractPublicKey()
	literal, _ := embedded.Raw()
	if err := record.Verify(); err != nil || !bytes.Equal(literal, record.PublicKey) || record.Visibility != Public {
		t.Fatalf("the public attestation must verify from the peer ID alone: %v", err)
	}
	tampered := record
	tampered.Visibility = Private
	if tampered.Verify() == nil {
		t.Fatal("a changed visibility must not verify")
	}

	call(srv, http.MethodDelete, OperatorPath+"entries/"+ada["id"].(string), nil, http.StatusNoContent)
	srv.Close()

	srv = serve() // the node restarts on the same disk
	defer srv.Close()
	if len(publicEntries(srv)) != 0 {
		t.Fatal("a revoked entry must stay revoked after a restart")
	}
	all, _ := call(srv, http.MethodGet, OperatorPath+"entries", nil, http.StatusOK)["entries"].([]any)
	if len(all) != 1 || !strings.Contains(all[0].(map[string]any)["vcard"].(string), "Grace Hopper") {
		t.Fatalf("the operator keeps the private entry, got %v", all)
	}
}
