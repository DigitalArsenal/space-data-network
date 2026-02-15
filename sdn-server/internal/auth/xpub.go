package auth

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/binary"
	"fmt"

	"github.com/mr-tron/base58"
)

// SDN Extended Public Key format
//
// SLIP-10 Ed25519 only supports hardened child derivation, so an extended
// public key cannot derive children. The SDN xpub at the signing key path
// m/44'/0'/0'/0'/0' therefore directly embeds the Ed25519 signing public
// key. It uses BIP-32 serialization structure (78 bytes + 4-byte checksum)
// with custom version bytes to distinguish it from secp256k1 xpubs.
//
// Layout (78 bytes payload):
//
//	[0:4]   version      — 0x0534ED10 (SDN Ed25519 public key)
//	[4]     depth        — path depth (5 for m/44'/0'/0'/0'/0')
//	[5:9]   fingerprint  — parent key fingerprint (or 0x00000000)
//	[9:13]  child_index  — last derivation index (with hardened flag)
//	[13:45] chain_code   — 32-byte SLIP-10 chain code
//	[45:78] key_data     — 0x00 prefix + 32-byte Ed25519 public key
//
// Encoding: Base58Check (payload + double-SHA256 checksum first 4 bytes).

var (
	// SDNXPubVersion identifies an SDN Ed25519 extended public key.
	SDNXPubVersion = [4]byte{0x05, 0x34, 0xED, 0x10}

	// SDNXPrvVersion identifies an SDN Ed25519 extended private key (not used in config).
	SDNXPrvVersion = [4]byte{0x05, 0x34, 0xED, 0x11}
)

const (
	xpubPayloadLen  = 78
	xpubChecksumLen = 4
	xpubTotalLen    = xpubPayloadLen + xpubChecksumLen // 82
)

// SDNExtendedPubKey holds the parsed fields of an SDN extended public key.
type SDNExtendedPubKey struct {
	Version     [4]byte
	Depth       uint8
	Fingerprint [4]byte
	ChildIndex  uint32
	ChainCode   [32]byte
	PubKey      [32]byte // Ed25519 public key (without the 0x00 prefix byte)
}

// SerializeSDNXPub encodes an SDN extended public key to Base58Check.
func SerializeSDNXPub(key *SDNExtendedPubKey) string {
	var payload [xpubPayloadLen]byte

	copy(payload[0:4], key.Version[:])
	payload[4] = key.Depth
	copy(payload[5:9], key.Fingerprint[:])
	binary.BigEndian.PutUint32(payload[9:13], key.ChildIndex)
	copy(payload[13:45], key.ChainCode[:])
	payload[45] = 0x00 // Ed25519 key type prefix
	copy(payload[46:78], key.PubKey[:])

	return base58CheckEncode(payload[:])
}

// ParseSDNXPub decodes a Base58Check-encoded SDN extended public key.
func ParseSDNXPub(encoded string) (*SDNExtendedPubKey, error) {
	data, err := base58CheckDecode(encoded)
	if err != nil {
		return nil, fmt.Errorf("invalid SDN xpub encoding: %w", err)
	}
	if len(data) != xpubPayloadLen {
		return nil, fmt.Errorf("invalid SDN xpub length: got %d, want %d", len(data), xpubPayloadLen)
	}

	var version [4]byte
	copy(version[:], data[0:4])
	if version != SDNXPubVersion {
		return nil, fmt.Errorf("unknown SDN xpub version: %x (expected %x)", version, SDNXPubVersion)
	}

	if data[45] != 0x00 {
		return nil, fmt.Errorf("invalid key type prefix: 0x%02x (expected 0x00 for Ed25519)", data[45])
	}

	key := &SDNExtendedPubKey{
		Version:    version,
		Depth:      data[4],
		ChildIndex: binary.BigEndian.Uint32(data[9:13]),
	}
	copy(key.Fingerprint[:], data[5:9])
	copy(key.ChainCode[:], data[13:45])
	copy(key.PubKey[:], data[46:78])

	return key, nil
}

// ExtractEd25519PubKeyFromXPub extracts the 32-byte Ed25519 public key from
// an xpub string. It supports:
//  1. SDN-format extended public key (Base58Check with SDN version bytes)
//  2. Standard BIP-32 xpub — returns error (secp256k1 key, not Ed25519)
//
// For backward compatibility, raw 64-char hex Ed25519 pubkeys are handled
// separately in the caller (userstore), not here.
func ExtractEd25519PubKeyFromXPub(xpub string) (ed25519.PublicKey, error) {
	parsed, err := ParseSDNXPub(xpub)
	if err != nil {
		return nil, err
	}
	return ed25519.PublicKey(parsed.PubKey[:]), nil
}

// NewSDNXPub creates an SDN extended public key from raw components.
// This is typically called after SLIP-10 derivation to produce a serializable xpub.
func NewSDNXPub(pubKey []byte, chainCode []byte, depth uint8, fingerprint [4]byte, childIndex uint32) (*SDNExtendedPubKey, error) {
	if len(pubKey) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("pubkey must be %d bytes, got %d", ed25519.PublicKeySize, len(pubKey))
	}
	if len(chainCode) != 32 {
		return nil, fmt.Errorf("chain code must be 32 bytes, got %d", len(chainCode))
	}

	key := &SDNExtendedPubKey{
		Version:     SDNXPubVersion,
		Depth:       depth,
		Fingerprint: fingerprint,
		ChildIndex:  childIndex,
	}
	copy(key.PubKey[:], pubKey)
	copy(key.ChainCode[:], chainCode)
	return key, nil
}

// base58CheckEncode encodes a payload with a 4-byte double-SHA256 checksum.
func base58CheckEncode(payload []byte) string {
	checksum := doubleSHA256(payload)
	full := make([]byte, len(payload)+xpubChecksumLen)
	copy(full, payload)
	copy(full[len(payload):], checksum[:xpubChecksumLen])
	return base58.Encode(full)
}

// base58CheckDecode decodes Base58Check, verifying the 4-byte checksum.
func base58CheckDecode(encoded string) ([]byte, error) {
	raw, err := base58.Decode(encoded)
	if err != nil {
		return nil, fmt.Errorf("base58 decode failed: %w", err)
	}
	if len(raw) < xpubChecksumLen+1 {
		return nil, fmt.Errorf("data too short for Base58Check")
	}

	payload := raw[:len(raw)-xpubChecksumLen]
	checksum := raw[len(raw)-xpubChecksumLen:]

	expected := doubleSHA256(payload)
	for i := 0; i < xpubChecksumLen; i++ {
		if checksum[i] != expected[i] {
			return nil, fmt.Errorf("checksum mismatch")
		}
	}

	return payload, nil
}

func doubleSHA256(data []byte) [32]byte {
	first := sha256.Sum256(data)
	return sha256.Sum256(first[:])
}
