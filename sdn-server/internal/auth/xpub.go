package auth

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"strings"

	"github.com/mr-tron/base58"
)

// IsValidXPub performs basic validation on a BIP-32 extended public key string.
// Standard xpubs are Base58Check-encoded and start with "xpub".
func IsValidXPub(xpub string) bool {
	xpub = strings.TrimSpace(xpub)
	return len(xpub) > 20 && strings.HasPrefix(xpub, "xpub")
}

// xpubMainnetPublicVersion is the BIP-32 version prefix for a mainnet public
// extended key ("xpub...").
const xpubMainnetPublicVersion uint32 = 0x0488B21E

// XPubDepth returns the BIP-32 depth of a serialized extended public key.
//
// ok is false when the string is not a well-formed, checksum-valid mainnet
// xpub — which includes every operator-chosen label and every test fixture.
// Callers MUST treat !ok as "unknown", never as "unsafe": the node has always
// accepted arbitrary opaque strings as UserStore keys, and this function exists
// to identify one specific PROVEN-dangerous shape, not to start rejecting
// labels.
//
// Depth 0 is the MASTER key. Depth 3 is a BIP-44 account node
// (m/44'/coin'/account'), which is what the node's own derivation reports and
// what §2 of graph/tasks/nst-node-admin-contract.md specifies.
func XPubDepth(xpub string) (int, bool) {
	trimmed := strings.TrimSpace(xpub)
	if trimmed == "" {
		return 0, false
	}
	decoded, err := base58.Decode(trimmed)
	if err != nil || len(decoded) != 82 {
		return 0, false
	}
	body, checksum := decoded[:78], decoded[78:]
	first := sha256.Sum256(body)
	second := sha256.Sum256(first[:])
	if !bytes.Equal(checksum, second[:4]) {
		return 0, false
	}
	if binary.BigEndian.Uint32(body[:4]) != xpubMainnetPublicVersion {
		return 0, false
	}
	return int(body[4]), true
}

// IsMasterXPub reports whether xpub is a PROVEN depth-0 BIP-32 master extended
// public key.
//
// # Why the node refuses to mint an operator identity from one
//
// A master xpub is strictly more sensitive than the account xpub the node's own
// derivation produces (§2: m/44'/0'/account'). It enumerates EVERY account and
// every address under the entire wallet — Bitcoin, Ethereum and Solana included
// (internal/wasm.DeriveIdentity derives them all beneath this node) — for
// anyone who reads it. An account xpub leaks one account; a master xpub leaks
// the wallet.
//
// This matters because hd-wallet-ui's LEGACY identity schemes — the only ones
// that can produce the raw-32 signature this node's admit point verifies —
// report the MASTER xpub as their accountXpub. Measured 2026-07-27 against
// hd-wallet-ui/hd-wallet-wasm 2.0.28:
//
//	legacy mnemonic import : accountXpub depth 0 (master), auth key at m/44'/0'/0'/0/0
//	modern password (v2)   : accountXpub depth 3 (account), auth key at m/44'/0'/0'/0'/0'
//
// So the defect is confined to the deprecated schemes, and the v2 admit point
// (§11.2) fixes it as a side effect: a modern identity reports a proper
// account-level xpub at exactly §2's hardened paths.
//
// Until then the node declines to WRITE a master xpub into its user store. It
// is a one-way door — once auth.db on an operator's machine holds a master
// xpub, that is a durable secret at rest that no later fix removes — and it
// silently diverges from `show-identity`, so a users: entry seeded from the
// documented derivation could never match. Declining to open a door that has
// never been open is not a regression.
//
// Sign-in itself is NOT gated on this: an already-registered operator
// authenticates by signing key with no xpub in the request at all (see
// handleChallenge's GetUserBySigningPubKey path), which is the recommended
// dashboard behavior and never transmits an xpub of any depth.
func IsMasterXPub(xpub string) bool {
	depth, ok := XPubDepth(xpub)
	return ok && depth == 0
}
