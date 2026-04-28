package main

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	cryptorand "crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"golang.org/x/crypto/pbkdf2"

	"github.com/spacedatanetwork/sdn-server/internal/wasm"
	"github.com/spf13/cobra"
)

const (
	sdkCLIWalletFileName                 = "wallet.json"
	sdkCLISessionFileName                = "sessions.json"
	sdkCLIWalletVersion                  = 1
	sdkCLIKDFIterations                  = 310000
	sdkCLIModulePackageVers              = 2
	sdkCLIDefaultPassword                = "SDN_WALLET_PASSWORD"
	sdkCLILocalContentKeyEnvelopeContext = "sdn-js/module-package/local-content-key/v1"
)

var sdkModuleSlugPattern = regexp.MustCompile(`[^A-Za-z0-9._-]`)

var sdkWalletCmd = &cobra.Command{
	Use:   "wallet",
	Short: "Manage the encrypted SDN SDK wallet",
}

var sdkWalletInitCmd = &cobra.Command{
	Use:   "init",
	Short: "Create an encrypted SDN SDK wallet",
	RunE:  runSDKWalletInit,
}

var sdkWalletInfoCmd = &cobra.Command{
	Use:   "info",
	Short: "Show the encrypted SDN SDK wallet public identity",
	RunE:  runSDKWalletInfo,
}

var sdkAuthCmd = &cobra.Command{
	Use:   "auth",
	Short: "Authenticate the SDK wallet with an SDN node",
}

var sdkAuthLoginCmd = &cobra.Command{
	Use:   "login",
	Short: "Login to an SDN node with the SDK wallet challenge flow",
	RunE:  runSDKAuthLogin,
}

var sdkAuthAddCurrentWalletCmd = &cobra.Command{
	Use:   "add-current-wallet",
	Short: "Grant the current SDK wallet upload permissions on a node",
	RunE:  runSDKAuthAddCurrentWallet,
}

var sdkModuleCmd = &cobra.Command{
	Use:   "module",
	Short: "Package, upload, list, and query encrypted plugin modules",
}

var sdkModulePackageCmd = &cobra.Command{
	Use:   "package",
	Short: "Encrypt and sign a WASM plugin module package",
	RunE:  runSDKModulePackage,
}

var sdkModuleUploadCmd = &cobra.Command{
	Use:   "upload",
	Short: "Upload an encrypted plugin module package to an SDN node",
	RunE:  runSDKModuleUpload,
}

var sdkModulePublishCmd = &cobra.Command{
	Use:   "publish",
	Short: "Encrypt, sign, and upload a WASM plugin module package",
	RunE:  runSDKModulePublish,
}

var sdkModuleListCmd = &cobra.Command{
	Use:   "list",
	Short: "List plugin modules installed on an SDN node",
	RunE:  runSDKModuleList,
}

var sdkModuleQueryCmd = &cobra.Command{
	Use:   "query",
	Short: "Request encrypted module delivery from an SDN node",
	RunE:  runSDKModuleQuery,
}

type sdkLoadedWallet struct {
	Name                   string
	Account                uint32
	XPub                   string
	PeerID                 string
	SigningPublicKeyHex    string
	EncryptionPublicKeyHex string
	Identity               *wasm.DerivedIdentity
}

type sdkWalletSecretPayload struct {
	SeedPhrase string `json:"seed_phrase"`
	Account    uint32 `json:"account"`
}

type sdkWalletFile struct {
	Version                int    `json:"version"`
	Name                   string `json:"name"`
	Account                uint32 `json:"account"`
	XPub                   string `json:"xpub"`
	PeerID                 string `json:"peer_id"`
	SigningPublicKeyHex    string `json:"signing_public_key_hex"`
	EncryptionPublicKeyHex string `json:"encryption_public_key_hex"`
	KDF                    struct {
		Name       string `json:"name"`
		Iterations int    `json:"iterations"`
		Salt       string `json:"salt"`
	} `json:"kdf"`
	Cipher struct {
		Name       string `json:"name"`
		IV         string `json:"iv"`
		Tag        string `json:"tag"`
		Ciphertext string `json:"ciphertext"`
	} `json:"cipher"`
	CreatedAt string `json:"created_at"`
}

type sdkSessionStore map[string]struct {
	Cookie    string `json:"cookie"`
	UpdatedAt string `json:"updated_at"`
}

type sdkModulePackageMetadata struct {
	ID                string   `json:"id"`
	Version           string   `json:"version"`
	RequiredScope     string   `json:"required_scope,omitempty"`
	ContentType       string   `json:"content_type,omitempty"`
	CacheControl      string   `json:"cache_control,omitempty"`
	AllowedDomains    []string `json:"allowed_domains,omitempty"`
	MaxGrantTimeoutMs int64    `json:"max_grant_timeout_ms,omitempty"`
}

type sdkModulePackageFile struct {
	PackageVersion          int                        `json:"package_version"`
	Metadata                sdkModulePackageMetadata   `json:"metadata"`
	EncryptedBundlePath     string                     `json:"encrypted_bundle_path"`
	LocalContentKeyEnvelope sdkLocalContentKeyEnvelope `json:"local_content_key_envelope"`
	SignatureHex            string                     `json:"signature_hex"`
	SignerPublicKeyHex      string                     `json:"signer_public_key_hex"`
	BundleSHA256            string                     `json:"bundle_sha256"`
	SizeBytes               int                        `json:"size_bytes"`
	CreatedAt               string                     `json:"created_at"`
}

type sdkLocalContentKeyEnvelope struct {
	Version    int    `json:"version"`
	Algorithm  string `json:"alg"`
	Nonce      string `json:"nonce"`
	Ciphertext string `json:"ciphertext"`
}

type sdkPackagedModule struct {
	PackageFile          sdkModulePackageFile
	PackagePath          string
	EncryptedBundlePath  string
	EncryptedBundleBytes []byte
}

func init() {
	addSDKPasswordFlag(sdkWalletInitCmd)
	sdkWalletInitCmd.Flags().String("name", "SDN CLI Wallet", "wallet display name")
	addSDKWalletRuntimeFlags(sdkWalletInitCmd)

	addSDKPasswordFlag(sdkWalletInfoCmd)
	addSDKWalletRuntimeFlags(sdkWalletInfoCmd)

	addSDKPasswordFlag(sdkAuthLoginCmd)
	sdkAuthLoginCmd.Flags().String("node", "", "SDN node origin")
	addSDKWalletRuntimeFlags(sdkAuthLoginCmd)

	addSDKPasswordFlag(sdkAuthAddCurrentWalletCmd)
	sdkAuthAddCurrentWalletCmd.Flags().String("node", "", "SDN node origin")
	sdkAuthAddCurrentWalletCmd.Flags().String("trust", "admin", "trust level: admin, trusted, or standard")
	addSDKWalletRuntimeFlags(sdkAuthAddCurrentWalletCmd)

	addSDKPasswordFlag(sdkModulePackageCmd)
	addSDKModulePackageFlags(sdkModulePackageCmd)

	addSDKPasswordFlag(sdkModuleUploadCmd)
	sdkModuleUploadCmd.Flags().String("node", "", "SDN node origin")
	sdkModuleUploadCmd.Flags().String("package", "", "path to .sdn-module.json package")
	addSDKWalletRuntimeFlags(sdkModuleUploadCmd)

	addSDKPasswordFlag(sdkModulePublishCmd)
	sdkModulePublishCmd.Flags().String("node", "", "SDN node origin")
	addSDKModulePackageFlags(sdkModulePublishCmd)

	sdkModuleListCmd.Flags().String("node", "", "SDN node origin")

	addSDKPasswordFlag(sdkModuleQueryCmd)
	sdkModuleQueryCmd.Flags().String("node", "", "SDN node origin")
	sdkModuleQueryCmd.Flags().String("module-id", "", "module id")
	sdkModuleQueryCmd.Flags().String("version", "", "module version")
	sdkModuleQueryCmd.Flags().String("requester-domain", "", "requester domain")
	sdkModuleQueryCmd.Flags().Int64("requested-timeout-ms", 300000, "requested grant timeout in milliseconds")
	addSDKWalletRuntimeFlags(sdkModuleQueryCmd)

	sdkWalletCmd.AddCommand(sdkWalletInitCmd, sdkWalletInfoCmd)
	sdkAuthCmd.AddCommand(sdkAuthLoginCmd, sdkAuthAddCurrentWalletCmd)
	sdkModuleCmd.AddCommand(
		sdkModulePackageCmd,
		sdkModulePublishCmd,
		sdkModuleUploadCmd,
		sdkModuleListCmd,
		sdkModuleQueryCmd,
	)
	rootCmd.AddCommand(sdkWalletCmd, sdkAuthCmd, sdkModuleCmd)
}

func addSDKPasswordFlag(cmd *cobra.Command) {
	cmd.Flags().String("password-env", sdkCLIDefaultPassword, "environment variable containing the wallet password")
}

func addSDKWalletRuntimeFlags(cmd *cobra.Command) {
	cmd.Flags().String("wallet-wasm", "", "path to hd-wallet.wasm")
	cmd.Flags().String("wasm", "", "deprecated alias for --wallet-wasm")
	_ = cmd.Flags().MarkHidden("wasm")
}

func addSDKModulePackageFlags(cmd *cobra.Command) {
	cmd.Flags().String("wasm", "", "path to the WASM module")
	cmd.Flags().String("out", "dist", "output directory")
	cmd.Flags().String("module-id", "", "module id")
	cmd.Flags().String("version", "", "module version")
	cmd.Flags().StringArray("allow-domain", nil, "allowed requester domain; may be repeated")
	cmd.Flags().String("required-scope", "", "required module scope")
	cmd.Flags().String("content-type", "", "module content type")
	cmd.Flags().String("cache-control", "", "module cache-control policy")
	cmd.Flags().Int64("max-grant-timeout-ms", 0, "maximum grant timeout in milliseconds")
	cmd.Flags().String("wallet-wasm", "", "path to hd-wallet.wasm")
}

func runSDKWalletInit(cmd *cobra.Command, args []string) error {
	_ = args
	password, err := sdkPasswordFromEnv(cmd)
	if err != nil {
		return err
	}
	name, err := cmd.Flags().GetString("name")
	if err != nil {
		return err
	}
	wasmPath, err := sdkWalletRuntimePathFromCommand(cmd)
	if err != nil {
		return err
	}
	wallet, err := sdkCreateWallet(cmd.Context(), password, name, wasmPath)
	if err != nil {
		return err
	}
	return sdkPrintJSON(map[string]any{
		"wallet_home":               sdkCLIHome(),
		"name":                      wallet.Name,
		"xpub":                      wallet.XPub,
		"peer_id":                   wallet.PeerID,
		"signing_public_key_hex":    wallet.SigningPublicKeyHex,
		"encryption_public_key_hex": wallet.EncryptionPublicKeyHex,
	})
}

func runSDKWalletInfo(cmd *cobra.Command, args []string) error {
	_ = args
	wallet, err := sdkLoadWalletFromCommand(cmd)
	if err != nil {
		return err
	}
	return sdkPrintJSON(map[string]any{
		"wallet_home":               sdkCLIHome(),
		"name":                      wallet.Name,
		"xpub":                      wallet.XPub,
		"peer_id":                   wallet.PeerID,
		"signing_public_key_hex":    wallet.SigningPublicKeyHex,
		"encryption_public_key_hex": wallet.EncryptionPublicKeyHex,
	})
}

func runSDKAuthLogin(cmd *cobra.Command, args []string) error {
	_ = args
	nodeURL, err := sdkRequiredFlag(cmd, "node")
	if err != nil {
		return err
	}
	wallet, err := sdkLoadWalletFromCommand(cmd)
	if err != nil {
		return err
	}
	result, err := sdkLoginToNode(cmd.Context(), nodeURL, wallet)
	if err != nil {
		return err
	}
	return sdkPrintJSON(result)
}

func runSDKAuthAddCurrentWallet(cmd *cobra.Command, args []string) error {
	_ = args
	nodeURL, err := sdkRequiredFlag(cmd, "node")
	if err != nil {
		return err
	}
	trust, err := cmd.Flags().GetString("trust")
	if err != nil {
		return err
	}
	trust = strings.TrimSpace(trust)
	if trust != "admin" && trust != "trusted" && trust != "standard" {
		return fmt.Errorf("--trust must be admin, trusted, or standard")
	}
	wallet, err := sdkLoadWalletFromCommand(cmd)
	if err != nil {
		return err
	}
	cookie, err := sdkReadSessionCookie(nodeURL)
	if err != nil {
		return err
	}
	if cookie == "" {
		return fmt.Errorf("no session for %s; run spacedatanetwork auth login first", nodeURL)
	}
	result, err := sdkAddUploadUser(cmd.Context(), nodeURL, cookie, wallet, trust)
	if err != nil {
		return err
	}
	return sdkPrintJSON(result)
}

func runSDKModulePackage(cmd *cobra.Command, args []string) error {
	_ = args
	wallet, err := sdkLoadWalletFromCommand(cmd)
	if err != nil {
		return err
	}
	packaged, err := sdkPackageModuleFromCommand(cmd, wallet)
	if err != nil {
		return err
	}
	return sdkPrintJSON(map[string]any{
		"package_path":          packaged.PackagePath,
		"encrypted_bundle_path": packaged.EncryptedBundlePath,
		"module_id":             packaged.PackageFile.Metadata.ID,
		"version":               packaged.PackageFile.Metadata.Version,
		"bundle_sha256":         packaged.PackageFile.BundleSHA256,
		"signature_hex":         packaged.PackageFile.SignatureHex,
	})
}

func runSDKModuleUpload(cmd *cobra.Command, args []string) error {
	_ = args
	nodeURL, err := sdkRequiredFlag(cmd, "node")
	if err != nil {
		return err
	}
	packagePath, err := sdkRequiredFlag(cmd, "package")
	if err != nil {
		return err
	}
	wallet, err := sdkLoadWalletFromCommand(cmd)
	if err != nil {
		return err
	}
	cookie, err := sdkReadSessionCookie(nodeURL)
	if err != nil {
		return err
	}
	if cookie == "" {
		return fmt.Errorf("no session for %s; run spacedatanetwork auth login first", nodeURL)
	}
	result, err := sdkUploadModule(cmd.Context(), nodeURL, packagePath, cookie, wallet)
	if err != nil {
		return err
	}
	return sdkPrintJSON(result)
}

func runSDKModulePublish(cmd *cobra.Command, args []string) error {
	_ = args
	nodeURL, err := sdkRequiredFlag(cmd, "node")
	if err != nil {
		return err
	}
	wallet, err := sdkLoadWalletFromCommand(cmd)
	if err != nil {
		return err
	}
	packaged, err := sdkPackageModuleFromCommand(cmd, wallet)
	if err != nil {
		return err
	}
	cookie, err := sdkReadSessionCookie(nodeURL)
	if err != nil {
		return err
	}
	if cookie == "" {
		return fmt.Errorf("no session for %s; run spacedatanetwork auth login first", nodeURL)
	}
	uploadResult, err := sdkUploadModule(cmd.Context(), nodeURL, packaged.PackagePath, cookie, wallet)
	if err != nil {
		return err
	}
	return sdkPrintJSON(map[string]any{
		"package_path": packaged.PackagePath,
		"upload":       uploadResult,
	})
}

func runSDKModuleList(cmd *cobra.Command, args []string) error {
	_ = args
	nodeURL, err := sdkRequiredFlag(cmd, "node")
	if err != nil {
		return err
	}
	cookie, err := sdkReadSessionCookie(nodeURL)
	if err != nil {
		return err
	}
	if cookie == "" {
		return fmt.Errorf("no session for %s; run spacedatanetwork auth login first", nodeURL)
	}
	result, err := sdkListModules(cmd.Context(), nodeURL, cookie)
	if err != nil {
		return err
	}
	return sdkPrintJSON(result)
}

func runSDKModuleQuery(cmd *cobra.Command, args []string) error {
	_ = args
	wallet, err := sdkLoadWalletFromCommand(cmd)
	if err != nil {
		return err
	}
	nodeURL, err := sdkRequiredFlag(cmd, "node")
	if err != nil {
		return err
	}
	moduleID, err := sdkRequiredFlag(cmd, "module-id")
	if err != nil {
		return err
	}
	requesterDomain, err := sdkRequiredFlag(cmd, "requester-domain")
	if err != nil {
		return err
	}
	version, _ := cmd.Flags().GetString("version")
	timeoutMs, _ := cmd.Flags().GetInt64("requested-timeout-ms")
	result, err := sdkQueryModuleDelivery(cmd.Context(), sdkModuleQueryOptions{
		NodeURL:            nodeURL,
		ModuleID:           moduleID,
		ModuleVersion:      strings.TrimSpace(version),
		RequesterDomain:    requesterDomain,
		RequestedTimeoutMs: timeoutMs,
		Wallet:             wallet,
	})
	if err != nil {
		return err
	}
	return sdkPrintJSON(result)
}

func sdkLoadWalletFromCommand(cmd *cobra.Command) (*sdkLoadedWallet, error) {
	password, err := sdkPasswordFromEnv(cmd)
	if err != nil {
		return nil, err
	}
	if cmd.Flags().Lookup("wallet-wasm") != nil {
		wasmPath, err := sdkWalletRuntimePathFromCommand(cmd)
		if err != nil {
			return nil, err
		}
		return sdkLoadWallet(cmd.Context(), password, wasmPath)
	}
	wasmPath, err := sdkWalletWasmFlag(cmd, "wasm")
	if err != nil {
		return nil, err
	}
	return sdkLoadWallet(cmd.Context(), password, wasmPath)
}

func sdkPackageModuleFromCommand(cmd *cobra.Command, wallet *sdkLoadedWallet) (*sdkPackagedModule, error) {
	wasmPath, err := sdkRequiredFlag(cmd, "wasm")
	if err != nil {
		return nil, err
	}
	outDir, err := cmd.Flags().GetString("out")
	if err != nil {
		return nil, err
	}
	moduleID, err := sdkRequiredFlag(cmd, "module-id")
	if err != nil {
		return nil, err
	}
	version, err := sdkRequiredFlag(cmd, "version")
	if err != nil {
		return nil, err
	}
	allowedDomains, err := cmd.Flags().GetStringArray("allow-domain")
	if err != nil {
		return nil, err
	}
	requiredScope, _ := cmd.Flags().GetString("required-scope")
	contentType, _ := cmd.Flags().GetString("content-type")
	cacheControl, _ := cmd.Flags().GetString("cache-control")
	maxGrantTimeoutMs, _ := cmd.Flags().GetInt64("max-grant-timeout-ms")

	return sdkPackageModule(sdkPackageModuleOptions{
		WasmPath:          wasmPath,
		OutDir:            outDir,
		ModuleID:          moduleID,
		Version:           version,
		AllowedDomains:    allowedDomains,
		RequiredScope:     requiredScope,
		ContentType:       contentType,
		CacheControl:      cacheControl,
		MaxGrantTimeoutMs: maxGrantTimeoutMs,
		Wallet:            wallet,
	})
}

type sdkPackageModuleOptions struct {
	WasmPath          string
	OutDir            string
	ModuleID          string
	Version           string
	AllowedDomains    []string
	RequiredScope     string
	ContentType       string
	CacheControl      string
	MaxGrantTimeoutMs int64
	Wallet            *sdkLoadedWallet
}

func sdkCreateWallet(ctx context.Context, password, name, walletWasmPath string) (*sdkLoadedWallet, error) {
	if err := sdkValidatePassword(password); err != nil {
		return nil, err
	}
	name = strings.TrimSpace(name)
	if name == "" {
		name = "SDN CLI Wallet"
	}
	target := sdkWalletPath()
	if err := os.MkdirAll(sdkCLIHome(), 0700); err != nil {
		return nil, fmt.Errorf("create cli home: %w", err)
	}
	if _, err := os.Stat(target); err == nil {
		return nil, fmt.Errorf("wallet already exists at %s", target)
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("stat wallet: %w", err)
	}

	hw, err := sdkOpenHDWallet(ctx, walletWasmPath)
	if err != nil {
		return nil, err
	}
	defer hw.Close(ctx)
	entropy := make([]byte, 64)
	if _, err := cryptorand.Read(entropy); err != nil {
		return nil, fmt.Errorf("read entropy: %w", err)
	}
	if err := hw.InjectEntropy(ctx, entropy); err != nil {
		return nil, fmt.Errorf("inject entropy: %w", err)
	}
	seedPhrase, err := hw.GenerateMnemonic(ctx, 24)
	if err != nil {
		return nil, fmt.Errorf("generate mnemonic: %w", err)
	}
	identity, xpub, err := sdkIdentityFromMnemonic(ctx, hw, seedPhrase, 0)
	if err != nil {
		return nil, err
	}
	wallet, err := sdkPublicWalletFromIdentity(name, xpub, identity)
	if err != nil {
		return nil, err
	}
	walletFile, err := sdkEncryptWalletFile(wallet, sdkWalletSecretPayload{
		SeedPhrase: seedPhrase,
		Account:    0,
	}, password)
	if err != nil {
		return nil, err
	}
	if err := sdkWriteJSONFile(target, walletFile, 0600); err != nil {
		return nil, err
	}
	_ = os.Chmod(sdkCLIHome(), 0700)
	_ = os.Chmod(target, 0600)
	return wallet, nil
}

func sdkLoadWallet(ctx context.Context, password, walletWasmPath string) (*sdkLoadedWallet, error) {
	if err := sdkValidatePassword(password); err != nil {
		return nil, err
	}
	var walletFile sdkWalletFile
	if err := sdkReadJSONFile(sdkWalletPath(), &walletFile); err != nil {
		return nil, fmt.Errorf("failed to read SDN CLI wallet: %w", err)
	}
	if walletFile.Version != sdkCLIWalletVersion {
		return nil, fmt.Errorf("unsupported wallet version %d", walletFile.Version)
	}
	payload, err := sdkDecryptWalletFile(walletFile, password)
	if err != nil {
		return nil, fmt.Errorf("wallet password could not decrypt local wallet: %w", err)
	}

	hw, err := sdkOpenHDWallet(ctx, walletWasmPath)
	if err != nil {
		return nil, err
	}
	defer hw.Close(ctx)
	identity, xpub, err := sdkIdentityFromMnemonic(ctx, hw, payload.SeedPhrase, payload.Account)
	if err != nil {
		return nil, err
	}
	wallet, err := sdkPublicWalletFromIdentity(walletFile.Name, xpub, identity)
	if err != nil {
		return nil, err
	}
	if wallet.XPub != walletFile.XPub ||
		wallet.PeerID != walletFile.PeerID ||
		wallet.SigningPublicKeyHex != walletFile.SigningPublicKeyHex {
		return nil, fmt.Errorf("wallet metadata does not match decrypted identity")
	}
	return wallet, nil
}

func sdkOpenHDWallet(ctx context.Context, explicitPath string) (*wasm.HDWalletModule, error) {
	resolved, err := sdkResolveHDWalletWasmPath(explicitPath)
	if err != nil {
		return nil, err
	}
	hw, err := wasm.NewHDWalletModule(ctx, resolved)
	if err != nil {
		return nil, fmt.Errorf("failed to load HD wallet WASM: %w", err)
	}
	return hw, nil
}

func sdkIdentityFromMnemonic(
	ctx context.Context,
	hw *wasm.HDWalletModule,
	mnemonic string,
	account uint32,
) (*wasm.DerivedIdentity, string, error) {
	identity, err := hw.IdentityFromMnemonic(ctx, mnemonic, "", account)
	if err != nil {
		return nil, "", fmt.Errorf("derive identity: %w", err)
	}
	seed, err := hw.MnemonicToSeed(ctx, mnemonic, "")
	if err != nil {
		return nil, "", fmt.Errorf("derive seed: %w", err)
	}
	xpub, err := hw.DeriveXPub(ctx, seed, account)
	if err != nil {
		return nil, "", fmt.Errorf("derive xpub: %w", err)
	}
	return identity, xpub, nil
}

func sdkPublicWalletFromIdentity(name, xpub string, identity *wasm.DerivedIdentity) (*sdkLoadedWallet, error) {
	signingPub, err := identity.SigningPubKey.Raw()
	if err != nil {
		return nil, fmt.Errorf("export signing public key: %w", err)
	}
	return &sdkLoadedWallet{
		Name:                   strings.TrimSpace(name),
		Account:                identity.Account,
		XPub:                   xpub,
		PeerID:                 identity.PeerID.String(),
		SigningPublicKeyHex:    hex.EncodeToString(signingPub),
		EncryptionPublicKeyHex: hex.EncodeToString(identity.EncryptionPub),
		Identity:               identity,
	}, nil
}

func sdkEncryptWalletFile(wallet *sdkLoadedWallet, payload sdkWalletSecretPayload, password string) (sdkWalletFile, error) {
	var walletFile sdkWalletFile
	salt := make([]byte, 16)
	iv := make([]byte, 12)
	if _, err := cryptorand.Read(salt); err != nil {
		return walletFile, err
	}
	if _, err := cryptorand.Read(iv); err != nil {
		return walletFile, err
	}
	key := sdkDeriveWalletKey(password, salt, sdkCLIKDFIterations)
	block, err := aes.NewCipher(key)
	if err != nil {
		return walletFile, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return walletFile, err
	}
	plain, err := json.Marshal(payload)
	if err != nil {
		return walletFile, err
	}
	sealed := gcm.Seal(nil, iv, plain, nil)
	ciphertext := sealed[:len(sealed)-gcm.Overhead()]
	tag := sealed[len(sealed)-gcm.Overhead():]

	walletFile.Version = sdkCLIWalletVersion
	walletFile.Name = wallet.Name
	walletFile.Account = wallet.Account
	walletFile.XPub = wallet.XPub
	walletFile.PeerID = wallet.PeerID
	walletFile.SigningPublicKeyHex = wallet.SigningPublicKeyHex
	walletFile.EncryptionPublicKeyHex = wallet.EncryptionPublicKeyHex
	walletFile.KDF.Name = "pbkdf2-sha256"
	walletFile.KDF.Iterations = sdkCLIKDFIterations
	walletFile.KDF.Salt = sdkBase64URL(salt)
	walletFile.Cipher.Name = "aes-256-gcm"
	walletFile.Cipher.IV = sdkBase64URL(iv)
	walletFile.Cipher.Tag = sdkBase64URL(tag)
	walletFile.Cipher.Ciphertext = sdkBase64URL(ciphertext)
	walletFile.CreatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	return walletFile, nil
}

func sdkDecryptWalletFile(walletFile sdkWalletFile, password string) (sdkWalletSecretPayload, error) {
	var payload sdkWalletSecretPayload
	if walletFile.KDF.Name != "pbkdf2-sha256" {
		return payload, fmt.Errorf("unsupported wallet KDF")
	}
	if walletFile.Cipher.Name != "aes-256-gcm" {
		return payload, fmt.Errorf("unsupported wallet cipher")
	}
	if walletFile.KDF.Iterations < 100000 {
		return payload, fmt.Errorf("wallet KDF iterations are too low")
	}
	salt, err := sdkDecodeBase64URL(walletFile.KDF.Salt)
	if err != nil {
		return payload, err
	}
	iv, err := sdkDecodeBase64URL(walletFile.Cipher.IV)
	if err != nil {
		return payload, err
	}
	tag, err := sdkDecodeBase64URL(walletFile.Cipher.Tag)
	if err != nil {
		return payload, err
	}
	ciphertext, err := sdkDecodeBase64URL(walletFile.Cipher.Ciphertext)
	if err != nil {
		return payload, err
	}
	key := sdkDeriveWalletKey(password, salt, walletFile.KDF.Iterations)
	block, err := aes.NewCipher(key)
	if err != nil {
		return payload, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return payload, err
	}
	sealed := append(append([]byte(nil), ciphertext...), tag...)
	plain, err := gcm.Open(nil, iv, sealed, nil)
	if err != nil {
		return payload, err
	}
	if err := json.Unmarshal(plain, &payload); err != nil {
		return payload, err
	}
	if strings.TrimSpace(payload.SeedPhrase) == "" {
		return payload, fmt.Errorf("wallet payload missing seed phrase")
	}
	return payload, nil
}

func sdkPackageModule(options sdkPackageModuleOptions) (*sdkPackagedModule, error) {
	moduleID := strings.TrimSpace(options.ModuleID)
	version := strings.TrimSpace(options.Version)
	if moduleID == "" {
		return nil, fmt.Errorf("moduleId is required")
	}
	if version == "" {
		return nil, fmt.Errorf("version is required")
	}
	wasmBytes, err := os.ReadFile(filepath.Clean(options.WasmPath))
	if err != nil {
		return nil, fmt.Errorf("read wasm: %w", err)
	}
	contentKey := make([]byte, 32)
	iv := make([]byte, 12)
	if _, err := cryptorand.Read(contentKey); err != nil {
		return nil, fmt.Errorf("generate content key: %w", err)
	}
	if _, err := cryptorand.Read(iv); err != nil {
		return nil, fmt.Errorf("generate iv: %w", err)
	}
	encryptedBundleBytes, err := sdkEncryptAESGCM(contentKey, iv, wasmBytes)
	if err != nil {
		return nil, err
	}
	bundleDigest := sha256.Sum256(encryptedBundleBytes)
	signature, err := options.Wallet.Identity.Sign(bundleDigest[:])
	if err != nil {
		return nil, fmt.Errorf("sign bundle digest: %w", err)
	}
	localContentKeyEnvelope, err := sdkEncryptLocalContentKeyEnvelope(contentKey, options.Wallet)
	sdkZeroBytes(contentKey)
	if err != nil {
		return nil, fmt.Errorf("encrypt local content key envelope: %w", err)
	}

	slug := sdkModuleSlugPattern.ReplaceAllString(moduleID+"-"+version, "_")
	outDir, err := filepath.Abs(strings.TrimSpace(options.OutDir))
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(outDir, 0700); err != nil {
		return nil, fmt.Errorf("create output dir: %w", err)
	}
	encryptedBundlePath := filepath.Join(outDir, slug+".wasm.enc")
	packagePath := filepath.Join(outDir, slug+".sdn-module.json")
	metadata := sdkModulePackageMetadata{
		ID:                moduleID,
		Version:           version,
		RequiredScope:     strings.TrimSpace(options.RequiredScope),
		ContentType:       strings.TrimSpace(options.ContentType),
		CacheControl:      strings.TrimSpace(options.CacheControl),
		AllowedDomains:    sdkCleanStringSlice(options.AllowedDomains),
		MaxGrantTimeoutMs: options.MaxGrantTimeoutMs,
	}
	packageFile := sdkModulePackageFile{
		PackageVersion:          sdkCLIModulePackageVers,
		Metadata:                metadata,
		EncryptedBundlePath:     filepath.Base(encryptedBundlePath),
		LocalContentKeyEnvelope: localContentKeyEnvelope,
		SignatureHex:            hex.EncodeToString(signature),
		SignerPublicKeyHex:      options.Wallet.SigningPublicKeyHex,
		BundleSHA256:            hex.EncodeToString(bundleDigest[:]),
		SizeBytes:               len(encryptedBundleBytes),
		CreatedAt:               time.Now().UTC().Format(time.RFC3339Nano),
	}
	if err := os.WriteFile(encryptedBundlePath, encryptedBundleBytes, 0600); err != nil {
		return nil, fmt.Errorf("write encrypted bundle: %w", err)
	}
	if err := sdkWriteJSONFile(packagePath, packageFile, 0600); err != nil {
		return nil, fmt.Errorf("write module package: %w", err)
	}
	return &sdkPackagedModule{
		PackageFile:          packageFile,
		PackagePath:          packagePath,
		EncryptedBundlePath:  encryptedBundlePath,
		EncryptedBundleBytes: encryptedBundleBytes,
	}, nil
}

func sdkReadModulePackage(packagePath string) (*sdkPackagedModule, error) {
	resolvedPackagePath, err := filepath.Abs(packagePath)
	if err != nil {
		return nil, err
	}
	var packageFile sdkModulePackageFile
	if err := sdkReadJSONFile(resolvedPackagePath, &packageFile); err != nil {
		return nil, err
	}
	if packageFile.PackageVersion != sdkCLIModulePackageVers {
		return nil, fmt.Errorf("unsupported SDN module package version %d", packageFile.PackageVersion)
	}
	encryptedBundlePath := filepath.Join(filepath.Dir(resolvedPackagePath), packageFile.EncryptedBundlePath)
	encryptedBundleBytes, err := os.ReadFile(encryptedBundlePath)
	if err != nil {
		return nil, fmt.Errorf("read encrypted bundle: %w", err)
	}
	return &sdkPackagedModule{
		PackageFile:          packageFile,
		PackagePath:          resolvedPackagePath,
		EncryptedBundlePath:  encryptedBundlePath,
		EncryptedBundleBytes: encryptedBundleBytes,
	}, nil
}

func sdkUploadModule(ctx context.Context, nodeURL, packagePath, sessionCookie string, wallet *sdkLoadedWallet) (map[string]any, error) {
	_ = sessionCookie
	packaged, err := sdkReadModulePackage(packagePath)
	if err != nil {
		return nil, err
	}
	contentKey, err := sdkDecryptLocalContentKeyEnvelope(packaged.PackageFile.LocalContentKeyEnvelope, wallet)
	if err != nil {
		return nil, err
	}
	defer sdkZeroBytes(contentKey)
	return sdkUploadModuleOverProtocol(ctx, sdkNormalizeNodeOrigin(nodeURL), packaged, contentKey, wallet)
}

func sdkListModules(ctx context.Context, nodeURL, sessionCookie string) (map[string]any, error) {
	nodeOrigin := sdkNormalizeNodeOrigin(nodeURL)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, nodeOrigin+"/api/v1/plugin-modules", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Cookie", sessionCookie)
	req.Header.Set("X-Requested-With", "sdn-cli")
	return sdkDoJSON(req)
}

func sdkLoginToNode(ctx context.Context, nodeURL string, wallet *sdkLoadedWallet) (map[string]any, error) {
	nodeOrigin := sdkNormalizeNodeOrigin(nodeURL)
	challenge, err := sdkPostJSON(ctx, nodeOrigin+"/api/auth/challenge", map[string]any{
		"xpub":              wallet.XPub,
		"client_pubkey_hex": wallet.SigningPublicKeyHex,
		"ts":                time.Now().Unix(),
	})
	if err != nil {
		return nil, err
	}
	challengeID, _ := challenge["challenge_id"].(string)
	challengeValue, _ := challenge["challenge"].(string)
	if strings.TrimSpace(challengeID) == "" || strings.TrimSpace(challengeValue) == "" {
		return nil, fmt.Errorf("node auth response missing challenge_id or challenge")
	}
	challengeBytes, err := sdkDecodeLooseBase64(challengeValue)
	if err != nil {
		return nil, fmt.Errorf("decode challenge: %w", err)
	}
	signature, err := wallet.Identity.Sign(challengeBytes)
	if err != nil {
		return nil, fmt.Errorf("sign challenge: %w", err)
	}
	verify, err := sdkPostJSONWithHeaders(ctx, nodeOrigin+"/api/auth/verify", map[string]any{
		"challenge_id":      challengeID,
		"xpub":              wallet.XPub,
		"client_pubkey_hex": wallet.SigningPublicKeyHex,
		"challenge":         challengeValue,
		"signature_hex":     hex.EncodeToString(signature),
	})
	if err != nil {
		return nil, err
	}
	headers, _ := verify["_headers"].(http.Header)
	delete(verify, "_headers")
	cookie := sdkExtractSessionCookie(headers)
	if cookie == "" {
		return nil, fmt.Errorf("node auth verify response did not include sdn_wallet_session cookie")
	}
	if err := sdkWriteSessionCookie(nodeOrigin, cookie); err != nil {
		return nil, err
	}
	return map[string]any{
		"node_url":      nodeOrigin,
		"cookie_stored": true,
		"expires_at":    verify["expires_at"],
		"user":          verify["user"],
	}, nil
}

func sdkAddUploadUser(ctx context.Context, nodeURL, sessionCookie string, wallet *sdkLoadedWallet, trust string) (map[string]any, error) {
	nodeOrigin := sdkNormalizeNodeOrigin(nodeURL)
	body := map[string]any{
		"xpub":               wallet.XPub,
		"name":               wallet.Name,
		"trust_level":        trust,
		"signing_pubkey_hex": wallet.SigningPublicKeyHex,
	}
	result, status, err := sdkDoJSONWithStatus(ctx, http.MethodPost, nodeOrigin+"/api/auth/users", sessionCookie, body)
	if err != nil {
		return nil, err
	}
	if status == http.StatusConflict {
		result, _, err = sdkDoJSONWithStatus(ctx, http.MethodPut, nodeOrigin+"/api/auth/users/"+url.PathEscape(wallet.XPub), sessionCookie, body)
		if err != nil {
			return nil, err
		}
	}
	return result, nil
}

func sdkPostJSON(ctx context.Context, endpoint string, body map[string]any) (map[string]any, error) {
	result, err := sdkPostJSONWithHeaders(ctx, endpoint, body)
	if err != nil {
		return nil, err
	}
	delete(result, "_headers")
	return result, nil
}

func sdkPostJSONWithHeaders(ctx context.Context, endpoint string, body map[string]any) (map[string]any, error) {
	requestBytes, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(requestBytes))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	return sdkDoJSON(req)
}

func sdkDoJSONWithStatus(ctx context.Context, method, endpoint, sessionCookie string, body map[string]any) (map[string]any, int, error) {
	requestBytes, err := json.Marshal(body)
	if err != nil {
		return nil, 0, err
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, bytes.NewReader(requestBytes))
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Cookie", sessionCookie)
	req.Header.Set("X-Requested-With", "sdn-cli")
	result, status, err := sdkDoJSONStatus(req)
	delete(result, "_headers")
	return result, status, err
}

func sdkDoJSON(req *http.Request) (map[string]any, error) {
	result, _, err := sdkDoJSONStatus(req)
	delete(result, "_headers")
	return result, err
}

func sdkDoJSONStatus(req *http.Request) (map[string]any, int, error) {
	client := &http.Client{Timeout: 60 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	payload, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, resp.StatusCode, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		if resp.StatusCode == http.StatusConflict {
			return map[string]any{}, resp.StatusCode, nil
		}
		return nil, resp.StatusCode, fmt.Errorf("node request failed: %d %s", resp.StatusCode, strings.TrimSpace(string(payload)))
	}
	var result map[string]any
	if len(bytes.TrimSpace(payload)) > 0 {
		if err := json.Unmarshal(payload, &result); err != nil {
			return nil, resp.StatusCode, err
		}
	} else {
		result = map[string]any{}
	}
	result["_headers"] = resp.Header.Clone()
	return result, resp.StatusCode, nil
}

func sdkEncryptAESGCM(key, iv, plaintext []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	encrypted := gcm.Seal(nil, iv, plaintext, nil)
	result := make([]byte, 0, len(iv)+len(encrypted))
	result = append(result, iv...)
	result = append(result, encrypted...)
	return result, nil
}

func sdkEncryptLocalContentKeyEnvelope(contentKey []byte, wallet *sdkLoadedWallet) (sdkLocalContentKeyEnvelope, error) {
	var envelope sdkLocalContentKeyEnvelope
	key, err := sdkDeriveLocalContentKeyWrappingKey(wallet)
	if err != nil {
		return envelope, err
	}
	defer sdkZeroBytes(key)
	nonce := make([]byte, 12)
	if _, err := cryptorand.Read(nonce); err != nil {
		return envelope, err
	}
	ciphertext, err := sdkSealAESGCM(key, nonce, contentKey, nil)
	if err != nil {
		return envelope, err
	}
	return sdkLocalContentKeyEnvelope{
		Version:    1,
		Algorithm:  "AES-256-GCM",
		Nonce:      base64.RawURLEncoding.EncodeToString(nonce),
		Ciphertext: base64.RawURLEncoding.EncodeToString(ciphertext),
	}, nil
}

func sdkDecryptLocalContentKeyEnvelope(envelope sdkLocalContentKeyEnvelope, wallet *sdkLoadedWallet) ([]byte, error) {
	if envelope.Version != 1 || envelope.Algorithm != "AES-256-GCM" {
		return nil, fmt.Errorf("unsupported local content key envelope")
	}
	key, err := sdkDeriveLocalContentKeyWrappingKey(wallet)
	if err != nil {
		return nil, err
	}
	defer sdkZeroBytes(key)
	nonce, err := base64.RawURLEncoding.DecodeString(strings.TrimSpace(envelope.Nonce))
	if err != nil {
		return nil, fmt.Errorf("decode local content key nonce: %w", err)
	}
	ciphertext, err := base64.RawURLEncoding.DecodeString(strings.TrimSpace(envelope.Ciphertext))
	if err != nil {
		return nil, fmt.Errorf("decode local content key ciphertext: %w", err)
	}
	contentKey, err := sdkOpenAESGCM(key, nonce, ciphertext, nil)
	if err != nil {
		return nil, fmt.Errorf("decrypt local content key envelope: %w", err)
	}
	if len(contentKey) != 32 {
		sdkZeroBytes(contentKey)
		return nil, fmt.Errorf("local content key must be 32 bytes, got %d", len(contentKey))
	}
	return contentKey, nil
}

func sdkDeriveLocalContentKeyWrappingKey(wallet *sdkLoadedWallet) ([]byte, error) {
	if wallet == nil || wallet.Identity == nil || len(wallet.Identity.EncryptionKey) != 32 {
		return nil, fmt.Errorf("wallet encryption key is required")
	}
	h := sha256.New()
	_, _ = h.Write([]byte(sdkCLILocalContentKeyEnvelopeContext))
	_, _ = h.Write(wallet.Identity.EncryptionKey)
	return h.Sum(nil), nil
}

func sdkSealAESGCM(key, nonce, plaintext, aad []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	if len(nonce) != gcm.NonceSize() {
		return nil, fmt.Errorf("invalid gcm nonce length: expected %d, got %d", gcm.NonceSize(), len(nonce))
	}
	return gcm.Seal(nil, nonce, plaintext, aad), nil
}

func sdkOpenAESGCM(key, nonce, ciphertext, aad []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	if len(nonce) != gcm.NonceSize() {
		return nil, fmt.Errorf("invalid gcm nonce length: expected %d, got %d", gcm.NonceSize(), len(nonce))
	}
	return gcm.Open(nil, nonce, ciphertext, aad)
}

func sdkZeroBytes(b []byte) {
	for i := range b {
		b[i] = 0
	}
}

func sdkDeriveWalletKey(password string, salt []byte, iterations int) []byte {
	return pbkdf2.Key([]byte(password), salt, iterations, 32, sha256.New)
}

func sdkValidatePassword(password string) error {
	if len(password) < 8 {
		return fmt.Errorf("wallet password must be at least 8 characters")
	}
	return nil
}

func sdkPasswordFromEnv(cmd *cobra.Command) (string, error) {
	envName, err := cmd.Flags().GetString("password-env")
	if err != nil {
		return "", err
	}
	envName = strings.TrimSpace(envName)
	if envName == "" {
		envName = sdkCLIDefaultPassword
	}
	password := os.Getenv(envName)
	if password == "" {
		return "", fmt.Errorf("set %s or pass --password-env NAME", envName)
	}
	return password, nil
}

func sdkRequiredFlag(cmd *cobra.Command, name string) (string, error) {
	value, err := cmd.Flags().GetString(name)
	if err != nil {
		return "", err
	}
	value = strings.TrimSpace(value)
	if value == "" {
		return "", fmt.Errorf("missing --%s", name)
	}
	return value, nil
}

func sdkWalletWasmFlag(cmd *cobra.Command, name string) (string, error) {
	flag := cmd.Flags().Lookup(name)
	if flag == nil {
		return "", nil
	}
	return cmd.Flags().GetString(name)
}

func sdkWalletRuntimePathFromCommand(cmd *cobra.Command) (string, error) {
	walletWasm, err := sdkWalletWasmFlag(cmd, "wallet-wasm")
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(walletWasm) != "" {
		return walletWasm, nil
	}
	return sdkWalletWasmFlag(cmd, "wasm")
}

func sdkResolveHDWalletWasmPath(explicitPath string) (string, error) {
	candidates := make([]string, 0, 8)
	if trimmed := strings.TrimSpace(explicitPath); trimmed != "" {
		candidates = append(candidates, trimmed)
	}
	if envPath := strings.TrimSpace(os.Getenv("HD_WALLET_WASM_PATH")); envPath != "" {
		candidates = append(candidates, envPath)
	}
	candidates = append(candidates,
		"../../hd-wallet-wasm/build-wasi/wasm/hd-wallet.wasm",
		"../../hd-wallet-wasm/build-wasi/wasm/hd-wallet-wasi.wasm",
		"../../hd-wallet-wasm/build/wasm/hd-wallet.wasm",
		"../../hd-wallet-wasm/build/wasm/hd-wallet-wasi.wasm",
		"../../../../hd-wallet-wasm/build-wasi/wasm/hd-wallet.wasm",
		"../../../../hd-wallet-wasm/build-wasi/wasm/hd-wallet-wasi.wasm",
		"../../../../hd-wallet-wasm/build/wasm/hd-wallet.wasm",
		"../../../../hd-wallet-wasm/build/wasm/hd-wallet-wasi.wasm",
	)
	if exe, err := os.Executable(); err == nil {
		exeDir := filepath.Dir(exe)
		candidates = append(candidates,
			filepath.Join(exeDir, "hd-wallet-wasi.wasm"),
			filepath.Join(exeDir, "hd-wallet.wasm"),
		)
	}
	for _, candidate := range candidates {
		if candidate == "" {
			continue
		}
		resolved, err := filepath.Abs(candidate)
		if err != nil {
			continue
		}
		if stat, err := os.Stat(resolved); err == nil && !stat.IsDir() {
			return resolved, nil
		}
	}
	return "", fmt.Errorf("WASI-compatible hd-wallet.wasm not found (set --wallet-wasm or HD_WALLET_WASM_PATH)")
}

func sdkCLIHome() string {
	if configured := strings.TrimSpace(os.Getenv("SDN_CLI_HOME")); configured != "" {
		if abs, err := filepath.Abs(configured); err == nil {
			return abs
		}
		return configured
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ".spacedatanetwork/sdn-js"
	}
	return filepath.Join(home, ".spacedatanetwork", "sdn-js")
}

func sdkWalletPath() string {
	return filepath.Join(sdkCLIHome(), sdkCLIWalletFileName)
}

func sdkSessionPath() string {
	return filepath.Join(sdkCLIHome(), sdkCLISessionFileName)
}

func sdkReadSessionCookie(nodeURL string) (string, error) {
	sessions, err := sdkReadSessions()
	if err != nil {
		return "", err
	}
	return sessions[sdkNormalizeNodeOrigin(nodeURL)].Cookie, nil
}

func sdkWriteSessionCookie(nodeURL, cookie string) error {
	if err := os.MkdirAll(sdkCLIHome(), 0700); err != nil {
		return err
	}
	sessions, err := sdkReadSessions()
	if err != nil {
		return err
	}
	sessions[sdkNormalizeNodeOrigin(nodeURL)] = struct {
		Cookie    string `json:"cookie"`
		UpdatedAt string `json:"updated_at"`
	}{
		Cookie:    cookie,
		UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano),
	}
	return sdkWriteJSONFile(sdkSessionPath(), sessions, 0600)
}

func sdkReadSessions() (sdkSessionStore, error) {
	var sessions sdkSessionStore
	if err := sdkReadJSONFile(sdkSessionPath(), &sessions); err != nil {
		if os.IsNotExist(err) {
			return sdkSessionStore{}, nil
		}
		return nil, err
	}
	if sessions == nil {
		sessions = sdkSessionStore{}
	}
	return sessions, nil
}

func sdkWriteJSONFile(path string, value any, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(value); err != nil {
		return err
	}
	return os.WriteFile(path, buffer.Bytes(), mode)
}

func sdkReadJSONFile(path string, target any) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	return json.NewDecoder(file).Decode(target)
}

func sdkPrintJSON(value any) error {
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
}

func sdkNormalizeNodeOrigin(nodeURL string) string {
	parsed, err := url.Parse(strings.TrimSpace(nodeURL))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return strings.TrimRight(strings.TrimSpace(nodeURL), "/")
	}
	return parsed.Scheme + "://" + parsed.Host
}

func sdkExtractSessionCookie(headers http.Header) string {
	for _, value := range headers.Values("Set-Cookie") {
		for _, part := range strings.Split(value, ",") {
			part = strings.TrimSpace(part)
			if strings.HasPrefix(part, "sdn_wallet_session=") {
				return strings.TrimSpace(strings.SplitN(part, ";", 2)[0])
			}
		}
	}
	return ""
}

func sdkDecodeLooseBase64(value string) ([]byte, error) {
	normalized := strings.NewReplacer("-", "+", "_", "/").Replace(strings.TrimSpace(value))
	if rem := len(normalized) % 4; rem != 0 {
		normalized += strings.Repeat("=", 4-rem)
	}
	return base64.StdEncoding.DecodeString(normalized)
}

func sdkBase64URL(bytes []byte) string {
	return base64.RawURLEncoding.EncodeToString(bytes)
}

func sdkDecodeBase64URL(value string) ([]byte, error) {
	return base64.RawURLEncoding.DecodeString(strings.TrimSpace(value))
}

func sdkCleanStringSlice(values []string) []string {
	cleaned := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			cleaned = append(cleaned, value)
		}
	}
	return cleaned
}
