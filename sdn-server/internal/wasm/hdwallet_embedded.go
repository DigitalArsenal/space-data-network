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
// Pin: hd-wallet-wasm 2.0.29, dist/hd-wallet-wasi.wasm, the module the local
// Mac nodes have run since their HD identities were made. An on-disk copy
// still wins (node.findHDWalletWasmPath); this is the last fallback.
//
//go:embed hd-wallet-wasi.wasm
var hdWalletWasiWasm []byte

const (
	// HDWalletWasiPackage is the npm package the embedded module comes from.
	HDWalletWasiPackage = "hd-wallet-wasm@2.0.29"
	// HDWalletWasiSHA256 is the sha256 of the embedded hd-wallet-wasi.wasm.
	HDWalletWasiSHA256 = "3e2604cb1f38c78a5c3549b7fb3f852151a797a4b5fcbcd42445f552956c62b4"
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
