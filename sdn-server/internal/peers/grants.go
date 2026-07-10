// This file (grants.go) implements Phase C4 of the trust alignment plan:
// node-key-signed permission grants as the authenticated edge producer for
// the web-of-trust graph (internal/trust) that peers.ComputeValidity /
// Registry.SetTrustGraph consume.
//
// # Gap being closed
//
// Trust/permission assignments (peer X asserts TrustLevel L about peer Y)
// were previously only ever unsigned local state (Registry.SetTrustLevel):
// no peer could verify who actually asserted what, and nothing produced
// edges for the web-of-trust graph wired by Registry.SetTrustGraph (Phase
// C2, trust.go) — that graph existed but had no live producer. A signed
// Grant is the natural authenticated edge: it is a portable, independently
// verifiable claim "I am Granter, and I assert Level about Subject",
// requiring no trust in whoever relays it.
//
// # Canonical encoding (documented choice)
//
// Grants are signed and serialized using a fixed-order, length-prefixed
// binary encoding (canonicalBytes / MarshalBinary) rather than canonical
// JSON:
//
//   - It removes any dependency on encoding/json's behavior (map key
//     ordering, number formatting, whitespace) ever silently changing the
//     byte sequence that was signed — a fixed-field-order, length-prefixed
//     layout is unambiguous by construction and trivially reproducible
//     byte-for-byte on any platform without a JSON canonicalization
//     library.
//   - SignedGrant is a small, fixed-shape record, so there is no ergonomic
//     loss from the encoding being opaque: it is a signing preimage / wire
//     format, never something an operator reads directly (an operator-
//     facing JSON projection can always be layered on top later, as long as
//     it is never the thing that gets signed).
//
// # Why not the LGR FlatBuffers schema?
//
// The repo vendors an LGR schema (internal/sds/schemas/LGR.fbs,
// third_party/spacedatastandards-go/LGR) but it is the "Licensing Grant
// Record" for MODULE/CAPABILITY licensing (MESSAGE_TYPE, MODULE_ID,
// REQUESTER_PEER_ID, REQUESTER_XPUB, GRANTED_DOMAIN, CAPABILITY_TOKEN,
// WRAPPED_CONTENT_KEY_*, GRANT_VERIFIER_PUBKEY, PROVIDER_SIGNATURE, …),
// already consumed end-to-end by internal/license and
// internal/node/licensing_bootstrap.go for module-capability licensing. It
// has no field for "peer X asserts TrustLevel L about peer Y", and
// repurposing it would mean forcing an existing SDS record type to carry a
// meaning its schema does not describe. Per the task constraints we also
// do not invent a new fake SDS schema, so this file uses the canonical
// encoding above; adding a proper SDS/FlatBuffers record type for
// web-of-trust grants (so they can be exchanged the same way other SDS
// records are) is recorded as a follow-up, not done here.
//
// # Identity model
//
// Granter/Subject are libp2p peer.IDs. A SignedGrant carries no separate
// public-key field: for Ed25519 libp2p identities, the public key is
// embedded in — and recoverable from — the peer ID itself, via the
// "identity" multihash inlining libp2p uses for keys under 42 bytes (see
// peer.ID.ExtractPublicKey and peer.AdvancedEnableInlining; Ed25519 keys
// are always small enough to qualify). This keeps a SignedGrant fully
// self-verifying: no registry lookup or out-of-band key exchange is needed
// to check one. SignGrant/Verify therefore require the signing (resp.
// granter) key to be Ed25519 and reject anything else — matching the
// node's own Ed25519 identity key (internal/node/identity_bundle.go /
// Node.Identity().SigningPrivKey, exposed via Node.SigningKey()).
package peers

import (
	"encoding/binary"
	"errors"
	"fmt"
	"time"

	libp2pcrypto "github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/peer"

	"github.com/spacedatanetwork/sdn-server/internal/trust"
)

// Grant-related errors.
var (
	// ErrGrantMissingFields is returned when Granter or Subject is empty.
	ErrGrantMissingFields = errors.New("peers: grant is missing required fields (granter/subject)")
	// ErrGrantNotEd25519 is returned when a grant's identity does not
	// resolve to an Ed25519 public key (either the signing key passed to
	// SignGrant, or the key embedded in a SignedGrant's Granter peer ID).
	ErrGrantNotEd25519 = errors.New("peers: grant identity does not resolve to an Ed25519 public key")
	// ErrGrantBadSignature is returned when a grant's signature does not
	// verify (missing, wrong key, or any tampered field).
	ErrGrantBadSignature = errors.New("peers: grant signature verification failed")
	// ErrGrantExpired is returned by Verify/VerifyAt for a grant whose
	// ExpiresAt is non-zero and not after the reference time.
	ErrGrantExpired = errors.New("peers: grant has expired")
	// ErrGrantMalformed is returned by UnmarshalSignedGrant for input that
	// is not validly encoded.
	ErrGrantMalformed = errors.New("peers: malformed grant encoding")
)

// Grant is the canonical, unsigned trust/permission assertion: Granter
// vouches for Subject at TrustLevel Level, valid from IssuedAt and (if
// ExpiresAt is non-zero) until ExpiresAt.
type Grant struct {
	// Granter is the peer ID of the identity making the assertion.
	Granter peer.ID
	// Subject is the peer ID the assertion is about.
	Subject peer.ID
	// Level is the trust level Granter asserts for Subject.
	Level TrustLevel
	// IssuedAt is when the grant was signed.
	IssuedAt time.Time
	// ExpiresAt is when the grant stops being valid. The zero value means
	// "no expiry".
	ExpiresAt time.Time
}

// SignedGrant is a Grant plus the granter's Ed25519 signature over its
// canonical encoding (see (Grant).canonicalBytes). It is self-verifying:
// Verify derives the granter's public key directly from the Granter peer
// ID, with no other input required.
type SignedGrant struct {
	Grant
	// Signature is the Ed25519 signature over Grant.canonicalBytes(),
	// produced by the Granter's own private key.
	Signature []byte
}

// SignGrant signs g with priv, which MUST be an Ed25519 libp2p private key
// (e.g. crypto.GenerateEd25519Key for tests, or a node's own
// identity.SigningPrivKey — see internal/node/identity_bundle.go /
// Node.Identity()).
//
//   - If g.Granter is unset, it is filled in from priv's own peer ID.
//   - If g.Granter IS set, it must equal priv's peer ID: a grant cannot be
//     signed on behalf of a different granter.
//   - If g.IssuedAt is zero, it defaults to time.Now().UTC().
//
// The returned SignedGrant's Signature covers exactly g (post-defaulting),
// via g.canonicalBytes().
func SignGrant(priv libp2pcrypto.PrivKey, g Grant) (SignedGrant, error) {
	if priv == nil {
		return SignedGrant{}, errors.New("peers: SignGrant: signing key is required")
	}
	pub := priv.GetPublic()
	if _, ok := pub.(*libp2pcrypto.Ed25519PublicKey); !ok {
		return SignedGrant{}, fmt.Errorf("%w: signing key type %s", ErrGrantNotEd25519, pub.Type())
	}
	granterID, err := peer.IDFromPublicKey(pub)
	if err != nil {
		return SignedGrant{}, fmt.Errorf("peers: SignGrant: derive granter peer ID: %w", err)
	}
	if g.Granter == "" {
		g.Granter = granterID
	} else if g.Granter != granterID {
		return SignedGrant{}, fmt.Errorf("peers: SignGrant: grant.Granter %s does not match signing key's peer ID %s", g.Granter, granterID)
	}
	if g.Subject == "" {
		return SignedGrant{}, ErrGrantMissingFields
	}
	if g.IssuedAt.IsZero() {
		g.IssuedAt = time.Now().UTC()
	}

	sig, err := priv.Sign(g.canonicalBytes())
	if err != nil {
		return SignedGrant{}, fmt.Errorf("peers: SignGrant: sign: %w", err)
	}
	return SignedGrant{Grant: g, Signature: append([]byte(nil), sig...)}, nil
}

// Verify checks sg's signature under the Ed25519 public key embedded in its
// own Granter peer ID, and that it is not expired as of time.Now(). See
// VerifyAt to check against an explicit reference time.
func (sg SignedGrant) Verify() error {
	return sg.VerifyAt(time.Now())
}

// VerifyAt is Verify against an explicit reference time `now` (used by
// BuildTrustGraphAt, and by tests that need deterministic expiry checks).
//
// Verification fails (distinct sentinel errors, wrapped with details) when:
//   - Granter or Subject is empty, or Signature is empty (ErrGrantMissingFields / ErrGrantBadSignature).
//   - The Granter peer ID does not embed a recoverable Ed25519 public key (ErrGrantNotEd25519).
//   - The signature does not verify over the CURRENT field values — i.e. it
//     also fails if Subject, Level, IssuedAt, or ExpiresAt was tampered
//     with after signing, since canonicalBytes() covers all of them
//     (ErrGrantBadSignature).
//   - ExpiresAt is non-zero and not after `now` (ErrGrantExpired) — checked
//     only once the signature itself is confirmed valid.
func (sg SignedGrant) VerifyAt(now time.Time) error {
	if sg.Granter == "" || sg.Subject == "" {
		return ErrGrantMissingFields
	}
	if len(sg.Signature) == 0 {
		return ErrGrantBadSignature
	}
	pub, err := sg.Granter.ExtractPublicKey()
	if err != nil {
		return fmt.Errorf("%w: %v", ErrGrantNotEd25519, err)
	}
	if _, ok := pub.(*libp2pcrypto.Ed25519PublicKey); !ok {
		return fmt.Errorf("%w: got %s", ErrGrantNotEd25519, pub.Type())
	}
	ok, err := pub.Verify(sg.Grant.canonicalBytes(), sg.Signature)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrGrantBadSignature, err)
	}
	if !ok {
		return ErrGrantBadSignature
	}
	if !sg.ExpiresAt.IsZero() && !now.Before(sg.ExpiresAt) {
		return ErrGrantExpired
	}
	return nil
}

// canonicalBytes returns the deterministic signing/wire preimage for g (see
// the package doc for why this encoding was chosen over canonical JSON).
// Layout, all integers big-endian, fixed field order:
//
//	uint32(len(Granter)) | Granter raw bytes
//	uint32(len(Subject)) | Subject raw bytes
//	int64(Level)
//	int64(IssuedAt.UnixNano())  -- 0 if IssuedAt.IsZero()
//	int64(ExpiresAt.UnixNano()) -- 0 if ExpiresAt.IsZero() (i.e. "no expiry")
func (g Grant) canonicalBytes() []byte {
	buf := make([]byte, 0, 4+len(g.Granter)+4+len(g.Subject)+24)
	buf = appendLP(buf, []byte(g.Granter))
	buf = appendLP(buf, []byte(g.Subject))
	buf = appendInt64(buf, int64(g.Level))
	buf = appendInt64(buf, unixNanoOrZero(g.IssuedAt))
	buf = appendInt64(buf, unixNanoOrZero(g.ExpiresAt))
	return buf
}

// MarshalBinary serializes sg deterministically as g.canonicalBytes()
// followed by a length-prefixed Signature. Round-tripping through
// MarshalBinary -> UnmarshalSignedGrant reproduces byte-identical output
// (sign -> serialize -> parse -> verify): a receiver can check a grant
// exactly as received, without needing any other channel or key exchange.
func (sg SignedGrant) MarshalBinary() ([]byte, error) {
	if sg.Granter == "" || sg.Subject == "" {
		return nil, ErrGrantMissingFields
	}
	buf := sg.Grant.canonicalBytes()
	buf = appendLP(buf, sg.Signature)
	return buf, nil
}

// UnmarshalSignedGrant parses the encoding produced by
// SignedGrant.MarshalBinary. It does not itself verify the signature —
// callers should call Verify/VerifyAt on the result.
func UnmarshalSignedGrant(data []byte) (SignedGrant, error) {
	granterBytes, rest, err := readLP(data)
	if err != nil {
		return SignedGrant{}, fmt.Errorf("%w: granter: %v", ErrGrantMalformed, err)
	}
	subjectBytes, rest, err := readLP(rest)
	if err != nil {
		return SignedGrant{}, fmt.Errorf("%w: subject: %v", ErrGrantMalformed, err)
	}
	level, rest, err := readInt64(rest)
	if err != nil {
		return SignedGrant{}, fmt.Errorf("%w: level: %v", ErrGrantMalformed, err)
	}
	issuedAtNano, rest, err := readInt64(rest)
	if err != nil {
		return SignedGrant{}, fmt.Errorf("%w: issued_at: %v", ErrGrantMalformed, err)
	}
	expiresAtNano, rest, err := readInt64(rest)
	if err != nil {
		return SignedGrant{}, fmt.Errorf("%w: expires_at: %v", ErrGrantMalformed, err)
	}
	sig, rest, err := readLP(rest)
	if err != nil {
		return SignedGrant{}, fmt.Errorf("%w: signature: %v", ErrGrantMalformed, err)
	}
	if len(rest) != 0 {
		return SignedGrant{}, fmt.Errorf("%w: trailing bytes", ErrGrantMalformed)
	}

	granter, err := peer.IDFromBytes(granterBytes)
	if err != nil {
		return SignedGrant{}, fmt.Errorf("%w: granter peer id: %v", ErrGrantMalformed, err)
	}
	subject, err := peer.IDFromBytes(subjectBytes)
	if err != nil {
		return SignedGrant{}, fmt.Errorf("%w: subject peer id: %v", ErrGrantMalformed, err)
	}

	return SignedGrant{
		Grant: Grant{
			Granter:   granter,
			Subject:   subject,
			Level:     TrustLevel(level),
			IssuedAt:  zeroOrUnixNano(issuedAtNano),
			ExpiresAt: zeroOrUnixNano(expiresAtNano),
		},
		Signature: sig,
	}, nil
}

func unixNanoOrZero(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixNano()
}

func zeroOrUnixNano(nano int64) time.Time {
	if nano == 0 {
		return time.Time{}
	}
	return time.Unix(0, nano).UTC()
}

func appendLP(buf, b []byte) []byte {
	var lenBuf [4]byte
	binary.BigEndian.PutUint32(lenBuf[:], uint32(len(b)))
	buf = append(buf, lenBuf[:]...)
	buf = append(buf, b...)
	return buf
}

func appendInt64(buf []byte, v int64) []byte {
	var b [8]byte
	binary.BigEndian.PutUint64(b[:], uint64(v))
	return append(buf, b[:]...)
}

func readLP(data []byte) (field, rest []byte, err error) {
	if len(data) < 4 {
		return nil, nil, errors.New("truncated length prefix")
	}
	n := binary.BigEndian.Uint32(data[:4])
	data = data[4:]
	if uint64(len(data)) < uint64(n) {
		return nil, nil, errors.New("truncated field")
	}
	return data[:n], data[n:], nil
}

func readInt64(data []byte) (v int64, rest []byte, err error) {
	if len(data) < 8 {
		return 0, nil, errors.New("truncated int64")
	}
	return int64(binary.BigEndian.Uint64(data[:8])), data[8:], nil
}

// trustEdgeKey identifies a (granter, subject) pair for de-duplication in
// BuildTrustGraphAt.
type trustEdgeKey struct {
	granter peer.ID
	subject peer.ID
}

// BuildTrustGraph converts grants into the internal/trust graph shape
// consumed by ComputeValidity / Registry.SetTrustGraph, checking expiry
// against time.Now(). See BuildTrustGraphAt for a deterministic variant
// with an explicit reference time.
func BuildTrustGraph(grants []SignedGrant) (*trust.Graph, int) {
	return BuildTrustGraphAt(grants, time.Now())
}

// BuildTrustGraphAt is BuildTrustGraph against an explicit reference time.
//
// Only grants that verify — valid Ed25519 signature under the granter's OWN
// embedded peer-ID public key, unexpired as of now — contribute an edge.
// Anything else (bad/missing signature, ANY tampered field, expired,
// unknown/never-level, a self-grant, or an edge that would close a cycle in
// the underlying DAG — see internal/trust.Graph.SetEdge/ErrCycle) is
// silently dropped and counted in the returned skipped total, so callers
// can log it without this function needing a logger dependency.
//
// Level -> edge mapping (task C4 step 4; reuses the existing
// TrustLevel.EdgeWeight() classification from trust.go verbatim, so this is
// always in sync with the PGP ownertrust bucketing used elsewhere):
//
//	Never / Untrusted (Unknown)        -> NO edge added (EdgeWeight() == 0)
//	Limited (Marginal) / Standard      -> MarginalEdgeWeight (0.5) edge
//	Trusted (Full) / Admin / Ultimate  -> FullEdgeWeight (1.0) edge
//
// When multiple valid grants exist for the same (Granter, Subject) pair,
// the one with the latest IssuedAt wins (last-writer-wins on the edge).
func BuildTrustGraphAt(grants []SignedGrant, now time.Time) (g *trust.Graph, skipped int) {
	g = trust.NewGraph()
	applied := make(map[trustEdgeKey]time.Time, len(grants))
	for _, sg := range grants {
		if err := sg.VerifyAt(now); err != nil {
			skipped++
			continue
		}
		if sg.Granter == sg.Subject {
			skipped++
			continue
		}
		weight := sg.Level.EdgeWeight()
		if weight <= 0 {
			skipped++
			continue
		}
		key := trustEdgeKey{sg.Granter, sg.Subject}
		if prev, ok := applied[key]; ok && !sg.IssuedAt.After(prev) {
			// An already-applied grant for this pair is at least as new, so
			// this one is dropped exactly like any other rejected grant —
			// count it in skipped too (the doc comment above promises
			// "anything else ... is silently dropped and counted in the
			// returned skipped total").
			skipped++
			continue
		}
		if err := g.SetEdge(trust.Edge{
			Truster:     sg.Granter.String(),
			Trustee:     sg.Subject.String(),
			Weight:      weight,
			UpdatedAtMs: sg.IssuedAt.UnixMilli(),
		}); err != nil {
			skipped++
			continue
		}
		applied[key] = sg.IssuedAt
	}
	return g, skipped
}
