// Package dnsproof implements the SDN domain-to-nodekey proof: a signed
// statement, published as a DNS TXT record, that binds a DNS domain to a node
// key in BOTH directions.
//
// WHY THIS PACKAGE EXISTS AT ALL (Hermes ruling 2026-07-30, task
// sdn-dns-key-proof-standard): the owner asked for an EXISTING standard. There
// is none. Every deployed DNS-TXT key mechanism proves exactly one direction:
//
//   - DKIM key records (RFC 6376 §3.6.1), libp2p _dnsaddr, atproto _atproto,
//     ENS _ens, and every commercial "site-verification" token PUBLISH or POINT
//     AT a key. Record presence proves the publisher controls DNS. Nothing in
//     the record proves the key holder agreed, so the same record copied to
//     another zone still "verifies".
//   - ACME dns-01 (RFC 8555 §8.4) is a HASH of a CA-chosen token, scoped to one
//     issuance session and meaningless outside it.
//   - OPENPGPKEY (RFC 7929) and DANE TLSA (RFC 6698) delegate the entire
//     question to DNSSEC and MUST fail closed when validation is not Secure.
//
// So this package COMPOSES rather than invents:
//
//  1. WIRE SYNTAX is DKIM's tag-value list (RFC 6376 §3.2) — the most widely
//     deployed, most widely tooled TXT grammar there is, including its
//     forward-compatibility rule that unknown tags are ignored.
//  2. OWNER NAME is the underscore-label convention (_domainkey, _acme-challenge,
//     _dnsaddr, _atproto) in OUR OWN namespace: _sdnkey. We do NOT publish under
//     _domainkey: the DKIM key-record tag registry is IETF-Review, so a foreign
//     v= value there would be a registry violation.
//  3. THE MISSING DIRECTION is supplied by the signed-statement pattern this
//     stack already runs in EPM CHAIN_PROOFS: a fixed-order canonical statement
//     naming the domain, signed by the key, replayed byte-for-byte by the
//     verifier.
//
// The security property that follows is the whole point: the verifier rebuilds
// the statement from the domain IT QUERIED, never from the record. A proof
// copied into another zone therefore fails automatically, with no revocation,
// no registry, and no third party in the path.
package dnsproof

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	"github.com/decred/dcrd/dcrec/secp256k1/v4/ecdsa"
)

const (
	// Version is the value of the mandatory v= tag. It is the FIRST tag in the
	// record, following DKIM's rule, so a parser can reject a foreign record
	// before spending anything on it.
	Version = "SDN1"

	// StatementPrefix is the first line of every canonical statement. It is
	// versioned separately from the record because the signed bytes and the
	// wire encoding can legitimately evolve at different rates; a verifier that
	// cannot parse the prefix MUST refuse rather than guess.
	StatementPrefix = "sdn-domain-proof/1"

	// OwnerLabel is the underscore label the proof is published under.
	// Full owner name: "_sdnkey.<domain>", or "<selector>._sdnkey.<domain>"
	// when one domain must carry several keys.
	OwnerLabel = "_sdnkey"

	// AlgEd25519 is the default algorithm: raw Ed25519 (RFC 8032) over the
	// canonical statement bytes. Ed25519 hashes internally, so there is no
	// separate digest step and therefore no digest-agility ambiguity.
	AlgEd25519 = "ed25519"

	// AlgSecp256k1 is ECDSA over secp256k1, DER-encoded, over
	// sha256(statement). This is NOT a free choice: it is exactly what
	// internal/epm/signature.go already accepts for secp256k1 EPM signing keys,
	// and a second convention for the same curve in the same node would be a
	// defect.
	AlgSecp256k1 = "secp256k1"

	// ClockSkew is how far into the future an issued-at may sit before the
	// proof is refused. Signers and verifiers do not share a clock; a proof
	// minted seconds ago on a slightly fast host must not be unverifiable.
	ClockSkew = 5 * time.Minute

	// ed25519PublicKeySize / secp256k1CompressedSize guard p= lengths so a
	// truncated paste fails at parse time with a legible error instead of
	// deep inside a curve library.
	ed25519PublicKeySize    = ed25519.PublicKeySize
	secp256k1CompressedSize = 33

	// maxTXTStringLen is the DNS <character-string> limit (RFC 1035 §3.3).
	// A record longer than this is still legal — it becomes multiple strings in
	// one RR — but staying inside one string is what survives registrar UIs, so
	// the generator reports when it has been exceeded.
	maxTXTStringLen = 255
)

// Errors are exported because both the CLI and the dashboard render them
// verbatim to an operator who is holding a DNS console open. "invalid proof"
// would send them looking in the wrong place.
var (
	ErrEmptyRecord      = errors.New("dnsproof: empty TXT record")
	ErrVersionNotFirst  = errors.New("dnsproof: v= must be the first tag")
	ErrUnknownVersion   = errors.New("dnsproof: unsupported proof version")
	ErrMissingTag       = errors.New("dnsproof: missing required tag")
	ErrUnknownAlgorithm = errors.New("dnsproof: unsupported key algorithm")
	ErrBadSignature     = errors.New("dnsproof: signature does not verify against the canonical statement")
	ErrExpired          = errors.New("dnsproof: proof has expired")
	ErrNotYetValid      = errors.New("dnsproof: proof is issued in the future beyond the allowed clock skew")
	ErrDomainMismatch   = errors.New("dnsproof: record domain does not match the queried domain")
	ErrBadDomain        = errors.New("dnsproof: invalid domain")
)

// Proof is one domain-to-key binding.
//
// Domain is deliberately NOT carried on the wire. The verifier fills it from
// the name it queried, which is what makes a copied record worthless. Keeping
// it out of the record also keeps the record inside one 255-byte DNS string for
// realistic domain lengths.
type Proof struct {
	Domain    string
	Algorithm string
	PublicKey []byte
	PeerID    string
	IssuedAt  int64
	ExpiresAt int64 // 0 = no expiry
	Signature []byte
}

// OwnerName returns the TXT owner name for a domain. selector may be empty.
func OwnerName(domain, selector string) (string, error) {
	d, err := NormalizeDomain(domain)
	if err != nil {
		return "", err
	}
	sel := strings.TrimSpace(strings.ToLower(selector))
	if sel == "" {
		return OwnerLabel + "." + d, nil
	}
	if strings.ContainsAny(sel, ". \t") {
		return "", fmt.Errorf("%w: selector %q must be a single label", ErrBadDomain, selector)
	}
	return sel + "." + OwnerLabel + "." + d, nil
}

// NormalizeDomain lowercases, strips a trailing root dot, and refuses anything
// that is not already an LDH/A-label form.
//
// It refuses non-ASCII outright rather than transcoding: IDNA has more than one
// mapping profile, and a canonical statement whose bytes depend on which
// library canonicalized the domain is not canonical. Callers hold U-labels
// convert to A-labels (punycode) before they get here.
func NormalizeDomain(domain string) (string, error) {
	d := strings.TrimSpace(domain)
	d = strings.TrimSuffix(d, ".")
	d = strings.ToLower(d)
	if d == "" {
		return "", fmt.Errorf("%w: empty", ErrBadDomain)
	}
	if !utf8.ValidString(d) {
		return "", fmt.Errorf("%w: not valid UTF-8", ErrBadDomain)
	}
	for _, r := range d {
		if r > 0x7f {
			return "", fmt.Errorf("%w: %q is not an A-label; convert IDN to punycode before proving it", ErrBadDomain, domain)
		}
	}
	if strings.ContainsAny(d, " \t;\"\\") {
		return "", fmt.Errorf("%w: %q contains a character that cannot appear in a canonical statement", ErrBadDomain, domain)
	}
	if strings.Contains(d, "..") || strings.HasPrefix(d, ".") {
		return "", fmt.Errorf("%w: %q has an empty label", ErrBadDomain, domain)
	}
	if !strings.Contains(d, ".") {
		return "", fmt.Errorf("%w: %q is not a fully qualified domain", ErrBadDomain, domain)
	}
	return d, nil
}

// CanonicalStatement returns the exact bytes covered by the signature.
//
// Rules that make this reproducible in Go, TypeScript and WASM without a
// canonicalization library:
//
//   - Fixed line order. No sorting, no optional lines, no omissions: an absent
//     value is an EMPTY value on a line that is still present, so there is no
//     way for two producers to disagree about layout.
//   - Exactly one LF (0x0A) after every line INCLUDING the last. The
//     trailing-newline question is the classic canonicalization bug; it is
//     answered here once.
//   - Lowercase hex for the key, so the same key never yields two statements.
//   - No CR, no padding, no whitespace tolerance. Producers emit; verifiers
//     replay. Nothing is "cleaned up".
func CanonicalStatement(p Proof) ([]byte, error) {
	domain, err := NormalizeDomain(p.Domain)
	if err != nil {
		return nil, err
	}
	alg, err := normalizeAlgorithm(p.Algorithm)
	if err != nil {
		return nil, err
	}
	if err := checkPublicKeyLength(alg, p.PublicKey); err != nil {
		return nil, err
	}
	if p.IssuedAt <= 0 {
		return nil, fmt.Errorf("%w: issued-at must be a positive unix timestamp", ErrMissingTag)
	}
	if p.ExpiresAt < 0 {
		return nil, fmt.Errorf("%w: expires-at must not be negative", ErrMissingTag)
	}
	if p.ExpiresAt != 0 && p.ExpiresAt <= p.IssuedAt {
		return nil, fmt.Errorf("%w: expires-at %d is not after issued-at %d", ErrMissingTag, p.ExpiresAt, p.IssuedAt)
	}
	peerID := strings.TrimSpace(p.PeerID)
	if strings.ContainsAny(peerID, " \t\r\n;") {
		return nil, fmt.Errorf("%w: peer id %q contains whitespace or a tag separator", ErrMissingTag, p.PeerID)
	}

	var b strings.Builder
	b.WriteString(StatementPrefix)
	b.WriteByte('\n')
	b.WriteString("domain=")
	b.WriteString(domain)
	b.WriteByte('\n')
	b.WriteString("key=")
	b.WriteString(alg)
	b.WriteByte(':')
	b.WriteString(hex.EncodeToString(p.PublicKey))
	b.WriteByte('\n')
	b.WriteString("peerid=")
	b.WriteString(peerID)
	b.WriteByte('\n')
	b.WriteString("issued=")
	b.WriteString(strconv.FormatInt(p.IssuedAt, 10))
	b.WriteByte('\n')
	b.WriteString("expires=")
	b.WriteString(strconv.FormatInt(p.ExpiresAt, 10))
	b.WriteByte('\n')
	return []byte(b.String()), nil
}

// Record renders the TXT value.
//
// k= is emitted only for non-default algorithms, exactly as DKIM omits its own
// defaults: the common record stays shorter, and a verifier that has to guess
// is a verifier with a bug.
func (p Proof) Record() (string, error) {
	if len(p.Signature) == 0 {
		return "", fmt.Errorf("%w: sig", ErrMissingTag)
	}
	if _, err := CanonicalStatement(p); err != nil {
		return "", err
	}
	alg, err := normalizeAlgorithm(p.Algorithm)
	if err != nil {
		return "", err
	}

	parts := []string{"v=" + Version}
	if alg != AlgEd25519 {
		parts = append(parts, "k="+alg)
	}
	parts = append(parts, "p="+b64.EncodeToString(p.PublicKey))
	if peerID := strings.TrimSpace(p.PeerID); peerID != "" {
		parts = append(parts, "id="+peerID)
	}
	parts = append(parts, "ts="+strconv.FormatInt(p.IssuedAt, 10))
	if p.ExpiresAt != 0 {
		parts = append(parts, "xp="+strconv.FormatInt(p.ExpiresAt, 10))
	}
	parts = append(parts, "sig="+b64.EncodeToString(p.Signature))
	return strings.Join(parts, "; "), nil
}

// RecordExceedsSingleString reports whether the rendered value needs more than
// one DNS <character-string>. Multi-string TXT is legal and deployed (DKIM RSA
// keys do it every day), but many registrar consoles mangle it, so the
// generator warns rather than silently shipping something an operator cannot
// paste.
func RecordExceedsSingleString(record string) bool {
	return len(record) > maxTXTStringLen
}

// ParseRecord parses one TXT value against the domain it was queried at.
//
// domain is a parameter, not a tag, on purpose — see Proof.Domain.
func ParseRecord(domain, record string) (Proof, error) {
	value := strings.TrimSpace(record)
	if value == "" {
		return Proof{}, ErrEmptyRecord
	}
	normalizedDomain, err := NormalizeDomain(domain)
	if err != nil {
		return Proof{}, err
	}

	tags, order, err := parseTagList(value)
	if err != nil {
		return Proof{}, err
	}
	if len(order) == 0 || order[0] != "v" {
		return Proof{}, ErrVersionNotFirst
	}
	if !strings.EqualFold(tags["v"], Version) {
		return Proof{}, fmt.Errorf("%w: %q (want %q)", ErrUnknownVersion, tags["v"], Version)
	}

	alg, err := normalizeAlgorithm(tags["k"])
	if err != nil {
		return Proof{}, err
	}

	// An explicit d= is optional and redundant. When an operator includes it
	// anyway, a mismatch is a hard failure: it means the record was copied.
	if d, ok := tags["d"]; ok && strings.TrimSpace(d) != "" {
		claimed, err := NormalizeDomain(d)
		if err != nil {
			return Proof{}, err
		}
		if claimed != normalizedDomain {
			return Proof{}, fmt.Errorf("%w: record claims %q, queried %q", ErrDomainMismatch, claimed, normalizedDomain)
		}
	}

	pub, err := decodeB64(tags["p"])
	if err != nil {
		return Proof{}, fmt.Errorf("%w: p=: %v", ErrMissingTag, err)
	}
	if err := checkPublicKeyLength(alg, pub); err != nil {
		return Proof{}, err
	}
	sig, err := decodeB64(tags["sig"])
	if err != nil {
		return Proof{}, fmt.Errorf("%w: sig=: %v", ErrMissingTag, err)
	}
	issued, err := parseUnix(tags["ts"], true)
	if err != nil {
		return Proof{}, fmt.Errorf("%w: ts=: %v", ErrMissingTag, err)
	}
	expires, err := parseUnix(tags["xp"], false)
	if err != nil {
		return Proof{}, fmt.Errorf("%w: xp=: %v", ErrMissingTag, err)
	}

	return Proof{
		Domain:    normalizedDomain,
		Algorithm: alg,
		PublicKey: pub,
		PeerID:    strings.TrimSpace(tags["id"]),
		IssuedAt:  issued,
		ExpiresAt: expires,
		Signature: sig,
	}, nil
}

// Normalize returns a copy with the domain lowercased and the algorithm
// resolved to an explicit value.
//
// Every entry point runs this. The alternative — letting the empty-string
// default for Algorithm survive into Verify, KeyFingerprint and Record — was a
// live bug the round-trip tests caught: CanonicalStatement resolved the default
// internally, so the statement was right while everything downstream compared
// against "". A default that is only honoured in one place is not a default.
func (p Proof) Normalize() (Proof, error) {
	domain, err := NormalizeDomain(p.Domain)
	if err != nil {
		return Proof{}, err
	}
	alg, err := normalizeAlgorithm(p.Algorithm)
	if err != nil {
		return Proof{}, err
	}
	out := p
	out.Domain = domain
	out.Algorithm = alg
	out.PeerID = strings.TrimSpace(p.PeerID)
	return out, nil
}

// Verify checks the signature and the validity window. It is the only function
// callers should gate trust on.
func Verify(proof Proof, now time.Time) error {
	p, err := proof.Normalize()
	if err != nil {
		return err
	}
	statement, err := CanonicalStatement(p)
	if err != nil {
		return err
	}
	if now.Unix() < p.IssuedAt-int64(ClockSkew.Seconds()) {
		return fmt.Errorf("%w: issued %d, now %d", ErrNotYetValid, p.IssuedAt, now.Unix())
	}
	if p.ExpiresAt != 0 && now.Unix() >= p.ExpiresAt {
		return fmt.Errorf("%w: expired %d, now %d", ErrExpired, p.ExpiresAt, now.Unix())
	}

	switch p.Algorithm {
	case AlgEd25519:
		if len(p.Signature) != ed25519.SignatureSize {
			return fmt.Errorf("%w: ed25519 signature is %d bytes, want %d", ErrBadSignature, len(p.Signature), ed25519.SignatureSize)
		}
		if !ed25519.Verify(ed25519.PublicKey(p.PublicKey), statement, p.Signature) {
			return ErrBadSignature
		}
		return nil
	case AlgSecp256k1:
		pk, err := secp256k1.ParsePubKey(p.PublicKey)
		if err != nil {
			return fmt.Errorf("%w: %v", ErrBadSignature, err)
		}
		sig, err := ecdsa.ParseDERSignature(p.Signature)
		if err != nil {
			return fmt.Errorf("%w: %v", ErrBadSignature, err)
		}
		digest := sha256.Sum256(statement)
		if !sig.Verify(digest[:], pk) {
			return ErrBadSignature
		}
		return nil
	default:
		return fmt.Errorf("%w: %q", ErrUnknownAlgorithm, p.Algorithm)
	}
}

// Sign produces the signature over the canonical statement. It is separate from
// Record so the private key touches exactly one function.
func Sign(p Proof, sign func(statement []byte) ([]byte, error)) (Proof, error) {
	statement, err := CanonicalStatement(p)
	if err != nil {
		return Proof{}, err
	}
	sig, err := sign(statement)
	if err != nil {
		return Proof{}, err
	}
	signed := p
	signed.Signature = sig
	// Never hand back a proof we have not just verified: a generator that emits
	// an unverifiable record sends an operator to edit DNS for nothing.
	if err := Verify(signed, time.Unix(p.IssuedAt, 0)); err != nil {
		return Proof{}, fmt.Errorf("generated proof does not verify: %w", err)
	}
	return signed, nil
}

// NormalizeTXTPresentation converts a DoH JSON `data` field to the raw TXT
// value.
//
// This exists because the two browser-usable JSON DoH providers DISAGREE, which
// was measured, not assumed (2026-07-30, google._domainkey.anthropic.com, a
// 410-byte DKIM key):
//
//	Cloudflare -> 415 chars, DNS presentation format: "<255 bytes>" "<rest>"
//	Google     -> 410 chars, already concatenated, unquoted
//
// A quorum check that compares these two strings directly always disagrees, and
// would silently report every proof on earth as unverifiable. So both forms
// normalize here first.
func NormalizeTXTPresentation(data string) (string, error) {
	s := strings.TrimSpace(data)
	if !strings.HasPrefix(s, "\"") {
		return s, nil
	}
	var out strings.Builder
	inString := false
	escaped := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case escaped:
			out.WriteByte(c)
			escaped = false
		case inString && c == '\\':
			escaped = true
		case c == '"':
			inString = !inString
		case inString:
			out.WriteByte(c)
		case c == ' ' || c == '\t':
			// separator between character-strings
		default:
			return "", fmt.Errorf("dnsproof: unexpected byte %q outside a quoted string in TXT presentation data", c)
		}
	}
	if inString || escaped {
		return "", errors.New("dnsproof: unterminated quoted string in TXT presentation data")
	}
	return out.String(), nil
}

// SelectProofs parses every TXT value at an owner name and returns the proofs
// that verify.
//
// Multiple TXT RRs at one name is the normal case, not an error: a domain may
// front several nodes, and key rotation overlaps old and new. The rule is
// accept-any-match, which is how SPF and DKIM survive rotation. Values that do
// not parse are skipped with a reason rather than failing the whole set, so one
// stale record cannot deny service to a good one.
func SelectProofs(domain string, values []string, now time.Time) (proofs []Proof, rejected []string) {
	seen := map[string]bool{}
	for _, raw := range values {
		value, err := NormalizeTXTPresentation(raw)
		if err != nil {
			rejected = append(rejected, err.Error())
			continue
		}
		if !strings.HasPrefix(strings.TrimSpace(strings.ToLower(value)), "v=sdn1") {
			continue // not ours; SPF/DKIM/verification tokens share this name space
		}
		p, err := ParseRecord(domain, value)
		if err != nil {
			rejected = append(rejected, err.Error())
			continue
		}
		if err := Verify(p, now); err != nil {
			rejected = append(rejected, err.Error())
			continue
		}
		fp := p.Algorithm + ":" + hex.EncodeToString(p.PublicKey)
		if seen[fp] {
			continue
		}
		seen[fp] = true
		proofs = append(proofs, p)
	}
	sort.Slice(proofs, func(i, j int) bool { return proofs[i].IssuedAt > proofs[j].IssuedAt })
	return proofs, rejected
}

// KeyFingerprint is the stable identifier a caller compares against the key
// that signed a module or a manifest.
func (p Proof) KeyFingerprint() string {
	return p.Algorithm + ":" + hex.EncodeToString(p.PublicKey)
}

// b64 is unpadded base64url. Unpadded because '=' is a tag separator's cousin
// in tag-value grammars and because it costs bytes in a 255-byte budget;
// decoding accepts padded input anyway, since operators paste what they are
// given.
var b64 = base64.RawURLEncoding

func decodeB64(v string) ([]byte, error) {
	s := strings.TrimSpace(v)
	if s == "" {
		return nil, errors.New("empty")
	}
	if b, err := b64.DecodeString(s); err == nil {
		return b, nil
	}
	if b, err := base64.URLEncoding.DecodeString(s); err == nil {
		return b, nil
	}
	// Standard base64 is tolerated on input only: DKIM's p= is standard
	// base64, so operator muscle memory produces it.
	if b, err := base64.StdEncoding.DecodeString(s); err == nil {
		return b, nil
	}
	b, err := base64.RawStdEncoding.DecodeString(s)
	if err != nil {
		return nil, fmt.Errorf("not base64: %w", err)
	}
	return b, nil
}

func parseUnix(v string, required bool) (int64, error) {
	s := strings.TrimSpace(v)
	if s == "" {
		if required {
			return 0, errors.New("required")
		}
		return 0, nil
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("not an integer: %w", err)
	}
	if n < 0 {
		return 0, errors.New("negative")
	}
	return n, nil
}

func normalizeAlgorithm(alg string) (string, error) {
	a := strings.ToLower(strings.TrimSpace(alg))
	switch a {
	case "", AlgEd25519:
		return AlgEd25519, nil
	case AlgSecp256k1:
		return AlgSecp256k1, nil
	default:
		return "", fmt.Errorf("%w: %q", ErrUnknownAlgorithm, alg)
	}
}

func checkPublicKeyLength(alg string, pub []byte) error {
	switch alg {
	case AlgEd25519:
		if len(pub) != ed25519PublicKeySize {
			return fmt.Errorf("%w: ed25519 public key is %d bytes, want %d", ErrMissingTag, len(pub), ed25519PublicKeySize)
		}
	case AlgSecp256k1:
		if len(pub) != secp256k1CompressedSize {
			return fmt.Errorf("%w: secp256k1 public key is %d bytes, want %d compressed", ErrMissingTag, len(pub), secp256k1CompressedSize)
		}
	default:
		return fmt.Errorf("%w: %q", ErrUnknownAlgorithm, alg)
	}
	return nil
}

// parseTagList implements the DKIM tag-value grammar (RFC 6376 §3.2): tags are
// separated by ';', whitespace around '=' and ';' is insignificant, a trailing
// ';' is allowed, and UNKNOWN TAGS ARE IGNORED so a future version can add tags
// without breaking today's verifiers. Duplicates are refused, because "which
// one wins" is exactly the ambiguity an attacker looks for.
func parseTagList(value string) (map[string]string, []string, error) {
	tags := map[string]string{}
	var order []string
	for _, chunk := range strings.Split(value, ";") {
		part := strings.TrimSpace(chunk)
		if part == "" {
			continue
		}
		name, val, ok := strings.Cut(part, "=")
		if !ok {
			return nil, nil, fmt.Errorf("dnsproof: tag %q has no '='", part)
		}
		name = strings.ToLower(strings.TrimSpace(name))
		if name == "" {
			return nil, nil, fmt.Errorf("dnsproof: empty tag name in %q", part)
		}
		if _, dup := tags[name]; dup {
			return nil, nil, fmt.Errorf("dnsproof: duplicate tag %q", name)
		}
		tags[name] = strings.TrimSpace(val)
		order = append(order, name)
	}
	return tags, order, nil
}
