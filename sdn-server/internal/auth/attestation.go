package auth

// Task F2 — node-verified attestations.
//
// # Gap being closed
//
// A wallet user has an Ed25519 signing key bound to their xpub in the
// UserStore (User.SigningPubKeyHex — see tofu.go's trust-on-first-use
// binding), but until now nothing let that user PROVE to the node, on a
// per-request basis, "this really came from the holder of that key" using a
// small, portable, independently verifiable record. Attestation is that
// record: a user signs one with their wallet's Ed25519 signing key, and the
// node verifies the signature against the SAME key it already has on file
// for that xpub.
//
// # Reconciling browser-self (Ultimate) with node verification (task F2 step 3)
//
// In the browser, the user's wallet key IS the node's own key: applying a
// wallet identity (see applyWalletNodeIdentity in
// desktop/src/static-http-server.js) makes the resulting self-node record
// report the canonical PGP scale's ceiling, 'ultimate' (peers.Ultimate ==
// 5) — by SELF-RECOGNITION (this identity IS the node's own root identity,
// mirroring Registry.SetRootIdentity, internal/peers/trust.go C6), never by
// the operator-assignment path that C7's assignableTrustLevel
// (handler.go) intentionally blocks for Ultimate/Never.
//
// That same wallet key is what signs an Attestation. VerifyAttestation
// here checks the signature against the xpub's STORED SigningPubKeyHex —
// the very key TOFU-bound (or config-provisioned) for that user. So there
// is exactly one identity end to end: the browser holds the private key
// and self-signs as Ultimate; the daemon verifies purely against the public
// key it already trusts on file for that xpub. Verifying an Attestation
// does not itself grant or change any trust level — it only confirms a
// request genuinely came from the holder of an already-registered key,
// which is what lets a caller safely say "this Attestation came from the
// user at trust level X" by looking up the returned User.TrustLevel.
//
// # Canonical encoding (documented choice)
//
// Attestation is signed and serialized using a fixed-order,
// length-prefixed binary encoding (canonicalBytes), the same documented
// pattern internal/peers/grants.go uses for SignedGrant and for the same
// reason: a signing preimage must be unambiguous by construction rather
// than dependent on encoding/json's incidental behavior (map key order,
// number formatting, whitespace). This file does not import
// internal/peers/grants.go's unexported helpers (that package is out of
// scope for this change) — it defines its own small, self-contained
// equivalent below.
import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Attestation-related errors. Distinct sentinels so callers (and tests) can
// tell "wrong signer" apart from "no such user" apart from "malformed
// input" without string-matching error text.
var (
	// ErrAttestationMissingFields is returned when XPub or Claim is empty.
	ErrAttestationMissingFields = errors.New("auth: attestation is missing required fields (xpub/claim)")
	// ErrAttestationUnknownUser is returned when att.XPub does not match
	// any user registered in the UserStore.
	ErrAttestationUnknownUser = errors.New("auth: attestation xpub is not a registered user")
	// ErrAttestationNoSigningKey is returned when the registered user has
	// no Ed25519 signing key bound yet (TOFU-pending — see tofu.go).
	ErrAttestationNoSigningKey = errors.New("auth: attestation user has no signing key bound yet")
	// ErrAttestationBadSignature is returned when sig does not verify
	// against the user's stored signing key over att's exact fields
	// (wrong key, or any field tampered with after signing).
	ErrAttestationBadSignature = errors.New("auth: attestation signature verification failed")
)

// Attestation is a small, self-contained signed claim a wallet user makes
// to this node. It carries no session state and no trust assertion of its
// own — VerifyAttestation only proves who signed it (by checking against
// the signing key already on file for att.XPub) and returns that user's
// current record, so callers decide what the claim is allowed to do.
type Attestation struct {
	// XPub identifies the registered user whose stored signing key
	// (User.SigningPubKeyHex) this attestation must verify against.
	XPub string
	// Claim is the short, application-defined statement being attested to
	// (e.g. "self" for the browser-self case described above, or a
	// resource/action identifier). Opaque to this package — verification
	// only proves who signed it, not what it means.
	Claim string
	// IssuedAt is when the attestation was signed. SignAttestation fills
	// this in with time.Now().UTC() if left zero.
	IssuedAt time.Time
	// Nonce is caller-supplied (or SignAttestation-generated) random
	// bytes, so two attestations with identical XPub/Claim/IssuedAt still
	// produce different signatures. This package does not itself track
	// seen nonces — a caller that needs replay protection across requests
	// should record and reject reused (XPub, Nonce) pairs itself.
	Nonce []byte
}

// canonicalBytes returns the deterministic signing/wire preimage for a,
// covering every field so that tampering with ANY of them after signing
// invalidates the signature. Layout, fixed field order:
//
//	uint32(len(XPub))  | XPub raw bytes
//	uint32(len(Claim)) | Claim raw bytes
//	int64(IssuedAt.UnixNano()) -- 0 if IssuedAt.IsZero()
//	uint32(len(Nonce)) | Nonce raw bytes
func (a Attestation) canonicalBytes() []byte {
	buf := make([]byte, 0, 4+len(a.XPub)+4+len(a.Claim)+8+4+len(a.Nonce))
	buf = attestationAppendLP(buf, []byte(a.XPub))
	buf = attestationAppendLP(buf, []byte(a.Claim))
	buf = attestationAppendInt64(buf, attestationUnixNanoOrZero(a.IssuedAt))
	buf = attestationAppendLP(buf, a.Nonce)
	return buf
}

// SignAttestation signs att with priv, a 64-byte Ed25519 private key (e.g.
// crypto/ed25519.GenerateKey, or a wallet's derived Ed25519 signing key —
// never a real seed/mnemonic in tests: generate a fresh keypair instead).
//
//   - att.XPub and att.Claim are required; SignAttestation does not fill
//     these in.
//   - If att.IssuedAt is zero, it defaults to time.Now().UTC().
//   - If att.Nonce is empty, 16 random bytes are generated (crypto/rand).
//
// Returns the (possibly defaulted) Attestation plus the raw Ed25519
// signature over its canonical encoding.
func SignAttestation(priv ed25519.PrivateKey, att Attestation) (Attestation, []byte, error) {
	if len(priv) != ed25519.PrivateKeySize {
		return Attestation{}, nil, fmt.Errorf("auth: SignAttestation: signing key must be a %d-byte Ed25519 private key", ed25519.PrivateKeySize)
	}
	if strings.TrimSpace(att.XPub) == "" || strings.TrimSpace(att.Claim) == "" {
		return Attestation{}, nil, ErrAttestationMissingFields
	}
	if att.IssuedAt.IsZero() {
		att.IssuedAt = time.Now().UTC()
	}
	if len(att.Nonce) == 0 {
		nonce := make([]byte, 16)
		if _, err := rand.Read(nonce); err != nil {
			return Attestation{}, nil, fmt.Errorf("auth: SignAttestation: generate nonce: %w", err)
		}
		att.Nonce = nonce
	}
	sig := ed25519.Sign(priv, att.canonicalBytes())
	return att, sig, nil
}

// VerifyAttestation verifies that sig is a valid Ed25519 signature over
// att's canonical encoding, produced by the signing key already bound (via
// TOFU, tofu.go, or explicit config/UpdateSigningPubKey) to att.XPub in
// store. On success it returns the matching User (so callers can read its
// current TrustLevel). It fails distinctly, never panics, for:
//
//   - a missing XPub/Claim (ErrAttestationMissingFields)
//   - an XPub with no registered User in store (ErrAttestationUnknownUser)
//   - a registered user with no usable signing key bound yet
//     (ErrAttestationNoSigningKey)
//   - a signature that does not verify — wrong key, or ANY tampered field,
//     since canonicalBytes() covers all of them (ErrAttestationBadSignature)
func VerifyAttestation(store *UserStore, att Attestation, sig []byte) (*User, error) {
	if store == nil {
		return nil, errors.New("auth: VerifyAttestation: user store is required")
	}
	if strings.TrimSpace(att.XPub) == "" || strings.TrimSpace(att.Claim) == "" {
		return nil, ErrAttestationMissingFields
	}

	user, err := store.GetUser(att.XPub)
	if err != nil {
		return nil, fmt.Errorf("auth: VerifyAttestation: lookup user: %w", err)
	}
	if user == nil {
		return nil, ErrAttestationUnknownUser
	}

	pubHex, err := normalizeEd25519PubKeyHex(user.SigningPubKeyHex)
	if err != nil || pubHex == "" {
		return nil, ErrAttestationNoSigningKey
	}
	pubRaw, err := hex.DecodeString(pubHex)
	if err != nil || len(pubRaw) != ed25519.PublicKeySize {
		return nil, ErrAttestationNoSigningKey
	}

	if len(sig) != ed25519.SignatureSize || !ed25519.Verify(ed25519.PublicKey(pubRaw), att.canonicalBytes(), sig) {
		return nil, ErrAttestationBadSignature
	}

	return user, nil
}

// VerifyAttestation is the method form of the free VerifyAttestation
// function above, for callers that already hold a *UserStore.
func (s *UserStore) VerifyAttestation(att Attestation, sig []byte) (*User, error) {
	return VerifyAttestation(s, att, sig)
}

func attestationAppendLP(buf, b []byte) []byte {
	var lenBuf [4]byte
	binary.BigEndian.PutUint32(lenBuf[:], uint32(len(b)))
	buf = append(buf, lenBuf[:]...)
	buf = append(buf, b...)
	return buf
}

func attestationAppendInt64(buf []byte, v int64) []byte {
	var b [8]byte
	binary.BigEndian.PutUint64(b[:], uint64(v))
	return append(buf, b[:]...)
}

func attestationUnixNanoOrZero(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixNano()
}
