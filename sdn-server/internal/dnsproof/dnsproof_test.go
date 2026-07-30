package dnsproof

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	"github.com/decred/dcrd/dcrec/secp256k1/v4/ecdsa"
)

// livePubKeyHex is the Ed25519 EPM signing key published by sdn.spaceaware.io,
// read from its own EPM record on 2026-07-30
// (https://sdn.spaceaware.io/identity/16Uiu2HAm1LbvwjEHW2GDP2ZQZvwHLZrz2jbYoRLQmJEQ3wZ5Fm45.epm,
// signing_key_path m/44'/0'/0'/0'/0'). It is public key material and is used
// here only to prove the RECORD FITS the real deployment, not to sign anything.
const (
	livePubKeyHex = "0d80e1fd5f9a4e34dfdf36a0e152bd99a65cfff8bcc6cab2757b484ae442fc8c"
	livePeerID    = "16Uiu2HAm1LbvwjEHW2GDP2ZQZvwHLZrz2jbYoRLQmJEQ3wZ5Fm45"
	liveDomain    = "sdn.spaceaware.io"
)

func mustHex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatalf("hex %q: %v", s, err)
	}
	return b
}

// TestCanonicalStatementIsGolden pins the signed bytes. If this test has to be
// updated, every already-published proof in DNS has just been invalidated —
// which is the entire reason the bytes are pinned in a test instead of trusted
// to review.
func TestCanonicalStatementIsGolden(t *testing.T) {
	p := Proof{
		Domain:    liveDomain,
		Algorithm: AlgEd25519,
		PublicKey: mustHex(t, livePubKeyHex),
		PeerID:    livePeerID,
		IssuedAt:  1785400000,
		ExpiresAt: 1816936000,
	}
	got, err := CanonicalStatement(p)
	if err != nil {
		t.Fatalf("CanonicalStatement: %v", err)
	}
	want := "sdn-domain-proof/1\n" +
		"domain=sdn.spaceaware.io\n" +
		"key=ed25519:0d80e1fd5f9a4e34dfdf36a0e152bd99a65cfff8bcc6cab2757b484ae442fc8c\n" +
		"peerid=16Uiu2HAm1LbvwjEHW2GDP2ZQZvwHLZrz2jbYoRLQmJEQ3wZ5Fm45\n" +
		"issued=1785400000\n" +
		"expires=1816936000\n"
	if string(got) != want {
		t.Fatalf("canonical statement drifted.\n got: %q\nwant: %q", got, want)
	}
	if !strings.HasSuffix(string(got), "\n") {
		t.Fatal("canonical statement must end with exactly one LF")
	}
}

// TestCanonicalStatementAlwaysEmitsEveryLine proves an absent peer id does not
// remove the line, because a producer that omits lines and a verifier that
// expects them is the failure mode this rule exists to prevent.
func TestCanonicalStatementAlwaysEmitsEveryLine(t *testing.T) {
	p := Proof{
		Domain:    "example.org",
		PublicKey: make([]byte, 32),
		IssuedAt:  1,
	}
	got, err := CanonicalStatement(p)
	if err != nil {
		t.Fatalf("CanonicalStatement: %v", err)
	}
	if n := strings.Count(string(got), "\n"); n != 6 {
		t.Fatalf("expected 6 LF-terminated lines, got %d: %q", n, got)
	}
	if !strings.Contains(string(got), "peerid=\n") {
		t.Fatalf("empty peer id must still emit its line: %q", got)
	}
	if !strings.Contains(string(got), "expires=0\n") {
		t.Fatalf("no-expiry must serialize as 0: %q", got)
	}
}

func signEd25519(t *testing.T, priv ed25519.PrivateKey, p Proof) Proof {
	t.Helper()
	signed, err := Sign(p, func(statement []byte) ([]byte, error) {
		return ed25519.Sign(priv, statement), nil
	})
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	return signed
}

func TestEd25519RoundTrip(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	now := time.Unix(1785400000, 0)
	signed := signEd25519(t, priv, Proof{
		Domain:    liveDomain,
		Algorithm: AlgEd25519,
		PublicKey: pub,
		PeerID:    livePeerID,
		IssuedAt:  now.Unix(),
		ExpiresAt: now.Unix() + 31536000,
	})

	record, err := signed.Record()
	if err != nil {
		t.Fatalf("Record: %v", err)
	}
	if strings.Contains(record, "k=") {
		t.Errorf("ed25519 is the default and must not emit k=: %s", record)
	}
	if !strings.HasPrefix(record, "v="+Version+";") {
		t.Errorf("v= must be the first tag: %s", record)
	}
	if RecordExceedsSingleString(record) {
		t.Errorf("record for the real deployment must fit one 255-byte DNS string, got %d: %s", len(record), record)
	}

	parsed, err := ParseRecord(liveDomain, record)
	if err != nil {
		t.Fatalf("ParseRecord: %v", err)
	}
	if err := Verify(parsed, now); err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if parsed.KeyFingerprint() != signed.KeyFingerprint() {
		t.Errorf("fingerprint mismatch: %s vs %s", parsed.KeyFingerprint(), signed.KeyFingerprint())
	}
}

func TestSecp256k1RoundTripMatchesEPMConvention(t *testing.T) {
	priv, err := secp256k1.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("GeneratePrivateKey: %v", err)
	}
	pub := priv.PubKey().SerializeCompressed()
	now := time.Unix(1785400000, 0)

	signed, err := Sign(Proof{
		Domain:    "example.org",
		Algorithm: AlgSecp256k1,
		PublicKey: pub,
		IssuedAt:  now.Unix(),
	}, func(statement []byte) ([]byte, error) {
		// DER over sha256(statement) — the convention
		// internal/epm/signature.go already enforces for secp256k1 EPM keys.
		digest := sha256.Sum256(statement)
		return ecdsa.Sign(priv, digest[:]).Serialize(), nil
	})
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	record, err := signed.Record()
	if err != nil {
		t.Fatalf("Record: %v", err)
	}
	if !strings.Contains(record, "k=secp256k1") {
		t.Errorf("non-default algorithm must be declared: %s", record)
	}
	parsed, err := ParseRecord("example.org", record)
	if err != nil {
		t.Fatalf("ParseRecord: %v", err)
	}
	if err := Verify(parsed, now); err != nil {
		t.Fatalf("Verify: %v", err)
	}
}

// TestCopiedRecordFailsAtAnotherDomain is THE security property. The verifier
// rebuilds the statement from the domain it queried, so a record lifted from
// one zone into another cannot verify, with no revocation machinery involved.
func TestCopiedRecordFailsAtAnotherDomain(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	now := time.Unix(1785400000, 0)
	signed := signEd25519(t, priv, Proof{
		Domain:    "honest.example",
		PublicKey: pub,
		IssuedAt:  now.Unix(),
	})
	record, err := signed.Record()
	if err != nil {
		t.Fatalf("Record: %v", err)
	}

	stolen, err := ParseRecord("attacker.example", record)
	if err != nil {
		t.Fatalf("ParseRecord: %v", err)
	}
	if err := Verify(stolen, now); !errors.Is(err, ErrBadSignature) {
		t.Fatalf("a copied record MUST NOT verify at another domain; got %v", err)
	}
}

func TestValidityWindow(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	issued := int64(1785400000)
	signed := signEd25519(t, priv, Proof{
		Domain:    "example.org",
		PublicKey: pub,
		IssuedAt:  issued,
		ExpiresAt: issued + 100,
	})

	if err := Verify(signed, time.Unix(issued+99, 0)); err != nil {
		t.Fatalf("inside the window must verify: %v", err)
	}
	if err := Verify(signed, time.Unix(issued+100, 0)); !errors.Is(err, ErrExpired) {
		t.Fatalf("expiry is exclusive; want ErrExpired, got %v", err)
	}
	if err := Verify(signed, time.Unix(issued-int64(ClockSkew.Seconds())-1, 0)); !errors.Is(err, ErrNotYetValid) {
		t.Fatalf("want ErrNotYetValid beyond skew, got %v", err)
	}
	if err := Verify(signed, time.Unix(issued-1, 0)); err != nil {
		t.Fatalf("a proof minted one second ahead of the verifier's clock must still verify: %v", err)
	}
}

func TestParseRecordRejections(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	now := time.Unix(1785400000, 0)
	signed := signEd25519(t, priv, Proof{Domain: "example.org", PublicKey: pub, IssuedAt: now.Unix()})
	good, err := signed.Record()
	if err != nil {
		t.Fatalf("Record: %v", err)
	}

	cases := []struct {
		name   string
		record string
		want   error
	}{
		{"empty", "  ", ErrEmptyRecord},
		// A leading unknown tag is the realistic shape of this mistake: an
		// operator prefixes a comment-ish tag and v= is no longer first.
		{"version not first", "note=hello; " + good, ErrVersionNotFirst},
		{"foreign version", "v=DKIM1; k=rsa; p=AAAA", ErrUnknownVersion},
		{"unknown algorithm", "v=SDN1; k=rsa; p=AAAA; ts=1; sig=AAAA", ErrUnknownAlgorithm},
		{"duplicate tag", good + "; ts=2", nil},
		{"d= mismatch", strings.Replace(good, "v=SDN1;", "v=SDN1; d=other.example;", 1), ErrDomainMismatch},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseRecord("example.org", tc.record)
			if err == nil {
				t.Fatalf("expected rejection, got none")
			}
			if tc.want != nil && !errors.Is(err, tc.want) {
				t.Fatalf("want %v, got %v", tc.want, err)
			}
		})
	}
}

// TestUnknownTagsAreIgnored is DKIM's forward-compatibility rule (RFC 6376
// §3.2): today's verifier must not choke on a tag a later version adds.
func TestUnknownTagsAreIgnored(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	now := time.Unix(1785400000, 0)
	signed := signEd25519(t, priv, Proof{Domain: "example.org", PublicKey: pub, IssuedAt: now.Unix()})
	record, err := signed.Record()
	if err != nil {
		t.Fatalf("Record: %v", err)
	}
	withFuture := record + "; bond=0xdeadbeef; note=hello"
	parsed, err := ParseRecord("example.org", withFuture)
	if err != nil {
		t.Fatalf("unknown tags must be ignored: %v", err)
	}
	if err := Verify(parsed, now); err != nil {
		t.Fatalf("Verify with unknown tags: %v", err)
	}
}

func TestWhitespaceAndPaddingTolerance(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	now := time.Unix(1785400000, 0)
	signed := signEd25519(t, priv, Proof{Domain: "example.org", PublicKey: pub, IssuedAt: now.Unix()})
	record, err := signed.Record()
	if err != nil {
		t.Fatalf("Record: %v", err)
	}
	// What a DNS console hands back after an operator has been near it.
	messy := strings.ReplaceAll(record, "; ", " ;  ") + " ; "
	parsed, err := ParseRecord("EXAMPLE.ORG.", messy)
	if err != nil {
		t.Fatalf("tag-value whitespace must be insignificant: %v", err)
	}
	if err := Verify(parsed, now); err != nil {
		t.Fatalf("Verify: %v", err)
	}
}

// TestNormalizeTXTPresentation encodes the MEASURED disagreement between the
// two browser-usable JSON DoH providers (2026-07-30). Without this
// normalization a 2-resolver quorum never agrees.
func TestNormalizeTXTPresentation(t *testing.T) {
	long := strings.Repeat("a", 255)
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"google form: bare value", "v=SDN1; p=AAAA", "v=SDN1; p=AAAA"},
		{"cloudflare form: single quoted string", `"v=SDN1; p=AAAA"`, "v=SDN1; p=AAAA"},
		{"cloudflare form: two character-strings", `"` + long + `" "tail"`, long + "tail"},
		{"escaped quote inside value", `"say \"hi\""`, `say "hi"`},
		{"escaped backslash", `"a\\b"`, `a\b`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := NormalizeTXTPresentation(tc.in)
			if err != nil {
				t.Fatalf("NormalizeTXTPresentation: %v", err)
			}
			if got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
	if _, err := NormalizeTXTPresentation(`"unterminated`); err == nil {
		t.Fatal("an unterminated quoted string must be an error, not a guess")
	}
}

// TestSelectProofsSurvivesNeighbours proves one bad or foreign record at the
// owner name cannot deny service to a good one — the SPF/DKIM rotation lesson.
func TestSelectProofsSurvivesNeighbours(t *testing.T) {
	pubA, privA, _ := ed25519.GenerateKey(nil)
	pubB, privB, _ := ed25519.GenerateKey(nil)
	now := time.Unix(1785400000, 0)

	older := signEd25519(t, privA, Proof{Domain: "example.org", PublicKey: pubA, IssuedAt: now.Unix() - 500})
	newer := signEd25519(t, privB, Proof{Domain: "example.org", PublicKey: pubB, IssuedAt: now.Unix()})
	recOlder, _ := older.Record()
	recNewer, _ := newer.Record()

	// A proof that expired, a proof for another domain, and an unrelated record.
	stale := signEd25519(t, privA, Proof{
		Domain:    "example.org",
		PublicKey: pubA,
		IssuedAt:  now.Unix() - 1000,
		ExpiresAt: now.Unix() - 10,
	})
	recStale, _ := stale.Record()
	foreign := signEd25519(t, privB, Proof{Domain: "other.example", PublicKey: pubB, IssuedAt: now.Unix()})
	recForeign, _ := foreign.Record()

	values := []string{
		`"v=spf1 include:mailgun.org ~all"`,
		recStale,
		`"` + recOlder + `"`, // cloudflare presentation form
		recForeign,
		recNewer,
	}
	proofs, rejected := SelectProofs("example.org", values, now)
	if len(proofs) != 2 {
		t.Fatalf("want 2 admitted proofs, got %d (rejected: %v)", len(proofs), rejected)
	}
	if proofs[0].IssuedAt <= proofs[1].IssuedAt {
		t.Fatal("proofs must be newest-first so a caller preferring the freshest key gets it")
	}
	if len(rejected) != 2 {
		t.Fatalf("want the expired and the foreign record reported as rejected, got %v", rejected)
	}
}

func TestOwnerName(t *testing.T) {
	got, err := OwnerName("SDN.SpaceAware.io.", "")
	if err != nil {
		t.Fatalf("OwnerName: %v", err)
	}
	if got != "_sdnkey.sdn.spaceaware.io" {
		t.Fatalf("got %q", got)
	}
	got, err = OwnerName("sdn.spaceaware.io", "k2")
	if err != nil {
		t.Fatalf("OwnerName: %v", err)
	}
	if got != "k2._sdnkey.sdn.spaceaware.io" {
		t.Fatalf("got %q", got)
	}
	if _, err := OwnerName("sdn.spaceaware.io", "a.b"); err == nil {
		t.Fatal("a multi-label selector must be refused")
	}
}

func TestNormalizeDomainRefusesUnicode(t *testing.T) {
	// IDNA has more than one mapping profile; a canonical statement whose bytes
	// depend on which library ran is not canonical.
	if _, err := NormalizeDomain("bücher.example"); !errors.Is(err, ErrBadDomain) {
		t.Fatalf("want ErrBadDomain for a U-label, got %v", err)
	}
	if _, err := NormalizeDomain("localhost"); !errors.Is(err, ErrBadDomain) {
		t.Fatal("a non-FQDN must be refused")
	}
}

// TestSignRefusesToEmitAnUnverifiableProof: a generator that hands an operator
// a record which cannot verify sends them to edit DNS for nothing.
func TestSignRefusesToEmitAnUnverifiableProof(t *testing.T) {
	pub, _, _ := ed25519.GenerateKey(nil)
	_, err := Sign(Proof{
		Domain:    "example.org",
		PublicKey: pub,
		IssuedAt:  1785400000,
	}, func([]byte) ([]byte, error) {
		return make([]byte, ed25519.SignatureSize), nil // wrong signature
	})
	if err == nil {
		t.Fatal("Sign must verify its own output before returning it")
	}
}

// TestLiveRecordFitsOneDNSString is the deployment-shape check: the real
// sdn.spaceaware.io key, peer id and domain, with a full-length signature, must
// paste into a DNS console as a single character-string.
func TestLiveRecordFitsOneDNSString(t *testing.T) {
	p := Proof{
		Domain:    liveDomain,
		Algorithm: AlgEd25519,
		PublicKey: mustHex(t, livePubKeyHex),
		PeerID:    livePeerID,
		IssuedAt:  1785400000,
		ExpiresAt: 1816936000,
		Signature: make([]byte, ed25519.SignatureSize),
	}
	record, err := p.Record()
	if err != nil {
		t.Fatalf("Record: %v", err)
	}
	if RecordExceedsSingleString(record) {
		t.Fatalf("live-shaped record is %d bytes, over the 255-byte single-string limit: %s", len(record), record)
	}
	t.Logf("live-shaped record is %d bytes", len(record))
}
