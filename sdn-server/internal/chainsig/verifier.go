// Package chainsig verifies chain-native signatures shared by SDN identity
// surfaces.
package chainsig

import (
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	"github.com/decred/dcrd/dcrec/secp256k1/v4/ecdsa"
	"golang.org/x/crypto/sha3"
)

const compactSignatureLength = 65

// SignatureEncoding identifies the byte order of a recoverable ECDSA
// signature.  It is deliberately explicit because a wallet RSV signature can
// also be accepted by compact-signature recovery as a different, valid-looking
// VRS signature.
type SignatureEncoding string

const (
	// SignatureEncodingWalletRSV is the format returned by injected Ethereum
	// providers: 32-byte R, 32-byte S, then V in {0, 1, 27, 28}.
	SignatureEncodingWalletRSV SignatureEncoding = "wallet-rsv"
	// SignatureEncodingCompactVRS is dcrd compact format: recovery header,
	// 32-byte R, then 32-byte S. All dcrd recovery headers 27 through 34 are
	// accepted so existing compact producers retain their prior behavior.
	SignatureEncodingCompactVRS SignatureEncoding = "compact-vrs"
)

var (
	// ErrInvalidSignatureLength indicates that a signature is not exactly 65
	// bytes.
	ErrInvalidSignatureLength = errors.New("invalid signature length")
	// ErrInvalidSignatureEncoding indicates an unsupported signature byte
	// order.
	ErrInvalidSignatureEncoding = errors.New("invalid signature encoding")
	// ErrInvalidRecoveryID indicates a V/header value outside the accepted
	// Ethereum recovery IDs.
	ErrInvalidRecoveryID = errors.New("invalid recovery id")
	// ErrInvalidSignature indicates malformed R or S scalar material.
	ErrInvalidSignature = errors.New("invalid signature")
	// ErrHighS indicates a malleable signature whose S scalar exceeds half the
	// secp256k1 group order.
	ErrHighS = errors.New("high-S signature")
	// ErrPointAtInfinity indicates that recovery produced the identity element
	// instead of a public key.
	ErrPointAtInfinity = errors.New("signature recovers to point at infinity")
)

// RecoveredIdentity is the identity computed from an EIP-191 personal_sign
// signature.
type RecoveredIdentity struct {
	CompressedPublicKey []byte
	EthereumAddress     string
}

// PersonalSignHash returns keccak256("\x19Ethereum Signed Message:\n" +
// decimal(len(message)) + message), as required by EIP-191 personal_sign.
func PersonalSignHash(message []byte) []byte {
	prefix := "\x19Ethereum Signed Message:\n" + strconv.Itoa(len(message))
	h := sha3.NewLegacyKeccak256()
	_, _ = h.Write([]byte(prefix))
	_, _ = h.Write(message)
	return h.Sum(nil)
}

// VerifyPersonalSign normalizes a 65-byte recoverable signature, enforces
// low-S canonical form, recovers its compressed secp256k1 public key, and
// derives the EIP-55 Ethereum address. The encoding is mandatory so RSV bytes
// are never silently consumed as VRS bytes.
func VerifyPersonalSign(message, signature []byte, encoding SignatureEncoding) (RecoveredIdentity, error) {
	compact, err := normalizeSignature(signature, encoding)
	if err != nil {
		return RecoveredIdentity{}, err
	}

	var s secp256k1.ModNScalar
	if overflow := s.SetByteSlice(compact[33:65]); overflow || s.IsZero() {
		return RecoveredIdentity{}, fmt.Errorf("%w: S scalar is zero or out of range", ErrInvalidSignature)
	}
	if s.IsOverHalfOrder() {
		return RecoveredIdentity{}, ErrHighS
	}

	recovered, _, err := ecdsa.RecoverCompact(compact, PersonalSignHash(message))
	if err != nil {
		if errors.Is(err, ecdsa.ErrPointNotOnCurve) && strings.Contains(err.Error(), "point at infinity") {
			return RecoveredIdentity{}, fmt.Errorf("%w: %v", ErrPointAtInfinity, err)
		}
		return RecoveredIdentity{}, fmt.Errorf("%w: %v", ErrInvalidSignature, err)
	}

	compressed := recovered.SerializeCompressed()
	return RecoveredIdentity{
		CompressedPublicKey: compressed,
		EthereumAddress:     ethereumAddress(recovered),
	}, nil
}

func normalizeSignature(signature []byte, encoding SignatureEncoding) ([]byte, error) {
	if len(signature) != compactSignatureLength {
		return nil, fmt.Errorf("%w: got %d bytes, want %d", ErrInvalidSignatureLength, len(signature), compactSignatureLength)
	}

	compact := make([]byte, compactSignatureLength)
	var recoveryID byte
	switch encoding {
	case SignatureEncodingWalletRSV:
		var ok bool
		recoveryID, ok = walletRecoveryID(signature[64])
		if !ok {
			return nil, fmt.Errorf("%w: wallet V=%d (want 0, 1, 27, or 28)", ErrInvalidRecoveryID, signature[64])
		}
		copy(compact[1:], signature[:64])
	case SignatureEncodingCompactVRS:
		var ok bool
		recoveryID, ok = compactRecoveryID(signature[0])
		if !ok {
			return nil, fmt.Errorf("%w: compact V=%d (want a dcrd header from 27 through 34)", ErrInvalidRecoveryID, signature[0])
		}
		copy(compact[1:], signature[1:])
	default:
		return nil, fmt.Errorf("%w: %q", ErrInvalidSignatureEncoding, encoding)
	}

	// Always request compressed serialization from dcrd. The recovery ID is
	// unchanged; only the returned serialization preference is canonicalized.
	compact[0] = 31 + recoveryID
	return compact, nil
}

func walletRecoveryID(v byte) (byte, bool) {
	switch v {
	case 0, 27:
		return 0, true
	case 1, 28:
		return 1, true
	default:
		return 0, false
	}
}

func compactRecoveryID(v byte) (byte, bool) {
	if v < 27 || v > 34 {
		return 0, false
	}
	return (v - 27) & 3, true
}

func ethereumAddress(publicKey *secp256k1.PublicKey) string {
	uncompressed := publicKey.SerializeUncompressed()
	h := sha3.NewLegacyKeccak256()
	_, _ = h.Write(uncompressed[1:])
	digest := h.Sum(nil)
	return eip55Checksum(hex.EncodeToString(digest[12:]))
}

func eip55Checksum(lowerHexAddress string) string {
	h := sha3.NewLegacyKeccak256()
	_, _ = h.Write([]byte(lowerHexAddress))
	digest := h.Sum(nil)

	var result strings.Builder
	result.Grow(42)
	result.WriteString("0x")
	for i, char := range lowerHexAddress {
		if char >= '0' && char <= '9' {
			result.WriteByte(byte(char))
			continue
		}
		nibble := digest[i/2] & 0x0f
		if i%2 == 0 {
			nibble = digest[i/2] >> 4
		}
		if nibble >= 8 {
			result.WriteByte(byte(char - ('a' - 'A')))
		} else {
			result.WriteByte(byte(char))
		}
	}
	return result.String()
}
