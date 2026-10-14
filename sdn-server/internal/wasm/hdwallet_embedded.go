package wasm

import (
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
)

// The WASI HD-wallet module built into the binary. It turns a node's mnemonic
// into its peer identity and its sealed-transport key, so a node that cannot
// find a copy on disk must still have one: the darwin cli-bundle carries only
// the binary, and a Mac node restarted without HD_WALLET_WASM_PATH came up on
// its legacy key with no sealed-transport key (owner 2026-10-09: "it shouldn't
// lose the encryption key, even for a mac").
//
// Pin: hd-wallet-wasm 2.1.0, dist/hd-wallet-wasi.wasm, byte for byte the
// module hd-wallet's own release build published to npm (gitHead 62e752d,
// tag v2.1.0). 2.1.0 signs every ECDSA signature over one hash; the identity
// derivation this module serves the node is unchanged from 2.0.29, which the
// local Mac nodes' HD identities were made with. An on-disk copy still wins
// (node.findHDWalletWasmPath); this is the last fallback.
//
//go:embed hd-wallet-wasi.wasm
var hdWalletWasiWasm []byte

const (
	// HDWalletWasiPackage is the npm package the embedded module comes from.
	HDWalletWasiPackage = "hd-wallet-wasm@2.1.0"
	// HDWalletWasiSHA256 is the sha256 of the embedded hd-wallet-wasi.wasm.
	HDWalletWasiSHA256 = "25d00466a8251d46099962536fadb3d8fcb7a62e2b24a7f9468b27a0a2a31920"
)

// EmbeddedHDWalletWasm returns the embedded module's bytes, or nil when they do
// not hash to HDWalletWasiSHA256.
func EmbeddedHDWalletWasm() []byte {
	sum := sha256.Sum256(hdWalletWasiWasm)
	if hex.EncodeToString(sum[:]) != HDWalletWasiSHA256 {
		return nil
	}
	return hdWalletWasiWasm
}
