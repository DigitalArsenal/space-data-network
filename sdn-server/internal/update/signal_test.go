package update

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/sigdomain"
)

// A signal authorizes nothing, but it does decide whether the whole fleet
// spends bandwidth. These tests pin the gates that make a broadcast safe:
// signature under its OWN domain, target/channel/sequence relevance, freshness,
// and — the one that stops an infinite reinstall loop — the local quarantine.

func signalTestKey(t *testing.T) (ed25519.PublicKey, ed25519.PrivateKey, TrustedRoots, string) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	der, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		t.Fatalf("marshal key: %v", err)
	}
	encoded := base64.StdEncoding.EncodeToString(der)
	keyID := "testkey01234"
	return pub, priv, TrustedRoots{keyID: encoded}, keyID
}

// signTestSignal builds a signed signal, optionally under a different domain so
// cross-domain replay can be exercised.
func signTestSignal(t *testing.T, priv ed25519.PrivateKey, keyID string, mutate func(*Signal), domain string) []byte {
	t.Helper()
	signal := &Signal{
		Schema:      SignalSchema,
		UpdateID:    "sdn-cli-bundle-1.2.3",
		Version:     "1.2.3",
		Sequence:    100,
		Channel:     "beta",
		Target:      ManifestTarget{Platform: "linux", Arch: "amd64", Kind: "cli-bundle"},
		FeedBaseURL: "https://feed.example/updates",
		ManifestURL: "https://feed.example/updates/cli-bundle/beta/linux/amd64/1.2.3/manifest.json",
		CarrierURL:  "https://feed.example/updates/cli-bundle/beta/linux/amd64/1.2.3/update.wasm",
		PublishedAt: time.Now().UTC().Format(time.RFC3339),
		Signing: SignalSigning{
			KeyID:           keyID,
			Algorithm:       SignalSignatureAlgorithm,
			StatementDomain: domain,
		},
	}
	if mutate != nil {
		mutate(signal)
	}
	unsigned, err := json.Marshal(signal)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	canonical, err := CanonicalSignedDocument(unsigned)
	if err != nil {
		t.Fatalf("canonicalize: %v", err)
	}
	sum := sha256.Sum256(canonical)
	statement, err := sigdomain.Statement(domain, sum[:])
	if err != nil {
		t.Fatalf("statement: %v", err)
	}
	signal.Signing.Signature = base64.StdEncoding.EncodeToString(ed25519.Sign(priv, statement))
	signed, err := json.Marshal(signal)
	if err != nil {
		t.Fatalf("marshal signed: %v", err)
	}
	return signed
}

func hostOpts(roots TrustedRoots) SignalVerifyOptions {
	return SignalVerifyOptions{
		TrustedRoots:    roots,
		Platform:        "linux",
		Arch:            "amd64",
		Kind:            "cli-bundle",
		Channel:         "beta",
		CurrentSequence: 50,
		Now:             time.Now(),
	}
}

func TestSignalVerifiesAndIsActionable(t *testing.T) {
	_, priv, roots, keyID := signalTestKey(t)
	raw := signTestSignal(t, priv, keyID, nil, sigdomain.DomainUpdateSignalV1)
	signal, err := ParseSignal(raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if err := signal.Verify(hostOpts(roots)); err != nil {
		t.Fatalf("a well-formed signal from a trusted key must verify: %v", err)
	}
}

// The point of the separate statement domain: a signature minted over a MANIFEST
// must never be accepted as a signal, and vice versa. Without this, the cheap,
// frequent, broadcast document and the document that authorizes bytes would
// live in one preimage space.
func TestSignalRefusesAnotherDomainsSignature(t *testing.T) {
	_, priv, roots, keyID := signalTestKey(t)
	for _, domain := range []string{sigdomain.DomainUpdateManifestV1, sigdomain.DomainModulePublicationV1} {
		raw := signTestSignal(t, priv, keyID, nil, domain)
		signal, err := ParseSignal(raw)
		if err != nil {
			t.Fatalf("parse: %v", err)
		}
		err = signal.Verify(hostOpts(roots))
		if err == nil {
			t.Fatalf("a %s signature was accepted as an update signal — cross-protocol replay is possible", domain)
		}
		if !strings.Contains(err.Error(), sigdomain.DomainUpdateSignalV1) {
			t.Fatalf("refusal should name the required domain, got %v", err)
		}
	}
}

func TestSignalRefusesUntrustedKeyAndTamperedFields(t *testing.T) {
	_, priv, roots, keyID := signalTestKey(t)

	raw := signTestSignal(t, priv, keyID, nil, sigdomain.DomainUpdateSignalV1)
	signal, err := ParseSignal(raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if err := signal.Verify(hostOpts(TrustedRoots{"someoneelse": "AAAA"})); err == nil {
		t.Fatal("a signal signed by a key this box does not trust must be refused")
	}

	// Tamper with a field the signature covers: the carrier URL. This is the
	// attack the signature exists to stop — repointing the fetch.
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	doc["carrier_url"] = "https://attacker.example/update.wasm"
	tampered, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	tamperedSignal, err := ParseSignal(tampered)
	if err != nil {
		t.Fatalf("parse tampered: %v", err)
	}
	if err := tamperedSignal.Verify(hostOpts(roots)); err == nil {
		t.Fatal("a signal whose carrier URL was rewritten must not verify")
	}
}

func TestSignalIrrelevanceIsRefusedBeforeAnythingIsFetched(t *testing.T) {
	_, priv, roots, keyID := signalTestKey(t)

	cases := map[string]struct {
		mutate func(*Signal)
		opts   func(SignalVerifyOptions) SignalVerifyOptions
	}{
		"wrong platform": {mutate: func(s *Signal) { s.Target.Platform = "darwin" }},
		"wrong arch":     {mutate: func(s *Signal) { s.Target.Arch = "arm64" }},
		"wrong kind":     {mutate: func(s *Signal) { s.Target.Kind = "module-update" }},
		"wrong channel":  {mutate: func(s *Signal) { s.Channel = "stable" }},
		"not newer": {
			opts: func(o SignalVerifyOptions) SignalVerifyOptions { o.CurrentSequence = 100; return o },
		},
		"expired": {
			mutate: func(s *Signal) {
				s.PublishedAt = time.Now().Add(-72 * time.Hour).UTC().Format(time.RFC3339)
			},
		},
		"from the future": {
			mutate: func(s *Signal) {
				s.PublishedAt = time.Now().Add(2 * time.Hour).UTC().Format(time.RFC3339)
			},
		},
		"plain http carrier": {
			mutate: func(s *Signal) { s.CarrierURL = "http://feed.example/update.wasm" },
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			raw := signTestSignal(t, priv, keyID, tc.mutate, sigdomain.DomainUpdateSignalV1)
			signal, err := ParseSignal(raw)
			if err != nil {
				return // a malformed document refused at parse is also a pass
			}
			opts := hostOpts(roots)
			if tc.opts != nil {
				opts = tc.opts(opts)
			}
			if err := signal.Verify(opts); err == nil {
				t.Fatalf("%s: signal was accepted; it must be refused before any fetch happens", name)
			}
		})
	}
}

func TestSignalTopicMatchesTheBundleConvention(t *testing.T) {
	if got := SignalTopic("beta"); got != "/sdn/updates/v1/beta" {
		t.Fatalf("SignalTopic(beta) = %q, want the topic shipped bundle manifests already declare", got)
	}
	if got := SignalTopic(""); got != "/sdn/updates/v1/stable" {
		t.Fatalf("SignalTopic(\"\") = %q, want the stable default", got)
	}
}

// The quarantine is what makes a replayed signal harmless. A box that rolled
// back has a LOWER sequence again, so the sequence gate alone would let the
// same signal reinstall the build the box just judged unhealthy — on a loop.
func TestFailedUpdateQuarantine(t *testing.T) {
	paths := slotPaths(t)
	if HasFailedUpdate(paths, "u1") {
		t.Fatal("nothing has failed yet")
	}
	if err := os.MkdirAll(filepath.Join(paths.Failed, "u1"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if !HasFailedUpdate(paths, "u1") {
		t.Fatal("an update this box already tried and reversed must be quarantined")
	}
	if HasFailedUpdate(paths, "") {
		t.Fatal("an empty update id must never match")
	}
}

// The helper runs the swap. It must never be handed the daemon's secrets: under
// systemd-run every passed variable becomes a queryable unit property.
func TestHelperRuntimeEnvCarriesRuntimeVarsAndNoSecrets(t *testing.T) {
	env := helperRuntimeEnv([]string{
		"LD_LIBRARY_PATH=/opt/wasmedge/lib64",
		"WASMEDGE_DIR=/opt/wasmedge",
		"PATH=/usr/bin",
		"SDN_UPDATE_TRUST_ROOTS=/opt/trust.json",
		"SDN_KEY_PASSWORD=hunter2",
		"SDN_MNEMONIC_FILE=/root/seed",
		"AWS_SECRET_ACCESS_KEY=abc",
		"SDN_SESSION_TOKEN=deadbeef",
		"RANDOM_UNRELATED=1",
	})
	joined := strings.Join(env, "\n")
	for _, want := range []string{"LD_LIBRARY_PATH=", "WASMEDGE_DIR=", "PATH=", "SDN_UPDATE_TRUST_ROOTS="} {
		if !strings.Contains(joined, want) {
			t.Fatalf("helper env is missing %s; the helper cannot even exec without its dynamic linker view", want)
		}
	}
	for _, forbidden := range []string{"PASSWORD", "MNEMONIC", "SECRET", "TOKEN", "hunter2", "deadbeef"} {
		if strings.Contains(joined, forbidden) {
			t.Fatalf("helper env leaked %q — under systemd-run this becomes a queryable unit property", forbidden)
		}
	}
	if strings.Contains(joined, "RANDOM_UNRELATED") {
		t.Fatal("helper env should be an allow-list, not a filter over everything")
	}
}
