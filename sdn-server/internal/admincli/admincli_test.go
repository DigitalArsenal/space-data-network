package admincli

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/auth"
	"github.com/spacedatanetwork/sdn-server/internal/flatsqldrv"
	"github.com/spacedatanetwork/sdn-server/internal/peers"
	"github.com/spacedatanetwork/sdn-server/internal/walletderive"
	"github.com/spacedatanetwork/sdn-server/internal/wasm"
)

// node is an auth store plus the real sign-in routes over it.
type node struct {
	store AuthDBStore
	mux   *http.ServeMux
}

func newNode(t *testing.T) node {
	t.Helper()
	dir := t.TempDir()
	users, err := auth.NewUserStore(filepath.Join(dir, "auth.db"), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { users.Close() })
	sdb, closer, err := flatsqldrv.OpenStandalone(filepath.Join(dir, "sessions.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { closer() })
	sessions, err := auth.NewSessionStore(sdb)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	auth.NewHandler(users, sessions, time.Hour, "", "").RegisterRoutes(mux)
	return node{store: AuthDBStore{Users: users, Path: "auth.db"}, mux: mux}
}

// signIn runs the dashboard's challenge/verify exchange (no xpub, raw
// 32-byte challenge signed with Ed25519) and returns the granted trust.
func (n node) signIn(t *testing.T, priv ed25519.PrivateKey) (peers.TrustLevel, int) {
	t.Helper()
	pubHex := hex.EncodeToString(priv.Public().(ed25519.PublicKey))
	post := func(path string, body any) *httptest.ResponseRecorder {
		raw, _ := json.Marshal(body)
		req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(raw))
		req.RemoteAddr = "203.0.113.7:4000"
		rec := httptest.NewRecorder()
		n.mux.ServeHTTP(rec, req)
		return rec
	}
	ch := post("/api/auth/challenge", map[string]any{"client_pubkey_hex": pubHex, "ts": time.Now().Unix()})
	if ch.Code != http.StatusOK {
		t.Fatalf("challenge: %d %s", ch.Code, ch.Body.String())
	}
	var c struct {
		ChallengeID string `json:"challenge_id"`
		Challenge   string `json:"challenge"`
	}
	if err := json.Unmarshal(ch.Body.Bytes(), &c); err != nil {
		t.Fatal(err)
	}
	challenge, err := base64.RawStdEncoding.DecodeString(c.Challenge)
	if err != nil {
		t.Fatal(err)
	}
	ver := post("/api/auth/verify", map[string]any{
		"challenge_id":      c.ChallengeID,
		"client_pubkey_hex": pubHex,
		"challenge":         c.Challenge,
		"signature_hex":     hex.EncodeToString(ed25519.Sign(priv, challenge)),
	})
	var v struct {
		User struct {
			TrustLevel peers.TrustLevel `json:"trust_level"`
		} `json:"user"`
	}
	_ = json.Unmarshal(ver.Body.Bytes(), &v)
	return v.User.TrustLevel, ver.Code
}

func terminal(input string, interactive bool) (Terminal, *bytes.Buffer) {
	out := &bytes.Buffer{}
	in := bufio.NewReader(strings.NewReader(input))
	term := Terminal{In: in, Out: out}
	if interactive {
		// Secrets arrive on the same scripted input, one line each.
		term.ReadSecret = func(string) ([]byte, error) {
			line, err := in.ReadString('\n')
			return []byte(strings.TrimRight(line, "\n")), err
		}
	}
	return term, out
}

var testKeys = ServerKeys{PeerID: "16Uiu2HAtest", SigningPubKeyHex: strings.Repeat("ab", 32), SigningKeyPath: "m/44'/0'/0'/0'/0'"}

func TestPastedKeyAdminSignsInAtAdminTrust(t *testing.T) {
	n := newNode(t)
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)

	// Before enrollment the key signs in at no trust.
	if trust, _ := n.signIn(t, priv); trust >= peers.Admin {
		t.Fatalf("unenrolled key reached %v", trust)
	}

	term, out := terminal("Ops Lead\n3\n"+hex.EncodeToString(pub)+"\ny\n", true)
	e, err := Init(context.Background(), Options{}, term, testKeys, nil, n.store)
	if err != nil {
		t.Fatalf("Init: %v\n%s", err, out)
	}
	if e.RowKey != walletderive.PubKeyRowPrefix+hex.EncodeToString(pub) {
		t.Fatalf("row key %q", e.RowKey)
	}
	if trust, code := n.signIn(t, priv); code != http.StatusOK || trust != peers.Admin {
		t.Fatalf("enrolled key: status %d trust %v", code, trust)
	}

	// The same key cannot be enrolled twice.
	term, _ = terminal("", false)
	if _, err := Init(context.Background(), Options{Name: "Again", Source: SourcePublicKey, PublicKey: hex.EncodeToString(pub), Yes: true}, term, testKeys, nil, n.store); err == nil {
		t.Fatal("enrolled the same key twice")
	}
}

func TestPasswordAdminSignsInWithTheWalletKey(t *testing.T) {
	w := testWallet(t)
	n := newNode(t)
	const user, pass = "remote-admin", "correct horse battery staple"

	term, out := terminal(pass+"\n", false)
	opts := Options{Name: "Remote Admin", Source: SourcePassword, Username: user, SecretFromStdin: true, Yes: true}
	if _, err := Init(context.Background(), opts, term, testKeys, w, n.store); err != nil {
		t.Fatalf("Init: %v\n%s", err, out)
	}
	if strings.Contains(out.String(), pass) {
		t.Fatal("the wizard echoed the password")
	}

	// The browser wallet's key for the same credentials, derived independently.
	seed, err := walletderive.PasswordSeed([]byte(user), []byte(pass))
	if err != nil {
		t.Fatal(err)
	}
	scalar, err := w.DeriveSecp256k1PrivateKey(context.Background(), seed, walletderive.SignInKeyPath)
	if err != nil {
		t.Fatal(err)
	}
	if trust, code := n.signIn(t, ed25519.NewKeyFromSeed(scalar)); code != http.StatusOK || trust != peers.Admin {
		t.Fatalf("password admin: status %d trust %v", code, trust)
	}
}

func TestWizardRefusals(t *testing.T) {
	n := newNode(t)
	ctx := context.Background()

	term, _ := terminal("short\n", false)
	if _, err := Init(ctx, Options{Name: "A", Source: SourcePassword, Username: "a", SecretFromStdin: true, Yes: true}, term, testKeys, nil, n.store); err == nil || !strings.Contains(err.Error(), "at least") {
		t.Fatalf("short password: %v", err)
	}

	term, _ = terminal("a very long password here\nnot the same password\n", true)
	if _, err := Init(ctx, Options{Name: "A", Source: SourcePassword, Username: "a"}, term, testKeys, nil, n.store); err == nil || !strings.Contains(err.Error(), "do not match") {
		t.Fatalf("mismatched confirmation: %v", err)
	}

	term, _ = terminal("", false)
	if _, err := Init(ctx, Options{Source: SourcePublicKey}, term, testKeys, nil, n.store); err == nil || !strings.Contains(err.Error(), "no terminal") {
		t.Fatalf("missing name without a terminal: %v", err)
	}

	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	term, _ = terminal("n\n", true)
	if _, err := Init(ctx, Options{Name: "A", Source: SourcePublicKey, PublicKey: hex.EncodeToString(pub)}, term, testKeys, nil, n.store); err == nil {
		t.Fatal("enrolled after the operator answered no")
	}
	if ok, _ := n.store.HasAdmin(ctx); ok {
		t.Fatal("a refused run left an admin behind")
	}
}

func testWallet(t *testing.T) walletderive.HDWallet {
	t.Helper()
	path := ""
	for _, p := range []string{os.Getenv("HD_WALLET_WASM_PATH"), "../../../sdn-js/node_modules/hd-wallet-wasm/dist/hd-wallet-wasi.wasm"} {
		if p == "" {
			continue
		}
		if _, err := os.Stat(p); err == nil {
			path = p
			break
		}
	}
	if path == "" {
		t.Skip("HD wallet WASI artifact not found; set HD_WALLET_WASM_PATH")
	}
	ctx := context.Background()
	hw, err := wasm.NewHDWalletModule(ctx, path)
	if err != nil {
		t.Fatalf("load HD wallet WASM: %v", err)
	}
	entropy := make([]byte, 64)
	if _, err := rand.Read(entropy); err != nil {
		t.Fatal(err)
	}
	if err := hw.InjectEntropy(ctx, entropy); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { hw.Close(context.Background()) })
	return walletderive.HDWallet{HDWalletModule: hw}
}
