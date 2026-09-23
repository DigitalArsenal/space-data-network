package auth

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestBase58(t *testing.T) {
	if got := Base58(make([]byte, 32)); got != "11111111111111111111111111111111" {
		t.Fatalf("zero key = %s", got)
	}
	for i := 0; i < 20; i++ {
		b := make([]byte, 32)
		rand.Read(b)
		back, err := DecodeBase58(Base58(b))
		if err != nil || !bytes.Equal(back, b) {
			t.Fatalf("round trip %x -> %x %v", b, back, err)
		}
	}
	if _, err := DecodeBase58("0OIl"); err == nil {
		t.Fatal("accepted non-base58 characters")
	}
}

// Pinned so the dashboard (lib/session/siws.js) builds the same bytes.
func TestSIWSMessageText(t *testing.T) {
	wallet := bytes.Repeat([]byte{1}, 32)
	session := bytes.Repeat([]byte{2}, 32)
	got := SIWSMessage("sdn.example.org", "https://sdn.example.org", wallet, bytes.Repeat([]byte{3}, 32), session,
		time.UnixMilli(1_800_000_000_000), time.UnixMilli(1_800_028_800_000))
	want := "sdn.example.org wants you to administer this Space Data Network node with your Solana account:\n" +
		Base58(wallet) + "\n\n" + SIWSStatement + "\n\n" +
		"URI: https://sdn.example.org\nVersion: 1\nChain ID: solana\n" +
		"Nonce: AwMDAwMDAwMDAwMDAwMDAwMDAwMDAwMDAwMDAwMDAwM\n" +
		"Issued At: 2027-01-15T08:00:00.000Z\nExpiration Time: 2027-01-15T16:00:00.000Z\n" +
		"Session Key: " + hex.EncodeToString(session)
	if got != want {
		t.Fatalf("message\n%q\nwant\n%q", got, want)
	}
}

func TestSIWSDelegation(t *testing.T) {
	mux, h, wallet := delegationFixture(t)
	walletPub := wallet.Public().(ed25519.PublicKey)
	session, _, _ := ed25519.GenerateKey(rand.Reader)
	expires := time.Now().Add(time.Hour).Truncate(time.Millisecond)

	send := func(host, origin string, issued time.Time, signText func(challenge []byte) []byte) int {
		id, challenge := challengeFor(t, mux, walletPub)
		body := map[string]any{
			"challenge_id": id, "client_pubkey_hex": hex.EncodeToString(walletPub),
			"session_pubkey_hex": hex.EncodeToString(session), "expires_at_ms": expires.UnixMilli(),
			"scheme": "siws", "origin": origin, "issued_at_ms": issued.UnixMilli(),
			"signature_hex": hex.EncodeToString(ed25519.Sign(wallet, signText(challenge))),
		}
		raw, _ := json.Marshal(body)
		req := httptest.NewRequest(http.MethodPost, "/api/auth/delegate", bytes.NewReader(raw))
		req.Host = host
		req.RemoteAddr = "203.0.113.4:1000"
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec.Code
	}
	now := time.Now().Truncate(time.Millisecond)
	honest := func(host, origin string) func([]byte) []byte {
		return func(c []byte) []byte {
			return []byte(SIWSMessage(host, origin, walletPub, c, session, now, expires))
		}
	}

	// A message that names another host, or an origin on another host, fails.
	if code := send("sdn.example.org", "https://sdn.example.org", now, honest("evil.example", "https://sdn.example.org")); code == http.StatusOK {
		t.Fatal("accepted a message naming another host")
	}
	if code := send("sdn.example.org", "https://evil.example", now, honest("sdn.example.org", "https://evil.example")); code == http.StatusOK {
		t.Fatal("accepted an origin on another host")
	}
	// A stale issue time fails.
	if code := send("sdn.example.org", "https://sdn.example.org", now.Add(-10*time.Minute), honest("sdn.example.org", "https://sdn.example.org")); code == http.StatusOK {
		t.Fatal("accepted a stale message")
	}
	// The raw digest signed under the SIWS scheme fails: opaque bytes are
	// never what an external wallet is asked to sign.
	if code := send("sdn.example.org", "https://sdn.example.org", now, func(c []byte) []byte {
		return DelegationDigest(c, session, uint64(expires.UnixMilli()))
	}); code == http.StatusOK {
		t.Fatal("accepted a raw digest under siws")
	}

	if code := send("sdn.example.org", "https://sdn.example.org", now, honest("sdn.example.org", "https://sdn.example.org")); code != http.StatusOK {
		t.Fatalf("honest SIWS delegation = %d", code)
	}
	if got, _, ok := h.DelegatedWallet(session); !ok || !bytes.Equal(got, walletPub) {
		t.Fatal("session not delegated")
	}
}
