package auth

// Locks the master-xpub guard (graph task nst-node-admin-contract, integration
// finding 1). hd-wallet-ui's LEGACY identity schemes — the only ones that can
// produce the raw-32 signature this node's admit point verifies — report the
// BIP-32 MASTER xpub (depth 0) as their accountXpub. Measured 2026-07-27
// against hd-wallet-ui/hd-wallet-wasm 2.0.28; the modern (v2) identity reports
// a proper depth-3 account xpub instead.
//
// The node therefore refuses to MINT an operator identity from a master xpub,
// because that write is a one-way door: once auth.db holds a master xpub it is
// a durable at-rest secret enumerating the whole wallet, and it can never match
// a users: entry seeded from show-identity.

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mr-tron/base58"

	"github.com/spacedatanetwork/sdn-server/internal/flatsqldrv"
)

// makeXPub builds a checksum-valid mainnet xpub at the requested depth. Key
// material is arbitrary and generated here — never a real wallet's.
func makeXPub(t *testing.T, depth byte) string {
	t.Helper()

	body := make([]byte, 78)
	binary.BigEndian.PutUint32(body[0:4], xpubMainnetPublicVersion)
	body[4] = depth
	// parent fingerprint (4) + child number (4) + chain code (32) may be any
	// bytes for a depth/serialization test.
	for i := 5; i < 45; i++ {
		body[i] = byte(i)
	}
	// A syntactically valid compressed secp256k1 point prefix.
	body[45] = 0x02
	for i := 46; i < 78; i++ {
		body[i] = byte(i * 3)
	}
	first := sha256.Sum256(body)
	second := sha256.Sum256(first[:])
	return base58.Encode(append(body, second[:4]...))
}

func TestXPubDepthReadsSerializedDepth(t *testing.T) {
	t.Parallel()

	for _, depth := range []byte{0, 1, 3, 5} {
		depth := depth
		t.Run(string(rune('0'+depth)), func(t *testing.T) {
			t.Parallel()
			got, ok := XPubDepth(makeXPub(t, depth))
			if !ok {
				t.Fatalf("depth %d xpub was not recognised", depth)
			}
			if got != int(depth) {
				t.Fatalf("depth = %d, want %d", got, depth)
			}
		})
	}
}

// TestXPubDepthTreatsNonXPubsAsUnknown locks that the guard NEVER turns opaque
// operator labels or test fixtures into rejections. The node has always
// accepted arbitrary strings as UserStore keys; this helper identifies one
// proven-dangerous shape, it does not start policing labels.
func TestXPubDepthTreatsNonXPubsAsUnknown(t *testing.T) {
	t.Parallel()

	valid := makeXPub(t, 0)
	corrupted := valid[:len(valid)-1] + string(rune(valid[len(valid)-1]^1))

	for name, value := range map[string]string{
		"empty":            "",
		"operator label":   "xpub-test-admin",
		"not base58":       "xpub!!!!not-base58!!!!",
		"too short":        "xpub6D",
		"bad checksum":     corrupted,
		"unrelated string": "operator@example.invalid",
	} {
		name, value := name, value
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if _, ok := XPubDepth(value); ok {
				t.Fatalf("XPubDepth(%q) claimed to know a depth", value)
			}
			if IsMasterXPub(value) {
				t.Fatalf("IsMasterXPub(%q) = true; unknown must never mean unsafe", value)
			}
		})
	}
}

func TestIsMasterXPubOnlyForDepthZero(t *testing.T) {
	t.Parallel()

	if !IsMasterXPub(makeXPub(t, 0)) {
		t.Fatal("a depth-0 xpub was not identified as a master key")
	}
	for _, depth := range []byte{1, 2, 3, 4} {
		if IsMasterXPub(makeXPub(t, depth)) {
			t.Fatalf("a depth-%d xpub was misidentified as a master key", depth)
		}
	}
}

// newBootstrapHandler builds a handler over an EMPTY user store — the
// first-admin bootstrap condition.
func newBootstrapHandler(t *testing.T) *Handler {
	t.Helper()

	dir := t.TempDir()
	userStore, err := NewUserStore(filepath.Join(dir, "users.db"), nil)
	if err != nil {
		t.Fatalf("NewUserStore: %v", err)
	}
	t.Cleanup(func() { _ = userStore.Close() })

	sdb, closer, err := flatsqldrv.OpenStandalone(filepath.Join(dir, "sessions.db"))
	if err != nil {
		t.Fatalf("OpenStandalone: %v", err)
	}
	t.Cleanup(func() { _ = closer() })

	sessions, err := NewSessionStore(sdb)
	if err != nil {
		t.Fatalf("NewSessionStore: %v", err)
	}
	if userStore.HasAdmin() {
		t.Fatal("fixture user store already has an admin")
	}
	return NewHandler(userStore, sessions, time.Hour, "", "")
}

// bootstrapAttempt drives a full challenge+verify first-admin bootstrap with
// the supplied xpub and reports whether an admin was created.
func bootstrapAttempt(t *testing.T, h *Handler, xpub string) (created bool, verifyStatus int) {
	t.Helper()

	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	pubHex := hex.EncodeToString(pub)

	body := `{"xpub":"` + xpub + `","client_pubkey_hex":"` + pubHex +
		`","ts":` + itoa(time.Now().Unix()) + `}`
	rec := httptest.NewRecorder()
	h.handleChallenge(rec, httptest.NewRequest(http.MethodPost, "/api/auth/challenge", strings.NewReader(body)))
	if rec.Code != http.StatusOK {
		t.Fatalf("challenge status = %d: %s", rec.Code, rec.Body.String())
	}

	var ch struct {
		ChallengeID string `json:"challenge_id"`
		Challenge   string `json:"challenge"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &ch); err != nil {
		t.Fatalf("decode challenge: %v", err)
	}
	raw, err := base64.RawStdEncoding.DecodeString(ch.Challenge)
	if err != nil {
		t.Fatalf("decode challenge bytes: %v", err)
	}

	vbody := `{"challenge_id":"` + ch.ChallengeID + `","xpub":"` + xpub +
		`","client_pubkey_hex":"` + pubHex + `","challenge":"` + ch.Challenge +
		`","signature_hex":"` + hex.EncodeToString(ed25519.Sign(priv, raw)) + `"}`
	vrec := httptest.NewRecorder()
	h.handleVerify(vrec, httptest.NewRequest(http.MethodPost, "/api/auth/verify", strings.NewReader(vbody)))

	return h.userStore.HasAdmin(), vrec.Code
}

// TestBootstrapRefusesMasterXPub is the finding-1 lock: a wallet presenting its
// legacy master xpub cannot mint the first admin.
func TestBootstrapRefusesMasterXPub(t *testing.T) {
	t.Parallel()

	h := newBootstrapHandler(t)
	created, status := bootstrapAttempt(t, h, makeXPub(t, 0))

	if created {
		t.Fatal("a BIP-32 master xpub was written into the user store as the first admin")
	}
	if status != http.StatusForbidden {
		t.Fatalf("verify status = %d, want %d", status, http.StatusForbidden)
	}
}

// TestBootstrapAcceptsAccountXPub locks that the guard is narrow: a proper
// account-level xpub — what the node's own derivation and the modern (v2)
// wallet identity both report — still bootstraps.
func TestBootstrapAcceptsAccountXPub(t *testing.T) {
	t.Parallel()

	h := newBootstrapHandler(t)
	created, status := bootstrapAttempt(t, h, makeXPub(t, 3))

	if !created {
		t.Fatalf("a depth-3 account xpub failed to bootstrap; verify status = %d", status)
	}
	if status != http.StatusOK {
		t.Fatalf("verify status = %d, want %d", status, http.StatusOK)
	}
}

// TestBootstrapAcceptsOpaqueOperatorLabel locks that config-style identifiers,
// which are not serialized xpubs at all, are unaffected.
func TestBootstrapAcceptsOpaqueOperatorLabel(t *testing.T) {
	t.Parallel()

	h := newBootstrapHandler(t)
	created, status := bootstrapAttempt(t, h, "xpub-operator-label")

	if !created {
		t.Fatalf("an opaque operator label failed to bootstrap; verify status = %d", status)
	}
	if status != http.StatusOK {
		t.Fatalf("verify status = %d, want %d", status, http.StatusOK)
	}
}
