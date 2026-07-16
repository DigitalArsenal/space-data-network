// Package nodeepm builds a signed SDS $EPM record for a kubo SDN node from its
// libp2p identity, and derives vCard / QR exports from it. It is the node-side
// analogue of sdn-server/internal/epm, but the node has no HD wallet: its
// signing identity is its libp2p Ed25519 key (core.IpfsNode.PrivateKey /
// .Identity), so the EPM is built from what that identity can truthfully
// provide and self-signed with the node private key.
//
// # What the node EPM populates vs omits
//
// Populated (all derived from the libp2p identity):
//   - ENTITY_TYPE = Node
//   - DN = the node peer ID (so vCard FN / QR label identify the node)
//   - KEYS = one Signing CryptoKey: the Ed25519 public key (hex), ADDRESS_TYPE
//     "ed25519". This is the key the EPM self-signature verifies under.
//   - MULTIFORMAT_ADDRESS = "/p2p/<peerID>" plus the node's listen multiaddrs.
//   - SIGNATURE_TIMESTAMP + SIGNATURE (Ed25519 over the canonical JCS payload).
//
// Omitted, because the node identity cannot truthfully provide them (they are
// HD-wallet artifacts on sdn-server, and faking them would be dishonest):
//   - CHAIN_PROOFS (bitcoin/ethereum/solana address attestations)
//   - XPUB / secp256k1 derived keys
//   - an encryption CryptoKey — EPM signature verification only reads Signing
//     keys (see VerifyEPMSignature), so no encryption key is required and none
//     is fabricated.
//
// The EPM verifies under the node's own Ed25519 public key by construction:
// BuildNodeEPM signs the exact canonical payload the verifier recomputes from
// the wire bytes, then round-trips VerifyEPMSignature before returning.
package nodeepm

import (
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	flatbuffers "github.com/google/flatbuffers/go"

	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/EPM"
)

const (
	// ed25519PublicKeySize is the raw Ed25519 public-key length. The node's
	// libp2p GetPublic().Raw() returns exactly this for an Ed25519 identity.
	ed25519PublicKeySize = 32
	// signingKeyAddress records the provenance of the node's signing key: it IS
	// the libp2p identity key, not an HD-derived path.
	signingKeyAddress = "libp2p"
)

var (
	// ErrNoPeerID / ErrBadSigningKey / ErrNoSigner guard BuildNodeEPM inputs.
	ErrNoPeerID      = errors.New("nodeepm: peer id is required")
	ErrBadSigningKey = errors.New("nodeepm: signing public key must be a 32-byte ed25519 key")
	ErrNoSigner      = errors.New("nodeepm: a signing function is required")
)

// Identity is the minimal libp2p-derived material the node EPM is built from.
// It is intentionally free of libp2p types so it can be constructed both from a
// real *core.IpfsNode (by the plugin) and from a bare Ed25519 key (in tests).
type Identity struct {
	// PeerID is the node's libp2p peer ID string (core.IpfsNode.Identity).
	PeerID string
	// SigningPub is the raw 32-byte Ed25519 public key
	// (core.IpfsNode.PrivateKey.GetPublic().Raw()).
	SigningPub []byte
	// Multiaddrs are the node's listen multiaddrs (may be empty). Each is
	// advertised in MULTIFORMAT_ADDRESS alongside the canonical "/p2p/<peerID>".
	Multiaddrs []string
	// Sign produces an Ed25519 signature over payload using the node private key
	// (core.IpfsNode.PrivateKey.Sign). For a libp2p Ed25519 key this is a
	// standard ed25519 signature that verifies under SigningPub.
	Sign func(payload []byte) ([]byte, error)
}

// BuildNodeEPM builds a signed, size-prefixed $EPM FlatBuffer for the node.
//
// It builds twice: first unsigned to obtain the exact wire bytes, then again
// with the embedded signature over the canonical payload derived from those
// bytes — the same two-pass construction sdn-server uses, so signer and
// verifier agree by construction. The result is verified before it is returned.
func BuildNodeEPM(id Identity) ([]byte, error) {
	peerID := strings.TrimSpace(id.PeerID)
	if peerID == "" {
		return nil, ErrNoPeerID
	}
	if len(id.SigningPub) != ed25519PublicKeySize {
		return nil, ErrBadSigningKey
	}
	if id.Sign == nil {
		return nil, ErrNoSigner
	}

	signatureTimestamp := time.Now().Unix()

	unsigned, err := buildEPMBytes(peerID, id.SigningPub, id.Multiaddrs, "", signatureTimestamp)
	if err != nil {
		return nil, err
	}

	payload, err := EPMSigningPayload(unsigned)
	if err != nil {
		return nil, fmt.Errorf("nodeepm: derive signing payload: %w", err)
	}
	sig, err := id.Sign(payload)
	if err != nil {
		return nil, fmt.Errorf("nodeepm: sign EPM: %w", err)
	}
	signatureHex := hex.EncodeToString(sig)

	signed, err := buildEPMBytes(peerID, id.SigningPub, id.Multiaddrs, signatureHex, signatureTimestamp)
	if err != nil {
		return nil, err
	}
	if err := VerifyEPMSignature(signed); err != nil {
		return nil, fmt.Errorf("nodeepm: built EPM failed self-verification: %w", err)
	}
	return signed, nil
}

// buildEPMBytes serializes the node EPM wire bytes with the given signature
// (hex, empty for the unsigned pre-image pass) and timestamp. Deterministic for
// fixed inputs.
func buildEPMBytes(peerID string, signingPub []byte, multiaddrs []string, signatureHex string, signatureTimestamp int64) ([]byte, error) {
	builder := flatbuffers.NewBuilder(1024)

	// DN = peer ID, so downstream vCard FN and QR label carry the node identity.
	dnOff := builder.CreateString(peerID)

	// Single Ed25519 signing key — the libp2p identity public key. This is the
	// key VerifyEPMSignature and any SDN directory lookup read from KEYS.
	pubOff := builder.CreateString(hex.EncodeToString(signingPub))
	addrTypeOff := builder.CreateString("ed25519")
	keyAddrOff := builder.CreateString(signingKeyAddress)
	EPM.CryptoKeyStart(builder)
	EPM.CryptoKeyAddPUBLIC_KEY(builder, pubOff)
	EPM.CryptoKeyAddADDRESS_TYPE(builder, addrTypeOff)
	EPM.CryptoKeyAddKEY_ADDRESS(builder, keyAddrOff)
	EPM.CryptoKeyAddKEY_TYPE(builder, EPM.KeyTypeSigning)
	keyOff := EPM.CryptoKeyEnd(builder)

	EPM.EPMStartKEYSVector(builder, 1)
	builder.PrependUOffsetT(keyOff)
	keysOff := builder.EndVector(1)

	// MULTIFORMAT_ADDRESS: canonical "/p2p/<peerID>" first (so peerIDFromEPM
	// resolves), then the node's listen multiaddrs, de-duplicated in order.
	addresses := normalizeAddresses(append([]string{"/p2p/" + peerID}, multiaddrs...))
	var multiAddrOff flatbuffers.UOffsetT
	if len(addresses) > 0 {
		addrOffsets := make([]flatbuffers.UOffsetT, len(addresses))
		for i, addr := range addresses {
			addrOffsets[i] = builder.CreateString(addr)
		}
		EPM.EPMStartMULTIFORMAT_ADDRESSVector(builder, len(addrOffsets))
		for i := len(addrOffsets) - 1; i >= 0; i-- {
			builder.PrependUOffsetT(addrOffsets[i])
		}
		multiAddrOff = builder.EndVector(len(addrOffsets))
	}

	var signatureOff flatbuffers.UOffsetT
	if signatureHex != "" {
		signatureOff = builder.CreateString(signatureHex)
	}

	EPM.EPMStart(builder)
	EPM.EPMAddENTITY_TYPE(builder, EPM.EntityTypeNode)
	EPM.EPMAddDN(builder, dnOff)
	EPM.EPMAddKEYS(builder, keysOff)
	if multiAddrOff != 0 {
		EPM.EPMAddMULTIFORMAT_ADDRESS(builder, multiAddrOff)
	}
	if signatureOff != 0 {
		EPM.EPMAddSIGNATURE(builder, signatureOff)
	}
	EPM.EPMAddSIGNATURE_TIMESTAMP(builder, signatureTimestamp)
	epmOff := EPM.EPMEnd(builder)

	EPM.FinishSizePrefixedEPMBuffer(builder, epmOff)

	result := make([]byte, len(builder.FinishedBytes()))
	copy(result, builder.FinishedBytes())
	return result, nil
}

// normalizeAddresses trims, drops empties, and de-duplicates while preserving
// first-seen order.
func normalizeAddresses(in []string) []string {
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, a := range in {
		a = strings.TrimSpace(a)
		if a == "" {
			continue
		}
		if _, ok := seen[a]; ok {
			continue
		}
		seen[a] = struct{}{}
		out = append(out, a)
	}
	return out
}

// EPMToJSON projects a signed node EPM into a stable JSON-friendly map for the
// GET /sdn/v1/node/epm?format=json response. Keys mirror the directory-record
// shape used elsewhere in the stack; SDS field names inside `keys` keep their
// canonical lowercase projection.
func EPMToJSON(epmBytes []byte) (map[string]any, error) {
	if len(epmBytes) == 0 {
		return nil, ErrEmptyEPMData
	}
	if !EPM.SizePrefixedEPMBufferHasIdentifier(epmBytes) {
		return nil, ErrInvalidEPMData
	}
	rec := EPM.GetSizePrefixedRootAsEPM(epmBytes, 0)

	out := map[string]any{
		"entity_type": rec.ENTITY_TYPE().String(),
		"epm_base64":  base64.StdEncoding.EncodeToString(epmBytes),
	}
	if dn := strings.TrimSpace(string(rec.DN())); dn != "" {
		out["dn"] = dn
		out["peer_id"] = dn
	}

	key := new(EPM.CryptoKey)
	if n := rec.KEYSLength(); n > 0 {
		keys := make([]map[string]any, 0, n)
		for i := 0; i < n; i++ {
			if !rec.KEYS(key, i) {
				continue
			}
			entry := map[string]any{}
			if v := strings.TrimSpace(string(key.PUBLIC_KEY())); v != "" {
				entry["public_key"] = v
			}
			if v := strings.TrimSpace(string(key.ADDRESS_TYPE())); v != "" {
				entry["address_type"] = v
			}
			if v := strings.TrimSpace(string(key.KEY_ADDRESS())); v != "" {
				entry["key_address"] = v
			}
			switch key.KEY_TYPE() {
			case EPM.KeyTypeSigning:
				entry["key_type"] = "signing"
			case EPM.KeyTypeEncryption:
				entry["key_type"] = "encryption"
			}
			if len(entry) > 0 {
				keys = append(keys, entry)
			}
		}
		if len(keys) > 0 {
			out["keys"] = keys
		}
	}

	if n := rec.MULTIFORMAT_ADDRESSLength(); n > 0 {
		addrs := make([]string, 0, n)
		for i := 0; i < n; i++ {
			if v := strings.TrimSpace(string(rec.MULTIFORMAT_ADDRESS(i))); v != "" {
				addrs = append(addrs, v)
			}
		}
		if len(addrs) > 0 {
			out["multiformat_address"] = addrs
		}
	}

	if sig := strings.TrimSpace(string(rec.SIGNATURE())); sig != "" {
		out["signature"] = sig
	}
	if ts := rec.SIGNATURE_TIMESTAMP(); ts != 0 {
		out["signature_timestamp"] = ts
	}
	return out, nil
}
