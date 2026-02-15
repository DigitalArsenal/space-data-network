package wasm

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"fmt"
	"strings"

	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	"github.com/mr-tron/base58"
	"golang.org/x/crypto/ripemd160"
	"golang.org/x/crypto/sha3"
)

// CoinAddress holds a derived blockchain address with its derivation path.
type CoinAddress struct {
	Address string `json:"address"`
	Path    string `json:"path"`
}

// CoinAddresses holds derived standard blockchain addresses for display.
type CoinAddresses struct {
	Bitcoin  *CoinAddress `json:"bitcoin,omitempty"`
	Ethereum *CoinAddress `json:"ethereum,omitempty"`
	Solana   *CoinAddress `json:"solana,omitempty"`
}

// Standard BIP-44/84 derivation paths (account 0, external chain, index 0)
const (
	BitcoinDerivePath  = "m/84'/0'/0'/0/0"  // BIP-84 Native SegWit
	EthereumDerivePath = "m/44'/60'/0'/0/0"  // BIP-44 Ethereum
	SolanaDerivePath   = "m/44'/501'/0'/0'"  // BIP-44 Solana (all hardened for Ed25519)
)

// Curve constants matching the WASM enum (types.h)
const (
	CurveSecp256k1 = 0
	CurveEd25519   = 1
)

// DeriveCoinAddresses derives Bitcoin, Ethereum, and Solana addresses from a 64-byte seed.
// Uses WASM for all key derivation; pure Go only for address encoding.
// Failures for individual coins are non-fatal; unavailable addresses are left nil.
func (hw *HDWalletModule) DeriveCoinAddresses(ctx context.Context, seed []byte) (*CoinAddresses, error) {
	if len(seed) != 64 {
		return nil, ErrHDWalletInvalidSeed
	}

	addrs := &CoinAddresses{}

	if addr, err := hw.deriveBitcoinAddress(ctx, seed); err == nil {
		addrs.Bitcoin = addr
	}
	if addr, err := hw.deriveEthereumAddress(ctx, seed); err == nil {
		addrs.Ethereum = addr
	}
	if addr, err := hw.deriveSolanaAddress(ctx, seed); err == nil {
		addrs.Solana = addr
	}

	return addrs, nil
}

// ---------------------------------------------------------------------------
// BIP-32 secp256k1 key derivation (via WASM)
// ---------------------------------------------------------------------------

// deriveSecp256k1PubKey derives a compressed secp256k1 public key at the given BIP-32 path.
func (hw *HDWalletModule) deriveSecp256k1PubKey(ctx context.Context, seed []byte, path string) ([]byte, error) {
	pubkey, err := hw.deriveSecp256k1PubKeyWASM(ctx, seed, path)
	if err != nil {
		return nil, err
	}
	if len(pubkey) != 33 {
		return nil, fmt.Errorf("expected 33-byte pubkey, got %d", len(pubkey))
	}
	if _, parseErr := secp256k1.ParsePubKey(pubkey); parseErr != nil {
		return nil, fmt.Errorf("invalid secp256k1 pubkey: %w", parseErr)
	}
	return pubkey, nil
}

// deriveSecp256k1PubKeyWASM derives via WASM handle API.
func (hw *HDWalletModule) deriveSecp256k1PubKeyWASM(ctx context.Context, seed []byte, path string) ([]byte, error) {
	hw.mu.Lock()
	defer hw.mu.Unlock()

	if hw.keyFromSeed == nil || hw.keyDerivePath == nil || hw.keyGetPublic == nil || hw.keyDestroy == nil {
		return nil, ErrHDWalletNoModule
	}

	ctx, cancel := context.WithTimeout(ctx, wasmCallTimeout)
	defer cancel()

	seedPtr, err := hw.allocate(ctx, seed)
	if err != nil {
		return nil, err
	}
	defer hw.deallocate(ctx, seedPtr, uint32(len(seed)))

	results, err := hw.keyFromSeed.Call(ctx,
		uint64(seedPtr), uint64(len(seed)),
		uint64(CurveSecp256k1),
	)
	if err != nil {
		return nil, fmt.Errorf("key_from_seed failed: %w", err)
	}
	masterHandle := results[0]
	if masterHandle == 0 {
		return nil, fmt.Errorf("key_from_seed returned null handle")
	}
	defer hw.keyDestroy.Call(ctx, masterHandle)

	pathPtr, err := hw.allocateString(ctx, path)
	if err != nil {
		return nil, err
	}
	defer hw.deallocate(ctx, pathPtr, uint32(len(path)+1))

	results, err = hw.keyDerivePath.Call(ctx, masterHandle, uint64(pathPtr))
	if err != nil {
		return nil, fmt.Errorf("key_derive_path failed: %w", err)
	}
	derivedHandle := results[0]
	if derivedHandle == 0 {
		return nil, fmt.Errorf("key_derive_path returned null handle for path %s", path)
	}
	defer hw.keyDestroy.Call(ctx, derivedHandle)

	pubSize := uint32(33)
	pubPtr, err := hw.allocateSize(ctx, pubSize)
	if err != nil {
		return nil, err
	}
	defer hw.deallocate(ctx, pubPtr, pubSize)

	results, err = hw.keyGetPublic.Call(ctx, derivedHandle, uint64(pubPtr), uint64(pubSize))
	if err != nil {
		return nil, fmt.Errorf("key_get_public failed: %w", err)
	}
	// Return value is an error code (0 = OK, negative = error), not byte count.
	errCode := int32(results[0])
	if errCode < 0 {
		return nil, fmt.Errorf("key_get_public error: %d", errCode)
	}

	return hw.readMemory(ctx, pubPtr, pubSize)
}

// ---------------------------------------------------------------------------
// Coin address derivation (WASM key derivation + pure Go encoding)
// ---------------------------------------------------------------------------

// deriveBitcoinAddress derives a P2WPKH (bc1q...) address at m/84'/0'/0'/0/0.
func (hw *HDWalletModule) deriveBitcoinAddress(ctx context.Context, seed []byte) (*CoinAddress, error) {
	pubkey, err := hw.deriveSecp256k1PubKey(ctx, seed, BitcoinDerivePath)
	if err != nil {
		return nil, err
	}

	addr, err := bitcoinP2WPKH(pubkey)
	if err != nil {
		return nil, err
	}

	return &CoinAddress{Address: addr, Path: BitcoinDerivePath}, nil
}

// deriveEthereumAddress derives a checksummed Ethereum address at m/44'/60'/0'/0/0.
func (hw *HDWalletModule) deriveEthereumAddress(ctx context.Context, seed []byte) (*CoinAddress, error) {
	pubkey, err := hw.deriveSecp256k1PubKey(ctx, seed, EthereumDerivePath)
	if err != nil {
		return nil, err
	}

	addr, err := ethereumAddress(pubkey)
	if err != nil {
		return nil, err
	}

	return &CoinAddress{Address: addr, Path: EthereumDerivePath}, nil
}

// deriveSolanaAddress derives a Solana address at m/44'/501'/0'/0' via WASM SLIP-10.
func (hw *HDWalletModule) deriveSolanaAddress(ctx context.Context, seed []byte) (*CoinAddress, error) {
	derived, err := hw.DeriveEd25519Key(ctx, seed, SolanaDerivePath)
	if err != nil {
		return nil, err
	}

	privKey := ed25519.NewKeyFromSeed(derived.PrivateKey)
	pubKey := privKey.Public().(ed25519.PublicKey)

	addr := base58.Encode(pubKey)
	return &CoinAddress{Address: addr, Path: SolanaDerivePath}, nil
}

// ---------------------------------------------------------------------------
// Pure Go address encoding
// ---------------------------------------------------------------------------

// bitcoinP2WPKH encodes a compressed secp256k1 public key as a P2WPKH bech32 address.
func bitcoinP2WPKH(compressedPubKey []byte) (string, error) {
	if len(compressedPubKey) != 33 {
		return "", fmt.Errorf("expected 33-byte compressed pubkey, got %d", len(compressedPubKey))
	}
	// Hash160 = RIPEMD160(SHA256(pubkey))
	s := sha256.Sum256(compressedPubKey)
	r := ripemd160.New()
	r.Write(s[:])
	hash160 := r.Sum(nil) // 20 bytes

	return bech32SegwitEncode("bc", 0, hash160)
}

// ethereumAddress encodes a compressed secp256k1 public key as an EIP-55 checksummed Ethereum address.
func ethereumAddress(compressedPubKey []byte) (string, error) {
	if len(compressedPubKey) != 33 {
		return "", fmt.Errorf("expected 33-byte compressed pubkey, got %d", len(compressedPubKey))
	}
	// Decompress to uncompressed point (65 bytes: 04 || x || y)
	pubKey, err := secp256k1.ParsePubKey(compressedPubKey)
	if err != nil {
		return "", fmt.Errorf("invalid secp256k1 pubkey: %w", err)
	}
	uncompressed := pubKey.SerializeUncompressed() // 65 bytes

	// Keccak256(x || y) — skip 04 prefix
	h := sha3.NewLegacyKeccak256()
	h.Write(uncompressed[1:]) // 64 bytes: x || y
	hash := h.Sum(nil)        // 32 bytes

	// Last 20 bytes = address
	addrBytes := hash[12:]
	addrHex := fmt.Sprintf("%x", addrBytes)

	// EIP-55 checksum
	return eip55Checksum(addrHex), nil
}

// eip55Checksum applies EIP-55 mixed-case checksum to a hex address.
func eip55Checksum(addrHex string) string {
	h := sha3.NewLegacyKeccak256()
	h.Write([]byte(addrHex))
	hash := h.Sum(nil)

	var result strings.Builder
	result.WriteString("0x")
	for i, c := range addrHex {
		if c >= '0' && c <= '9' {
			result.WriteByte(byte(c))
		} else {
			// Hash nibble at position i
			hashByte := hash[i/2]
			var nibble byte
			if i%2 == 0 {
				nibble = hashByte >> 4
			} else {
				nibble = hashByte & 0x0f
			}
			if nibble >= 8 {
				result.WriteByte(byte(c) - 32) // uppercase
			} else {
				result.WriteByte(byte(c))
			}
		}
	}
	return result.String()
}

// ---------------------------------------------------------------------------
// Bech32 segwit encoding (BIP-173/350)
// ---------------------------------------------------------------------------

const bech32Charset = "qpzry9x8gf2tvdw0s3jn54khce6mua7l"

func bech32SegwitEncode(hrp string, witnessVersion byte, program []byte) (string, error) {
	if len(program) < 2 || len(program) > 40 {
		return "", fmt.Errorf("invalid witness program length: %d", len(program))
	}
	// Convert program to 5-bit groups
	conv, err := bech32ConvertBits(program, 8, 5, true)
	if err != nil {
		return "", err
	}
	// Prepend witness version
	data := append([]byte{witnessVersion}, conv...)

	// Bech32 encoding (v0 uses bech32, v1+ uses bech32m)
	enc := bech32Encode(hrp, data, 1) // bech32 constant = 1
	return enc, nil
}

func bech32Encode(hrp string, data []byte, spec uint32) string {
	values := append(data, 0, 0, 0, 0, 0, 0)
	polymod := bech32Polymod(bech32HRPExpand(hrp), values) ^ spec
	var checksum [6]byte
	for i := 0; i < 6; i++ {
		checksum[i] = byte((polymod >> uint(5*(5-i))) & 31)
	}
	combined := append(data, checksum[:]...)
	var result strings.Builder
	result.WriteString(hrp)
	result.WriteByte('1')
	for _, b := range combined {
		result.WriteByte(bech32Charset[b])
	}
	return result.String()
}

func bech32HRPExpand(hrp string) []byte {
	ret := make([]byte, 0, len(hrp)*2+1)
	for _, c := range hrp {
		ret = append(ret, byte(c>>5))
	}
	ret = append(ret, 0)
	for _, c := range hrp {
		ret = append(ret, byte(c&31))
	}
	return ret
}

func bech32Polymod(hrp, values []byte) uint32 {
	gen := [5]uint32{0x3b6a57b2, 0x26508e6d, 0x1ea119fa, 0x3d4233dd, 0x2a1462b3}
	chk := uint32(1)
	for _, v := range hrp {
		b := chk >> 25
		chk = (chk&0x1ffffff)<<5 ^ uint32(v)
		for i := 0; i < 5; i++ {
			if (b>>uint(i))&1 == 1 {
				chk ^= gen[i]
			}
		}
	}
	for _, v := range values {
		b := chk >> 25
		chk = (chk&0x1ffffff)<<5 ^ uint32(v)
		for i := 0; i < 5; i++ {
			if (b>>uint(i))&1 == 1 {
				chk ^= gen[i]
			}
		}
	}
	return chk
}

func bech32ConvertBits(data []byte, fromBits, toBits uint, pad bool) ([]byte, error) {
	acc := uint32(0)
	bits := uint(0)
	maxv := uint32((1 << toBits) - 1)
	var ret []byte
	for _, b := range data {
		acc = (acc << fromBits) | uint32(b)
		bits += fromBits
		for bits >= toBits {
			bits -= toBits
			ret = append(ret, byte((acc>>bits)&maxv))
		}
	}
	if pad {
		if bits > 0 {
			ret = append(ret, byte((acc<<(toBits-bits))&maxv))
		}
	} else if bits >= fromBits || (acc<<(toBits-bits))&maxv != 0 {
		return nil, fmt.Errorf("invalid padding")
	}
	return ret, nil
}
