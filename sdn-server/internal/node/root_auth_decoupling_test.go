package node

// THE §18 TRAP LOCK.
//
// §18 makes the EPM's SIGNING PATH and ENCRYPTION PATH operator-editable, with
// a GEN KEY button that rotates them. The obvious-looking "improvement" that
// would follow — make §14 root-admin recognition derive at whatever path the
// EPM currently declares, so the node "keeps up" with rotation — is a
// SELF-LOCKOUT BUG, and this file exists to stop anyone landing it.
//
// # Why following the active EPM path locks the owner out
//
// The key the node must recognise at sign-in is the key THE WALLET PRESENTS,
// and the wallet derives it at a path fixed by its own identity scheme:
//
//	hd-wallet-ui legacy schemes : m/44'/0'/<account>'/0/0   (bip32-scalar)
//	hd-wallet-ui modern / v2    : m/44'/0'/<account>'/0'/0' (SLIP-10)
//
// Those paths are hardcoded IN THE WALLET. The wallet never reads this node's
// EPM and has no idea the operator rotated anything. So if root recognition
// followed the rotated EPM path, the node would start looking for a key at
// m/44'/0'/0'/0'/1' while the wallet keeps presenting m/44'/0'/0'/0'/0' — no
// match, opaque 403, and the operator holding the node's own root mnemonic is
// locked out of their own console by pressing a button in that console.
//
// The EPM signing key and the auth key are DIFFERENT KEYS FOR DIFFERENT
// PURPOSES: one signs records, one proves possession at sign-in. Rotating the
// record-signing key must not touch the sign-in key, and §14's derivation is
// therefore pinned to the wallet-determined constants — never to editable
// record state.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"

	"github.com/spacedatanetwork/sdn-server/internal/wasm"
)

// TestRootAuthPathsAreWalletDeterminedConstants locks the two path templates
// root recognition derives at. They must match what hd-wallet-ui derives, so
// changing either is a deliberate, reviewable act rather than a side effect of
// someone editing EPM behaviour.
func TestRootAuthPathsAreWalletDeterminedConstants(t *testing.T) {
	t.Parallel()

	if wasm.SigningKeyPath != "m/44'/0'/%d'/0'/0'" {
		t.Fatalf("SigningKeyPath = %q; root recognition must derive where the MODERN wallet identity signs", wasm.SigningKeyPath)
	}
	if wasm.LegacyAuthKeyPath != "m/44'/0'/%d'/0/0" {
		t.Fatalf("LegacyAuthKeyPath = %q; root recognition must derive where the LEGACY wallet identity signs", wasm.LegacyAuthKeyPath)
	}
	if wasm.SigningKeyPath == wasm.LegacyAuthKeyPath {
		t.Fatal("the two wallet auth paths collapsed to one; §14 registers BOTH because the wallet's identity scheme decides which signs")
	}
}

// TestRootAuthDerivationDoesNotReadTheEPM is the structural half of the lock.
//
// Root recognition must be a pure function of (node seed, account) and the
// constants above. If the file that derives it ever imports the EPM package, it
// has gained the ability to follow operator-editable record state — which is
// exactly the self-lockout described at the top of this file. Failing here does
// not mean "add an exception": it means the derivation is about to become
// rotatable, and that is the bug.
func TestRootAuthDerivationDoesNotReadTheEPM(t *testing.T) {
	t.Parallel()

	const file = "identity_bundle.go"
	fset := token.NewFileSet()
	parsed, err := parser.ParseFile(fset, file, nil, parser.ImportsOnly)
	if err != nil {
		t.Fatalf("parse %s: %v", file, err)
	}

	for _, imp := range parsed.Imports {
		path := strings.Trim(imp.Path.Value, `"`)
		if strings.HasSuffix(path, "/internal/epm") {
			t.Fatalf(
				"%s imports %s.\n\n"+
					"Root-admin recognition (§14) must NOT be able to read EPM state. The EPM's\n"+
					"signing/encryption paths are operator-editable and rotatable (§18); the key the\n"+
					"node must recognise at sign-in is the one the WALLET presents, at a path the\n"+
					"wallet hardcodes. Deriving root keys from the record instead would let an\n"+
					"operator lock themselves out of their own node by pressing GEN KEY.",
				file, path)
		}
	}
}

// TestRootAuthPublicKeysIsSeedDerivedOnly documents the remaining half of the
// invariant in executable form: the exported entry point takes no EPM input and
// no path argument, so a caller cannot inject a rotated path even by accident.
func TestRootAuthPublicKeysIsSeedDerivedOnly(t *testing.T) {
	t.Parallel()

	const file = "identity_bundle.go"
	fset := token.NewFileSet()
	parsed, err := parser.ParseFile(fset, file, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", file, err)
	}

	var found *ast.FuncDecl
	ast.Inspect(parsed, func(n ast.Node) bool {
		fn, ok := n.(*ast.FuncDecl)
		if ok && fn.Name.Name == "RootAuthPublicKeys" {
			found = fn
			return false
		}
		return true
	})
	if found == nil {
		t.Fatal("RootAuthPublicKeys not found; §14 root sign-in derives through it")
	}
	if found.Type.Params != nil && len(found.Type.Params.List) != 0 {
		t.Fatalf("RootAuthPublicKeys gained %d parameter(s); it must derive from the node's own seed alone, so no rotated path can be passed in", len(found.Type.Params.List))
	}
}
