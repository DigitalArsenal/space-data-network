package walletderive

// Vectors come from hd-wallet-wasm test/fixtures/sdn-wallet-vectors.v1.json
// (legacyIdentities). The recovery phrase is the public BIP-39 test phrase,
// assembled at runtime so no wordlist run sits in this file (the repository's
// mnemonic guard refuses one).

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"os"
	"strings"
	"testing"

	"github.com/spacedatanetwork/sdn-server/internal/wasm"
)

const (
	passwordVectorUser   = "fixture-legacy-user"
	passwordVectorPass   = "Fixture-Only-Legacy-Secret-0001!"
	passwordVectorSeed   = "ac83330e5389d0910c2684cc9840ff139f3d98b0a2b6092bc6a711dc349cc5a3dafef19a460d5c7d16788e8f06c1b79036bd23b68f7614cd17094a6a1ce204fe"
	passwordVectorSignIn = "019a0da9799107657f72d4ae3d5e483b51e7b46b0428f8695fc5412efdb178b4"

	mnemonicVectorSeed   = "5eb00bbddcf069084889a8ab9155568165f5c453ccb85e70811aaed6f6da5fc19a5ac40b389cd370d086206dec8aa6c43daea6690f20ad3d8d48b2d2ce9e38e4"
	mnemonicVectorSignIn = "3c35d187ea9428787cb3343d4a724fc961902012bbce5ce4f43369861e19127f"
)

func mnemonicVectorPhrase() string {
	return strings.Repeat("abandon ", 11) + "about"
}

func TestPasswordSeedMatchesTheWalletVector(t *testing.T) {
	seed, err := PasswordSeed([]byte(passwordVectorUser), []byte(passwordVectorPass))
	if err != nil {
		t.Fatal(err)
	}
	if got := hex.EncodeToString(seed); got != passwordVectorSeed {
		t.Fatalf("seed = %s, want %s", got, passwordVectorSeed)
	}
}

func TestPasswordSeedRefusesBadInput(t *testing.T) {
	long := make([]byte, maxSecretBytes+1)
	for i := range long {
		long[i] = 'a'
	}
	for name, in := range map[string][2][]byte{
		"empty username": {nil, []byte("p")},
		"empty password": {[]byte("u"), nil},
		"invalid utf-8":  {[]byte{0xff}, []byte("p")},
		"too long":       {[]byte("u"), long},
	} {
		if _, err := PasswordSeed(in[0], in[1]); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestNormalizeMnemonic(t *testing.T) {
	want := mnemonicVectorPhrase()
	messy := "  " + strings.ToUpper(strings.ReplaceAll(want, " ", " \t\n ")) + "\n"
	got, err := NormalizeMnemonic(messy)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("normalized = %q, want %q", got, want)
	}
	if _, err := NormalizeMnemonic("one two three"); err == nil {
		t.Error("accepted a 3-word phrase")
	}
	if _, err := NormalizeMnemonic(strings.Replace(want, "about", "abóut", 1)); err == nil {
		t.Error("accepted a non-ASCII phrase")
	}
}

func TestFromPublicKey(t *testing.T) {
	e, err := FromPublicKey("  " + strings.ToUpper(passwordVectorSignIn) + " ")
	if err != nil {
		t.Fatal(err)
	}
	if e.SigningPubKeyHex != passwordVectorSignIn || e.RowKey != PubKeyRowPrefix+passwordVectorSignIn || e.Source != SourcePublicKey {
		t.Fatalf("unexpected enrollment %+v", e)
	}
	for _, bad := range []string{"", "zz", passwordVectorSignIn[:62], passwordVectorSignIn + "00"} {
		if _, err := FromPublicKey(bad); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
}

func TestFingerprint(t *testing.T) {
	// SHA-256("") = e3b0c442 98fc1c14 ...
	if got := Fingerprint(nil); got != "e3b0:c442:98fc:1c14" {
		t.Fatalf("Fingerprint = %s", got)
	}
}

func TestSignInKeysMatchTheWalletVectors(t *testing.T) {
	w := testWallet(t)
	ctx := context.Background()

	pw, err := FromPassword(ctx, w, []byte(passwordVectorUser), []byte(passwordVectorPass))
	if err != nil {
		t.Fatal(err)
	}
	if pw.SigningPubKeyHex != passwordVectorSignIn {
		t.Fatalf("password sign-in key = %s, want %s", pw.SigningPubKeyHex, passwordVectorSignIn)
	}
	if !strings.HasPrefix(pw.RowKey, "xpub") || pw.Source != SourcePassword {
		t.Fatalf("unexpected password enrollment %+v", pw)
	}

	seed, err := MnemonicSeed(ctx, w, "  "+strings.ToUpper(mnemonicVectorPhrase()))
	if err != nil {
		t.Fatal(err)
	}
	if got := hex.EncodeToString(seed); got != mnemonicVectorSeed {
		t.Fatalf("mnemonic seed = %s, want %s", got, mnemonicVectorSeed)
	}
	mn, err := FromMnemonic(ctx, w, mnemonicVectorPhrase())
	if err != nil {
		t.Fatal(err)
	}
	if mn.SigningPubKeyHex != mnemonicVectorSignIn {
		t.Fatalf("mnemonic sign-in key = %s, want %s", mn.SigningPubKeyHex, mnemonicVectorSignIn)
	}

	bad := strings.Replace(mnemonicVectorPhrase(), "about", "abandon", 1)
	if _, err := FromMnemonic(ctx, w, bad); err == nil {
		t.Fatal("accepted a phrase with a bad checksum")
	}
}

func testWallet(t *testing.T) HDWallet {
	t.Helper()
	path := ""
	for _, p := range []string{os.Getenv("HD_WALLET_WASM_PATH"), "../../../sdn-js/node_modules/hd-wallet-wasm/dist/hd-wallet-wasi.wasm"} {
		if p == "" {
			continue
		}
		if _, err := os.Stat(p); err == nil {
			path = p
			break
		}
	}
	if path == "" {
		t.Skip("HD wallet WASI artifact not found; set HD_WALLET_WASM_PATH")
	}
	ctx := context.Background()
	hw, err := wasm.NewHDWalletModule(ctx, path)
	if err != nil {
		t.Fatalf("load HD wallet WASM: %v", err)
	}
	entropy := make([]byte, 64)
	if _, err := rand.Read(entropy); err != nil {
		t.Fatal(err)
	}
	if err := hw.InjectEntropy(ctx, entropy); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { hw.Close(context.Background()) })
	return HDWallet{hw}
}
