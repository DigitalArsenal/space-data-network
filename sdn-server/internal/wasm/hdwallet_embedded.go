package wasm

import (
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
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
// local Mac nodes' HD identities were made with. An on-disk copy found by
// node.findHDWalletWasmPath is used only when it is itself a pinned build
// (CheckHDWalletPin, SEC-040).
//
//go:embed hd-wallet-wasi.wasm
var hdWalletWasiWasm []byte

const (
	// HDWalletWasiPackage is the npm package the embedded module comes from.
	HDWalletWasiPackage = "hd-wallet-wasm@2.1.0"
	// HDWalletWasiSHA256 is the sha256 of the embedded hd-wallet-wasi.wasm.
	HDWalletWasiSHA256 = "25d00466a8251d46099962536fadb3d8fcb7a62e2b24a7f9468b27a0a2a31920"
)

// pinnedHDWalletWasiSHA256 is every hd-wallet-wasi.wasm build a node or CLI
// may run, by sha256 (SEC-040). This module turns the node mnemonic into its
// peer identity and sealed-transport key, so a copy found on disk (a project's
// node_modules, a sibling checkout, ~/.local/lib) runs only when it is
// byte-for-byte a pinned release. Add a release here, with its package name,
// when the embedded module moves to it.
var pinnedHDWalletWasiSHA256 = map[string]string{
	HDWalletWasiSHA256: HDWalletWasiPackage,
}

// AllowUnpinnedHDWalletEnv is the development-only escape hatch: set to "1",
// an unpinned hd-wallet module is run anyway, with an error logged each time.
// Never set it on a production node.
const AllowUnpinnedHDWalletEnv = "SDN_DEV_ALLOW_UNPINNED_HD_WALLET_WASM"

// ErrHDWalletUnpinned refuses an hd-wallet module that is not a pinned build.
var ErrHDWalletUnpinned = errors.New("hd-wallet WASM module is not a pinned release")

// CheckHDWalletPin returns nil when wasmBytes hash to a pinned hd-wallet
// release (or the development escape hatch is set), else ErrHDWalletUnpinned
// naming the hash.
func CheckHDWalletPin(wasmBytes []byte) error {
	sum := sha256.Sum256(wasmBytes)
	digest := hex.EncodeToString(sum[:])
	if _, ok := pinnedHDWalletWasiSHA256[digest]; ok {
		return nil
	}
	if os.Getenv(AllowUnpinnedHDWalletEnv) == "1" {
		fmt.Fprintf(os.Stderr, "SECURITY WARNING: running an UNPINNED hd-wallet WASM module (sha256 %s) because %s=1; never do this on a production node\n", digest, AllowUnpinnedHDWalletEnv)
		return nil
	}
	return fmt.Errorf("%w: sha256 %s (pinned: %s %s; set %s=1 only for local development)", ErrHDWalletUnpinned, digest, HDWalletWasiPackage, HDWalletWasiSHA256, AllowUnpinnedHDWalletEnv)
}

// EmbeddedHDWalletWasm returns the embedded module's bytes, or nil when they do
// not hash to HDWalletWasiSHA256.
func EmbeddedHDWalletWasm() []byte {
	sum := sha256.Sum256(hdWalletWasiWasm)
	if hex.EncodeToString(sum[:]) != HDWalletWasiSHA256 {
		return nil
	}
	return hdWalletWasiWasm
}
