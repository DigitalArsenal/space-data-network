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

// The flow that must hold (owner 2026-10-07: "all should be visible"): every
// entry is on the public route and verifies from the node's peer ID alone, an
// entry signed private before that is signed again as public, and a revoked
// entry stays revoked when the node restarts.
func TestEveryEntryIsPublicAndRevocationLasts(t *testing.T) {
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
	publicNames := func(srv *httptest.Server) []string {
		t.Helper()
		entries, _ := call(srv, http.MethodGet, PublicPath, nil, http.StatusOK)["entries"].([]any)
		var names []string
		for _, entry := range entries {
			for _, name := range []string{"Ada Lovelace", "Grace Hopper", "Katherine Johnson"} {
				if strings.Contains(entry.(map[string]any)["vcard"].(string), name) {
					names = append(names, name)
				}
			}
		}
		return names
	}
	card := func(name string) string {
		return "BEGIN:VCARD\r\nVERSION:3.0\r\nFN:" + name + "\r\nORG:Example Space\r\nEMAIL:" + strings.ToLower(strings.Fields(name)[0]) + "@example.org\r\nEND:VCARD\r\n"
	}

	// Only the public route is open to anyone.
	gate, err := accessgate.New(accessgate.DefaultPublic, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !gate.Public(http.MethodGet, PublicPath) || gate.Public(http.MethodGet, OperatorPath+"entries") {
		t.Fatal("the access gate must open the public entries and nothing else of the address book")
	}

	// An entry the book signed private before every entry became public.
	legacy, _, refusal := ReadCard(card("Katherine Johnson"), "", nil)
	if legacy == nil {
		t.Fatal(refusal)
	}
	old := Record{EntryID: "legacy", Profile: legacy, ProfileSHA256: digest(legacy), CreatedAt: 1, UpdatedAt: 1, Visibility: Private}
	if err := old.sign(key); err != nil {
		t.Fatal(err)
	}
	if err := (store{dir: dir}).save(map[string]Record{old.EntryID: old}); err != nil {
		t.Fatal(err)
	}

	srv := serve()
	ada := call(srv, http.MethodPost, OperatorPath+"entries", map[string]any{"vcard": card("Ada Lovelace")}, http.StatusCreated)["entry"].(map[string]any)
	call(srv, http.MethodPost, OperatorPath+"entries", map[string]any{"vcard": card("Grace Hopper")}, http.StatusCreated)
	if got := publicNames(srv); len(got) != 3 {
		t.Fatalf("every entry is on the public route, got %v", got)
	}

	// Verify each the way a stranger would: from the record and the peer ID.
	entries, _ := call(srv, http.MethodGet, PublicPath, nil, http.StatusOK)["entries"].([]any)
	for _, entry := range entries {
		attestation := entry.(map[string]any)["attestation"].(map[string]any)
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
			t.Fatalf("every attestation is public and verifies from the peer ID alone: %v", err)
		}
		tampered := record
		tampered.Visibility = Private
		if tampered.Verify() == nil {
			t.Fatal("a changed record must not verify")
		}
	}

	call(srv, http.MethodDelete, OperatorPath+"entries/"+ada["id"].(string), nil, http.StatusNoContent)
	srv.Close()

	srv = serve() // the node restarts on the same disk
	defer srv.Close()
	if got := publicNames(srv); len(got) != 2 || got[0] == "Ada Lovelace" || got[1] == "Ada Lovelace" {
		t.Fatalf("a revoked entry must stay revoked after a restart, got %v", got)
	}
}
