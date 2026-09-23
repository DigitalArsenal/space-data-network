package sealed

import (
	"bytes"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	secp256k1 "github.com/decred/dcrd/dcrec/secp256k1/v4"
	"golang.org/x/crypto/curve25519"

	"github.com/spacedatanetwork/sdn-server/internal/ecies"
)

// Cross-runtime vectors with sdn-js src/sealed-rpc.ts. Fixed, test-only keys.
var (
	vecNodeEncPriv  = bytes.Repeat([]byte{0x11}, 32)
	vecNodeSignSeed = bytes.Repeat([]byte{0x22}, 32)
	vecAdminSeed    = bytes.Repeat([]byte{0x33}, 32)
	vecReplyPriv    = bytes.Repeat([]byte{0x44}, 32)
)

type vector struct {
	NodeEncPrivHex string `json:"nodeEncPrivHex"`
	NodeEncPubHex  string `json:"nodeEncPubHex"`
	NodeSignPubHex string `json:"nodeSignPubHex"`
	AdminSeedHex   string `json:"adminSeedHex"`
	ReplyPrivHex   string `json:"replyPrivHex"`
	RequestHex     string `json:"requestHex"`
	RequestJSON    string `json:"requestCanonicalJson"`
	ResponseHex    string `json:"responseHex"`
	Method         string `json:"method"`
	Route          string `json:"route"`
	Body           string `json:"body"`
}

// Regenerate with: SEALED_WRITE_VECTOR=1 go test ./internal/sealed -run TestGoVector
func TestGoVector(t *testing.T) {
	path := filepath.Join("testdata", "go-vector.json")
	if os.Getenv("SEALED_WRITE_VECTOR") == "" {
		if _, err := os.Stat(path); err != nil {
			t.Skip("no committed vector")
		}
		return
	}
	nodePub := secp256k1.PrivKeyFromBytes(vecNodeEncPriv).PubKey().SerializeCompressed()
	replyPub, _ := curve25519.X25519(vecReplyPriv, curve25519.Basepoint)
	admin := ed25519.NewKeyFromSeed(vecAdminSeed)
	nodeSign := ed25519.NewKeyFromSeed(vecNodeSignSeed)
	now := time.UnixMilli(1_800_000_000_000)
	req, err := Seal(Body{Method: "PUT", Route: "/api/node/epm", Body: []byte("hello <node> & co"), ReplyKey: replyPub},
		SealOptions{RecipientPub: nodePub, KeyExchange: ecies.Secp256k1, Signer: admin, SessionID: "s-1", Now: now})
	if err != nil {
		t.Fatal(err)
	}
	env, err := Open(req)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := Seal(Body{Status: 200, Body: []byte("ok"), BodyFileID: "text/plain", RequestNonce: env.Nonce, RequestDigest: Digest(req)},
		SealOptions{Response: true, RecipientPub: replyPub, KeyExchange: ecies.X25519, Signer: nodeSign, SessionID: "s-1", Now: now})
	if err != nil {
		t.Fatal(err)
	}
	out, _ := json.MarshalIndent(vector{
		NodeEncPrivHex: hex.EncodeToString(vecNodeEncPriv), NodeEncPubHex: hex.EncodeToString(nodePub),
		NodeSignPubHex: hex.EncodeToString(nodeSign.Public().(ed25519.PublicKey)),
		AdminSeedHex:   hex.EncodeToString(vecAdminSeed), ReplyPrivHex: hex.EncodeToString(vecReplyPriv),
		RequestHex: hex.EncodeToString(req), RequestJSON: string(CanonicalJSON(env)), ResponseHex: hex.EncodeToString(resp),
		Method: "PUT", Route: "/api/node/epm", Body: "hello <node> & co",
	}, "", "  ")
	if err := os.WriteFile(path, out, 0o644); err != nil {
		t.Fatal(err)
	}
}

// The committed JavaScript vector must open here.
func TestJSVectorOpensInGo(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "js-vector.json"))
	if err != nil {
		t.Skip("no committed JS vector yet")
	}
	var v struct {
		NodeEncPrivHex      string `json:"nodeEncPrivHex"`
		SessionPubHex       string `json:"sessionPubHex"`
		RequestHex          string `json:"requestHex"`
		Method, Route, Body string
	}
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatal(err)
	}
	req, _ := hex.DecodeString(v.RequestHex)
	env, err := Open(req)
	if err != nil {
		t.Fatalf("Go could not verify the JS envelope: %v", err)
	}
	if hex.EncodeToString(env.Signer) != v.SessionPubHex {
		t.Fatal("signer mismatch")
	}
	priv, _ := hex.DecodeString(v.NodeEncPrivHex)
	body, err := env.Decrypt(priv)
	if err != nil {
		t.Fatalf("Go could not open the JS envelope: %v", err)
	}
	if body.Method != v.Method || body.Route != v.Route || string(body.Body) != v.Body {
		t.Fatalf("body %+v", body)
	}
}
