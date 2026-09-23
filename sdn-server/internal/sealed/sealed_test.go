package sealed

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	secp256k1 "github.com/decred/dcrd/dcrec/secp256k1/v4"
	"golang.org/x/crypto/curve25519"

	"github.com/spacedatanetwork/sdn-server/internal/auth"
	"github.com/spacedatanetwork/sdn-server/internal/ecies"
	"github.com/spacedatanetwork/sdn-server/internal/peers"
)

type fixture struct {
	h                   *Handler
	nodeEnc             []byte // node's secp256k1 encryption public key
	nodeSign            ed25519.PublicKey
	admin               ed25519.PrivateKey
	viewer              ed25519.PrivateKey
	replyPriv, replyPub []byte
	now                 time.Time
}

type users map[string]*auth.User

func (u users) GetUserBySigningPubKey(h string) (*auth.User, error) { return u[h], nil }

func newFixture(t *testing.T) *fixture {
	t.Helper()
	encKey, _ := secp256k1.GeneratePrivateKey()
	signPub, signPriv, _ := ed25519.GenerateKey(rand.Reader)
	adminPub, adminPriv, _ := ed25519.GenerateKey(rand.Reader)
	viewerPub, viewerPriv, _ := ed25519.GenerateKey(rand.Reader)
	replyPriv := make([]byte, 32)
	rand.Read(replyPriv)
	replyPub, _ := curve25519.X25519(replyPriv, curve25519.Basepoint)

	// The inner mux reports who it ran as, and echoes the body.
	mux := http.NewServeMux()
	mux.HandleFunc("/api/echo", func(w http.ResponseWriter, r *http.Request) {
		s := auth.SessionFromContext(r.Context())
		body, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/plain")
		io.WriteString(w, r.Method+" "+s.XPub+" "+s.TrustLevel.String()+" "+string(body))
	})
	f := &fixture{
		nodeEnc: encKey.PubKey().SerializeCompressed(), nodeSign: signPub,
		admin: adminPriv, viewer: viewerPriv, replyPriv: replyPriv, replyPub: replyPub,
		now: time.UnixMilli(1_800_000_000_000),
	}
	f.h = &Handler{
		EncryptionPriv: encKey.Serialize(),
		Signer:         signPriv,
		Admins: users{
			hex.EncodeToString(adminPub):  {XPub: "xpub-admin", TrustLevel: peers.Admin},
			hex.EncodeToString(viewerPub): {XPub: "xpub-viewer", TrustLevel: peers.Standard},
		},
		Next: mux,
		Now:  func() time.Time { return f.now },
	}
	return f
}

func (f *fixture) request(t *testing.T, signer ed25519.PrivateKey, method, route, body string, at time.Time) []byte {
	t.Helper()
	raw, err := Seal(Body{Method: method, Route: route, Body: []byte(body), ReplyKey: f.replyPub}, SealOptions{
		RecipientPub: f.nodeEnc, KeyExchange: ecies.Secp256k1, Signer: signer, SessionID: "s1", Now: at,
	})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func (f *fixture) send(raw []byte) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	f.h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, Route, bytes.NewReader(raw)))
	return rec
}

func TestSealedCommandRoundTrip(t *testing.T) {
	f := newFixture(t)
	req := f.request(t, f.admin, "PUT", "/api/echo", "rename node", f.now)
	if bytes.Contains(req, []byte("rename node")) || bytes.Contains(req, []byte("/api/echo")) {
		t.Fatal("the route or body travels in the clear")
	}
	rec := f.send(req)
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != ContentType {
		t.Fatalf("status %d %s", rec.Code, rec.Body.String())
	}

	// The browser's side: verify the node's signature, open with the reply key.
	env, err := Open(rec.Body.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if !env.Response || !bytes.Equal(env.Signer, f.nodeSign) {
		t.Fatalf("response not signed by the node: %+v", env)
	}
	body, err := env.Decrypt(f.replyPriv)
	if err != nil {
		t.Fatal(err)
	}
	reqEnv, _ := Open(req)
	if !bytes.Equal(body.RequestNonce, reqEnv.Nonce) || !bytes.Equal(body.RequestDigest, Digest(req)) {
		t.Fatal("response does not bind to the request")
	}
	if body.Status != 200 || string(body.Body) != "PUT xpub-admin admin rename node" {
		t.Fatalf("inner result %d %q", body.Status, body.Body)
	}
}

func TestSealedCommandRefusals(t *testing.T) {
	f := newFixture(t)
	cases := map[string]func() []byte{
		"non-admin signer": func() []byte { return f.request(t, f.viewer, "GET", "/api/echo", "", f.now) },
		"stale timestamp":  func() []byte { return f.request(t, f.admin, "GET", "/api/echo", "", f.now.Add(-3*time.Minute)) },
		"future timestamp": func() []byte { return f.request(t, f.admin, "GET", "/api/echo", "", f.now.Add(3*time.Minute)) },
		"flipped ciphertext bit": func() []byte {
			raw := f.request(t, f.admin, "GET", "/api/echo", "hello", f.now)
			env, _ := Open(raw)
			i := bytes.Index(raw, env.Ciphertext)
			raw[i] ^= 1
			return raw
		},
		"re-signed by another key": func() []byte {
			// Strip the admin's signature and re-sign the same ciphertext
			// as the viewer promoted to admin: the sealed body still names
			// the original signer.
			raw := f.request(t, f.viewer, "GET", "/api/echo", "", f.now)
			env, _ := Open(raw)
			env.Signer = f.admin.Public().(ed25519.PublicKey)
			env.SenderKeyID = env.Signer
			buf := encodeEnvelope(env)
			return resign(buf, env, f.admin)
		},
		"garbage": func() []byte { return []byte("not an envelope at all") },
		"sealed to the wrong node": func() []byte {
			other, _ := secp256k1.GeneratePrivateKey()
			raw, _ := Seal(Body{Method: "GET", Route: "/api/echo", ReplyKey: f.replyPub}, SealOptions{
				RecipientPub: other.PubKey().SerializeCompressed(), KeyExchange: ecies.Secp256k1, Signer: f.admin, Now: f.now,
			})
			return raw
		},
	}
	for name, build := range cases {
		if rec := f.send(build()); rec.Code == http.StatusOK {
			t.Errorf("%s: accepted", name)
		}
	}

	// A replay of an accepted command is refused.
	raw := f.request(t, f.admin, "GET", "/api/echo", "", f.now)
	if rec := f.send(raw); rec.Code != http.StatusOK {
		t.Fatalf("first send: %d", rec.Code)
	}
	if rec := f.send(raw); rec.Code != http.StatusConflict {
		t.Fatalf("replay: %d", rec.Code)
	}

	// The sealed route cannot be nested inside itself.
	env, _ := Open(f.send(f.request(t, f.admin, "POST", Route, "", f.now)).Body.Bytes())
	if body, _ := env.Decrypt(f.replyPriv); body.Status != http.StatusBadRequest {
		t.Fatalf("nested route: %d", body.Status)
	}
}

func TestCanonicalJSONIsStable(t *testing.T) {
	env := Envelope{
		Header:      ecies.Header{KeyExchange: ecies.Secp256k1, EphemeralPub: []byte{1, 2}, NonceStart: []byte{3}, Context: ContextRequest},
		SessionID:   "a<b>&c",
		SenderKeyID: []byte{4}, Timestamp: time.UnixMilli(5), Nonce: []byte{6}, Ciphertext: []byte{7}, Signer: []byte{8},
	}
	got := string(CanonicalJSON(env))
	want := `{"VERSION":1,"DIRECTION":"Request","ENCRYPTION":{"VERSION":1,"KEY_EXCHANGE":"Secp256k1","SYMMETRIC":"AES_256_CTR","KEY_DERIVATION":"HKDF_SHA256","EPHEMERAL_PUBLIC_KEY":"AQI=","NONCE_START":"Aw==","CONTEXT":"RPC/1|request"},"SESSION_ID":"a<b>&c","SENDER_KEY_ID":"BA==","TIMESTAMP":5,"NONCE":"Bg==","CIPHERTEXT":"Bw==","SIGNER_PUBLIC_KEY":"CA==","SIGNATURE_TYPE":"Ed25519"}`
	if got != want {
		t.Fatalf("canonical JSON\n got %s\nwant %s", got, want)
	}
	if strings.Contains(got, "\\u003c") {
		t.Fatal("HTML escaping leaked into the canonical form")
	}
}

// resign signs buf (built with zeroed signature vectors) as key.
func resign(buf []byte, env Envelope, key ed25519.PrivateKey) []byte {
	fb := ed25519.Sign(key, buf)
	js := ed25519.Sign(key, CanonicalJSON(env))
	root := getRoot(buf)
	for i := range fb {
		root.MutateSIGNATURE(i, fb[i])
		root.MutateCANONICAL_JSON_SIGNATURE(i, js[i])
	}
	return buf
}

func getRoot(buf []byte) interface {
	MutateSIGNATURE(int, byte) bool
	MutateCANONICAL_JSON_SIGNATURE(int, byte) bool
} {
	return rpcRoot(buf)
}

type fakeDelegations map[string]ed25519.PublicKey

func (d fakeDelegations) DelegatedWallet(s ed25519.PublicKey) (ed25519.PublicKey, bool, bool) {
	w, ok := d[string(s)]
	return w, false, ok
}
func (d fakeDelegations) RevokeDelegation(s ed25519.PublicKey) { delete(d, string(s)) }

// A browser session key signs commands at its delegating wallet's trust, now.
func TestDelegatedSessionKeys(t *testing.T) {
	f := newFixture(t)
	_, viewerSessionPriv, _ := ed25519.GenerateKey(rand.Reader)
	_, adminSessionPriv, _ := ed25519.GenerateKey(rand.Reader)
	delegations := fakeDelegations{
		string(adminSessionPriv.Public().(ed25519.PublicKey)):  f.admin.Public().(ed25519.PublicKey),
		string(viewerSessionPriv.Public().(ed25519.PublicKey)): f.viewer.Public().(ed25519.PublicKey),
	}
	f.h.Delegations = delegations

	rec := f.send(f.request(t, adminSessionPriv, "GET", "/api/echo", "", f.now))
	env, err := Open(rec.Body.Bytes())
	if rec.Code != http.StatusOK || err != nil {
		t.Fatalf("admin session key: %d %v", rec.Code, err)
	}
	if body, _ := env.Decrypt(f.replyPriv); !strings.Contains(string(body.Body), "xpub-admin admin") {
		t.Fatalf("ran as %q", body.Body)
	}
	if rec := f.send(f.request(t, viewerSessionPriv, "GET", "/api/echo", "", f.now)); rec.Code != http.StatusForbidden {
		t.Fatalf("viewer session key = %d, want 403", rec.Code)
	}

	// Demoting the wallet takes effect on the next command.
	users := f.h.Admins.(users)
	users[hex.EncodeToString(f.admin.Public().(ed25519.PublicKey))].TrustLevel = peers.Standard
	if rec := f.send(f.request(t, adminSessionPriv, "GET", "/api/echo", "", f.now)); rec.Code != http.StatusForbidden {
		t.Fatalf("demoted wallet's session key = %d, want 403", rec.Code)
	}
	users[hex.EncodeToString(f.admin.Public().(ed25519.PublicKey))].TrustLevel = peers.Admin

	// Sign-out revokes the session key.
	if rec := f.send(f.request(t, adminSessionPriv, "POST", RevokeRoute, "", f.now)); rec.Code != http.StatusOK {
		t.Fatalf("revoke = %d", rec.Code)
	}
	if rec := f.send(f.request(t, adminSessionPriv, "GET", "/api/echo", "", f.now)); rec.Code != http.StatusForbidden {
		t.Fatalf("revoked session key = %d, want 403", rec.Code)
	}
}
