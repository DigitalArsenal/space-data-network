package epm

// EPM ChainProof verification (security audit SEC-013).
//
// An EPM's CHAIN_PROOFS claim that chain addresses (Bitcoin, Ethereum, Solana)
// belong to the same HD wallet as the record's signing key. The EPM
// self-signature proves nothing about those addresses: VerifyEPMSignature
// accepts a signature from any signing key the record itself carries, so a
// self-signed record can name any address it likes (an exchange cold wallet,
// say) and, before this file, the trust engine credited that address's
// balance to the record's peer as a security bond.
//
// A chain proof is honoured here only when ALL of these hold:
//
//  1. The EPM's own signature verifies, and the key that produced it is known.
//  2. SIGNED_PAYLOAD is present and is a version-1 identity attestation
//     payload (the Go node's identityAttestationPayload, or the hd-wallet
//     buildCanonicalPayload form; both are JSON objects).
//  3. The payload's signing_pubkey_hex is the key that signed this EPM. A
//     valid proof copied out of another record names that record's signing
//     key, which the copier cannot sign with, so it is refused.
//  4. When the caller knows the peer the record speaks for, the payload's
//     identity_pubkey_hex derives exactly that peer ID.
//  5. issued_at is set and not in the future (beyond a small clock skew).
//  6. The chain signature verifies over SIGNED_PAYLOAD with the proof's
//     PUBLIC_KEY, per the chain's algorithm and encoding, and ADDRESS is
//     recomputed from that key (P2WPKH, Ethereum address, base58 Ed25519).
//     When the payload names an address for the chain it must be ADDRESS.
//
// Anything else is dropped: an unverified proof never reaches a directory
// projection, a bond lookup or a provenance rung.

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/EPM"
	libp2pcrypto "github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/peer"
)

// ChainProofMaxFutureSkew is how far in the future a chain proof's issued_at
// may sit before the proof is refused.
const ChainProofMaxFutureSkew = 5 * time.Minute

// ErrChainProofUnverified wraps every reason a chain proof is refused.
var ErrChainProofUnverified = errors.New("chain proof not verified")

// VerifiedChainProof is a chain address whose ChainProof verified against the
// EPM that carries it.
type VerifiedChainProof struct {
	// Index is the proof's position in the record's CHAIN_PROOFS vector.
	Index     int
	Chain     string
	Address   string
	PublicKey string
	KeyPath   string
}

// chainProofPayload is the subset of the attested payload the verifier binds.
// Both producers (the Go node and the hd-wallet) sign a JSON object carrying
// these names; fields a producer omits decode as empty.
type chainProofPayload struct {
	Version           string `json:"version"`
	XPub              string `json:"xpub"`
	IdentityPubKeyHex string `json:"identity_pubkey_hex"`
	SigningPubKeyHex  string `json:"signing_pubkey_hex"`
	IssuedAt          int64  `json:"issued_at"`
	BitcoinAddress    string `json:"bitcoin_address"`
	EthereumAddress   string `json:"ethereum_address"`
	SolanaAddress     string `json:"solana_address"`
}

// VerifiedEPMChainProofs verifies the EPM's own signature and returns the chain
// proofs that verify against it. expectedPeerID, when non-empty, is the peer
// the record must speak for (the peer that served it, or the directory row's
// peer); every honoured proof's identity key must derive that peer ID.
//
// The error is non-nil only when the EPM itself does not verify, in which case
// no proof is honoured. Refused proofs are reported in rejected, one error per
// proof, so callers can log why an address was dropped.
func VerifiedEPMChainProofs(epmData []byte, expectedPeerID string, now time.Time) (verified []VerifiedChainProof, rejected []error, err error) {
	record, signerHex, err := verifiedEPMSigner(epmData)
	if err != nil {
		return nil, nil, err
	}
	proof := new(EPM.ChainProof)
	for i := 0; i < record.CHAIN_PROOFSLength(); i++ {
		if !record.CHAIN_PROOFS(proof, i) {
			continue
		}
		vp, verr := verifyEPMChainProof(proof, signerHex, strings.TrimSpace(expectedPeerID), now)
		if verr != nil {
			rejected = append(rejected, fmt.Errorf("chain proof %d: %w", i, verr))
			continue
		}
		vp.Index = i
		verified = append(verified, vp)
	}
	return verified, rejected, nil
}

// verifiedEPMSigner verifies the EPM signature and returns the record and the
// lowercase hex of the signing key that produced it.
func verifiedEPMSigner(epmData []byte) (*EPM.EPM, string, error) {
	record, signature, payload, err := verifyEPMSignaturePrep(epmData)
	if err != nil {
		return nil, "", err
	}
	key := new(EPM.CryptoKey)
	for i := 0; i < record.KEYSLength(); i++ {
		if !record.KEYS(key, i) || key.KEY_TYPE() != EPM.KeyTypeSigning {
			continue
		}
		pub, derr := decodeHexString(string(key.PUBLIC_KEY()))
		if derr != nil {
			continue
		}
		if verifySignatureForKey(pub, string(key.ALGORITHM()), payload, signature) {
			return record, hex.EncodeToString(pub), nil
		}
	}
	return nil, "", ErrInvalidEPMSignature
}

func verifyEPMChainProof(proof *EPM.ChainProof, signerHex, expectedPeerID string, now time.Time) (VerifiedChainProof, error) {
	fail := func(format string, args ...any) (VerifiedChainProof, error) {
		return VerifiedChainProof{}, fmt.Errorf("%w: %s", ErrChainProofUnverified, fmt.Sprintf(format, args...))
	}

	chain := strings.ToLower(strings.TrimSpace(string(proof.CHAIN())))
	switch chain {
	case identityChainBitcoin, identityChainEthereum, identityChainSolana:
	default:
		return fail("unsupported chain %q", chain)
	}

	payloadHex := strings.TrimSpace(string(proof.SIGNED_PAYLOAD()))
	if payloadHex == "" {
		return fail("%s proof carries no signed payload", chain)
	}
	payload, err := hex.DecodeString(payloadHex)
	if err != nil || len(payload) == 0 {
		return fail("%s proof signed payload is not hex", chain)
	}
	var claims chainProofPayload
	decoder := json.NewDecoder(bytes.NewReader(payload))
	if err := decoder.Decode(&claims); err != nil {
		return fail("%s proof signed payload is not an attestation: %v", chain, err)
	}
	if decoder.More() {
		return fail("%s proof signed payload has trailing data", chain)
	}
	if claims.Version != identityAttestationVersion {
		return fail("%s proof attestation version %q is not %q", chain, claims.Version, identityAttestationVersion)
	}

	// Binding to THIS record: the attestation must name the key that signed
	// the EPM. A proof lifted from another record names that record's key.
	if signerHex == "" || !strings.EqualFold(strings.TrimSpace(claims.SigningPubKeyHex), signerHex) {
		return fail("%s proof is bound to signing key %q, not this record's signer", chain, claims.SigningPubKeyHex)
	}

	// Binding to the peer the record speaks for.
	if expectedPeerID != "" {
		attested, err := peerIDFromIdentityKeyHex(claims.IdentityPubKeyHex)
		if err != nil {
			return fail("%s proof names no usable identity key: %v", chain, err)
		}
		if attested.String() != expectedPeerID {
			return fail("%s proof is bound to peer %s, not %s", chain, attested, expectedPeerID)
		}
	}

	if claims.IssuedAt <= 0 {
		return fail("%s proof has no issued_at", chain)
	}
	if time.Unix(claims.IssuedAt, 0).After(now.Add(ChainProofMaxFutureSkew)) {
		return fail("%s proof issued_at %d is in the future", chain, claims.IssuedAt)
	}

	candidate := IdentityAttestationChainProof{
		Chain:              chain,
		Address:            strings.TrimSpace(string(proof.ADDRESS())),
		KeyPath:            strings.TrimSpace(string(proof.KEY_PATH())),
		PublicKeyHex:       strings.ToLower(strings.TrimSpace(string(proof.PUBLIC_KEY()))),
		Signature:          strings.ToLower(strings.TrimSpace(string(proof.SIGNATURE()))),
		SignedPayloadHex:   hex.EncodeToString(payload),
		SignatureAlgorithm: strings.TrimSpace(string(proof.ALGORITHM())),
		SignatureEncoding:  strings.TrimSpace(string(proof.ENCODING())),
	}
	if candidate.Address == "" {
		return fail("%s proof has no address", chain)
	}

	// Each per-chain verifier recovers or checks the key against the
	// signature, then recomputes the address from that key and compares it
	// with ADDRESS. expectedAddress additionally pins the payload's own claim.
	switch chain {
	case identityChainBitcoin:
		err = verifyBitcoinChainProof(payload, candidate, claims.BitcoinAddress)
	case identityChainEthereum:
		err = verifyEthereumChainProof(payload, candidate, claims.EthereumAddress)
	case identityChainSolana:
		err = verifySolanaChainProof(payload, candidate, claims.SolanaAddress)
	}
	if err != nil {
		return fail("%v", err)
	}
	return VerifiedChainProof{
		Chain:     chain,
		Address:   candidate.Address,
		PublicKey: candidate.PublicKeyHex,
		KeyPath:   candidate.KeyPath,
	}, nil
}

// peerIDFromIdentityKeyHex derives the libp2p peer ID of a raw identity public
// key: 33-byte compressed secp256k1 (the node's HD identity) or 32-byte
// Ed25519.
func peerIDFromIdentityKeyHex(identityHex string) (peer.ID, error) {
	raw, err := decodeHexString(identityHex)
	if err != nil || len(raw) == 0 {
		return "", errors.New("identity_pubkey_hex is missing or not hex")
	}
	var pub libp2pcrypto.PubKey
	switch len(raw) {
	case 33:
		pub, err = libp2pcrypto.UnmarshalSecp256k1PublicKey(raw)
	case 32:
		pub, err = libp2pcrypto.UnmarshalEd25519PublicKey(raw)
	default:
		return "", fmt.Errorf("identity key of %d bytes is neither secp256k1 nor ed25519", len(raw))
	}
	if err != nil {
		return "", err
	}
	return peer.IDFromPublicKey(pub)
}

// ApplyVerifiedChainProofs projects the EPM's chain proofs into a directory
// JSON row (info), keeping ONLY the proofs that verify against the record
// and peerID: the chain_proofs entries and the flattened <chain>_address and
// <chain>_key_path fields that directory search and the trust engine read.
// An unverified proof leaves no trace in the row. It returns the reasons
// proofs were refused (and the record error, when the EPM itself does not
// verify) so the caller can log them.
func ApplyVerifiedChainProofs(info map[string]any, epmBytes []byte, peerID string, now time.Time) []error {
	verified, rejected, err := VerifiedEPMChainProofs(epmBytes, peerID, now)
	if err != nil {
		return []error{err}
	}
	if len(verified) == 0 {
		return rejected
	}
	record := EPM.GetSizePrefixedRootAsEPM(epmBytes, 0)
	proof := new(EPM.ChainProof)
	proofs := make([]map[string]any, 0, len(verified))
	for _, vp := range verified {
		if !record.CHAIN_PROOFS(proof, vp.Index) {
			continue
		}
		entry := map[string]any{
			"chain":   vp.Chain,
			"address": vp.Address,
		}
		addLowerString(entry, "public_key", proof.PUBLIC_KEY())
		addLowerString(entry, "key_path", proof.KEY_PATH())
		addLowerString(entry, "signature", proof.SIGNATURE())
		addLowerString(entry, "signed_payload", proof.SIGNED_PAYLOAD())
		addLowerString(entry, "algorithm", proof.ALGORITHM())
		addLowerString(entry, "encoding", proof.ENCODING())
		proofs = append(proofs, entry)
		if _, taken := info[vp.Chain+"_address"]; !taken {
			info[vp.Chain+"_address"] = vp.Address
			if vp.KeyPath != "" {
				info[vp.Chain+"_key_path"] = vp.KeyPath
			}
		}
	}
	if len(proofs) > 0 {
		info["chain_proofs"] = proofs
	}
	return rejected
}
