package main

// Tests for the §19 admin ceremony and the coverage commands
// (graph task nst-cli-full-coverage).

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// TestSessionTokenOverridePrecedence locks the escape hatch: the flag wins over
// the environment, and the environment is used when the flag is absent. This is
// the path a remote or non-root operator depends on, since they cannot read the
// node's seed.
func TestSessionTokenOverridePrecedence(t *testing.T) {
	cmd := &cobra.Command{Use: "x"}
	addSessionTokenFlag(cmd)

	t.Setenv("SDN_SESSION_TOKEN", "from-env")
	if got := sessionTokenOverride(cmd); got != "from-env" {
		t.Fatalf("env token not used: %q", got)
	}

	if err := cmd.Flags().Set("session-token", "from-flag"); err != nil {
		t.Fatal(err)
	}
	if got := sessionTokenOverride(cmd); got != "from-flag" {
		t.Fatalf("flag must beat env, got %q", got)
	}

	t.Setenv("SDN_SESSION_TOKEN", "")
	if err := cmd.Flags().Set("session-token", ""); err != nil {
		t.Fatal(err)
	}
	if got := sessionTokenOverride(cmd); got != "" {
		t.Fatalf("expected no override, got %q", got)
	}
}

// TestEveryAdminGatedCommandHasTheOverride locks that the escape hatch is
// uniform. A command that can only authenticate by reading the seed is unusable
// for a remote operator, and the audit found exactly that gap — most
// Admin-gated commands had no flag at all.
func TestEveryAdminGatedCommandHasTheOverride(t *testing.T) {
	for _, c := range []*cobra.Command{
		accountsListCmd, accountsAddCmd, accountsTrustCmd,
		identitySetCmd, identityKeysCmd, identityGenKeyCmd,
		pubsubTopicsCmd, pubsubMessagesCmd, pubsubPublishCmd,
		tablesListCmd, tablesQueryCmd,
	} {
		if c.Flags().Lookup("session-token") == nil {
			t.Fatalf("%q has no --session-token override; a remote operator cannot use it", c.CommandPath())
		}
	}
}

// TestAccountsTrustRequiresAnExplicitTarget locks §16's rule that a merged
// account row has TWO underlying records. Guessing which one to edit would
// silently change the wrong authority.
func TestAccountsTrustRequiresAnExplicitTarget(t *testing.T) {
	reset := func() { accountsTrustPeerID, accountsTrustXPub, accountsTrustLevel = "", "", "full" }

	reset()
	if err := accountsTrustCmd.RunE(accountsTrustCmd, nil); err == nil {
		t.Fatal("neither target supplied was accepted")
	} else if !strings.Contains(err.Error(), "exactly one") {
		t.Fatalf("error should demand exactly one target: %v", err)
	}

	reset()
	accountsTrustPeerID, accountsTrustXPub = "12D3KooW", "xpub-x"
	if err := accountsTrustCmd.RunE(accountsTrustCmd, nil); err == nil {
		t.Fatal("both targets supplied were accepted; the command must not guess")
	}

	reset()
	accountsTrustPeerID, accountsTrustLevel = "12D3KooW", ""
	if err := accountsTrustCmd.RunE(accountsTrustCmd, nil); err == nil {
		t.Fatal("missing --level was accepted")
	} else if !strings.Contains(err.Error(), "--level") {
		t.Fatalf("error should name --level: %v", err)
	}
	reset()
}

// TestCommandsRequireTheirIdentifiers locks the cheap argument guards, which all
// run BEFORE any network call so a typo never reaches the daemon.
func TestCommandsRequireTheirIdentifiers(t *testing.T) {
	accountsAddPeerID, accountsAddPublicKey, accountsAddVCardFile = "", "", ""
	if err := accountsAddCmd.RunE(accountsAddCmd, nil); err == nil {
		t.Fatal("accounts add with no identity was accepted")
	}

	identityGenKeySlot = ""
	if err := identityGenKeyCmd.RunE(identityGenKeyCmd, nil); err == nil {
		t.Fatal("gen-key with no --slot was accepted")
	}

	identitySetFile = ""
	if err := identitySetCmd.RunE(identitySetCmd, nil); err == nil {
		t.Fatal("identity set with no --file was accepted")
	}

	pubsubPublishTopic, pubsubPublishFile = "", ""
	if err := pubsubPublishCmd.RunE(pubsubPublishCmd, nil); err == nil {
		t.Fatal("pubsub publish with no --topic was accepted")
	}

	tablesQuerySQL, tablesQueryProvider, tablesQuerySchema, tablesQueryLimit = "", "", "", 0
	if err := tablesQueryCmd.RunE(tablesQueryCmd, nil); err == nil {
		t.Fatal("tables query with no selector was accepted")
	}
}

// TestJSONRawIsNotDoubleEncoded locks that an already-encoded profile passes
// through intact. Re-encoding it would send the whole document as a JSON
// string and the daemon would reject it as a malformed profile.
func TestJSONRawIsNotDoubleEncoded(t *testing.T) {
	body := jsonRaw([]byte(`{"dn":"Node","signing_key_path":"m/44'/0'/0'/0/1"}`))
	encoded, err := json.Marshal(map[string]any{"profile": body})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), `"dn":"Node"`) {
		t.Fatalf("raw JSON was re-encoded: %s", encoded)
	}
	if strings.Contains(string(encoded), `\"dn\"`) {
		t.Fatalf("raw JSON was double-encoded: %s", encoded)
	}
}

// TestAdminClientSendsSessionAndCSRFHeader locks the request envelope: the
// session cookie the daemon expects, plus X-Requested-With, which the CSRF
// middleware requires on cookie-authenticated state changes. A CLI has no
// Origin, so without that header every write would be refused.
func TestAdminClientSendsSessionAndCSRFHeader(t *testing.T) {
	var gotCookie, gotXRW, gotMethod string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotXRW = r.Header.Get("X-Requested-With")
		if c, err := r.Cookie(sessionCookieName); err == nil {
			gotCookie = c.Value
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	c := &adminClient{baseURL: srv.URL, token: "tok-123", http: srv.Client()}
	var out map[string]any
	if err := c.do(context.Background(), "PUT", "/api/node/epm", map[string]any{"dn": "x"}, &out); err != nil {
		t.Fatalf("request failed: %v", err)
	}
	if gotCookie != "tok-123" {
		t.Fatalf("session cookie not sent, got %q", gotCookie)
	}
	if gotXRW != "XMLHttpRequest" {
		t.Fatalf("X-Requested-With not sent (CSRF middleware would refuse), got %q", gotXRW)
	}
	if gotMethod != "PUT" {
		t.Fatalf("method = %q", gotMethod)
	}
	if out["ok"] != true {
		t.Fatalf("response not decoded: %+v", out)
	}
}

// TestAdminClientSurfacesServerErrorsVerbatim locks that a 400 from the daemon
// reaches the operator with its text intact — §18's path-validation messages
// explain the hardening rule, and swallowing them would strand the operator.
func TestAdminClientSurfacesServerErrorsVerbatim(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte("signing_key_path: component 4 (0') is hardened, so it cannot be derived"))
	}))
	defer srv.Close()

	c := &adminClient{baseURL: srv.URL, http: srv.Client()}
	err := c.do(context.Background(), "PUT", "/api/node/epm", map[string]any{}, nil)
	if err == nil {
		t.Fatal("a 400 was not reported as an error")
	}
	if !strings.Contains(err.Error(), "hardened") {
		t.Fatalf("server message was lost: %v", err)
	}
}

// TestSignInPerformsTheRealChallengeVerifyCeremony is the §19 lock. The CLI must
// sign the RAW 32 challenge bytes and echo the challenge back VERBATIM
// (unpadded RawStdEncoding — padding it would fail the server's decode), then
// use the issued cookie. This proves it goes through the real admit point
// rather than short-circuiting.
func TestSignInPerformsTheRealChallengeVerifyCeremony(t *testing.T) {
	const challengeB64 = "AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8" // 32 bytes, unpadded
	raw, err := base64.RawStdEncoding.DecodeString(challengeB64)
	if err != nil || len(raw) != 32 {
		t.Fatalf("fixture challenge is not 32 bytes: %v", err)
	}

	var verifiedSig, verifiedPub, echoedChallenge string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/auth/challenge":
			_, _ = w.Write([]byte(`{"challenge_id":"cid","challenge":"` + challengeB64 + `"}`))
		case "/api/auth/verify":
			verifiedSig, _ = body["signature_hex"].(string)
			verifiedPub, _ = body["client_pubkey_hex"].(string)
			echoedChallenge, _ = body["challenge"].(string)
			http.SetCookie(w, &http.Cookie{Name: sessionCookieName, Value: "issued-token"})
			_, _ = w.Write([]byte(`{"user":{"trust_level":"admin"}}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	// Drive the wire half directly with a known key, so the test does not need
	// a node seed on disk.
	c := &adminClient{baseURL: srv.URL, http: srv.Client()}
	priv := ed25519TestKey(t)
	token, err := signInWire(context.Background(), c, priv)
	if err != nil {
		t.Fatalf("sign-in failed: %v", err)
	}
	if token != "issued-token" {
		t.Fatalf("session cookie not adopted, got %q", token)
	}
	if echoedChallenge != challengeB64 {
		t.Fatalf("challenge must be echoed VERBATIM (unpadded); got %q", echoedChallenge)
	}
	sig, err := hex.DecodeString(verifiedSig)
	if err != nil {
		t.Fatalf("signature is not hex: %v", err)
	}
	pub, err := hex.DecodeString(verifiedPub)
	if err != nil {
		t.Fatalf("pubkey is not hex: %v", err)
	}
	if !verifyEd25519(pub, raw, sig) {
		t.Fatal("signature does not verify over the RAW challenge bytes — the admit point would reject it")
	}
}

// --- test helpers -----------------------------------------------------------

func ed25519TestKey(t *testing.T) ed25519.PrivateKey {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	return priv
}

func verifyEd25519(pub, message, sig []byte) bool {
	if len(pub) != ed25519.PublicKeySize || len(sig) != ed25519.SignatureSize {
		return false
	}
	return ed25519.Verify(ed25519.PublicKey(pub), message, sig)
}
