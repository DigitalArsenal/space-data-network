package auth

// Operator identities as trust-matrix entries.
//
// OWNER DIRECTIVE 2026-07-27, verbatim: "...then after that it should store the
// sign-ins for the node the same as it would peers in the trust matrix (using
// whatever space data standards facilitates that), and then use the entries in
// an sqlite database to handle that. Also I think it should probably be in a
// separate database file that the other standards for safety."
//
// # The SDS record (Themis ruling, 2026-07-27)
//
// The standard that facilitates this is **PRR — the Peer Registry Record**
// (spacedatastandards.org schema/PRR/main.fbs). It is exactly "an identity plus
// a trust assertion about it": PEER_ID (required), TRUST_LEVEL, NAME,
// ORGANIZATION, GROUPS, NOTES, ADDED_AT, LAST_SEEN, LAST_CONNECTED,
// CONNECTION_COUNT, EPM_DATA, VCARD_DATA, METADATA. internal/peers.TrustedPeer
// is already a projection of PRR, so "the same as it would peers" is a
// concrete, existing shape — nothing here is invented.
//
// Themis also ruled the binding: PRR.PEER_ID is REQUIRED, and there is no
// xpub-keyed identity space in SDS. Deriving the peer ID from the account
// xpub's secp256k1 key — the same key internal/wasm.DeriveIdentity turns into
// the node's own PeerID — is therefore the mandatory canonical binding, not a
// design choice. Operators enter the SAME key space as peers.
//
// # Where the owner and the oracle disagreed, and why the owner wins
//
// Themis further recommended collapsing this store into the PRR-backed peer
// registry, calling the separate SQLite file a defect. That recommendation is
// NOT followed, because the owner directive above is explicit and contrary:
// sqlite, separate file, "for safety". The reasons are sound and worth
// recording — operator credentials and session state are a different blast
// radius from network peer bookkeeping, and keeping them out of the standards
// store means a corrupt or rebuilt standards store cannot lock an operator out
// of their own node, nor can a peer-registry bug reach auth rows.
//
// So the ruling is a synthesis: **PRR governs the SHAPE (Themis), the owner
// governs the LOCATION.** This file carries the PRR projection for operator
// entries in the auth store's own SQLite file.
//
// # Trust scale
//
// internal/peers uses the 7-value PGP ownertrust scale
// (never/unknown/marginal/standard/full/admin/ultimate); PRR's IDL enum has 5
// (Untrusted/Limited/Standard/Trusted/Admin). That mapping is lossy and
// pre-exists in internal/peers/persistence.go. Operator rows store the numeric
// peers.TrustLevel so nothing is lost in OUR file; the lossy narrowing only
// happens if a row is serialized out as a PRR record, which is the same
// contract peers already live under.

import (
	"strings"

	"github.com/spacedatanetwork/sdn-server/internal/epm"
	"github.com/spacedatanetwork/sdn-server/internal/peers"
)

// TrustMatrixEntry is the PRR projection of an operator identity. Field names
// mirror internal/peers.TrustedPeer so the two surfaces are recognisably the
// same record; the JSON keys are the node-local snake_case the auth API already
// uses, NOT PRR's IDL capitalization — these are node-synthesized API fields,
// and only an actual serialized SDS record carries IDL-capitalized keys.
type TrustMatrixEntry struct {
	// PeerID is PRR.PEER_ID, derived from the account xpub's secp256k1 key.
	// Empty when the identifier is not a parseable xpub (config labels).
	PeerID string `json:"peer_id,omitempty"`
	// XPub is this store's own key. PRR has no xpub-keyed space; the xpub is
	// carried inside EPM_DATA's KEYS[].XPUB when an EPM is present.
	XPub string `json:"xpub"`

	Name         string           `json:"name,omitempty"`
	Organization string           `json:"organization,omitempty"`
	TrustLevel   peers.TrustLevel `json:"trust_level"`
	Groups       []string         `json:"groups,omitempty"`
	Notes        string           `json:"notes,omitempty"`

	AddedAt         int64 `json:"added_at"`
	LastConnected   int64 `json:"last_connected,omitempty"`
	ConnectionCount int64 `json:"connection_count"`

	// EPMData is PRR.EPM_DATA — the operator's serialized SDS EPM record when
	// one is known. VCardData is PRR.VCARD_DATA.
	EPMData   []byte `json:"epm_data,omitempty"`
	VCardData string `json:"vcard_data,omitempty"`

	Source string `json:"source"`
}

// OperatorPeerID derives the PRR PEER_ID for an operator identified by an
// account xpub, per the Themis binding: take the secp256k1 public key the xpub
// serializes and turn it into a libp2p peer ID exactly as the node does for its
// own identity.
//
// ok is false for anything that is not a parseable xpub — config labels,
// fixtures, and the empty string. Callers store an empty PEER_ID in that case
// rather than inventing one: a fabricated peer ID would collide with a real
// identity's key space, which is precisely what Themis warned against.
func OperatorPeerID(xpub string) (string, bool) {
	trimmed := strings.TrimSpace(xpub)
	if trimmed == "" {
		return "", false
	}
	pubKey, ok := epm.AccountPublicKeyFromXPub(trimmed)
	if !ok || len(pubKey) == 0 {
		return "", false
	}
	id, err := peers.PeerIDFromPublicKey(hexEncode(pubKey))
	if err != nil {
		return "", false
	}
	return id.String(), true
}

// hexEncode is a tiny local helper so OperatorPeerID can reuse
// peers.PeerIDFromPublicKey's text-decoding contract without importing
// encoding/hex at the call site.
func hexEncode(b []byte) string {
	const digits = "0123456789abcdef"
	out := make([]byte, 0, len(b)*2)
	for _, c := range b {
		out = append(out, digits[c>>4], digits[c&0x0f])
	}
	return string(out)
}

// TrustMatrix returns every operator identity as a PRR projection — the
// "sign-ins stored the same as peers in the trust matrix" surface.
func (s *UserStore) TrustMatrix() ([]TrustMatrixEntry, error) {
	users, err := s.ListUsers()
	if err != nil {
		return nil, err
	}

	entries := make([]TrustMatrixEntry, 0, len(users))
	for _, u := range users {
		entry := TrustMatrixEntry{
			XPub:       u.XPub,
			Name:       u.Name,
			TrustLevel: u.TrustLevel,
			AddedAt:    u.CreatedAt.Unix(),
			Source:     u.Source,
		}
		if peerID, ok := OperatorPeerID(u.XPub); ok {
			entry.PeerID = peerID
		}
		if u.LastLogin != nil {
			entry.LastConnected = u.LastLogin.Unix()
		}
		entry.ConnectionCount = u.SignInCount
		entry.EPMData = u.EPMData
		entry.VCardData = u.VCardData
		entry.Organization = u.Organization
		entry.Notes = u.Notes
		if strings.TrimSpace(u.Groups) != "" {
			entry.Groups = strings.Split(u.Groups, ",")
		}
		entries = append(entries, entry)
	}
	return entries, nil
}
