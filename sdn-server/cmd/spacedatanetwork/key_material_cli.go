package main

// `sdn key` — import and export of the node's key material in several formats
// (owner directive 2026-07-27, graph task nst-key-material-cli).
//
// # Connectors only
//
// The node's identity root is the encrypted BIP-39 mnemonic at
// config.MnemonicPathResolved. Legacy keys.Manager backups contain a different
// Ed25519/X25519 key set and MUST NOT be used for node-identity migration.
// This command derives the same identity bundle as the daemon through the
// canonical hd-wallet WASM module.
//
// It also deliberately does NOT modify internal/keys/backup.go. That file
// embeds a BIP-39 wordlist (a 128-word run) and the repository's mnemonic guard
// blocks re-staging it. It does not need modifying — its API is already
// complete, which is the whole point.
//
// # The secret/public split is the design
//
// Formats fall into three custody classes and the command treats them
// differently rather than uniformly:
//
//	PUBLIC     xpub, peerid, epm, vcard  — safe to print, pipe, and commit
//	ENCRYPTED  backup                    — safe at rest; useless without the password
//	SECRET     mnemonic, kubo, libp2p,
//	           hex, base64               — plaintext private material
//
// A SECRET export is refused unless the operator passes --insecure-plaintext,
// and even then it prints a warning to stderr (never stdout, so it cannot
// silently end up inside a redirected file). That mirrors the existing
// `show-identity --show-mnemonic` precedent rather than inventing a new posture.

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	libp2pcrypto "github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/spf13/cobra"

	"github.com/spacedatanetwork/sdn-server/internal/auth"
	"github.com/spacedatanetwork/sdn-server/internal/config"
	"github.com/spacedatanetwork/sdn-server/internal/keys"
	"github.com/spacedatanetwork/sdn-server/internal/wasm"
)

// keyFormat identifies an import/export encoding.
type keyFormat string

const (
	keyFormatXPub     keyFormat = "xpub"
	keyFormatPeerID   keyFormat = "peerid"
	keyFormatEPM      keyFormat = "epm"
	keyFormatVCard    keyFormat = "vcard"
	keyFormatBackup   keyFormat = "backup"
	keyFormatMnemonic keyFormat = "mnemonic"
	keyFormatKubo     keyFormat = "kubo"
	keyFormatLibp2p   keyFormat = "libp2p"
	keyFormatHex      keyFormat = "hex"
	keyFormatBase64   keyFormat = "base64"
)

// keyFormatCustody classifies a format. Callers gate on this rather than
// special-casing format names, so a format added later cannot accidentally
// default to "printable".
type keyFormatCustody int

const (
	custodyPublic keyFormatCustody = iota
	custodyEncrypted
	custodySecret
)

func (c keyFormatCustody) String() string {
	switch c {
	case custodyPublic:
		return "public"
	case custodyEncrypted:
		return "encrypted"
	case custodySecret:
		return "SECRET"
	default:
		return "unknown"
	}
}

// keyFormatInfo is the format table. It is the single source of truth for what
// `key export` and `key import` accept and how each is treated.
var keyFormatInfo = map[keyFormat]struct {
	Custody     keyFormatCustody
	Exportable  bool
	Importable  bool
	Description string
}{
	keyFormatXPub: {custodyPublic, true, false,
		"BIP-32 account extended public key (m/44'/0'/account') — NEVER the master key"},
	keyFormatPeerID: {custodyPublic, true, false,
		"libp2p peer ID derived from the account key"},
	keyFormatEPM: {custodyPublic, true, false,
		"the node's signed EPM record (size-prefixed FlatBuffer)"},
	keyFormatVCard: {custodyPublic, true, false,
		"the node's contact card (no key bytes, no embedded record — see §18)"},
	keyFormatBackup: {custodyEncrypted, true, true,
		"password-encrypted identity backup; restorable with `key import --format backup`"},
	keyFormatMnemonic: {custodySecret, true, true,
		"BIP-39 recovery phrase — THE ENTIRE NODE IDENTITY IN PLAINTEXT"},
	keyFormatKubo: {custodySecret, true, false,
		"Kubo Identity.PrivKey: base64 protobuf private key (secp256k1); verify-only on import"},
	keyFormatLibp2p: {custodySecret, true, false,
		"libp2p protobuf private key bytes (secp256k1); verify-only on import"},
	keyFormatHex: {custodySecret, true, false,
		"raw secp256k1 private scalar encoded as hex; verify-only on import"},
	keyFormatBase64: {custodySecret, true, false,
		"raw secp256k1 private scalar encoded as base64; verify-only on import"},
}

func knownKeyFormats(filter func(keyFormat) bool) []string {
	var out []string
	for f := range keyFormatInfo {
		if filter == nil || filter(f) {
			out = append(out, string(f))
		}
	}
	// Deterministic help text.
	for i := 0; i < len(out); i++ {
		for j := i + 1; j < len(out); j++ {
			if out[j] < out[i] {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	return out
}

var (
	keyExportFormat    string
	keyExportOutput    string
	keyExportPlaintext bool
	keyImportFormat    string
	keyImportInput     string
	keyImportForce     bool
	keyVerifyFormat    string
	keyVerifyInput     string
)

var keyCmd = &cobra.Command{
	Use:   "key",
	Short: "Import and export the node's key material",
	Long: `Import and export the node's key material in several formats.

Formats are classified by custody, and the command enforces that class:

  public     xpub, peerid, epm, vcard   safe to print, pipe, and share
  encrypted  backup                     safe at rest; needs the key password
  SECRET     mnemonic                   plaintext seed — the entire node

Exporting a SECRET format requires --insecure-plaintext and prints a warning.
Importing REPLACES this node's identity and is destructive; see 'key import --help'.`,
}

func keyFormatHelp() string {
	formats := knownKeyFormats(nil)
	var b strings.Builder
	for _, name := range formats {
		info := keyFormatInfo[keyFormat(name)]
		fmt.Fprintf(&b, "  %-10s %-9s %s\n", name, info.Custody.String(), info.Description)
	}
	return strings.TrimRight(b.String(), "\n")
}

var keyExportCmd = &cobra.Command{
	Use:   "export",
	Short: "Export the node's key material in a chosen format",
	Long: `Export the node's key material.

Public formats print to stdout (or --output). The 'backup' format produces a
password-encrypted blob that is safe to store. The 'mnemonic' format is the
plaintext seed and REQUIRES --insecure-plaintext.`,
	RunE: runKeyExport,
}

var keyImportCmd = &cobra.Command{
	Use:   "import",
	Short: "Import key material, REPLACING this node's identity",
	Long: `Import key material, REPLACING this node's identity.

THIS IS DESTRUCTIVE AND CHANGES WHO THIS NODE IS:

  * the libp2p PeerID changes, so peers must re-trust this node;
  * previously published records stay signed by the OLD key and this node can no
    longer produce that signature;
  * root-admin sign-in follows the NEW seed (§14), so the operator must hold the
    imported phrase in their wallet or they will be locked out of the console.

Refuses to overwrite an existing identity unless --force is passed.`,
	RunE: runKeyImport,
}

var keyVerifyCmd = &cobra.Command{
	Use:   "verify",
	Short: "Verify exported private key material against this node without writing it",
	RunE:  runKeyVerify,
}

func init() {
	exportable := knownKeyFormats(func(f keyFormat) bool { return keyFormatInfo[f].Exportable })
	importable := knownKeyFormats(func(f keyFormat) bool { return keyFormatInfo[f].Importable })
	keyCmd.Long = "Import and export the node's key material.\n\nFormats (custody is enforced):\n" + keyFormatHelp()
	keyExportCmd.Long = "Export the node's key material. SECRET formats require --insecure-plaintext.\n\nFormats:\n" + keyFormatHelp()

	keyExportCmd.Flags().StringVar(&keyExportFormat, "format", string(keyFormatXPub),
		"output format: "+strings.Join(exportable, ", "))
	keyExportCmd.Flags().StringVarP(&keyExportOutput, "output", "o", "",
		"write to a file instead of stdout")
	keyExportCmd.Flags().BoolVar(&keyExportPlaintext, "insecure-plaintext", false,
		"REQUIRED to export a SECRET format; prints plaintext private material")

	keyImportCmd.Flags().StringVar(&keyImportFormat, "format", string(keyFormatMnemonic),
		"input format: "+strings.Join(importable, ", "))
	keyImportCmd.Flags().StringVarP(&keyImportInput, "input", "i", "-",
		"read from a file, or '-' for stdin")
	keyImportCmd.Flags().BoolVar(&keyImportForce, "force", false,
		"overwrite an existing node identity (DESTRUCTIVE)")

	keyVerifyCmd.Flags().StringVar(&keyVerifyFormat, "format", string(keyFormatKubo),
		"input format: kubo, libp2p, hex, base64")
	keyVerifyCmd.Flags().StringVarP(&keyVerifyInput, "input", "i", "-", "read from a file, or '-' for stdin")
	keyCmd.AddCommand(keyExportCmd)
	keyCmd.AddCommand(keyImportCmd)
	keyCmd.AddCommand(keyVerifyCmd)
	rootCmd.AddCommand(keyCmd)
}

// resolveKeyCLIPassword mirrors the precedence show-identity uses: env, then
// config, then the machine-derived default. It ERRORS instead of degrading —
// both a configured-but-unreadable password file and a refused machine
// derivation (unknown user, unreadable source) must surface as the actual
// fault, never as a "wrong password" on intact material or, worse, a seal
// under a substitute key.
func resolveKeyCLIPassword(cfg *config.Config) (string, error) {
	// Routed through config.KeyPassword so SDN_KEY_PASSWORD_FILE — the mounted
	// secret the remote deploy script feeds containers — is honoured everywhere
	// the CLI touches key material, not just in one command.
	password, err := config.KeyPassword(cfg)
	if err != nil {
		return "", fmt.Errorf("resolve key password: %w", err)
	}
	if password != "" {
		return password, nil
	}
	return keys.DeriveDefaultPassword()
}

// validateExportFormat resolves a format name and enforces the custody gate.
func validateExportFormat(name string, allowPlaintext bool) (keyFormat, error) {
	f := keyFormat(strings.ToLower(strings.TrimSpace(name)))
	info, ok := keyFormatInfo[f]
	if !ok {
		return "", fmt.Errorf("unknown format %q: expected one of %s", name,
			strings.Join(knownKeyFormats(func(k keyFormat) bool { return keyFormatInfo[k].Exportable }), ", "))
	}
	if !info.Exportable {
		return "", fmt.Errorf("format %q cannot be exported", name)
	}
	if info.Custody == custodySecret && !allowPlaintext {
		return "", fmt.Errorf(
			"refusing to export %q: it is %s (%s).\n"+
				"Pass --insecure-plaintext if you really intend to write plaintext private material.\n"+
				"Prefer `--format backup`, which is encrypted and restorable",
			name, info.Custody, info.Description)
	}
	return f, nil
}

func validateImportFormat(name string) (keyFormat, error) {
	f := keyFormat(strings.ToLower(strings.TrimSpace(name)))
	info, ok := keyFormatInfo[f]
	if ok && (f == keyFormatKubo || f == keyFormatLibp2p || f == keyFormatHex || f == keyFormatBase64) {
		return "", fmt.Errorf(
			"format %q is a derived secp256k1 key, not the node identity root: importing it cannot restore the BIP-39 seed, xpub, Ed25519 signing key, X25519 encryption key, or root-admin identity; use `key verify --format %s` to compare PeerIDs, or import mnemonic/backup to restore the complete identity",
			name, f)
	}
	if !ok || !info.Importable {
		return "", fmt.Errorf("unknown or non-importable format %q: expected one of %s", name,
			strings.Join(knownKeyFormats(func(k keyFormat) bool { return keyFormatInfo[k].Importable }), ", "))
	}
	return f, nil
}

func runKeyExport(cmd *cobra.Command, _ []string) error {
	format, err := validateExportFormat(keyExportFormat, keyExportPlaintext)
	if err != nil {
		return err
	}
	cfg, cfgRes, err := config.LoadResolved(configPath)
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}
	keyDir := config.KeyDir(cfg)

	var payload []byte
	appendNewline := true
	switch format {
	case keyFormatMnemonic:
		// Warning goes to STDERR, never stdout: `key export --format mnemonic
		// -o file` or a shell redirect must not fold the warning into the
		// exported material.
		fmt.Fprintf(cmd.ErrOrStderr(),
			"WARNING: exporting the plaintext BIP-39 recovery phrase for %s.\n"+
				"Anyone holding it controls this node completely and can sign as it.\n",
			keyDir)
		var mnemonic string
		mnemonic, _, err = loadStoredMnemonic(cfg, cfgRes)
		payload = []byte(strings.TrimSpace(mnemonic))
	case keyFormatBackup:
		var mnemonic, password string
		mnemonic, _, err = loadStoredMnemonic(cfg, cfgRes)
		if err == nil {
			password, err = resolveKeyCLIPassword(cfg)
		}
		if err == nil {
			payload, err = keys.EncryptMnemonic(strings.TrimSpace(mnemonic), password)
			appendNewline = false
		}
	default:
		var rendered string
		if format == keyFormatKubo || format == keyFormatLibp2p || format == keyFormatHex || format == keyFormatBase64 {
			payload, appendNewline, err = exportPrivateKeyMaterial(cmd, cfg, cfgRes, format)
		} else {
			rendered, err = exportPublicKeyMaterial(cmd, cfg, cfgRes, format)
			payload = []byte(rendered)
		}
	}
	if err != nil {
		return fmt.Errorf("export %s: %w", format, err)
	}

	if keyExportOutput != "" {
		// Secret and encrypted material is written 0600; public material uses
		// the same mode rather than guessing, because an operator redirecting
		// identity output rarely wants it world-readable.
		if appendNewline {
			payload = append(payload, '\n')
		}
		if err := os.WriteFile(keyExportOutput, payload, 0o600); err != nil {
			return fmt.Errorf("write %s: %w", keyExportOutput, err)
		}
		fmt.Fprintf(cmd.ErrOrStderr(), "wrote %s (%s, mode 0600)\n", keyExportOutput, format)
		return nil
	}
	if _, err := cmd.OutOrStdout().Write(payload); err != nil {
		return err
	}
	if appendNewline {
		fmt.Fprintln(cmd.OutOrStdout())
	}
	return nil
}

func runKeyImport(cmd *cobra.Command, _ []string) error {
	format, err := validateImportFormat(keyImportFormat)
	if err != nil {
		return err
	}
	cfg, cfgRes, err := config.LoadResolved(configPath)
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}
	keyDir := config.KeyDir(cfg)

	// Refuse to clobber an existing identity by accident. The mnemonic file is
	// the authoritative marker: if it exists, this node already HAS an
	// identity, and importing replaces who it is.
	mnemonicPath := config.MnemonicPathResolved(cfg)
	if _, statErr := os.Stat(mnemonicPath); statErr == nil && !keyImportForce {
		return fmt.Errorf(
			"this node already has an identity at %s (config: %s).\n"+
				"Importing REPLACES it: the PeerID changes, peers must re-trust this node,\n"+
				"previously published records stay signed by the old key, and root-admin\n"+
				"sign-in will follow the NEW seed — so the operator must hold the imported\n"+
				"phrase in their wallet or they will be locked out of the console.\n"+
				"Export a backup first (`key export --format backup -o backup.txt`), then\n"+
				"re-run with --force if that is genuinely what you intend",
			mnemonicPath, cfgRes.Describe())
	}

	raw, err := readKeyImportInput(cmd)
	if err != nil {
		return err
	}
	if len(raw) == 0 {
		return errors.New("no key material on input")
	}

	switch format {
	case keyFormatMnemonic:
		material := strings.TrimSpace(raw)
		if material == "" {
			return errors.New("no key material on input")
		}
		err = importIdentityMnemonic(cmd, cfg, material)
	case keyFormatBackup:
		var password string
		password, err = resolveKeyCLIPassword(cfg)
		if err != nil {
			return err
		}
		var mnemonic string
		mnemonic, err = keys.DecryptMnemonic([]byte(raw), password)
		if err == nil {
			err = importIdentityMnemonic(cmd, cfg, mnemonic)
		}
	default:
		return fmt.Errorf("format %q cannot be imported", format)
	}
	if err != nil {
		return fmt.Errorf("import %s: %w", format, err)
	}

	fmt.Fprintf(cmd.ErrOrStderr(),
		"imported %s into %s.\n"+
			"This node's identity has CHANGED. Restart the daemon, re-check peer trust,\n"+
			"and confirm the operator's wallet holds this seed before relying on\n"+
			"root-admin sign-in.\n", format, keyDir)
	return nil
}

func importIdentityMnemonic(cmd *cobra.Command, cfg *config.Config, mnemonic string) error {
	wp, err := resolveHDWalletWasmPath()
	if err != nil {
		return err
	}
	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	hw, err := wasm.NewHDWalletModule(ctx, wp)
	if err != nil {
		return fmt.Errorf("load HD wallet WASM: %w", err)
	}
	defer hw.Close(ctx)
	valid, err := hw.ValidateMnemonic(ctx, strings.TrimSpace(mnemonic))
	if err != nil {
		return fmt.Errorf("validate BIP-39 mnemonic: %w", err)
	}
	if !valid {
		return errors.New("invalid BIP-39 mnemonic (word list or checksum)")
	}
	password, err := resolveKeyCLIPassword(cfg)
	if err != nil {
		return err
	}
	sealed, err := keys.EncryptMnemonic(strings.TrimSpace(mnemonic), password)
	if err != nil {
		return fmt.Errorf("encrypt mnemonic: %w", err)
	}
	path := config.MnemonicPathResolved(cfg)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(path, sealed, 0o600); err != nil {
		return fmt.Errorf("write identity mnemonic: %w", err)
	}
	return nil
}

// readKeyImportInput reads material from --input or stdin.
func readKeyImportInput(cmd *cobra.Command) (string, error) {
	if keyImportInput != "" && keyImportInput != "-" {
		data, err := os.ReadFile(keyImportInput)
		if err != nil {
			return "", fmt.Errorf("read %s: %w", keyImportInput, err)
		}
		return string(data), nil
	}
	reader := bufio.NewReader(cmd.InOrStdin())
	data, err := io.ReadAll(reader)
	if err != nil {
		return "", fmt.Errorf("read stdin: %w", err)
	}
	return string(data), nil
}

func deriveNodeIdentity(cmd *cobra.Command, cfg *config.Config, res config.Resolution) (*wasm.DerivedIdentity, error) {
	mnemonic, _, err := loadStoredMnemonic(cfg, res)
	if err != nil {
		return nil, err
	}
	wp, err := resolveHDWalletWasmPath()
	if err != nil {
		return nil, err
	}
	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	hw, err := wasm.NewHDWalletModule(ctx, wp)
	if err != nil {
		return nil, fmt.Errorf("load HD wallet WASM: %w", err)
	}
	defer hw.Close(ctx)
	seed, err := hw.MnemonicToSeed(ctx, strings.TrimSpace(mnemonic), "")
	if err != nil {
		return nil, fmt.Errorf("derive seed: %w", err)
	}
	return hw.DeriveIdentity(ctx, seed, 0)
}

func exportPrivateKeyMaterial(cmd *cobra.Command, cfg *config.Config, res config.Resolution, format keyFormat) ([]byte, bool, error) {
	id, err := deriveNodeIdentity(cmd, cfg, res)
	if err != nil {
		return nil, false, err
	}
	marshaled, err := libp2pcrypto.MarshalPrivateKey(id.IdentityPrivKey)
	if err != nil {
		return nil, false, err
	}
	raw, err := id.IdentityPrivKey.Raw()
	if err != nil {
		return nil, false, err
	}
	fmt.Fprintf(cmd.ErrOrStderr(), "WARNING: exporting SECRET secp256k1 identity private key for PeerID %s.\n", id.PeerID)
	switch format {
	case keyFormatKubo:
		return []byte(base64.StdEncoding.EncodeToString(marshaled)), true, nil
	case keyFormatLibp2p:
		return marshaled, false, nil
	case keyFormatHex:
		return []byte(hex.EncodeToString(raw)), true, nil
	case keyFormatBase64:
		return []byte(base64.StdEncoding.EncodeToString(raw)), true, nil
	default:
		return nil, false, fmt.Errorf("format %q has no private exporter", format)
	}
}

func decodePrivateKeyMaterial(format keyFormat, material []byte) (libp2pcrypto.PrivKey, error) {
	trimmed := strings.TrimSpace(string(material))
	var encoded []byte
	var err error
	switch format {
	case keyFormatKubo:
		encoded, err = base64.StdEncoding.DecodeString(trimmed)
		if err == nil {
			return libp2pcrypto.UnmarshalPrivateKey(encoded)
		}
	case keyFormatLibp2p:
		return libp2pcrypto.UnmarshalPrivateKey(material)
	case keyFormatHex:
		encoded, err = hex.DecodeString(strings.TrimPrefix(trimmed, "0x"))
		if err == nil {
			return libp2pcrypto.UnmarshalSecp256k1PrivateKey(encoded)
		}
	case keyFormatBase64:
		encoded, err = base64.StdEncoding.DecodeString(trimmed)
		if err == nil {
			return libp2pcrypto.UnmarshalSecp256k1PrivateKey(encoded)
		}
	default:
		return nil, fmt.Errorf("format %q is not a private-key format", format)
	}
	return nil, fmt.Errorf("decode %s private key: %w", format, err)
}

func runKeyVerify(cmd *cobra.Command, _ []string) error {
	format := keyFormat(strings.ToLower(strings.TrimSpace(keyVerifyFormat)))
	if format != keyFormatKubo && format != keyFormatLibp2p && format != keyFormatHex && format != keyFormatBase64 {
		return fmt.Errorf("format %q is not verifiable: expected kubo, libp2p, hex, or base64", keyVerifyFormat)
	}
	oldInput := keyImportInput
	keyImportInput = keyVerifyInput
	raw, err := readKeyImportInput(cmd)
	keyImportInput = oldInput
	if err != nil {
		return err
	}
	priv, err := decodePrivateKeyMaterial(format, []byte(raw))
	if err != nil {
		return err
	}
	if priv.Type() != libp2pcrypto.Secp256k1 {
		return fmt.Errorf("key type is %s, want secp256k1", priv.Type())
	}
	got, err := peer.IDFromPrivateKey(priv)
	if err != nil {
		return err
	}
	cfg, res, err := config.LoadResolved(configPath)
	if err != nil {
		return err
	}
	want, err := deriveNodeIdentity(cmd, cfg, res)
	if err != nil {
		return err
	}
	if got != want.PeerID {
		return fmt.Errorf("private key PeerID %s does not match this node %s", got, want.PeerID)
	}
	fmt.Fprintf(cmd.OutOrStdout(), "MATCH %s (secp256k1)\n", got)
	return nil
}

// exportPublicKeyMaterial renders the PUBLIC formats.
//
// It reuses exactly the derivation `show-identity` performs — read the stored
// mnemonic, derive via the hd-wallet WASM module — so the CLI can never report
// an identity the daemon would disagree with. Nothing here derives differently
// or caches.
//
// EPM and vCard are deliberately NOT produced here: those are SIGNED records the
// running EPM service owns, and re-deriving them from the key store would
// produce an unsigned lookalike. The error points at the surface that already
// serves them.
func exportPublicKeyMaterial(cmd *cobra.Command, cfg *config.Config, res config.Resolution, format keyFormat) (string, error) {
	switch format {
	case keyFormatEPM:
		return "", errors.New(
			"format \"epm\" is a SIGNED record owned by the running daemon, not the key store.\n" +
				"Use `spacedatanetwork identity export --format flatbuffer` or GET /api/node/epm")
	case keyFormatVCard:
		return "", errors.New(
			"format \"vcard\" is derived from the SIGNED record owned by the running daemon.\n" +
				"Use `spacedatanetwork identity export --format text` or GET /api/node/epm/vcard")
	case keyFormatXPub, keyFormatPeerID:
	default:
		return "", fmt.Errorf("format %q has no public exporter", format)
	}

	mnemonic, _, err := loadStoredMnemonic(cfg, res)
	if err != nil {
		return "", err
	}
	wp, err := resolveHDWalletWasmPath()
	if err != nil {
		return "", err
	}
	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	hw, err := wasm.NewHDWalletModule(ctx, wp)
	if err != nil {
		return "", fmt.Errorf("load HD wallet WASM: %w", err)
	}
	defer hw.Close(ctx)

	seed, err := hw.MnemonicToSeed(ctx, mnemonic, "")
	if err != nil {
		return "", fmt.Errorf("derive seed: %w", err)
	}

	switch format {
	case keyFormatXPub:
		xpub, err := hw.DeriveXPub(ctx, seed, 0)
		if err != nil {
			return "", fmt.Errorf("derive xpub: %w", err)
		}
		// ⚠ Must be the ACCOUNT key (m/44'/0'/account', depth 3), never the
		// BIP-32 MASTER key. A master xpub enumerates every account and every
		// address under the whole wallet; §13.1 refuses to STORE one, and this
		// refuses to EMIT one.
		if depth, ok := auth.XPubDepth(xpub); ok && depth == 0 {
			return "", errors.New(
				"refusing to export a BIP-32 MASTER xpub (depth 0): it enumerates every " +
					"account and address under this wallet. Only the account xpub is exportable")
		}
		return xpub, nil
	default: // keyFormatPeerID
		identity, err := hw.DeriveIdentity(ctx, seed, 0)
		if err != nil {
			return "", fmt.Errorf("derive identity: %w", err)
		}
		return identity.PeerID.String(), nil
	}
}

// loadStoredMnemonic reads and decrypts the node's stored mnemonic, using the
// same path and password precedence as show-identity.
func loadStoredMnemonic(cfg *config.Config, res config.Resolution) (mnemonic string, path string, err error) {
	keyDir := config.KeyDir(cfg)
	path = config.MnemonicPathResolved(cfg)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", path, config.DescribeMissingNodeState("node identity (mnemonic)", path, res)
		}
		if os.IsPermission(err) {
			return "", path, config.DescribePermissionDenied("the node mnemonic", path, keyDirOwner(keyDir), res)
		}
		return "", path, fmt.Errorf("read %s (config: %s): %w", path, res.Describe(), err)
	}
	if keys.IsMnemonicEncrypted(data) {
		password, perr := resolveKeyCLIPassword(cfg)
		if perr != nil {
			return "", path, perr
		}
		mnemonic, err = keys.DecryptMnemonic(data, password)
		if err != nil {
			return "", path, fmt.Errorf("decrypt mnemonic (wrong password?): %w", err)
		}
		return mnemonic, path, nil
	}
	return string(data), path, nil
}

// mustLoadResolved is the non-error-returning shape a few call sites use
// (`cfg, _ := ...`). It applies the SAME resolution order as LoadResolved and
// falls back to defaults rather than failing, matching the previous
// config.Load behaviour for those sites.
func mustLoadResolved(explicit string) (*config.Config, error) {
	cfg, _, err := config.LoadResolved(explicit)
	if err != nil {
		return config.Default(), err
	}
	return cfg, nil
}

// resolvedConfig loads the config AND returns the resolution, for commands that
// need to name the config in an error. This is the shape every command touching
// node state should use.
func resolvedConfig() (*config.Config, config.Resolution, error) {
	return config.LoadResolved(configPath)
}

// keyDirOwner returns the owning username of a key directory, for the
// permission-denied message. Best effort: an unknown owner still yields a
// useful error, it just says "the service user".
func keyDirOwner(dir string) string {
	info, err := os.Stat(dir)
	if err != nil {
		return ""
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return ""
	}
	if u, err := user.LookupId(strconv.FormatUint(uint64(stat.Uid), 10)); err == nil {
		return u.Username
	}
	return "uid " + strconv.FormatUint(uint64(stat.Uid), 10)
}
