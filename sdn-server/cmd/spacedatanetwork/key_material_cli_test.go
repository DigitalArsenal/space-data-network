package main

// Locks the custody gates on `sdn key` (graph task nst-key-material-cli).
//
// The command's whole job is to move key material, so the tests that matter are
// the ones proving it REFUSES to move it in the wrong direction: no plaintext
// seed without an explicit flag, no silent identity replacement, no master xpub.

import (
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	libp2pcrypto "github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/spf13/cobra"

	"github.com/spacedatanetwork/sdn-server/internal/config"
	"github.com/spacedatanetwork/sdn-server/internal/keys"
)

// TestSecretFormatsRequireTheExplicitFlag is the central gate. A plaintext
// BIP-39 phrase is the entire node; exporting one must be a deliberate act.
func TestSecretFormatsRequireTheExplicitFlag(t *testing.T) {
	t.Parallel()

	_, err := validateExportFormat("mnemonic", false)
	if err == nil {
		t.Fatal("mnemonic exported without --insecure-plaintext")
	}
	for _, want := range []string{"SECRET", "--insecure-plaintext", "backup"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("refusal should mention %q so the operator knows the safe path; got: %v", want, err)
		}
	}

	if _, err := validateExportFormat("mnemonic", true); err != nil {
		t.Fatalf("mnemonic refused even with --insecure-plaintext: %v", err)
	}
}

// TestPublicAndEncryptedFormatsNeedNoFlag locks that the gate is narrow: it
// applies to SECRET material only, so routine use is not trained to pass a
// scary flag (which would make the flag meaningless when it matters).
func TestPublicAndEncryptedFormatsNeedNoFlag(t *testing.T) {
	t.Parallel()

	for _, format := range []string{"xpub", "peerid", "epm", "vcard", "backup"} {
		if _, err := validateExportFormat(format, false); err != nil {
			t.Fatalf("format %q required --insecure-plaintext but is not SECRET: %v", format, err)
		}
	}
}

// TestEveryFormatIsClassified locks that a format cannot be added without
// declaring its custody class. The gate reads the class, so an unclassified
// format would silently default to printable.
func TestEveryFormatIsClassified(t *testing.T) {
	t.Parallel()

	if len(keyFormatInfo) == 0 {
		t.Fatal("no formats registered")
	}
	for format, info := range keyFormatInfo {
		if strings.TrimSpace(string(format)) == "" {
			t.Fatal("a format has an empty name")
		}
		if strings.TrimSpace(info.Description) == "" {
			t.Fatalf("format %q has no description; the help text is the operator's only warning", format)
		}
		if !info.Exportable && !info.Importable {
			t.Fatalf("format %q is neither exportable nor importable", format)
		}
		switch info.Custody {
		case custodyPublic, custodyEncrypted, custodySecret:
		default:
			t.Fatalf("format %q has an unknown custody class %d", format, info.Custody)
		}
	}
	// Every private-key representation is SECRET and therefore inherits the
	// same table-driven gate as the mnemonic.
	if keyFormatInfo[keyFormatMnemonic].Custody != custodySecret {
		t.Fatal("mnemonic is not classified SECRET")
	}
	for _, secret := range []keyFormat{keyFormatKubo, keyFormatLibp2p, keyFormatHex, keyFormatBase64} {
		if keyFormatInfo[secret].Custody != custodySecret {
			t.Fatalf("%q is not classified SECRET", secret)
		}
	}
	if keyFormatInfo[keyFormatBackup].Custody != custodyEncrypted {
		t.Fatal("backup is not classified encrypted")
	}
	for _, public := range []keyFormat{keyFormatXPub, keyFormatPeerID, keyFormatEPM, keyFormatVCard} {
		if keyFormatInfo[public].Custody != custodyPublic {
			t.Fatalf("%q is not classified public", public)
		}
	}
}

func TestEverySecretFormatRequiresExplicitPlaintext(t *testing.T) {
	t.Parallel()
	for _, format := range []string{"mnemonic", "kubo", "libp2p", "hex", "base64"} {
		if _, err := validateExportFormat(format, false); err == nil {
			t.Errorf("%s exported without --insecure-plaintext", format)
		}
		if _, err := validateExportFormat(format, true); err != nil {
			t.Errorf("%s refused with --insecure-plaintext: %v", format, err)
		}
	}
}

// This is an interoperability test, not a self-round-trip: the encoders are
// the CLI formats while the decoders are go-libp2p's public crypto API, the
// implementation Kubo uses for Identity.PrivKey.
func TestKuboAndLibp2pFormatsInteropWithLibp2p(t *testing.T) {
	t.Parallel()
	priv, _, err := libp2pcrypto.GenerateSecp256k1Key(bytes.NewReader(bytes.Repeat([]byte{0x42}, 64)))
	if err != nil {
		t.Fatal(err)
	}
	want, err := peer.IDFromPrivateKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	protobuf, err := libp2pcrypto.MarshalPrivateKey(priv)
	if err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name    string
		format  keyFormat
		payload []byte
	}{
		{"kubo-config-Identity.PrivKey", keyFormatKubo, []byte(base64.StdEncoding.EncodeToString(protobuf))},
		{"libp2p-protobuf", keyFormatLibp2p, protobuf},
	} {
		t.Run(tc.name, func(t *testing.T) {
			decoded, err := decodePrivateKeyMaterial(tc.format, tc.payload)
			if err != nil {
				t.Fatalf("decode: %v", err)
			}
			got, err := peer.IDFromPrivateKey(decoded)
			if err != nil {
				t.Fatal(err)
			}
			if got != want {
				t.Fatalf("PeerID = %s, want %s", got, want)
			}
			reencoded, err := libp2pcrypto.MarshalPrivateKey(decoded)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(reencoded, protobuf) {
				t.Fatal("protobuf changed across libp2p decode/re-encode")
			}
		})
	}
}

func TestRawFormatsDeclareAndPreserveSecp256k1(t *testing.T) {
	t.Parallel()
	priv, _, err := libp2pcrypto.GenerateSecp256k1Key(bytes.NewReader(bytes.Repeat([]byte{0x24}, 64)))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := priv.Raw()
	if err != nil {
		t.Fatal(err)
	}
	want, _ := peer.IDFromPrivateKey(priv)
	for _, tc := range []struct {
		format  keyFormat
		payload []byte
	}{
		{keyFormatHex, []byte(hex.EncodeToString(raw))},
		{keyFormatBase64, []byte(base64.StdEncoding.EncodeToString(raw))},
	} {
		decoded, err := decodePrivateKeyMaterial(tc.format, tc.payload)
		if err != nil {
			t.Fatalf("%s decode: %v", tc.format, err)
		}
		if decoded.Type() != libp2pcrypto.Secp256k1 {
			t.Fatalf("%s type = %s", tc.format, decoded.Type())
		}
		got, _ := peer.IDFromPrivateKey(decoded)
		if got != want {
			t.Fatalf("%s PeerID = %s, want %s", tc.format, got, want)
		}
	}
}

func TestHelpIsGeneratedFromFormatTableWithCustody(t *testing.T) {
	t.Parallel()
	help := keyFormatHelp()
	for format, info := range keyFormatInfo {
		if !strings.Contains(help, string(format)) || !strings.Contains(help, info.Custody.String()) {
			t.Fatalf("help omits %s custody %s:\n%s", format, info.Custody, help)
		}
	}
}

func TestMnemonicAndEncryptedBackupRestoreTheActualNodePeerID(t *testing.T) {
	const password = "portable-explicit-test-password"
	walletWASM := strings.TrimSpace(os.Getenv("HD_WALLET_WASM_PATH"))
	if walletWASM == "" {
		walletWASM = filepath.Clean("../../../sdn-js/node_modules/hd-wallet-wasm/dist/hd-wallet-wasi.wasm")
	}
	if _, err := os.Stat(walletWASM); err != nil {
		t.Skipf("authoritative hd-wallet WASM unavailable: %v", err)
	}
	t.Setenv("HD_WALLET_WASM_PATH", walletWASM)
	t.Setenv(config.EnvKeyPassword, password)
	t.Setenv(config.EnvKeyPasswordFile, "")
	t.Setenv(config.EnvMnemonicFile, "")

	source, mnemonic := newSealedIdentityWizardNode(t, password)
	cmd := &cobra.Command{}
	sourceID, err := deriveNodeIdentity(cmd, source, config.Resolution{})
	if err != nil {
		t.Fatalf("derive source identity: %v", err)
	}

	backup, err := keys.EncryptMnemonic(mnemonic, password)
	if err != nil {
		t.Fatalf("export backup: %v", err)
	}
	restoredMnemonic, err := keys.DecryptMnemonic(backup, password)
	if err != nil {
		t.Fatalf("import backup: %v", err)
	}

	destination := config.Default()
	destination.Storage.Path = filepath.Join(t.TempDir(), "data")
	if err := importIdentityMnemonic(cmd, destination, restoredMnemonic); err != nil {
		t.Fatalf("restore identity mnemonic: %v", err)
	}
	destinationID, err := deriveNodeIdentity(cmd, destination, config.Resolution{})
	if err != nil {
		t.Fatalf("derive restored identity: %v", err)
	}
	if destinationID.PeerID != sourceID.PeerID {
		t.Fatalf("restored PeerID = %s, want %s", destinationID.PeerID, sourceID.PeerID)
	}
}

// TestOnlyIdentityBearingFormatsAreImportable locks that import is restricted
// to material that actually IS the node identity. Importing an xpub or a vCard
// would be importing someone's PUBLIC record, which belongs to the peer/account
// surface (§8), not to identity replacement.
func TestOnlyIdentityBearingFormatsAreImportable(t *testing.T) {
	t.Parallel()

	for _, format := range []string{"mnemonic", "backup"} {
		if _, err := validateImportFormat(format); err != nil {
			t.Fatalf("format %q should be importable: %v", format, err)
		}
	}
	for _, format := range []string{"xpub", "peerid", "epm", "vcard"} {
		if _, err := validateImportFormat(format); err == nil {
			t.Fatalf("format %q was accepted for identity import; it is a PUBLIC record, not this node's identity", format)
		}
	}
	for _, format := range []string{"kubo", "libp2p", "hex", "base64"} {
		_, err := validateImportFormat(format)
		if err == nil {
			t.Fatalf("derived key format %q was accepted as a complete identity root", format)
		}
		for _, want := range []string{"BIP-39 seed", "root-admin", "key verify"} {
			if !strings.Contains(err.Error(), want) {
				t.Fatalf("%s refusal omits %q: %v", format, want, err)
			}
		}
	}
}

// TestUnknownFormatsAreRefusedWithGuidance locks that a typo produces the list
// of real formats rather than a bare failure.
func TestUnknownFormatsAreRefusedWithGuidance(t *testing.T) {
	t.Parallel()

	_, err := validateExportFormat("mnemonics", false)
	if err == nil {
		t.Fatal("a misspelled format was accepted")
	}
	if !strings.Contains(err.Error(), "xpub") {
		t.Fatalf("refusal should list the valid formats; got: %v", err)
	}

	if _, err := validateExportFormat("", false); err == nil {
		t.Fatal("an empty format was accepted")
	}
	if _, err := validateImportFormat("nonsense"); err == nil {
		t.Fatal("an unknown import format was accepted")
	}
}

// TestFormatNamesAreCaseAndSpaceTolerant locks light input normalisation — the
// operator typing "Mnemonic " should still hit the SECRET gate rather than
// falling through to "unknown format".
func TestFormatNamesAreCaseAndSpaceTolerant(t *testing.T) {
	t.Parallel()

	if _, err := validateExportFormat("  XPUB  ", false); err != nil {
		t.Fatalf("uppercase/padded format was refused: %v", err)
	}
	err := validateExportFormatSecretProbe(t, " Mnemonic ")
	if err == nil {
		t.Fatal("a padded, capitalised SECRET format bypassed the gate")
	}
	if !strings.Contains(err.Error(), "--insecure-plaintext") {
		t.Fatalf("normalised SECRET format hit the wrong error path: %v", err)
	}
}

func validateExportFormatSecretProbe(t *testing.T, name string) error {
	t.Helper()
	_, err := validateExportFormat(name, false)
	return err
}

// TestExportableAndImportableListsAreNonEmpty guards the help text: the flag
// descriptions are built from these, and an empty list would ship a command
// advertising no formats.
func TestExportableAndImportableListsAreNonEmpty(t *testing.T) {
	t.Parallel()

	exportable := knownKeyFormats(func(f keyFormat) bool { return keyFormatInfo[f].Exportable })
	importable := knownKeyFormats(func(f keyFormat) bool { return keyFormatInfo[f].Importable })

	if len(exportable) < 5 {
		t.Fatalf("exportable formats = %v, want the full public+encrypted+secret set", exportable)
	}
	if len(importable) != 2 {
		t.Fatalf("importable formats = %v, want exactly mnemonic and backup", importable)
	}
	// Sorted, so help output is stable.
	for i := 1; i < len(exportable); i++ {
		if exportable[i-1] > exportable[i] {
			t.Fatalf("exportable list is not sorted: %v", exportable)
		}
	}
}
