// Package main provides the entry point for the Space Data Network server.
// This is a specialized fork of IPFS (Kubo) tailored for space data standards.
package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	_ "embed"
	"encoding/base64"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/signal"
	"path"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	logging "github.com/ipfs/go-log/v2"
	libp2phost "github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/multiformats/go-multiaddr"
	qrgen "github.com/skip2/go-qrcode"
	"github.com/spf13/cobra"

	"github.com/spacedatanetwork/sdn-server/internal/adminui"
	"github.com/spacedatanetwork/sdn-server/internal/api"
	"github.com/spacedatanetwork/sdn-server/internal/bootstrap"
	"github.com/spacedatanetwork/sdn-server/internal/assetpin"
	"github.com/spacedatanetwork/sdn-server/internal/auth"
	"github.com/spacedatanetwork/sdn-server/internal/bundle"
	"github.com/spacedatanetwork/sdn-server/internal/config"
	"github.com/spacedatanetwork/sdn-server/internal/credstore"
	"github.com/spacedatanetwork/sdn-server/internal/directory"
	"github.com/spacedatanetwork/sdn-server/internal/epm"
	"github.com/spacedatanetwork/sdn-server/internal/flowrt"
	"github.com/spacedatanetwork/sdn-server/internal/flowrt/editor"
	"github.com/spacedatanetwork/sdn-server/internal/frontend"
	"github.com/spacedatanetwork/sdn-server/internal/gateway"
	"github.com/spacedatanetwork/sdn-server/internal/geoip"
	"github.com/spacedatanetwork/sdn-server/internal/keys"
	"github.com/spacedatanetwork/sdn-server/internal/license"
	"github.com/spacedatanetwork/sdn-server/internal/modulert"
	"github.com/spacedatanetwork/sdn-server/internal/node"
	"github.com/spacedatanetwork/sdn-server/internal/peers"
	"github.com/spacedatanetwork/sdn-server/internal/sds"
	nodestatus "github.com/spacedatanetwork/sdn-server/internal/status"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
	"github.com/spacedatanetwork/sdn-server/internal/storefront"
	"github.com/spacedatanetwork/sdn-server/internal/tlsmgr"
	"github.com/spacedatanetwork/sdn-server/internal/tor"
	sdnupdate "github.com/spacedatanetwork/sdn-server/internal/update"
	sdnvcard "github.com/spacedatanetwork/sdn-server/internal/vcard"
	"github.com/spacedatanetwork/sdn-server/internal/versioninfo"
	"github.com/spacedatanetwork/sdn-server/internal/wasm"
	"github.com/spacedatanetwork/sdn-server/plugins"
)

var (
	log              = logging.Logger("sdn")
	processStartTime = time.Now()
)

var errLocalSDNDaemonUnavailable = errors.New("local SDN daemon unavailable")

var rootCmd = &cobra.Command{
	Use:   "spacedatanetwork",
	Short: "Space Data Network - FlatBuffer-native P2P for space data",
	Long: `spacedatanetwork is a specialized fork of IPFS tailored for the Space Data Network.
It replaces generic content-addressed storage with FlatBuffer-native data handling
and SQLite-based structured storage, optimized for space data standards.`,
}

var daemonCmd = &cobra.Command{
	Use:   "daemon",
	Short: "Start the SDN daemon",
	Long:  `Start the Space Data Network daemon in full node mode.`,
	RunE:  runDaemon,
}

var initCmd = &cobra.Command{
	Use:   "init",
	Short: "Initialize SDN configuration",
	Long:  `Initialize the Space Data Network configuration and data directories.`,
	RunE:  runInit,
}

var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Print SDN version information",
	RunE: func(cmd *cobra.Command, args []string) error {
		fmt.Fprintf(cmd.OutOrStdout(), "version=%s\n", versioninfo.SuiteVersion)
		fmt.Fprintf(cmd.OutOrStdout(), "agent=%s\n", versioninfo.AgentVersion)
		return nil
	},
}

var configCmd = &cobra.Command{
	Use:   "config",
	Short: "Print the SDN configuration path",
	RunE: func(cmd *cobra.Command, args []string) error {
		path := strings.TrimSpace(configPath)
		if path == "" {
			path = config.DefaultPath()
		}
		fmt.Fprintln(cmd.OutOrStdout(), path)
		return nil
	},
}

var statusCmd = &cobra.Command{
	Use:   "status",
	Short: "Print local SDN daemon status",
	RunE: func(cmd *cobra.Command, args []string) error {
		return runStatus(cmd)
	},
}

var openCmd = &cobra.Command{
	Use:   "open",
	Short: "Print the local SDN UI URL",
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load(configPath)
		if err != nil {
			return err
		}
		fmt.Fprintln(cmd.OutOrStdout(), adminURL(cfg))
		return nil
	},
}

var reindexCmd = &cobra.Command{
	Use:   "reindex",
	Short: "Rebuild storage indexes for fast API queries",
	Long:  `Rebuilds the sdn_record_index table from existing schema records.`,
	RunE:  runReindex,
}

var deriveXPubCmd = &cobra.Command{
	Use:   "derive-xpub",
	Short: "Derive a BIP-32 xpub from a BIP-39 mnemonic",
	Long: `Derives the standard BIP-32 extended public key at m/44'/0'/0' from a BIP-39 mnemonic.
The resulting xpub can be pasted directly into config.yaml as the user's xpub field.
The Ed25519 signing key is bound on first wallet login (TOFU).`,
	RunE: runDeriveXPub,
}

var showIdentityCmd = &cobra.Command{
	Use:   "show-identity",
	Short: "Show the node's identity (PeerID, xpub, mnemonic)",
	Long: `Decrypts the stored mnemonic and derives the node's full identity:
PeerID, xpub, signing public key, and optionally the mnemonic phrase itself.

The mnemonic is only shown when --show-mnemonic is passed.
Password is resolved from SDN_KEY_PASSWORD env, config, or machine default.`,
	RunE: runShowIdentity,
}

var identityCmd = &cobra.Command{
	Use:   "identity",
	Short: "Inspect and export the node's public identity records",
}

var identityExportCmd = &cobra.Command{
	Use:   "export",
	Short: "Print the node's public EPM contact record",
	RunE:  runIdentityExport,
}

var (
	configPath           string
	listenAddr           string
	debug                bool
	wasmPath             string
	showMnemonic         bool
	identityExportFormat string
	identityExportOutput string
)

func init() {
	rootCmd.PersistentFlags().StringVarP(&configPath, "config", "c", "", "config file path")
	rootCmd.PersistentFlags().BoolVarP(&debug, "debug", "d", false, "enable debug logging")

	daemonCmd.Flags().StringVarP(&listenAddr, "listen", "l", "", "override listen address")
	deriveXPubCmd.Flags().StringVar(&wasmPath, "wasm", "", "path to hd-wallet-wasi.wasm (default: $HD_WALLET_WASM_PATH or ../../hd-wallet-wasm/build-wasi/wasm/hd-wallet-wasi.wasm)")
	initCmd.Flags().StringVar(&wasmPath, "wasm", "", "path to hd-wallet-wasi.wasm")
	showIdentityCmd.Flags().BoolVar(&showMnemonic, "show-mnemonic", false, "display the decrypted mnemonic phrase (SENSITIVE)")
	showIdentityCmd.Flags().StringVar(&wasmPath, "wasm", "", "path to hd-wallet-wasi.wasm")
	identityExportCmd.Flags().StringVar(&identityExportFormat, "format", "text", "output format: text, json, csv, flatbuffer, qrcode")
	identityExportCmd.Flags().StringVarP(&identityExportOutput, "output", "o", "", "write FlatBuffer output to path")
	identityCmd.AddCommand(identityExportCmd)

	rootCmd.AddCommand(daemonCmd)
	rootCmd.AddCommand(initCmd)
	rootCmd.AddCommand(statusCmd)
	rootCmd.AddCommand(openCmd)
	rootCmd.AddCommand(versionCmd)
	rootCmd.AddCommand(configCmd)
	rootCmd.AddCommand(reindexCmd)
	rootCmd.AddCommand(deriveXPubCmd)
	rootCmd.AddCommand(showIdentityCmd)
	rootCmd.AddCommand(identityCmd)
	rootCmd.AddCommand(searchCmd)
	rootCmd.AddCommand(providersCmd)
	rootCmd.AddCommand(conjunctionCmd)
}

func main() {
	if debug {
		logging.SetAllLoggers(logging.LevelDebug)
	} else {
		logging.SetAllLoggers(logging.LevelInfo)
	}

	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func runStatus(cmd *cobra.Command) error {
	cfg, err := config.Load(configPath)
	if err != nil {
		return err
	}
	out := cmd.OutOrStdout()
	baseURL := adminURL(cfg)
	fmt.Fprintf(out, "admin_url=%s\n", baseURL)
	return writeDaemonStatus(cmd.Context(), out, baseURL)
}

func writeDaemonStatus(ctx context.Context, out io.Writer, baseURL string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(baseURL, "/")+"/api/v1/data/health", nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")

	resp, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
	if err != nil {
		fmt.Fprintln(out, "daemon_status=unavailable")
		fmt.Fprintln(out, "data_health=unknown")
		return nil
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		fmt.Fprintln(out, "daemon_status=unhealthy")
		fmt.Fprintln(out, "data_health=unhealthy")
		fmt.Fprintf(out, "data_status=%s\n", resp.Status)
		return nil
	}

	var health struct {
		Healthy bool           `json:"healthy"`
		Details map[string]any `json:"details"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&health); err != nil {
		fmt.Fprintln(out, "daemon_status=unhealthy")
		fmt.Fprintln(out, "data_health=unhealthy")
		fmt.Fprintf(out, "data_message=%s\n", strings.ReplaceAll(err.Error(), "\n", " "))
		return nil
	}

	if health.Healthy {
		fmt.Fprintln(out, "daemon_status=running")
		fmt.Fprintln(out, "data_health=healthy")
	} else {
		fmt.Fprintln(out, "daemon_status=unhealthy")
		fmt.Fprintln(out, "data_health=unhealthy")
	}
	writeStatusDetail(out, "data_runtime", health.Details["runtime"])
	writeStatusDetail(out, "data_message", health.Details["message"])
	return nil
}

func writeStatusDetail(out io.Writer, key string, value any) {
	text, ok := value.(string)
	if !ok {
		return
	}
	if text = strings.TrimSpace(text); text == "" {
		return
	}
	fmt.Fprintf(out, "%s=%s\n", key, strings.ReplaceAll(text, "\n", " "))
}

func runIdentityExport(cmd *cobra.Command, args []string) error {
	cfg, err := config.Load(configPath)
	if err != nil {
		return err
	}
	return exportIdentityWithLocalFallback(cmd.Context(), cmd.OutOrStdout(), cfg, identityExportFormat, identityExportOutput)
}

func exportIdentity(ctx context.Context, out io.Writer, baseURL string, format string) error {
	return exportIdentityWithOutput(ctx, out, baseURL, format, "")
}

func exportIdentityWithLocalFallback(ctx context.Context, out io.Writer, cfg *config.Config, format, outputPath string) error {
	daemonErr := exportIdentityWithOutput(ctx, out, adminURL(cfg), format, outputPath)
	if daemonErr == nil {
		return nil
	}
	if !errors.Is(daemonErr, errLocalSDNDaemonUnavailable) {
		return daemonErr
	}
	if localErr := exportLocalIdentity(ctx, out, cfg, format, outputPath); localErr == nil {
		return nil
	}
	return daemonErr
}

func exportIdentityWithOutput(ctx context.Context, out io.Writer, baseURL string, format string, outputPath string) error {
	switch normalizeIdentityExportFormat(format) {
	case "text":
		vcardBytes, err := fetchLocalIdentityEndpoint(ctx, baseURL, "/api/node/epm/vcard")
		if err != nil {
			return err
		}
		_, err = out.Write(ensureTrailingNewline(vcardBytes))
		return err
	case "json":
		jsonBytes, err := fetchLocalIdentityEndpoint(ctx, baseURL, "/api/node/epm/json")
		if err != nil {
			return err
		}
		return writeIndentedJSON(out, jsonBytes)
	case "csv":
		jsonBytes, err := fetchLocalIdentityEndpoint(ctx, baseURL, "/api/node/epm/json")
		if err != nil {
			return err
		}
		return writeIdentityCSV(out, jsonBytes)
	case "flatbuffer":
		epmBytes, err := fetchLocalIdentityEndpoint(ctx, baseURL, "/api/node/epm")
		if err != nil {
			return err
		}
		return writeIdentityFlatBufferOutput(out, epmBytes, outputPath)
	case "qrcode":
		vcardBytes, err := fetchLocalIdentityEndpoint(ctx, baseURL, "/api/node/epm/vcard")
		if err != nil {
			return err
		}
		qr, err := qrgen.New(string(vcardBytes), qrgen.Medium)
		if err != nil {
			return fmt.Errorf("encode EPM vCard QR code: %w", err)
		}
		_, err = io.WriteString(out, qr.ToSmallString(false))
		return err
	default:
		return fmt.Errorf("unsupported identity export format %q (use text, json, csv, flatbuffer, or qrcode)", format)
	}
}

func exportLocalIdentity(ctx context.Context, out io.Writer, cfg *config.Config, format string, outputPath string) error {
	if cfg == nil {
		return fmt.Errorf("config is required")
	}
	validator, err := sds.NewValidator(nil)
	if err != nil {
		return fmt.Errorf("failed to initialize schema validator: %w", err)
	}
	store, err := openStoreForReading(cfg.Storage.Path, validator)
	if err != nil {
		return err
	}
	defer store.Close()

	records, err := store.QueryRawRecords(storage.RawRecordQuery{
		SchemaName: "EPM.fbs",
		ProviderID: "local-node",
		SourceName: "local-epm",
		BatchID:    "local",
		Limit:      1,
	})
	if err != nil {
		return fmt.Errorf("load local public EPM identity: %w", err)
	}
	if len(records) == 0 || len(records[0].Data) == 0 {
		return fmt.Errorf("no local public EPM identity found; run spacedatanetwork identity wizard or start the daemon once")
	}
	return writeLocalIdentityExport(ctx, out, records[0].Data, records[0].PeerID, format, outputPath)
}

func writeLocalIdentityExport(ctx context.Context, out io.Writer, epmBytes []byte, peerID string, format string, outputPath string) error {
	switch normalizeIdentityExportFormat(format) {
	case "text":
		vcardText, err := sdnvcard.EPMToVCard(epmBytes)
		if err != nil {
			return fmt.Errorf("build EPM vCard: %w", err)
		}
		_, err = io.WriteString(out, string(ensureTrailingNewline([]byte(vcardText))))
		return err
	case "json":
		epmJSON, err := epm.DirectoryRecordJSONFromEPM(epmBytes, peerID)
		if err != nil {
			return fmt.Errorf("build EPM directory JSON: %w", err)
		}
		jsonBytes, err := json.Marshal(epmJSON)
		if err != nil {
			return fmt.Errorf("encode EPM JSON: %w", err)
		}
		return writeIndentedJSON(out, jsonBytes)
	case "csv":
		epmJSON, err := epm.DirectoryRecordJSONFromEPM(epmBytes, peerID)
		if err != nil {
			return fmt.Errorf("build EPM directory JSON: %w", err)
		}
		jsonBytes, err := json.Marshal(epmJSON)
		if err != nil {
			return fmt.Errorf("encode EPM JSON: %w", err)
		}
		return writeIdentityCSV(out, jsonBytes)
	case "flatbuffer":
		return writeIdentityFlatBufferOutput(out, epmBytes, outputPath)
	case "qrcode":
		vcardText, err := sdnvcard.EPMToVCard(epmBytes)
		if err != nil {
			return fmt.Errorf("build EPM vCard: %w", err)
		}
		qr, err := qrgen.New(vcardText, qrgen.Medium)
		if err != nil {
			return fmt.Errorf("encode EPM vCard QR code: %w", err)
		}
		_, err = io.WriteString(out, qr.ToSmallString(false))
		return err
	default:
		return fmt.Errorf("unsupported identity export format %q (use text, json, csv, flatbuffer, or qrcode)", format)
	}
}

func normalizeIdentityExportFormat(format string) string {
	switch strings.ToLower(strings.TrimSpace(format)) {
	case "", "text", "vcard", "vcf":
		return "text"
	case "json":
		return "json"
	case "csv":
		return "csv"
	case "flatbuffer", "fbs", "epm":
		return "flatbuffer"
	case "qr", "qrcode", "qr-code":
		return "qrcode"
	default:
		return strings.ToLower(strings.TrimSpace(format))
	}
}

func fetchLocalIdentityEndpoint(ctx context.Context, baseURL string, endpoint string) ([]byte, error) {
	requestURL := strings.TrimRight(baseURL, "/") + endpoint
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: connect to local SDN daemon at %s: %v", errLocalSDNDaemonUnavailable, baseURL, err)
	}
	defer resp.Body.Close()
	body, readErr := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		detail := strings.TrimSpace(string(body))
		if detail == "" {
			detail = resp.Status
		}
		return nil, fmt.Errorf("local SDN daemon returned %s for %s: %s", resp.Status, endpoint, detail)
	}
	if readErr != nil {
		return nil, readErr
	}
	return body, nil
}

func writeIndentedJSON(out io.Writer, data []byte) error {
	var payload any
	if err := json.Unmarshal(data, &payload); err != nil {
		return fmt.Errorf("decode EPM JSON: %w", err)
	}
	encoder := json.NewEncoder(out)
	encoder.SetIndent("", "  ")
	return encoder.Encode(payload)
}

func writeIdentityCSV(out io.Writer, data []byte) error {
	var payload map[string]any
	if err := json.Unmarshal(data, &payload); err != nil {
		return fmt.Errorf("decode EPM JSON: %w", err)
	}
	fields := []string{
		"peer_id",
		"dn",
		"legal_name",
		"xpub",
		"bitcoin_address",
		"ethereum_address",
		"solana_address",
		"signing_pubkey_hex",
		"encryption_pubkey_hex",
		"epm_cid",
	}
	writer := csv.NewWriter(out)
	if err := writer.Write(fields); err != nil {
		return err
	}
	row := make([]string, 0, len(fields))
	for _, field := range fields {
		row = append(row, identityCSVString(payload[field]))
	}
	if err := writer.Write(row); err != nil {
		return err
	}
	writer.Flush()
	return writer.Error()
}

func identityCSVString(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case nil:
		return ""
	default:
		raw, err := json.Marshal(typed)
		if err != nil {
			return fmt.Sprint(typed)
		}
		return string(raw)
	}
}

func ensureTrailingNewline(data []byte) []byte {
	if len(data) == 0 || data[len(data)-1] == '\n' {
		return data
	}
	return append(append([]byte(nil), data...), '\n')
}

func adminURL(cfg *config.Config) string {
	addr := "127.0.0.1:5001"
	scheme := "http"
	if cfg != nil {
		if strings.TrimSpace(cfg.Admin.ListenAddr) != "" {
			addr = cfg.Admin.ListenAddr
		}
		if cfg.Admin.EffectiveTLSMode() != tlsmgr.ModeDisabled {
			scheme = "https"
		}
	}
	return fmt.Sprintf("%s://%s/", scheme, addr)
}

func channelHandlerOptionsForIdentity(identity *wasm.DerivedIdentity) api.ChannelHandlerOptions {
	if identity == nil || len(identity.EncryptionKey) != 32 {
		return api.ChannelHandlerOptions{}
	}
	return api.ChannelHandlerOptions{
		EncryptedStreams: api.NewFlatBuffersEncryptedNativeStreamDecryptor(identity.EncryptionKey),
	}
}

func applyBundleDefaults(cfg *config.Config, layout bundle.Layout) {
	if cfg == nil || layout.Root == "" {
		return
	}
	if strings.TrimSpace(cfg.Admin.FrontendPath) == "" && pathExists(layout.SDNUIPath) {
		cfg.Admin.FrontendPath = layout.SDNUIPath
	}
	if strings.TrimSpace(cfg.Admin.WebuiPath) == "" && pathExists(layout.WebUIPath) {
		cfg.Admin.WebuiPath = layout.WebUIPath
	}
}

func pathExists(pathValue string) bool {
	if strings.TrimSpace(pathValue) == "" {
		return false
	}
	_, err := os.Stat(pathValue)
	return err == nil
}

func resolveHDWalletWasmPath() (string, error) {
	return resolveHDWalletWasmPathFromInputs(
		strings.TrimSpace(wasmPath),
		strings.TrimSpace(os.Getenv("HD_WALLET_WASM_PATH")),
		bundle.ResolveCurrent(),
		defaultHDWalletWasmCandidates(),
	)
}

func resolveHDWalletWasmPathFromInputs(explicit, envPath string, layout bundle.Layout, candidates []string) (string, error) {
	if explicit != "" {
		if pathExists(explicit) {
			return explicit, nil
		}
		return "", fmt.Errorf("hd-wallet-wasi.wasm not found at %q", explicit)
	}
	if envPath != "" {
		if pathExists(envPath) {
			return envPath, nil
		}
		return "", fmt.Errorf("hd-wallet-wasi.wasm not found at %q from HD_WALLET_WASM_PATH", envPath)
	}
	if pathExists(layout.HDWalletWASM) {
		return layout.HDWalletWASM, nil
	}
	for _, candidate := range candidates {
		if pathExists(candidate) {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("hd-wallet-wasi.wasm not found; set --wasm or HD_WALLET_WASM_PATH")
}

func defaultHDWalletWasmCandidates() []string {
	return []string{
		"sdn-js/node_modules/hd-wallet-wasm/dist/hd-wallet-wasi.wasm",
		"node_modules/hd-wallet-wasm/dist/hd-wallet-wasi.wasm",
		"../../sdn-js/node_modules/hd-wallet-wasm/dist/hd-wallet-wasi.wasm",
		"../../../sdn-js/node_modules/hd-wallet-wasm/dist/hd-wallet-wasi.wasm",
		"../../hd-wallet-wasm/build-wasi/wasm/hd-wallet-wasi.wasm",
		"../hd-wallet-wasm/build-wasi/wasm/hd-wallet-wasi.wasm",
		"/opt/spacedatanetwork/wasm/hd-wallet-wasi.wasm",
		"/usr/local/lib/hd-wallet-wasi.wasm",
	}
}

func validateAssetPinPreNodeConfig(cfg *config.Config) error {
	if cfg == nil {
		return errors.New("daemon configuration is required")
	}
	if cfg.AssetPins.Enabled && !cfg.Admin.Enabled {
		return errors.New("asset pin capability requires the admin listener to be enabled")
	}
	return nil
}

func validateAssetPinAdminUIAvailability(cfg *config.Config, available bool) error {
	if cfg == nil {
		return errors.New("daemon configuration is required")
	}
	if cfg.AssetPins.Enabled && !available {
		return errors.New("asset pin capability requires an available admin HTTP surface")
	}
	return nil
}

func runDaemon(cmd *cobra.Command, args []string) error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	updateShutdown := make(chan struct{}, 1)

	// Load configuration
	cfg, err := config.Load(configPath)
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}
	if err := validateAssetPinPreNodeConfig(cfg); err != nil {
		return err
	}

	// Override listen address if specified
	if listenAddr != "" {
		cfg.Network.Listen = []string{listenAddr}
	}

	// Allow environment variable overrides for paths commonly set via systemd env files
	if cfg.Admin.WalletUIPath == "" {
		if envPath := os.Getenv("SDN_WALLET_UI_PATH"); envPath != "" {
			cfg.Admin.WalletUIPath = envPath
		}
	}
	if cfg.Admin.AdminUIPath == "" {
		if envPath := os.Getenv("SDN_ADMIN_UI_PATH"); envPath != "" {
			cfg.Admin.AdminUIPath = envPath
		}
	}
	if cfg.Admin.WebuiPath == "" {
		if envPath := os.Getenv("SDN_WEBUI_PATH"); envPath != "" {
			cfg.Admin.WebuiPath = envPath
		}
	}
	if cfg.Admin.IPFSAPIURL == "" {
		if envURL := os.Getenv("SDN_IPFS_API_URL"); envURL != "" {
			cfg.Admin.IPFSAPIURL = envURL
		}
	}
	if cfg.Admin.IPFSGatewayURL == "" {
		if envURL := os.Getenv("SDN_IPFS_GATEWAY_URL"); envURL != "" {
			cfg.Admin.IPFSGatewayURL = envURL
		}
	}
	if envPath := os.Getenv("SDN_FRONTEND_PATH"); envPath != "" {
		cfg.Admin.FrontendPath = envPath
	}
	layout := bundle.ResolveCurrent()
	applyBundleDefaults(cfg, layout)
	// Resolve empty frontend path to the built SDN Svelte UI when available,
	// then fall back to the managed frontend directory.
	cfg.Admin.FrontendPath = resolveFrontendPath(cfg.Admin.FrontendPath)
	if cfg.Admin.FrontendPath == "" {
		cfg.Admin.FrontendPath = config.DefaultFrontendPath()
	}
	// Auto-provision frontend directory with default page if it doesn't exist
	if err := provisionFrontendDir(cfg.Admin.FrontendPath); err != nil {
		log.Warnf("Could not provision frontend directory %q: %v", cfg.Admin.FrontendPath, err)
	}

	// Create and start the node
	n, err := node.New(ctx, cfg)
	if err != nil {
		return fmt.Errorf("failed to create node: %w", err)
	}
	var assetRetainer *assetpin.Retainer
	var assetMutationGate *assetpin.MutationGate
	if cfg.AssetPins.Enabled {
		assetMutationGate = assetpin.NewMutationGate()
		if n.Store() == nil || strings.TrimSpace(n.Store().Path()) == "" {
			return errors.New("asset retention requires local storage")
		}
		kuboURL, err := canonicalAssetPinKuboAPIURL(cfg.Admin.IPFSAPIURL)
		if err != nil {
			return fmt.Errorf("configure asset retention Kubo client: %w", err)
		}
		retentionPins, err := assetpin.NewKuboRetentionClient(kuboURL)
		if err != nil {
			return fmt.Errorf("configure asset retention Kubo client: %w", err)
		}
		recoveryStore, err := assetpin.NewFileAssetPinRecoveryStore(filepath.Dir(n.Store().Path()))
		if err != nil {
			return fmt.Errorf("configure asset pin recovery reconciliation: %w", err)
		}
		assetRetainer, err = assetpin.NewRetainer(assetpin.RetainerOptions{
			Store:    n.Store(),
			Pins:     retentionPins,
			Recovery: recoveryStore,
			Gate:     assetMutationGate,
		})
		if err != nil {
			return fmt.Errorf("configure asset retention: %w", err)
		}
	}

	torStartTimeout := 30 * time.Second
	if raw := strings.TrimSpace(cfg.Tor.StartTimeout); raw != "" {
		if parsed, parseErr := time.ParseDuration(raw); parseErr != nil {
			log.Warnf("Invalid tor.start_timeout %q, using %s", raw, torStartTimeout)
		} else {
			torStartTimeout = parsed
		}
	}

	hiddenServiceTarget := strings.TrimSpace(cfg.Tor.HiddenServiceTarget)
	if hiddenServiceTarget == "" {
		hiddenServiceTarget = cfg.Admin.ListenAddr
	}
	if strings.TrimSpace(hiddenServiceTarget) == "" {
		hiddenServiceTarget = "127.0.0.1:5001"
	}
	hiddenServicePort := cfg.Tor.HiddenServicePort
	if hiddenServicePort <= 0 {
		if cfg.Admin.EffectiveTLSMode() != tlsmgr.ModeDisabled {
			hiddenServicePort = 443
		} else {
			hiddenServicePort = 80
		}
	}

	torRuntime, err := tor.Start(ctx, tor.StartOptions{
		Enabled:                 cfg.Tor.Enabled,
		BinaryPath:              cfg.Tor.BinaryPath,
		StoragePath:             cfg.Storage.Path,
		DataDir:                 cfg.Tor.DataDir,
		SocksAddress:            cfg.Tor.SocksAddress,
		StartTimeout:            torStartTimeout,
		HiddenServiceEnabled:    cfg.Admin.Enabled && cfg.Tor.HiddenServiceEnabled,
		HiddenServicePort:       hiddenServicePort,
		HiddenServiceTarget:     hiddenServiceTarget,
		NodeIdentityKeyMaterial: n.IdentityKeyMaterial(),
	})
	if err != nil {
		return fmt.Errorf("failed to start tor runtime: %w", err)
	}
	if torRuntime != nil {
		defer func() {
			stopCtx, cancelStop := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancelStop()
			if stopErr := torRuntime.Stop(stopCtx); stopErr != nil {
				log.Warnf("TOR shutdown error: %v", stopErr)
			}
		}()

		if err := torRuntime.ApplyHTTPProxy(cfg.Tor.BypassLocalAddresses); err != nil {
			return fmt.Errorf("failed to apply tor proxy settings: %w", err)
		}
		log.Infof("Outbound HTTP proxying enabled via TOR (%s)", torRuntime.ProxyURL())

		if epmSvc := n.EPMService(); epmSvc != nil && torRuntime.OnionHost() != "" {
			useTLS := cfg.Admin.EffectiveTLSMode() != tlsmgr.ModeDisabled || hiddenServicePort == 443
			if err := epmSvc.SetRuntimeAddresses([]string{torRuntime.OnionURL(useTLS)}); err != nil {
				log.Warnf("Failed to inject onion metadata into EPM: %v", err)
			}
		}
	}

	log.Info("Starting Space Data Network daemon...")
	if err := n.Start(ctx); err != nil {
		return fmt.Errorf("failed to start node: %w", err)
	}

	// Print node info
	log.Infof("Peer ID: %s", n.PeerID())
	for _, addr := range n.ListenAddrs() {
		log.Infof("Listening on: %s", addr)
	}

	// Start admin server if enabled
	var adminServer *http.Server
	var httpChallengeServer *http.Server
	var localPublishServer *http.Server
	var authHandler *auth.Handler
	var storefrontSvc *storefront.Service
	var storefrontStore *storefront.Store
	var storefrontDelivery *storefront.DeliveryService
	if cfg.Admin.Enabled {
		var (
			adminUIHandler http.Handler
			legacyAdminUI  *peers.AdminUI
		)
		if adminUIPath := resolveAdminUIPath(cfg.Admin.AdminUIPath); adminUIPath != "" {
			host, hostErr := adminui.NewHost(adminUIPath)
			if hostErr != nil {
				log.Warnf("Failed to create hosted admin UI from %q: %v", adminUIPath, hostErr)
			} else {
				adminUIHandler = host
				log.Infof("Hosted admin UI available at /admin from %s", adminUIPath)
			}
		}
		if adminUIHandler == nil {
			legacyAdminUI, err = peers.NewAdminUI(n.PeerRegistry(), n.PeerGater())
			if err != nil {
				log.Warnf("Failed to create legacy admin UI: %v", err)
			} else {
				adminUIHandler = legacyAdminUI
				log.Warn("Falling back to legacy inline admin UI because no hosted admin build was found")
			}
		}
		if err := validateAssetPinAdminUIAvailability(cfg, adminUIHandler != nil); err != nil {
			return err
		}
		if adminUIHandler == nil {
			log.Warn("Admin UI disabled because no hosted or legacy admin handler could be created")
		} else {
			adminAddr := cfg.Admin.ListenAddr
			if adminAddr == "" {
				adminAddr = "127.0.0.1:5001"
			}
			tlsManager, err := tlsmgr.New(cfg.Admin)
			if err != nil {
				return fmt.Errorf("configure admin tls: %w", err)
			}
			adminTLS := tlsManager.UsesNativeTLS()

			if tlsManager.Mode() == tlsmgr.ModeManaged {
				identity := n.Identity()
				if identity == nil {
					return fmt.Errorf("managed tls requires an HD wallet-derived node identity")
				}
				info := identity.Info()
				bootstrapHosts := make([]string, 0, 1)
				host, _, splitErr := net.SplitHostPort(adminAddr)
				if splitErr == nil && host != "" {
					bootstrapHosts = append(bootstrapHosts, host)
				}
				if err := tlsManager.ConfigureBootstrap(tlsmgr.BootstrapIdentityInput{
					PeerID:                     info.PeerID,
					EncryptionPath:             info.EncryptionKeyPath,
					EncryptionX25519PublicKey:  append([]byte(nil), identity.EncryptionPub...),
					EncryptionProofEd25519Seed: append([]byte(nil), identity.EncryptionKey...),
					Hosts:                      bootstrapHosts,
				}); err != nil {
					return fmt.Errorf("configure bootstrap tls: %w", err)
				}
			}

			adminScheme := "http"
			if adminTLS {
				adminScheme = "https"
			}
			adminMux := http.NewServeMux()
			var wsUpgradeProxy http.Handler
			assetOIDCCapabilityMounted := false

			if cfg.AssetPins.Enabled {
				assetCapability, err := composeAssetPinCapability(
					ctx,
					n.Store(),
					cfg.Admin.IPFSAPIURL,
					cfg.AssetPins,
					assetMutationGate,
					defaultAssetPinCapabilityDependencies(),
				)
				if err != nil {
					return fmt.Errorf("configure asset pin capability: %w", err)
				}
				registerAssetPinCapabilityRoutes(adminMux, assetCapability.Handler)
				assetOIDCCapabilityMounted = true
				if assetCapability.HealthErr != nil {
					log.Errorf("Asset pin capability health error: %v", assetCapability.HealthErr)
				} else {
					log.Infof("GitHub OIDC asset capability available at %s://%s/api/v1/assets/pin", adminScheme, adminAddr)
				}
			}

			if adminTLS {
				listenAddrStrings := make([]string, 0, len(n.ListenAddrs()))
				for _, addr := range n.ListenAddrs() {
					listenAddrStrings = append(listenAddrStrings, addr.String())
				}
				if wsTarget, sourceAddr := resolveLocalLibp2pWsProxyTarget(listenAddrStrings); wsTarget != nil {
					wsProxy := httputil.NewSingleHostReverseProxy(wsTarget)
					wsProxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
						http.Error(w, "upstream libp2p websocket unavailable", http.StatusBadGateway)
					}
					wsUpgradeProxy = wsProxy
					log.Infof(
						"Proxying secure websocket upgrades to local libp2p transport (%s -> %s)",
						sourceAddr,
						wsTarget.String(),
					)
				} else {
					log.Warn("Admin TLS enabled but no local /ws libp2p listen address was discovered; secure browser key exchange may fail")
				}
			}

			// Plugin routes
			if n.PluginManager() != nil {
				n.PluginManager().RegisterRoutes(adminMux)
			}
			// Native data API routes (health/summary + the datasync sync
			// surface). Record retrieval (/api/v1/data/omm/bulk etc.) is
			// served by the data-retrieval flow mounted below via
			// n.MountFlows (config flows.mounts) — loop C.4 cutover.
			dataAPI := api.NewDataQueryHandler(n.Store())
			dataAPI.RegisterRoutes(adminMux)

			// Store maintenance (admin write): on-demand record-catalog
			// re-sync without a restart — replays journal-only records back
			// into the SQL control tables + rebuilds source summaries so
			// stats sources[]/data index recover. Behind the same admin wall.
			storeAdminAPI := api.NewStoreAdminHandler(n.Store())
			storeAdminAPI.RegisterRoutes(adminMux)
			log.Infof("Store maintenance API at %s://%s/api/v1/admin/store/hydrate", adminScheme, adminAddr)
			searchAPI := api.NewSearchHandlerWithOptions(n.Store(), api.SearchHandlerOptions{
				LiveBackend: newLiveDHTSearchBackend(n),
			})
			searchAPI.RegisterRoutes(adminMux)
			conjunctionAPI := api.NewConjunctionHandler(n.Store())
			conjunctionAPI.RegisterRoutes(adminMux)
			channelOpts := channelHandlerOptionsForIdentity(n.Identity())
			// U4.2 (M2): grant issuances land in the node's activity ring.
			channelOpts.ActivityRing = n.ActivityRing()
			channelAPI := api.NewChannelHandlerWithOptions(n.Store(), channelOpts)
			channelAPI.RegisterRoutes(adminMux)

			// Log API routes (publication log queries)
			if n.Store() != nil {
				logAPI := api.NewLogQueryHandler(n.Store())
				logAPI.RegisterRoutes(adminMux)
			}

			// Local dataset publication route used by ingest workers after a
			// successful provider sync.
			if n.Store() != nil {
				publicationSigningKey, err := datasetPublicationSigningKey(cfg, n.SigningKey())
				if err != nil {
					log.Warnf("Dataset publication signing unavailable: %v", err)
				}
				if len(publicationSigningKey) == ed25519.PrivateKeySize &&
					!identityAdvertisesPublicationKey(n.Identity() != nil, n.SigningKey(), ed25519.PrivateKey(publicationSigningKey)) {
					if epmSvc := n.EPMService(); epmSvc != nil {
						if err := epmSvc.SetRuntimeSigningKey(ed25519.PrivateKey(publicationSigningKey), "sdn/dataset-publication/v1"); err != nil {
							log.Warnf("Could not advertise dataset publication signing key in node EPM: %v", err)
						} else if err := n.IndexLocalNodeEPM(); err != nil {
							log.Warnf("Could not refresh local node EPM directory entry after adding dataset publication key: %v", err)
						}
					}
				}
				providerEPMCID := ""
				if n.EPMService() != nil {
					if epmCID, err := n.EPMService().GetNodeEPMCID(); err == nil {
						providerEPMCID = epmCID
					} else {
						log.Warnf("Could not resolve node EPM CID for dataset publications: %v", err)
					}
				}
				publicationDir := filepath.Join(filepath.Dir(cfg.Storage.Path), "dataset-publications")
				publicationService := api.NewConcreteDatasetPublicationService(
					n.Store(),
					n,
					publicationSigningKey,
					n.PeerID().String(),
					providerEPMCID,
					cfg.Admin.IPFSAPIURL,
					publicationDir,
				)
				publicationService.SetChannelRecorder(channelAPI)
				publicationAPI := api.NewDatasetPublicationHandler(publicationService)
				publicationAPI.RegisterRoutes(adminMux)
				log.Infof("Dataset publication API available at %s://%s/api/v1/admin/dataset-updates/publish", adminScheme, adminAddr)
			}

			// Catalog API route (public)
			if n.Store() != nil {
				catalogAPI := api.NewCatalogHandler(n.Store(), n.PeerID(), cfg)
				catalogAPI.RegisterRoutes(adminMux)
				log.Infof("Catalog API available at %s://%s/api/v1/catalog", adminScheme, adminAddr)
			}

			// Demo API routes (encrypted WASM demo)
			if demoPayloadPath := os.Getenv("SDN_DEMO_PAYLOAD_PATH"); demoPayloadPath != "" {
				ipfsAPIURL := strings.TrimSpace(cfg.Admin.IPFSAPIURL)
				demoAPI := api.NewDemoHandler(demoPayloadPath, ipfsAPIURL)
				demoAPI.RegisterRoutes(adminMux)
				log.Infof("Demo available at %s://%s/demo", adminScheme, adminAddr)
				log.Infof("Demo API available at %s://%s/api/v1/demo/payload", adminScheme, adminAddr)

				// Pin demo payload to IPFS in background if configured
				if ipfsAPIURL != "" {
					go func() {
						cid, err := demoAPI.PinToIPFS(ctx)
						if err != nil {
							log.Warnf("Failed to pin demo payload to IPFS: %v", err)
						} else {
							log.Infof("Demo payload pinned to IPFS: %s", cid)
							log.Infof("IPFS gateway: https://ipfs.io/ipfs/%s", cid)
						}
					}()
				}
			}

			// Optional: proxy Kubo RPC API so the React WebUI can talk to IPFS via the
			// authenticated SDN admin server.
			if rawIPFSURL := strings.TrimSpace(cfg.Admin.IPFSAPIURL); rawIPFSURL != "" {
				target, err := url.Parse(rawIPFSURL)
				if err != nil || target.Scheme == "" || target.Host == "" {
					log.Warnf("Invalid admin.ipfs_api_url %q: expected base URL like http://127.0.0.1:5001", rawIPFSURL)
				} else {
					if strings.TrimSpace(target.Path) != "" && target.Path != "/" {
						log.Warnf("admin.ipfs_api_url should not include a path (got %q); ignoring path", target.Path)
					}
					target.Path = ""
					proxy := httputil.NewSingleHostReverseProxy(target)
					origDirector := proxy.Director
					proxy.Director = func(req *http.Request) {
						origDirector(req)
						// Kubo's RPC API rejects browser User-Agent headers (403) and
						// Origins not in its allowlist. Strip all three when proxying.
						req.Header.Del("Origin")
						req.Header.Del("Referer")
						req.Header.Del("User-Agent")
					}
					proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
						http.Error(w, "upstream IPFS API unavailable", http.StatusBadGateway)
					}
					adminMux.Handle("/api/v0/", proxy)
					adminMux.Handle("/api/v0", http.RedirectHandler("/api/v0/", http.StatusPermanentRedirect))
					log.Infof("Proxying /api/v0/* to %s", rawIPFSURL)
				}
			}

			// Optional: proxy Kubo HTTP gateway so the WebUI can fetch IPFS content
			// via the same origin without needing direct access to the gateway port.
			if rawGWURL := strings.TrimSpace(cfg.Admin.IPFSGatewayURL); rawGWURL != "" {
				gwTarget, err := url.Parse(rawGWURL)
				if err != nil || gwTarget.Scheme == "" || gwTarget.Host == "" {
					log.Warnf("Invalid admin.ipfs_gateway_url %q: expected base URL like http://127.0.0.1:8080", rawGWURL)
				} else {
					gwTarget.Path = ""
					gwProxy := httputil.NewSingleHostReverseProxy(gwTarget)
					origGWDirector := gwProxy.Director
					gwProxy.Director = func(req *http.Request) {
						origGWDirector(req)
						req.Header.Del("Origin")
						req.Header.Del("Referer")
						req.Header.Del("User-Agent")
					}
					gwProxy.ModifyResponse = func(resp *http.Response) error {
						normalizeIPFSGatewayCORSHeaders(resp.Header)
						return nil
					}
					gwProxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
						http.Error(w, "upstream IPFS gateway unavailable", http.StatusBadGateway)
					}
					adminMux.Handle("/ipfs/", gwProxy)
					log.Infof("Proxying /ipfs/* to %s", rawGWURL)
				}
			}

			// Public IPFS WebUI mount.
			if webuiPath := strings.TrimSpace(cfg.Admin.WebuiPath); webuiPath != "" {
				webuiHandler, err := makeWebUIHandler(webuiPath, "/webui")
				if err != nil {
					log.Warnf("IPFS WebUI disabled at /webui: %v", err)
				} else {
					serveWebUI := func(w http.ResponseWriter, r *http.Request) {
						if r.URL.Path == "/webui" {
							http.Redirect(w, r, "/webui/", http.StatusMovedPermanently)
							return
						}
						if !strings.HasPrefix(r.URL.Path, "/webui/") {
							http.NotFound(w, r)
							return
						}

						serve := func(w http.ResponseWriter, r *http.Request) {
							http.StripPrefix("/webui", webuiHandler).ServeHTTP(w, r)
						}
						if cfg.Admin.RequireAuth {
							if authHandler == nil {
								http.Error(w, "authentication unavailable", http.StatusServiceUnavailable)
								return
							}
							authHandler.RequireAuth(peers.Standard, serve)(w, r)
							return
						}
						serve(w, r)
					}
					adminMux.HandleFunc("/webui", serveWebUI)
					adminMux.HandleFunc("/webui/", serveWebUI)
					log.Infof("IPFS WebUI at %s://%s/webui from %s", adminScheme, adminAddr, webuiPath)
				}
			}

			// Trusted peer registry management (admin UI React app consumes these endpoints).
			adminMux.Handle("/api/", peers.NewAPIHandler(n.PeerRegistry(), n.PeerGater()))

			// Storefront API (listings, purchases, Stripe checkout/webhooks).
			// Uses FlatSQL for content-addressed storage of STF/ACL/PUR/REV records.
			if n.Store() != nil {
				sfStore, err := storefront.NewStore(n.Store())
				if err != nil {
					log.Warnf("Failed to initialize storefront store: %v", err)
				} else {
					sfSigningKey, err := storefrontSigningKeyFromRaw(n.SigningKey())
					if err != nil {
						log.Warnf("Storefront grants will be unsigned; node signing key unavailable: %v", err)
					}
					sfSvc, err := storefront.NewService(sfStore, n.PeerID().String(), sfSigningKey, nil)
					if err != nil {
						log.Warnf("Failed to initialize storefront service: %v", err)
						_ = sfStore.Close()
					} else {
						sfCatalog := storefront.NewCatalog(sfStore, nil)
						sfDelivery := storefront.NewDeliveryService(storefront.DefaultDeliveryConfig(), nil)
						var chainVerifiers []storefront.ChainVerifier
						if cfg.Blockchain.Ethereum.RPCURL != "" {
							chainVerifiers = append(chainVerifiers, storefront.NewEthereumVerifier(storefront.ChainConfig{
								RPCURL:                cfg.Blockchain.Ethereum.RPCURL,
								RequiredConfirmations: cfg.Blockchain.Ethereum.RequiredConfirmations,
							}))
						}
						if cfg.Blockchain.Solana.RPCURL != "" {
							chainVerifiers = append(chainVerifiers, storefront.NewSolanaVerifier(storefront.ChainConfig{
								RPCURL:                cfg.Blockchain.Solana.RPCURL,
								RequiredConfirmations: cfg.Blockchain.Solana.RequiredConfirmations,
							}))
						}
						if cfg.Blockchain.Bitcoin.RPCURL != "" {
							chainVerifiers = append(chainVerifiers, storefront.NewBitcoinVerifier(storefront.ChainConfig{
								RPCURL:                cfg.Blockchain.Bitcoin.RPCURL,
								RequiredConfirmations: cfg.Blockchain.Bitcoin.RequiredConfirmations,
							}))
						}
						sfPayment := storefront.NewPaymentProcessor(sfStore, n.PeerID().String(), chainVerifiers...)
						sfTrust := storefront.NewTrustScorer(sfStore, storefront.DefaultTrustWeights())
						sfAPI := storefront.NewAPIHandler(sfSvc, sfCatalog, sfDelivery, sfPayment, sfTrust)
						sfAPI.RegisterRoutes(adminMux, authHandler)
						storefrontSvc = sfSvc
						storefrontStore = sfStore
						storefrontDelivery = sfDelivery
						log.Infof("Storefront API available at %s://%s/api/storefront/listings", adminScheme, adminAddr)
						log.Infof("Stripe webhook endpoint: %s://%s/api/storefront/payments/stripe/webhook", adminScheme, adminAddr)
						// manual-dev-paid grants purchases with no real payment; it
						// stays wired into the mux (it self-gates on every request)
						// but must never be enabled in production.
						if strings.TrimSpace(os.Getenv(storefront.DevPaymentsEnvVar)) == "1" {
							log.Warnf("Storefront manual/dev payment endpoint is ENABLED (%s=1) — do not set this in production", storefront.DevPaymentsEnvVar)
						}
					}
				}
			}

			// Flow management API and editor
			if fm := n.FlowManager(); fm != nil {
				flowrt.RegisterAPI(adminMux, fm)
				log.Infof("Flow management API registered at /api/v1/flows/")

				if cfg.Flows.EditorEnabled {
					editorPath := cfg.Flows.EditorPath
					if editorPath == "" {
						editorPath = "/flow-editor"
					}
					// The editor mount (including its /debug/ and /inject/
					// stub routes) lives outside the /api/ prefix, so the
					// top-level auth wall's isAPIOrPlugin check never covers
					// it. Gate it the same way /admin and /webui gate
					// themselves so it is never reachable unauthenticated
					// when cfg.Admin.RequireAuth is set.
					rawEditorHandler := editor.Handler(editorPath, fm)
					serveEditor := func(w http.ResponseWriter, r *http.Request) {
						gateAdminOnlyHandler(w, r, rawEditorHandler, authHandler, cfg.Admin.RequireAuth)
					}
					adminMux.HandleFunc(editorPath+"/", serveEditor)
					if !strings.HasSuffix(editorPath, "/") {
						adminMux.Handle(editorPath, http.RedirectHandler(editorPath+"/", http.StatusPermanentRedirect))
					}
					log.Infof("Flow editor embedded at %s (auth required: %v)", editorPath, cfg.Admin.RequireAuth)
				}
			}

			// Config-declared flow HTTP mounts (flows.mounts): each entry
			// loads a compiled flow bundle as a WASM module and binds it to
			// its listener path; the handler is pure socket plumbing.
			if err := n.MountFlows(adminMux); err != nil {
				log.Errorf("Failed to register flow HTTP mounts: %v", err)
			}

			// Anonymous-access policy (gateway loop G.2, docs/gateway-api.md
			// §4.4): mounted flows FEED the allowlist mechanically — a
			// mounted route is admitted anonymously iff its api block
			// declares anonymous:true AND gateway.anonymous.deny does not
			// veto it; gateway.anonymous.allow extends read access. The
			// static isPublicAPIRequest list remains the native baseline.
			// ONE predicate serves the auth wall, CORS, CSRF, and the
			// OpenAPI x-sdn-anonymous stamp, so spec and enforcement cannot
			// drift.
			flowRouteDecls := make([]gateway.RouteDecl, 0, 8)
			for _, mf := range n.MountedFlows() {
				doc := mf.APIDoc()
				if doc == nil {
					continue
				}
				for _, route := range doc.Routes {
					flowRouteDecls = append(flowRouteDecls, gateway.RouteDecl{
						Method:    route.Method,
						Path:      gateway.JoinMountPath(mf.MountPath(), route.Path),
						Anonymous: route.Anonymous,
					})
				}
			}
			anonymousPolicy := gateway.NewAnonymousPolicy(
				isPublicAPIRequest,
				flowRouteDecls,
				cfg.Gateway.Anonymous.Allow,
				cfg.Gateway.Anonymous.Deny,
			)
			publicAPIRequest := anonymousPolicy.Anonymous

			// Gateway API docs (loop G.1): OpenAPI generated FROM the flows
			// mounted above (their flow.json api extensions) + the Scalar
			// reference UI, self-hosted. x-sdn-anonymous stamps come from
			// the SAME predicate the auth wall enforces (anonymousPolicy)
			// so the spec cannot drift from enforcement. Temporary native
			// bootstrap routes — documented in docs/gateway-api.md §6.
			docFlows := make([]api.FlowDocSource, 0, len(n.MountedFlows()))
			for _, mf := range n.MountedFlows() {
				docFlows = append(docFlows, mf)
			}
			if docsHandler, err := api.NewDocsHandler(api.DocsHandlerOptions{
				Version:            versioninfo.AgentVersion,
				Flows:              docFlows,
				EffectiveAnonymous: publicAPIRequest,
			}); err != nil {
				log.Errorf("Failed to build gateway API docs: %v", err)
			} else {
				docsHandler.RegisterRoutes(adminMux)
				log.Infof("Gateway API docs at %s://%s/api/v1/docs (spec: /api/v1/openapi.json, %d mounted flows)",
					adminScheme, adminAddr, len(n.MountedFlows()))
			}

			if layout.Root != "" {
				adminMux.Handle("/api/v1/admin/update/shutdown", sdnupdate.NewControlHandler(sdnupdate.ControlHandlerOptions{
					BundleRoot: layout.Root,
					Shutdown: func() {
						select {
						case updateShutdown <- struct{}{}:
						default:
						}
					},
				}))
			}

			// Node info API endpoint
			adminMux.HandleFunc("/api/node/info", handleNodeInfo(n, torRuntime))
			adminMux.HandleFunc("/api/module-delivery/provider", handleProviderDescriptor(n))
			adminMux.HandleFunc("/api/module-delivery/listings", handleModuleDeliveryListings(n.PluginRegistry()))
			adminMux.HandleFunc("/api/v1/modules/runtime", handleModuleRuntimeSnapshot(n.PluginManager(), n.PluginRegistry()))
			adminMux.HandleFunc("/api/v1/modules/runtime/", handleModuleRuntimeMutation(n.PluginManager()))
			if capPolicy := n.CapabilityPolicy(); capPolicy != nil {
				capAPI := modulert.NewCapabilityPolicyAPI(capPolicy)
				adminMux.Handle("/api/modules/capabilities", capAPI)
				adminMux.Handle("/api/modules/capabilities/", capAPI)
			}
			adminMux.Handle("/api/directory/", directory.NewHTTPHandler(n.DirectoryService()))

			// Relay status endpoint (public, used by clients for load balancing)
			adminMux.HandleFunc("/api/relay/status", handleRelayStatus(n))

			// EPM (Entity Profile Message) API endpoints
			adminMux.HandleFunc("/api/node/epm/json", handleNodeEPMJSON(n))
			adminMux.HandleFunc("/api/node/epm/vcard", handleNodeEPMVCard(n))
			adminMux.HandleFunc("/api/node/epm/qr", handleNodeEPMQR(n))
			adminMux.HandleFunc("/api/node/epm", handleNodeEPM(n))

			// Peer graph API endpoints
			adminMux.HandleFunc("/api/peers/sdn", handleObservedSDNPeers(n))
			adminMux.HandleFunc("/api/peers/graph", handlePeerGraph(n))
			adminMux.HandleFunc("/api/peers/graph/schema", handlePeerGraphSchema)

			// libp2p bootstrap JS — serves a JS module with the node's raw IP,
			// peer ID, and ws:// multiaddr injected at request time so browsers
			// can connect using the raw IP without DNS.
			adminMux.HandleFunc("/sdn/libp2p.js", handleLibp2pJS(n))

			// C2 (OWNER DIRECTIVE 2026-07-11 conjunction-only ship): choose
			// which embedded UI the "/" surface serves. Default = conjunction
			// (shipped); SDN_UI_MODE=spaceaware restores the full app for dev.
			// Resolved once here so the /login wiring (below) and the "/"
			// surface handler (further below) agree on the mode.

			// Auth-surface mode: default = conjunction (shipped, isolated
			// external wallet presenter); SDN_UI_MODE=spaceaware restores the
			// legacy wallet login surfaces for dev. Resolved once here so the
			// /login wiring and wallet-ui mounts below agree on the mode. The
			// "/" surface always serves the embedded root SDS $APP.
			frontendUIMode := resolveUIMode()
			log.Infof("Auth surface mode: %s (SDN_UI_MODE)", frontendUIMode)

			// HD wallet authentication
			if cfg.Admin.RequireAuth {
				// The user store owns the private auth database; the session
				// store shares that same handle via userStore.DB().
				authDBPath := filepath.Join(cfg.Storage.Path, "auth.db")
				userStore, err := auth.NewUserStore(authDBPath, cfg.Users)
				if err != nil {
					return fmt.Errorf("admin authentication required: create user store: %w", err)
				}

				sessionStore, err := auth.NewSessionStore(userStore.DB())
				if err != nil {
					_ = userStore.Close()
					return fmt.Errorf("admin authentication required: create session store: %w", err)
				}

				sessionTTL, _ := time.ParseDuration(cfg.Admin.SessionExpiry)
				if sessionTTL == 0 {
					sessionTTL = 24 * time.Hour
				}

				cfgDisplayPath := configPath
				if cfgDisplayPath == "" {
					cfgDisplayPath = config.DefaultPath()
				}
				// The generic in-process wallet UI is a development-only legacy
				// surface. Shipped conjunction mode uses the isolated typed wallet
				// presenter and must not expose the legacy asset path or advertise it
				// through /api/auth/status.
				legacyWalletUIPath := legacyWalletUIPathForMode(frontendUIMode, cfg.Admin.WalletUIPath)
				authHandler = auth.NewHandler(userStore, sessionStore, sessionTTL, legacyWalletUIPath, cfgDisplayPath)
				authHandler.SetTLSManager(tlsManager)
				if epmSvc := n.EPMService(); epmSvc != nil {
					if att := epmSvc.GetIdentityAttestation(); att != nil {
						authHandler.SetNodeSigningAttestation(att)
					}
				}
				// /login wiring depends on the primary UI mode:
				//   - spaceaware (dev): U1.2 behavior — the embedded SpaceAware
				//     login (spaceaware_ui.go, served by the "/" frontend
				//     surface) owns GET /login; the legacy wallet-gated page
				//     moves to /login/legacy for wallet creation / first-admin
				//     bootstrap.
				//   - conjunction (SHIPPED default, C2): neither legacy login route
				//     is registered. The visible wallet presenter is isolated from
				//     this origin and the read-only conjunction shell owns the UI.
				authHandler.SetExternalLoginUI(frontendUIMode == uiModeSpaceAware)
				authHandler.RegisterRoutes(adminMux)
				n.SetModulePublishAuthorizer(func(xpub string) (license.ModulePublishPrincipal, error) {
					user, err := authHandler.UserStore().GetUser(xpub)
					if err != nil {
						return license.ModulePublishPrincipal{}, err
					}
					if user == nil {
						return license.ModulePublishPrincipal{}, nil
					}
					return license.ModulePublishPrincipal{
						XPub:             user.XPub,
						SigningPubKeyHex: user.SigningPubKeyHex,
						Admin:            user.TrustLevel >= peers.Admin,
					}, nil
				})
				if frontendUIMode == uiModeSpaceAware {
					log.Infof("HD wallet authentication enabled at %s://%s/login", adminScheme, adminAddr)
				} else {
					log.Infof("Authentication session APIs enabled; legacy wallet login UI disabled in conjunction mode")
				}

				if n.DirectoryService() != nil {
					adminDirectoryHandler := directory.NewAdminHTTPHandler(n.DirectoryService())
					adminMux.HandleFunc("/api/v1/admin/directory/import", authHandler.RequireAuth(peers.Standard, adminDirectoryHandler.ServeHTTP))
					log.Infof("Directory import API available at %s://%s/api/v1/admin/directory/import", adminScheme, adminAddr)
				}

				// Publish API (requires auth)
				if n.Store() != nil && cfg.Publishing.Enabled {
					quotas := api.NewStorageQuotaManager(n.Store(), cfg.Publishing.DefaultQuotaBytes)
					publishAPI := api.NewPublishHandler(n.Store(), n.Validator(), quotas, &cfg.Publishing, authHandler)
					publishAPI.SetLogService(n.LogService())
					publishAPI.RegisterRoutes(adminMux)
					log.Infof("Publish API available at %s://%s/api/v1/data/publish/", adminScheme, adminAddr)
				}

				// Peer ACL admin API (requires admin auth)
				if n.PeerRegistry() != nil {
					aclAPI := api.NewACLHandler(n.PeerRegistry(), authHandler)
					aclAPI.RegisterRoutes(adminMux)
					log.Infof("Peer ACL API available at %s://%s/api/v1/admin/peers", adminScheme, adminAddr)
				}

				// Pinning policy admin API (Admin-gated in RegisterRoutes) —
				// the TipQueue auto-fetch/auto-pin/TTL configuration surface (D1).
				if n.TipQueue() != nil {
					pinningAPI := api.NewPinningHandler(n.TipQueue().Config(), authHandler)
					pinningAPI.RegisterRoutes(adminMux)
					log.Infof("Pinning policy API available at %s://%s/api/v1/admin/pinning", adminScheme, adminAddr)
				}

				// Serve wallet-ui static files only for the explicitly selected
				// SpaceAware development surface. Production conjunction mode must
				// leave /wallet-ui unregistered even when old config still names it.
				if serveRoot, mounted := registerLegacyWalletStaticFiles(adminMux, frontendUIMode, legacyWalletUIPath); mounted {
					log.Infof("Wallet UI served at %s://%s/wallet-ui/ from %s", adminScheme, adminAddr, serveRoot)
				}

				if frontendUIMode == uiModeSpaceAware {
					// Discover legacy assets only in the same development-only mode.
					auth.DiscoverWalletAssets(legacyWalletUIPath)
					if legacyAdminUI != nil {
						if jsFile, cssFile := auth.WalletAssets(); jsFile != "" {
							legacyAdminUI.SetWalletAssets(jsFile, cssFile)
						}
					}
				}
			}

			// ----------------------------------------------------------------
			// Provider-credential API (admin-only, WRITE-ONLY, fails closed)
			// ----------------------------------------------------------------
			//
			// Operator-entered third-party credentials (Space-Track today),
			// stored encrypted at rest under the node's own key material
			// (internal/credstore) — NEVER as an SDS record, which would
			// replicate the credential to every peer.
			//
			// Registered UNCONDITIONALLY and gated inside the handler. That is
			// deliberate: nginx on the public host has no /api/ location block,
			// so a catch-all forwards every /api/** path here and the daemon's
			// own auth is the only thing in front of these routes. The handler
			// refuses to serve (503) whenever authentication is disabled or
			// unavailable — it never falls open, and because the gate lives
			// inside the handler it also survives a widened
			// gateway.anonymous.allow. Status only: no route returns a stored
			// secret to any caller.
			//
			// The at-rest key is derived from the UNLOCKED node identity private
			// key (n.IdentityKeyMaterial(), already unlocked at boot — it is used
			// the same way at daemon startup above) plus the machine fingerprint
			// and hostname. OpenStore fails closed if that key is unavailable, so
			// the API is not mounted rather than opened under a weaker key.
			if credStore, cerr := credstore.OpenStore(cfg.Storage.Path, n.IdentityKeyMaterial()); cerr != nil {
				log.Warnf("credential store unavailable; provider-credential API not mounted: %v", cerr)
			} else {
				credAPI := api.NewCredentialsHandler(credStore, authHandler, cfg.Admin.RequireAuth, map[string]api.Verifier{
					credstore.IDSpaceTrack: credstore.NewSpaceTrackVerifier(),
				})
				credAPI.RegisterRoutes(adminMux)
				if cfg.Admin.RequireAuth {
					log.Infof("Provider credential API at %s://%s/api/v1/admin/credentials (admin auth required)", adminScheme, adminAddr)
				} else {
					log.Warnf("Provider credential API mounted but REFUSING to serve: admin.require_auth is disabled (fail closed)")
				}
			}

			// When the operator explicitly disables admin authentication, keep
			// the enabled publishing surface available without accidentally
			// falling through to the broader /api/v1/data/ flow mount. Attribute
			// every write to this node's stable peer ID so quota and provenance
			// records remain individually auditable. The authenticated default
			// remains registered inside the RequireAuth branch above.
			if !cfg.Admin.RequireAuth && n.Store() != nil && cfg.Publishing.Enabled {
				quotas := api.NewStorageQuotaManager(n.Store(), cfg.Publishing.DefaultQuotaBytes)
				publishAPI := api.NewPublishHandler(n.Store(), n.Validator(), quotas, &cfg.Publishing, nil)
				publishAPI.SetLogService(n.LogService())
				publishAPI.RegisterUnauthenticatedRoutes(adminMux, n.PeerID().String())
				log.Infof("Publish API available without authentication at %s://%s/api/v1/data/publish/", adminScheme, adminAddr)
			}

			// ----------------------------------------------------------------
			// Plugin upload API (admin-only, requires auth + license plugin)
			// ----------------------------------------------------------------
			if authHandler != nil {
				if reg := n.PluginRegistry(); reg != nil {
					uploadHandler := license.NewUploadHandler(
						reg,
						func(xpub string) (string, error) {
							user, err := authHandler.UserStore().GetUser(xpub)
							if err != nil {
								return "", err
							}
							if user == nil {
								return "", fmt.Errorf("user not found")
							}
							return user.SigningPubKeyHex, nil
						},
						func(r *http.Request) (string, error) {
							session := auth.SessionFromContext(r.Context())
							if session == nil {
								return "", fmt.Errorf("no session")
							}
							return session.XPub, nil
						},
					)
					adminMux.HandleFunc("/api/v1/plugins/upload", uploadHandler.ServeHTTP)
					log.Infof("Plugin upload API at %s://%s/api/v1/plugins/upload", adminScheme, adminAddr)
				}
			}

			// ----------------------------------------------------------------
			// Core API: identity, stats, peers, pubsub endpoints
			// Registered unconditionally — public GET endpoints are open to all;
			// write/admin endpoints use RequireAuth internally.
			// ----------------------------------------------------------------
			{
				coreAPI := api.NewCoreAPIHandler(
					n.PeerID(),
					n.Host(),
					n.PubSub(),
					n,
					n.Store(),
					n.Validator(),
					&cfg.Admin,
					authHandler,
					n.ListenAddrs,
				)
				// SDN peer counts for /api/v1/stats — the anonymous read
				// surface app boards poll. Same evidence as the dashboard's
				// /api/peers/sdn (epm.BuildObservedSDNPeers), so "SDN peers"
				// on a board can never disagree with the peer list.
				coreAPI.SetSDNPeerCounter(func() api.SDNPeerCounts {
					counts := observedSDNPeerCounts(n)
					return api.SDNPeerCounts{Connected: counts.Connected, Known: counts.Known}
				})

				// Mounted gateway flows claim mux paths (incl. the trimmed
				// exact alias of trailing-slash subtree mounts); the core
				// API yields claimed surfaces — G.2: the peers-discovery
				// flow REPLACES the native /api/v1/peers read routes.
				flowClaimedPaths := make(map[string]bool, len(n.MountedFlows())*2)
				for _, mf := range n.MountedFlows() {
					mountPath := mf.MountPath()
					flowClaimedPaths[mountPath] = true
					if trimmed := strings.TrimSuffix(mountPath, "/"); trimmed != "" {
						flowClaimedPaths[trimmed] = true
					}
				}
				coreAPI.RegisterRoutesWithFlowMounts(adminMux, func(path string) bool {
					return flowClaimedPaths[path]
				})
				log.Infof("Core API available at %s://%s/api/v1/{id,version,stats,peers,pubsub}", adminScheme, adminAddr)

				// WebSocket bridge (gap B10.3): /ws lives outside the /api/
				// and /orbpro-key-broker/ prefixes the top-level auth
				// wall's isAPIOrPlugin check inspects, and an anonymous
				// client can use it to publish SDS records into local AND
				// libp2p pubsub — a state-changing surface, not merely a
				// read. Self-gate it the same way /webui gates itself
				// (Standard trust) so it is never reachable
				// unauthenticated when cfg.Admin.RequireAuth is set.
				// Subscribe and publish both sit behind the same session;
				// anonymous read access, if ever wanted, must be an
				// explicit future decision. ws.go's CheckOrigin adds a
				// second, independent same-origin check on top of this.
				wsHandler := api.NewWSHandler(n, n.Validator())
				serveWS := func(w http.ResponseWriter, r *http.Request) {
					gateHandlerWithTrust(w, r, wsHandler, authHandler, cfg.Admin.RequireAuth, peers.Standard)
				}
				adminMux.HandleFunc("/ws", serveWS)
				log.Infof("WebSocket bridge available at %s://%s/ws (auth required: %v)", adminScheme, adminAddr, cfg.Admin.RequireAuth)

				// Public read-only node-status feed (/ws/status). Unlike /ws
				// above, this is DELIBERATELY unauthenticated: it carries only
				// core-node telemetry already public via /api/v1/id and
				// /api/peers/sdn, reshaped into the $NST binary transport. It
				// lives outside the /api/ prefix the top-level auth wall's
				// isAPIOrPlugin check inspects and is intentionally NOT
				// self-gated, so an anonymous dashboard can subscribe. The
				// Broadcaster's CheckOrigin is the only handshake gate
				// (same-origin + loopback + configured allowlist).
				geoReader := geoip.Open(cfg.GeoIP.MMDBPath)
				// Bootstrap constants keep geo resolution working when the live
				// peerstore only holds relay/private addrs for a prod peer.
				statusFallbackAddrs := map[string][]string{}
				if infos, err := bootstrap.ParseBootstrapAddresses(bootstrap.DefaultBootstrapAddresses()); err == nil {
					for _, info := range infos {
						pid := info.AddrInfo.ID.String()
						for _, a := range info.AddrInfo.Addrs {
							statusFallbackAddrs[pid] = append(statusFallbackAddrs[pid], a.String())
						}
					}
				}
				statusBroadcaster := nodestatus.NewBroadcaster(func() []byte {
					snapshot := epm.BuildGraphSnapshot(n.Host(), n.PeerRegistry())
					var registryPeers []*peers.TrustedPeer
					if registry := n.PeerRegistry(); registry != nil {
						registryPeers = registry.ListPeers()
					}
					observed := epm.BuildObservedSDNPeers(
						snapshot,
						registryPeers,
						n.SDNAdvertisementFlagsByPeer(),
						n.SDNAdvertisementAddrsByPeer(),
					)
					selfVCard := ""
					if epmSvc := n.EPMService(); epmSvc != nil {
						if v, err := epmSvc.GetNodeVCard(); err == nil {
							selfVCard = v
						}
					}
					return nodestatus.BuildNodeStatusSet(nodestatus.Input{
						Snapshot:         snapshot,
						Observed:         observed,
						SelfPeerID:       n.PeerID().String(),
						SelfVCard:        selfVCard,
						AgentVersion:     versioninfo.AgentVersion,
						SuiteVersion:     versioninfo.SuiteVersion,
						StandardsVersion: versioninfo.SpaceDataStandardsVersion,
						Uptime:           time.Since(processStartTime),
						Geo:              geoReader,
						FallbackAddrs:    statusFallbackAddrs,
					})
				}, cfg.Status.AllowedOrigins)
				statusBroadcaster.Start()
				adminMux.HandleFunc("/ws/status", statusBroadcaster.ServeHTTP)
				log.Infof("Status feed available at %s://%s/ws/status (public read-only)", adminScheme, adminAddr)
			}

			// ----------------------------------------------------------------
			// Frontend management API (admin-only)
			// ----------------------------------------------------------------
			frontendMgr := frontend.NewManager(cfg.Admin.FrontendPath)
			frontendMgr.RegisterRoutes(adminMux)
			log.Infof("Frontend manager at %s://%s/api/admin/frontend/ (dir: %s)", adminScheme, adminAddr, cfg.Admin.FrontendPath)

			// Serve favicon.ico directly so root icon requests do not 404.
			// Prefer the public frontend favicon, then wallet UI favicon, then fallback
			// to a tiny built-in transparent icon.
			frontendFaviconPath := filepath.Join(strings.TrimSpace(cfg.Admin.FrontendPath), "favicon.ico")
			walletFaviconPath := ""
			if wui := strings.TrimSpace(cfg.Admin.WalletUIPath); wui != "" {
				walletFaviconPath = filepath.Join(wui, "favicon.ico")
			}
			adminMux.Handle("/favicon.ico", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				serveFavicon(w, r, []string{frontendFaviconPath, walletFaviconPath})
			}))

			// ----------------------------------------------------------------
			// Admin panel at /admin — admin/auth surface only
			// ----------------------------------------------------------------
			adminUISubtree := http.StripPrefix("/admin", adminUIHandler)
			serveAdminUI := func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/admin" {
					http.Redirect(w, r, "/admin/", http.StatusMovedPermanently)
					return
				}
				if !strings.HasPrefix(r.URL.Path, "/admin/") {
					http.NotFound(w, r)
					return
				}

				serve := func(w http.ResponseWriter, r *http.Request) {
					adminUISubtree.ServeHTTP(w, r)
				}
				if cfg.Admin.RequireAuth {
					if authHandler == nil {
						http.Error(w, "authentication unavailable", http.StatusServiceUnavailable)
						return
					}
					authHandler.RequireAuth(peers.Admin, serve)(w, r)
					return
				}
				serve(w, r)
			}
			adminMux.HandleFunc("/admin", serveAdminUI)
			adminMux.HandleFunc("/admin/", serveAdminUI)

			// ----------------------------------------------------------------
			// Public root at / — the SDN Node Status $APP homepage (single
			// self-contained file built from the design components, fed by the
			// public /ws/status feed). Its self-hosted web fonts are served
			// same-origin at /fonts/*.woff2. The isolated wallet callback and
			// the 404 behavior for every other root path are unchanged; /admin
			// and the APIs are unchanged.
			// ----------------------------------------------------------------
			adminMux.Handle("/fonts/", makeFontsHandler())
			adminMux.Handle("/", makeRootHandler())
			log.Infof("Node status dashboard at %s://%s/ (fed by /ws/status; admin portal remains at /admin)", adminScheme, adminAddr)

			adminServer = &http.Server{
				Addr:              adminAddr,
				ReadHeaderTimeout: 10 * time.Second,
				ReadTimeout:       30 * time.Second,
				WriteTimeout:      10 * time.Minute,
				IdleTimeout:       120 * time.Second,
				Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					// Tunnel secure websocket upgrades to the local libp2p ws listener.
					// This bypasses adminSecurityMiddleware entirely: it is a raw
					// proxy passthrough, not a normal admin-mux response. The admin
					// mux's OWN websocket endpoints (/ws pubsub bridge, /ws/status
					// telemetry) must never be tunneled — without this exemption
					// every ws client on a TLS admin listener gets a libp2p
					// multistream banner instead of the endpoint it dialed.
					if wsUpgradeProxy != nil && isWebSocketUpgradeRequest(r) && !isAdminWebSocketPath(r.URL.Path) {
						wsUpgradeProxy.ServeHTTP(w, r)
						return
					}

					adminSecurityMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						serveAdminMuxRequest(w, r, adminMux, cfg.Admin.RequireAuth, assetOIDCCapabilityMounted, authHandler, publicAPIRequest)
					}), tlsManager.Mode(), publicAPIRequest).ServeHTTP(w, r)
				}),
			}
			go func() {
				if cfg.Admin.RequireAuth && authHandler != nil {
					if frontendUIMode == uiModeConjunction {
						log.Infof("Admin interface at %s://%s/admin (requires external wallet authorization from the SDN UI at /)", adminScheme, adminAddr)
					} else {
						log.Infof("Admin interface at %s://%s/admin (requires HD wallet login at /login)", adminScheme, adminAddr)
					}
				} else {
					log.Infof("Admin interface available at %s://%s/admin", adminScheme, adminAddr)
				}
				log.Infof("Peer API available at %s://%s/api/peers", adminScheme, adminAddr)
				log.Infof("Node info API available at %s://%s/api/node/info", adminScheme, adminAddr)
				log.Infof("Module delivery provider descriptor available at %s://%s/api/module-delivery/provider", adminScheme, adminAddr)
				log.Infof("Public data API available at %s://%s/api/v1/data/omm/bulk", adminScheme, adminAddr)
				var err error
				if adminTLS {
					adminServer.TLSConfig = tlsManager.TLSConfig()
					err = adminServer.ListenAndServeTLS("", "")
				} else {
					err = adminServer.ListenAndServe()
				}
				if err != nil && err != http.ErrServerClosed {
					log.Warnf("Admin server error: %v", err)
				}
			}()

			if tlsManager.Mode() == tlsmgr.ModeManaged {
				challengeAddr := strings.TrimSpace(cfg.Admin.HTTPChallengeAddr)
				if challengeAddr == "" {
					challengeAddr = "127.0.0.1:5080"
				}
				httpChallengeServer = &http.Server{
					Addr:    challengeAddr,
					Handler: tlsManager.HTTPHandler(adminAddr),
				}
				go func() {
					if err := httpChallengeServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
						log.Warnf("HTTP challenge server error: %v", err)
					}
				}()
			}
		}
	}

	// ------------------------------------------------------------------
	// Local publish lane — a SEPARATE loopback-bound listener carrying only
	// the publish routes, with no HTTP auth, for a pipeline running ON this
	// host (e.g. the constellation OD pipeline writing its fitted OMM/OCM
	// records into this node's own store).
	//
	// It is a second socket on purpose. The admin/API listener above is what
	// nginx reverse-proxies to, so public traffic already reaches the daemon
	// from 127.0.0.1; trusting the client IP THERE would publish writes to the
	// internet. Authority here comes from the socket — a loopback address the
	// proxy does not forward to — so the public listener's require_auth
	// semantics are left completely untouched.
	//
	// Disabled unless publishing.local_publish_addr is set. A non-loopback
	// value is a fatal config error, never a silent public bind.
	// ------------------------------------------------------------------
	if localPublishAddr := strings.TrimSpace(cfg.Publishing.LocalPublishAddr); localPublishAddr != "" {
		if err := config.ValidateLoopbackListenAddr(localPublishAddr); err != nil {
			return fmt.Errorf("publishing.local_publish_addr: %w", err)
		}
		if !cfg.Publishing.Enabled {
			return fmt.Errorf("publishing.local_publish_addr is set but publishing.enabled is false")
		}
		if n.Store() == nil {
			return fmt.Errorf("publishing.local_publish_addr requires a node with storage (mode != edge)")
		}

		// Validates the address AND asserts the REAL bound address is loopback,
		// closing the socket rather than serving if it is not. ufw is inactive on
		// the prod hosts, so this bind is the boundary — it must fail closed.
		listener, err := config.ListenLoopback(localPublishAddr)
		if err != nil {
			return fmt.Errorf("local publish lane: %w", err)
		}

		localMux := http.NewServeMux()
		localQuotas := api.NewStorageQuotaManager(n.Store(), cfg.Publishing.DefaultQuotaBytes)
		localPublishAPI := api.NewPublishHandler(n.Store(), n.Validator(), localQuotas, &cfg.Publishing, nil)
		localPublishAPI.SetLogService(n.LogService())
		localPublishAPI.RegisterLocalLaneRoutes(localMux, n.PeerID().String())

		// GET /api/v1/stats on the lane too: the pipeline's completeness gate
		// polls it to confirm the node persisted the batch it just acked, so a
		// write-only lane would fail every run. It is already an anonymous read
		// on the public listener, so this adds no surface — it just spares the
		// pipeline from needing a second (TLS) base URL for reads.
		localCoreAPI := api.NewCoreAPIHandler(
			n.PeerID(),
			n.Host(),
			n.PubSub(),
			n,
			n.Store(),
			n.Validator(),
			&cfg.Admin,
			nil, // no auth handler: this mux is reachable only over loopback
			n.ListenAddrs,
		)
		localCoreAPI.RegisterLocalLaneReadRoutes(localMux)

		localPublishServer = &http.Server{
			Handler:           localMux,
			ReadHeaderTimeout: 10 * time.Second,
			ReadTimeout:       5 * time.Minute,
			WriteTimeout:      5 * time.Minute,
			IdleTimeout:       120 * time.Second,
		}
		go func() {
			if err := localPublishServer.Serve(listener); err != nil && err != http.ErrServerClosed {
				log.Warnf("Local publish lane error: %v", err)
			}
		}()
		log.Infof(
			"Local publish lane (loopback only, NO auth) at http://%s/api/v1/data/publish/{schema} "+
				"and /api/v1/admin/publish?schema= — attributed to %s. NEVER reverse-proxy this port.",
			listener.Addr(), n.PeerID().String(),
		)
	}

	var (
		assetRetentionCancel context.CancelFunc
		assetRetentionDone   <-chan struct{}
	)
	stopAssetRetention := func() {
		if assetRetentionCancel == nil {
			return
		}
		assetRetentionCancel()
		<-assetRetentionDone
		assetRetentionCancel = nil
	}
	defer stopAssetRetention()
	if assetRetainer != nil {
		retentionCtx, cancelRetention := context.WithCancel(ctx)
		done := make(chan struct{})
		assetRetentionCancel = cancelRetention
		assetRetentionDone = done
		go func() {
			defer close(done)
			assetRetainer.Run(
				retentionCtx,
				cfg.AssetPins.EffectiveRetentionInterval(),
				func() time.Time { return time.Now().UTC() },
				func(err error) { log.Errorf("Asset pin retention sweep failed: %v", err) },
			)
		}()
	}

	n.StartBackgroundRecordCatalogHydration(ctx)
	n.StartConfiguredFlowServices(ctx)

	// Wait for shutdown signal
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	select {
	case <-sigChan:
	case <-updateShutdown:
		log.Info("Update shutdown requested")
	}

	log.Info("Shutting down...")

	// Shutdown admin server
	if adminServer != nil {
		adminServer.Shutdown(ctx)
	}
	if httpChallengeServer != nil {
		httpChallengeServer.Shutdown(ctx)
	}
	if localPublishServer != nil {
		localPublishServer.Shutdown(ctx)
	}
	if storefrontSvc != nil {
		if err := storefrontSvc.Close(); err != nil {
			log.Warnf("Storefront service shutdown error: %v", err)
		}
	}
	if storefrontDelivery != nil {
		storefrontDelivery.Close()
	}
	if storefrontStore != nil {
		if err := storefrontStore.Close(); err != nil {
			log.Warnf("Storefront store close error: %v", err)
		}
	}

	stopAssetRetention()
	return n.Stop()
}

func loadLandingPage(customPath string) ([]byte, error) {
	if strings.TrimSpace(customPath) == "" {
		return []byte(defaultFrontendHTML), nil
	}

	content, err := os.ReadFile(customPath)
	if err != nil {
		return nil, fmt.Errorf("read admin.homepage_file %q: %w", customPath, err)
	}
	if len(bytes.TrimSpace(content)) == 0 {
		return nil, fmt.Errorf("admin.homepage_file %q is empty", customPath)
	}
	return content, nil
}

func resolveBuildAssetsDir(homepageFile string) string {
	path := strings.TrimSpace(homepageFile)
	if path == "" {
		return ""
	}
	return filepath.Join(filepath.Dir(path), "Build")
}

func publicHomepageFile(frontendPath string, homepageFile string) string {
	// homepage_file is a legacy single-file override. frontend_path supersedes it,
	// so preserve the documented behavior and fall back to the embedded landing page.
	if strings.TrimSpace(frontendPath) != "" {
		return ""
	}
	return strings.TrimSpace(homepageFile)
}

// identityAdvertisesPublicationKey reports whether the node EPM already
// advertises the dataset-publication Ed25519 public key through the HD
// identity. HD-identity nodes sign dataset publications with the identity
// Ed25519 signing key, which the node EPM now carries directly in its KEYS
// vector, so injecting a duplicate runtime signing key would be redundant.
// Nodes without an HD identity (or whose publication key differs from the
// identity signing key) still need SetRuntimeSigningKey to advertise it.
func identityAdvertisesPublicationKey(hasIdentity bool, identitySigningRaw []byte, publicationKey ed25519.PrivateKey) bool {
	if !hasIdentity || len(publicationKey) != ed25519.PrivateKeySize {
		return false
	}
	identityKey, err := storefrontSigningKeyFromRaw(identitySigningRaw)
	if err != nil {
		return false
	}
	identityPub, ok := identityKey.Public().(ed25519.PublicKey)
	if !ok {
		return false
	}
	publicationPub, ok := publicationKey.Public().(ed25519.PublicKey)
	if !ok {
		return false
	}
	return identityPub.Equal(publicationPub)
}

func storefrontSigningKeyFromRaw(raw []byte) (ed25519.PrivateKey, error) {
	switch len(raw) {
	case 0:
		return nil, fmt.Errorf("empty signing key")
	case ed25519.SeedSize:
		return ed25519.NewKeyFromSeed(raw), nil
	case ed25519.PrivateKeySize:
		return append(ed25519.PrivateKey(nil), raw...), nil
	default:
		return nil, fmt.Errorf("unexpected signing key length %d", len(raw))
	}
}

func datasetPublicationSigningKey(cfg *config.Config, raw []byte) ([]byte, error) {
	if len(raw) > 0 {
		return storefrontSigningKeyFromRaw(raw)
	}
	if cfg == nil {
		return nil, fmt.Errorf("config is required")
	}
	basePath := strings.TrimSpace(cfg.Setup.DataPath)
	if basePath == "" {
		storagePath := strings.TrimSpace(cfg.Storage.Path)
		if storagePath == "" {
			return nil, fmt.Errorf("storage path is required")
		}
		basePath = filepath.Dir(storagePath)
	}

	keyMgr, err := keys.NewManager(basePath)
	if err != nil {
		return nil, fmt.Errorf("create publication key manager: %w", err)
	}
	var identity *keys.Identity
	if keyMgr.HasIdentity() {
		identity, err = keyMgr.LoadIdentity()
	} else {
		identity, err = keyMgr.GenerateIdentity()
	}
	if err != nil {
		return nil, fmt.Errorf("load publication signing identity: %w", err)
	}
	if identity == nil || identity.SigningKey == nil || len(identity.SigningKey.PrivateKey) == 0 {
		return nil, fmt.Errorf("publication signing identity is unavailable")
	}
	return storefrontSigningKeyFromRaw(identity.SigningKey.PrivateKey)
}

const (
	assetPinCapabilityUnavailableJSON = "{\"error\":{\"message\":\"asset pin capability unavailable\"}}\n"
	assetPinKuboProbeTimeout          = 5 * time.Second
	assetPinKuboProbeMaxResponseBytes = 64 << 10
)

type assetPinCapabilityStore interface {
	api.AssetPinStore
	ConsumeAssetOIDCToken(context.Context, storage.AssetOIDCReceipt) error
	Path() string
}

type assetPinCapabilityRoutes interface {
	http.Handler
	RegisterRoutes(*http.ServeMux)
}

type assetPinCapabilityDependencies struct {
	clock       func() time.Time
	probeKubo   func(context.Context, string) error
	newPinner   func(string) (api.AssetPinPinner, error)
	newHandler  func(api.AssetPinHandlerOptions) (assetPinCapabilityRoutes, error)
	newVerifier func(context.Context, config.AssetPinConfig, assetpin.TokenReceiptConsumer) (api.AssetPinVerifier, error)
}

type assetPinCapability struct {
	Handler   http.Handler
	HealthErr error
}

// assetPinVerifierSlot lets static handler validation run before OIDC
// discovery. It is assigned before its private mux is created or returned, so
// no request can observe an unset verifier.
type assetPinVerifierSlot struct {
	verifier api.AssetPinVerifier
}

func (s *assetPinVerifierSlot) VerifyAndConsume(ctx context.Context, rawToken string, kind assetpin.WorkflowKind) (assetpin.Claims, error) {
	if s == nil || isNilLikeAssetPinCapabilityDependency(s.verifier) {
		return assetpin.Claims{}, assetpin.ErrTokenReceipt
	}
	return s.verifier.VerifyAndConsume(ctx, rawToken, kind)
}

func defaultAssetPinCapabilityDependencies() assetPinCapabilityDependencies {
	return assetPinCapabilityDependencies{
		clock:     func() time.Time { return time.Now().UTC() },
		probeKubo: probeAssetPinKuboReadiness,
		newPinner: func(apiURL string) (api.AssetPinPinner, error) {
			return api.NewKuboAssetPinner(apiURL)
		},
		newHandler: func(options api.AssetPinHandlerOptions) (assetPinCapabilityRoutes, error) {
			return api.NewAssetPinHandler(options)
		},
		newVerifier: func(ctx context.Context, cfg config.AssetPinConfig, consumer assetpin.TokenReceiptConsumer) (api.AssetPinVerifier, error) {
			return assetpin.NewVerifier(ctx, cfg, consumer)
		},
	}
}

func composeAssetPinCapability(
	ctx context.Context,
	store assetPinCapabilityStore,
	ipfsAPIURL string,
	cfg config.AssetPinConfig,
	gate *assetpin.MutationGate,
	dependencies assetPinCapabilityDependencies,
) (assetPinCapability, error) {
	if ctx == nil {
		return assetPinCapability{}, errors.New("asset pin capability context is required")
	}
	if isNilLikeAssetPinCapabilityDependency(store) {
		return assetPinCapability{}, errors.New("asset pin capability requires local storage")
	}
	if !cfg.Enabled {
		return assetPinCapability{}, errors.New("asset pin capability is disabled")
	}
	if gate == nil {
		return assetPinCapability{}, errors.New("asset pin mutation gate is required")
	}
	if err := validateAssetPinCapabilityOIDCConfig(cfg); err != nil {
		return assetPinCapability{}, err
	}
	if dependencies.clock == nil || dependencies.probeKubo == nil || dependencies.newPinner == nil || dependencies.newHandler == nil || dependencies.newVerifier == nil {
		return assetPinCapability{}, errors.New("asset pin capability dependencies are incomplete")
	}
	canonicalKuboURL, err := canonicalAssetPinKuboAPIURL(ipfsAPIURL)
	if err != nil {
		return assetPinCapability{}, err
	}
	storePath := strings.TrimSpace(store.Path())
	if storePath == "" {
		return assetPinCapability{}, errors.New("asset pin capability storage path is required")
	}
	pinner, err := dependencies.newPinner(canonicalKuboURL)
	if err != nil {
		return assetPinCapability{}, fmt.Errorf("configure asset pin Kubo client: %w", err)
	}
	if isNilLikeAssetPinCapabilityDependency(pinner) {
		return assetPinCapability{}, errors.New("asset pin Kubo client is required")
	}

	verifierSlot := &assetPinVerifierSlot{}
	routes, err := dependencies.newHandler(api.AssetPinHandlerOptions{
		Verifier: verifierSlot,
		Store:    store,
		Pinner:   pinner,
		Gate:     gate,
		Config:   cfg,
		DataDir:  filepath.Dir(storePath),
	})
	if err != nil {
		return assetPinCapability{}, fmt.Errorf("configure asset pin handler: %w", err)
	}
	if isNilLikeAssetPinCapabilityDependency(routes) {
		return assetPinCapability{}, errors.New("asset pin handler is required")
	}
	if probeErr := dependencies.probeKubo(ctx, canonicalKuboURL); probeErr != nil {
		return unavailableAssetPinCapability("Kubo readiness unavailable", probeErr), nil
	}

	receiptConsumer := newAssetPinTokenReceiptConsumer(store, dependencies.clock)
	verifier, discoveryErr := dependencies.newVerifier(ctx, cfg, receiptConsumer)
	if discoveryErr != nil {
		return unavailableAssetPinCapability("GitHub OIDC discovery unavailable", discoveryErr), nil
	}
	if isNilLikeAssetPinCapabilityDependency(verifier) {
		return assetPinCapability{}, errors.New("asset pin verifier is required")
	}
	verifierSlot.verifier = verifier

	privateMux := http.NewServeMux()
	routes.RegisterRoutes(privateMux)
	return assetPinCapability{Handler: privateMux}, nil
}

func unavailableAssetPinCapability(message string, cause error) assetPinCapability {
	return assetPinCapability{
		Handler:   http.HandlerFunc(writeAssetPinCapabilityUnavailable),
		HealthErr: fmt.Errorf("%s: %w", message, cause),
	}
}

func probeAssetPinKuboReadiness(ctx context.Context, apiURL string) error {
	if ctx == nil {
		return errors.New("Kubo readiness context is required")
	}
	endpoint, err := url.JoinPath(strings.TrimRight(apiURL, "/"), "/api/v0/version")
	if err != nil {
		return errors.New("build Kubo readiness URL")
	}
	parsed, err := url.Parse(endpoint)
	if err != nil {
		return errors.New("parse Kubo readiness URL")
	}
	parsed.RawQuery = ""
	parsed.Fragment = ""

	probeCtx, cancel := context.WithTimeout(ctx, assetPinKuboProbeTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(probeCtx, http.MethodPost, parsed.String(), nil)
	if err != nil {
		return errors.New("build Kubo readiness request")
	}
	client := &http.Client{
		Timeout: assetPinKuboProbeTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return errors.New("Kubo readiness redirect refused")
		},
	}
	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("call Kubo readiness RPC: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("Kubo readiness RPC returned status %d", response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, assetPinKuboProbeMaxResponseBytes+1))
	if err != nil {
		return errors.New("read Kubo readiness response")
	}
	if len(body) == 0 || len(body) > assetPinKuboProbeMaxResponseBytes {
		return errors.New("Kubo readiness response is empty or oversized")
	}
	var payload struct {
		Version string `json:"Version"`
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	if err := decoder.Decode(&payload); err != nil {
		return errors.New("decode Kubo readiness response")
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return errors.New("Kubo readiness response contains trailing data")
	}
	if payload.Version == "" || strings.TrimSpace(payload.Version) != payload.Version {
		return errors.New("Kubo readiness response has no valid version")
	}
	return nil
}

func isNilLikeAssetPinCapabilityDependency(dependency any) bool {
	if dependency == nil {
		return true
	}
	value := reflect.ValueOf(dependency)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Ptr, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}

func canonicalAssetPinKuboAPIURL(raw string) (string, error) {
	if raw == "" || strings.TrimSpace(raw) != raw {
		return "", errors.New("asset pin Kubo API URL must not contain surrounding whitespace")
	}
	parsed, err := url.Parse(raw)
	if err != nil ||
		(parsed.Scheme != "http" && parsed.Scheme != "https") ||
		parsed.Hostname() == "" || parsed.User != nil || parsed.Opaque != "" ||
		parsed.RawQuery != "" || parsed.ForceQuery ||
		parsed.Fragment != "" || parsed.RawFragment != "" || parsed.RawPath != "" ||
		parsed.String() != raw {
		return "", errors.New("asset pin Kubo API URL must be a canonical absolute HTTP base URL without userinfo, query, or fragment")
	}
	if err := validateAssetPinURLExplicitPort(parsed); err != nil {
		return "", errors.New("asset pin Kubo API URL has an invalid explicit port")
	}
	normalizedPath := strings.TrimSuffix(parsed.Path, "/")
	if normalizedPath != "" && path.Clean(normalizedPath) != normalizedPath {
		return "", errors.New("asset pin Kubo API URL path must be canonical")
	}
	parsed.Path = normalizedPath
	return parsed.String(), nil
}

func validateAssetPinCapabilityOIDCConfig(cfg config.AssetPinConfig) error {
	for _, field := range []struct {
		name  string
		value string
	}{
		{name: "issuer", value: cfg.Issuer},
		{name: "audience", value: cfg.Audience},
		{name: "repository", value: cfg.Repository},
		{name: "ref", value: cfg.Ref},
		{name: "pin workflow", value: cfg.PinWorkflow},
		{name: "decision workflow", value: cfg.DecisionWorkflow},
	} {
		if strings.TrimSpace(field.value) == "" {
			return fmt.Errorf("asset pin capability %s is required", field.name)
		}
		if strings.TrimSpace(field.value) != field.value {
			return fmt.Errorf("asset pin capability %s must not contain surrounding whitespace", field.name)
		}
	}
	issuer, err := url.Parse(cfg.Issuer)
	if err != nil || issuer.Scheme != "https" || issuer.Hostname() == "" ||
		issuer.User != nil || issuer.Opaque != "" || issuer.RawPath != "" ||
		issuer.RawQuery != "" || issuer.ForceQuery ||
		issuer.Fragment != "" || issuer.RawFragment != "" ||
		issuer.String() != cfg.Issuer {
		return errors.New("asset pin capability issuer must be a canonical absolute HTTPS URL without userinfo, query, or fragment")
	}
	if err := validateAssetPinURLExplicitPort(issuer); err != nil {
		return errors.New("asset pin capability issuer has an invalid explicit port")
	}
	return nil
}

func validateAssetPinURLExplicitPort(parsed *url.URL) error {
	if parsed == nil {
		return errors.New("URL is required")
	}

	host := parsed.Host
	port := ""
	if strings.HasPrefix(host, "[") {
		closingBracket := strings.LastIndexByte(host, ']')
		if closingBracket < 0 {
			return errors.New("IPv6 host is missing a closing bracket")
		}
		suffix := host[closingBracket+1:]
		if suffix == "" {
			return nil
		}
		if !strings.HasPrefix(suffix, ":") {
			return errors.New("IPv6 host has an invalid port separator")
		}
		port = suffix[1:]
	} else {
		separator := strings.LastIndexByte(host, ':')
		if separator < 0 {
			return nil
		}
		port = host[separator+1:]
	}

	if port == "" {
		return errors.New("explicit port is empty")
	}
	for _, digit := range port {
		if digit < '0' || digit > '9' {
			return errors.New("explicit port is not numeric")
		}
	}
	portNumber, err := strconv.Atoi(port)
	if err != nil || portNumber < 1 || portNumber > 65535 {
		return errors.New("explicit port is outside the valid range")
	}
	return nil
}

func newAssetPinTokenReceiptConsumer(store interface {
	ConsumeAssetOIDCToken(context.Context, storage.AssetOIDCReceipt) error
}, clock func() time.Time) assetpin.TokenReceiptConsumer {
	return func(ctx context.Context, digest string, expiresAt time.Time, claims assetpin.Claims) error {
		err := store.ConsumeAssetOIDCToken(ctx, storage.AssetOIDCReceipt{
			Digest:      digest,
			ExpiresAt:   expiresAt.UTC(),
			Repository:  claims.Repository,
			Ref:         claims.Ref,
			WorkflowRef: claims.WorkflowRef,
			Actor:       claims.Actor,
			RunID:       claims.RunID,
			RunAttempt:  claims.RunAttempt,
			SHA:         claims.SHA,
			ConsumedAt:  clock().UTC(),
		})
		if errors.Is(err, storage.ErrAssetOIDCTokenReplay) {
			return assetpin.ErrTokenReplay
		}
		return err
	}
}

func registerAssetPinCapabilityRoutes(mux *http.ServeMux, handler http.Handler) {
	mux.Handle("POST /api/v1/assets/pin", handler)
	mux.Handle("POST /api/v1/assets/reference-state", handler)
}

func writeAssetPinCapabilityUnavailable(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusServiceUnavailable)
	_, _ = io.WriteString(w, assetPinCapabilityUnavailableJSON)
}

func isAssetOIDCCapabilityRequest(method string, path string) bool {
	if method != http.MethodPost {
		return false
	}
	switch path {
	case "/api/v1/assets/pin", "/api/v1/assets/reference-state":
		return true
	default:
		return false
	}
}

func assetOIDCCapabilityRequestPath(r *http.Request) string {
	if r != nil && r.URL != nil && r.URL.RawPath != "" && r.URL.RawPath != r.URL.Path {
		return r.URL.RawPath
	}
	if r == nil || r.URL == nil {
		return ""
	}
	return r.URL.Path
}

// serveAdminMuxRequest is the daemon's wallet-authentication wall. Asset OIDC
// routes bypass only this wallet check; they remain outside the anonymous
// gateway policy and enforce their own workflow token before reading a body.
func serveAdminMuxRequest(
	w http.ResponseWriter,
	r *http.Request,
	adminMux http.Handler,
	requireAuth bool,
	assetOIDCCapabilityMounted bool,
	authHandler *auth.Handler,
	publicAPIRequest func(method, path string) bool,
) {
	// Default-deny: gate all API and plugin routes behind auth, except
	// explicitly listed public endpoints and the two exact OIDC capabilities.
	if requireAuth {
		if assetOIDCCapabilityMounted && isAssetOIDCCapabilityRequest(r.Method, assetOIDCCapabilityRequestPath(r)) {
			adminMux.ServeHTTP(w, r)
			return
		}
		if authHandler == nil {
			http.Error(w, "authentication unavailable", http.StatusServiceUnavailable)
			return
		}

		requestPath := r.URL.Path
		isAPIOrPlugin := strings.HasPrefix(requestPath, "/api/") ||
			strings.HasPrefix(requestPath, "/orbpro-key-broker/")

		if isAPIOrPlugin && !publicAPIRequest(r.Method, requestPath) {
			minTrust := peers.Standard
			if isAdminOnlyAPIPath(requestPath) {
				minTrust = peers.Admin
			}
			authHandler.RequireAuth(minTrust, func(w http.ResponseWriter, r *http.Request) {
				adminMux.ServeHTTP(w, r)
			})(w, r)
			return
		}
	}
	adminMux.ServeHTTP(w, r)
}

func isPublicAPIPath(path string) bool {
	return isPublicAPIRequest(http.MethodGet, path)
}

func isPublicAPIRequest(method string, path string) bool {
	method = strings.ToUpper(strings.TrimSpace(method))
	if method == http.MethodOptions {
		return isPublicAPIRequest(http.MethodGet, path) ||
			isPublicAPIRequest(http.MethodPost, path)
	}

	if method == http.MethodPost {
		switch path {
		case "/api/auth/challenge", "/api/auth/verify", "/api/storefront/listings/search", "/api/storefront/payments/stripe/webhook":
			return true
		}
		return false
	}

	if method != http.MethodGet && method != http.MethodHead {
		return false
	}

	return isPublicReadAPIPath(path)
}

func isPublicReadAPIPath(path string) bool {
	switch path {
	case "/api/module-delivery/provider",
		"/api/module-delivery/listings",
		// Gateway API docs surface (loop G.1, docs/gateway-api.md §4): the
		// spec + reference UI are part of the anonymous read surface — an
		// API you cannot read the docs of is not a public gateway.
		"/api/v1/openapi.json",
		"/api/v1/docs",
		"/api/node/info",
		"/api/node/epm",
		"/api/node/epm/json",
		"/api/node/epm/vcard",
		"/api/node/epm/qr",
		"/api/relay/status",
		"/api/auth/status",
		"/api/storefront/listings",
		"/api/v1/catalog",
		"/api/v1/data/health",
		// Anonymous per-record index page for explicitly selected schemas.
		"/api/v1/data/index",
		// Flow-served record retrieval (loop C.4: the data-retrieval flow
		// mounted at /api/v1/data/ owns routing/format/ETag inside wasm).
		"/api/v1/data/omm/bulk",
		"/sdn/libp2p.js",
		"/api/v1/id",
		"/api/v1/version",
		"/api/v1/stats",
		"/api/v1/pubsub/topics",
		"/api/v1/pubsub/messages",
		"/api/v1/peers":
		return true
	}

	return strings.HasPrefix(path, "/api/directory/") ||
		strings.HasPrefix(path, "/api/v1/docs/") ||
		path == "/api/v1/channels" ||
		strings.HasPrefix(path, "/api/v1/channels/") ||
		strings.HasPrefix(path, "/api/v1/demo/") ||
		strings.HasPrefix(path, "/api/storefront/listings/") ||
		strings.HasPrefix(path, "/api/storefront/trust/") ||
		strings.HasPrefix(path, "/api/v1/log/")
}

func isWebhookPath(path string) bool {
	return strings.HasPrefix(path, "/api/storefront/payments/stripe/webhook")
}

func applyPublicAPICORSHeaders(header http.Header, origin string) {
	allowedOrigin := strings.TrimSpace(origin)
	if allowedOrigin == "" {
		allowedOrigin = "*"
	}
	header.Set("Access-Control-Allow-Origin", allowedOrigin)
	header.Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
	header.Set("Access-Control-Allow-Headers", "Origin, X-Requested-With, Content-Type, Accept, Authorization")
	header.Set("Vary", "Origin")
}

func hasSessionCookie(r *http.Request) bool {
	if _, err := r.Cookie("sdn_wallet_session"); err == nil {
		return true
	}
	if _, err := r.Cookie("sdn_session"); err == nil {
		return true
	}
	return false
}

func isSameOrigin(r *http.Request, raw string) bool {
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return false
	}
	if u.Hostname() == "" {
		return false
	}

	originHost := strings.ToLower(u.Hostname())
	originPort := u.Port()
	if originPort == "" {
		originPort = defaultPortForScheme(u.Scheme)
	}

	expectedURL, err := url.Parse(u.Scheme + "://" + r.Host)
	if err != nil || expectedURL.Hostname() == "" {
		return false
	}
	expectedHost := strings.ToLower(expectedURL.Hostname())
	expectedPort := expectedURL.Port()
	if expectedPort == "" {
		expectedPort = defaultPortForScheme(u.Scheme)
	}

	return originHost == expectedHost && originPort == expectedPort
}

// adminSecurityMiddleware wraps next with the baseline security headers and
// CSRF protection enforced on every admin-server request (except tunneled
// websocket upgrades, which bypass this middleware entirely at the call
// site since they are a raw proxy passthrough). This is the single place
// that wiring is applied in front of adminMux; see
// TestAdminSecurityMiddlewareSetsSecurityHeaders and
// TestAdminSecurityMiddlewareBlocksCrossOriginStateChange, which fail if a
// future refactor stops wrapping adminMux with it.
func adminSecurityMiddleware(next http.Handler, tlsMode string, publicAPIRequest func(method, path string) bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Global security headers on ALL responses
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "strict-origin-when-cross-origin")

		// Cross-origin isolation headers are set by the frontend handler
		// (makeFrontendHandler) for OrbPro routes that need SharedArrayBuffer.
		if tlsMode == tlsmgr.ModeStatic {
			w.Header().Set("Strict-Transport-Security", "max-age=63072000; includeSubDomains")
		}

		if publicAPIRequest(r.Method, r.URL.Path) {
			applyPublicAPICORSHeaders(w.Header(), r.Header.Get("Origin"))
			if r.Method == http.MethodOptions {
				w.WriteHeader(http.StatusNoContent)
				return
			}
		}

		// The wallet callback is an exact, read-only static route. Let its
		// handler own method rejection so every non-GET/HEAD request receives
		// the callback-specific 405, Allow, and cache-isolation headers even
		// when a stale admin session cookie is present.
		if isWalletCallbackPath(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}

		// CSRF protection: for state-changing requests using cookie auth,
		// require same-origin Origin/Referer, or X-Requested-With.
		if r.Method != http.MethodGet && r.Method != http.MethodHead && r.Method != http.MethodOptions {
			if hasSessionCookie(r) && !isWebhookPath(r.URL.Path) && !publicAPIRequest(r.Method, r.URL.Path) {
				origin := strings.TrimSpace(r.Header.Get("Origin"))
				referer := strings.TrimSpace(r.Header.Get("Referer"))
				xrw := strings.TrimSpace(r.Header.Get("X-Requested-With"))

				// If Origin is present, enforce same-origin.
				if origin != "" {
					if !isSameOrigin(r, origin) {
						http.Error(w, "CSRF validation failed (origin mismatch)", http.StatusForbidden)
						return
					}
				} else if referer != "" {
					// Otherwise fall back to Referer check.
					if !isSameOrigin(r, referer) {
						http.Error(w, "CSRF validation failed (referer mismatch)", http.StatusForbidden)
						return
					}
				} else if xrw == "" {
					// No Origin/Referer: require explicit X-Requested-With (AJAX).
					http.Error(w, "CSRF validation failed (missing origin)", http.StatusForbidden)
					return
				}
			}
		}

		next.ServeHTTP(w, r)
	})
}

func defaultPortForScheme(scheme string) string {
	if scheme == "https" {
		return "443"
	}
	if scheme == "http" {
		return "80"
	}
	return ""
}

// isAdminWebSocketPath reports whether the path is a websocket endpoint served
// by the admin mux itself rather than the libp2p ws transport tunnel.
func isAdminWebSocketPath(path string) bool {
	switch strings.TrimSuffix(path, "/") {
	case "/ws", "/ws/status":
		return true
	}
	return false
}

func isWebSocketUpgradeRequest(r *http.Request) bool {
	if r == nil {
		return false
	}
	if !headerHasToken(r.Header.Get("Connection"), "upgrade") {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(r.Header.Get("Upgrade")), "websocket")
}

func headerHasToken(rawValue string, token string) bool {
	target := strings.ToLower(strings.TrimSpace(token))
	if target == "" {
		return false
	}
	for _, entry := range strings.Split(strings.ToLower(rawValue), ",") {
		if strings.TrimSpace(entry) == target {
			return true
		}
	}
	return false
}

func resolveLocalLibp2pWsProxyTarget(listenAddrs []string) (*url.URL, string) {
	for _, rawAddr := range listenAddrs {
		addr := strings.TrimSpace(rawAddr)
		if addr == "" {
			continue
		}
		if strings.Contains(addr, "/wss") || !strings.Contains(addr, "/ws") {
			continue
		}
		port := extractTCPPortFromMultiaddr(addr)
		if port == "" {
			continue
		}

		target, err := url.Parse("http://127.0.0.1:" + port)
		if err != nil {
			continue
		}
		return target, addr
	}

	return nil, ""
}

func extractTCPPortFromMultiaddr(addr string) string {
	clean := strings.Trim(addr, "/")
	if clean == "" {
		return ""
	}
	parts := strings.Split(clean, "/")
	for i := 0; i+1 < len(parts); i++ {
		if parts[i] != "tcp" {
			continue
		}
		port := strings.TrimSpace(parts[i+1])
		if port != "" {
			return port
		}
	}
	return ""
}

func isAdminOnlyAPIPath(path string) bool {
	return strings.HasPrefix(path, "/api/peers") ||
		strings.HasPrefix(path, "/api/groups") ||
		strings.HasPrefix(path, "/api/blocklist") ||
		strings.HasPrefix(path, "/api/settings") ||
		strings.HasPrefix(path, "/api/export") ||
		strings.HasPrefix(path, "/api/import") ||
		strings.HasPrefix(path, "/api/admin/") ||
		strings.HasPrefix(path, "/api/auth/users") ||
		strings.HasPrefix(path, "/api/v0") ||
		strings.HasPrefix(path, "/api/v1/admin/") ||
		path == "/api/v1/data/summary" ||
		path == "/api/v1/data/query" ||
		strings.HasPrefix(path, "/api/v1/search/") ||
		path == "/api/v1/conjunction/screen" ||
		strings.HasPrefix(path, "/api/v1/data/records/") ||
		strings.HasPrefix(path, "/api/v1/modules/runtime/") ||
		strings.HasPrefix(path, "/api/v1/plugins/") ||
		strings.HasPrefix(path, "/api/routing/") ||
		strings.HasPrefix(path, "/api/streaming/") ||
		strings.HasPrefix(path, "/api/relay/filters") ||
		strings.HasPrefix(path, "/api/storefront/dashboard/admin") ||
		// AI query-log operator diagnostic dashboard (gap B10.1): client
		// IPs, user queries, generated SQL, and provider/model info.
		strings.HasPrefix(path, "/api/v1/diag") ||
		// Module capability approvals (gap B10.1 audit follow-up):
		// POST .../approve and .../revoke are operator actions that were
		// previously reachable at Standard trust because this prefix was
		// missing. isAdminOnlyAPIPath is prefix-only with no method
		// granularity, so the whole /api/modules/capabilities surface
		// (including the GET list/tiers reads) becomes Admin-only rather
		// than splitting read/write here.
		strings.HasPrefix(path, "/api/modules/capabilities")
}

// gateHandlerWithTrust enforces a minimum trust level on inner before
// serving the request. It is the shared self-gating primitive for mounts
// (the flow editor, /webui, /ws) that live outside the /api/ and
// /orbpro-key-broker/ prefixes the top-level auth wall's isAPIOrPlugin check
// inspects, and so would otherwise bypass auth entirely even when
// cfg.Admin.RequireAuth is set.
func gateHandlerWithTrust(w http.ResponseWriter, r *http.Request, inner http.Handler, authHandler *auth.Handler, requireAuth bool, minTrust peers.TrustLevel) {
	if !requireAuth {
		inner.ServeHTTP(w, r)
		return
	}
	if authHandler == nil {
		http.Error(w, "authentication unavailable", http.StatusServiceUnavailable)
		return
	}
	authHandler.RequireAuth(minTrust, inner.ServeHTTP)(w, r)
}

// gateAdminOnlyHandler enforces admin-trust authentication on inner before
// serving the request, mirroring the self-gating pattern used by /admin and
// /webui. It exists for mounts (like the flow editor, whose /debug/ and
// /inject/ stub routes must never be reachable unauthenticated) that live
// outside the /api/ and /orbpro-key-broker/ prefixes the top-level auth
// wall's isAPIOrPlugin check inspects, and so would otherwise bypass auth
// entirely even when cfg.Admin.RequireAuth is set.
func gateAdminOnlyHandler(w http.ResponseWriter, r *http.Request, inner http.Handler, authHandler *auth.Handler, requireAuth bool) {
	gateHandlerWithTrust(w, r, inner, authHandler, requireAuth, peers.Admin)
}

func adminLandingHandler(next http.Handler, landingHTML []byte) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" || r.URL.Path == "/index.html" {
			if r.Method != http.MethodGet && r.Method != http.MethodHead {
				http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
				return
			}
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Header().Set("Cache-Control", "public, max-age=120")
			w.WriteHeader(http.StatusOK)
			if r.Method != http.MethodHead {
				_, _ = w.Write(landingHTML)
			}
			return
		}
		next.ServeHTTP(w, r)
	})
}

func serveFavicon(w http.ResponseWriter, r *http.Request, candidatePaths []string) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	for _, candidate := range candidatePaths {
		candidate = strings.TrimSpace(candidate)
		if candidate == "" {
			continue
		}
		if st, err := os.Stat(candidate); err == nil && !st.IsDir() {
			http.ServeFile(w, r, candidate)
			return
		}
	}

	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "public, max-age=3600")
	w.WriteHeader(http.StatusOK)
	if r.Method != http.MethodHead {
		_, _ = w.Write(defaultFaviconPNG)
	}
}

func normalizeIPFSGatewayCORSHeaders(header http.Header) {
	header.Set("Access-Control-Allow-Origin", "*")
	header.Set("Access-Control-Allow-Methods", "GET, HEAD, OPTIONS")
	header.Set("Access-Control-Allow-Headers", "Content-Type, Range, User-Agent, X-Requested-With")
	header.Set("Access-Control-Expose-Headers", "Content-Length, Content-Range, X-Chunked-Output, X-Ipfs-Path, X-Ipfs-Roots, X-Stream-Output")
}

func makeWebUIHandler(buildDir string, _ string) (http.Handler, error) {
	buildDir = strings.TrimSpace(buildDir)
	if buildDir == "" {
		return nil, fmt.Errorf("webui_path is empty")
	}

	indexPath := filepath.Join(buildDir, "index.html")
	if st, err := os.Stat(indexPath); err != nil {
		return nil, fmt.Errorf("webui_path %q: missing index.html: %w", buildDir, err)
	} else if st.IsDir() {
		return nil, fmt.Errorf("webui_path %q: index.html is a directory", buildDir)
	}

	fs := http.FileServer(http.Dir(buildDir))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		clean := path.Clean("/" + r.URL.Path)
		clean = strings.TrimPrefix(clean, "/")
		if clean != "" {
			full := filepath.Join(buildDir, filepath.FromSlash(clean))
			if st, err := os.Stat(full); err == nil && !st.IsDir() {
				fs.ServeHTTP(w, r)
				return
			}
		}

		if ext := path.Ext(r.URL.Path); ext != "" && r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}

		http.ServeFile(w, r, indexPath)
	}), nil
}

func resolveAdminUIPath(configuredPath string) string {
	candidates := []string{
		strings.TrimSpace(configuredPath),
		config.DefaultAdminUIPath(),
		"/opt/spacedatanetwork/admin-ui",
		filepath.Join("sdn-js", "ui", "dist"),
		filepath.Join("..", "sdn-js", "ui", "dist"),
		filepath.Join("..", "..", "sdn-js", "ui", "dist"),
	}
	for _, candidate := range candidates {
		candidate = strings.TrimSpace(candidate)
		if candidate == "" {
			continue
		}
		info, err := os.Stat(filepath.Join(candidate, "index.html"))
		if err == nil && !info.IsDir() {
			return candidate
		}
	}
	return ""
}

func resolveFrontendPath(configuredPath string) string {
	configuredPath = strings.TrimSpace(configuredPath)
	if configuredPath != "" {
		return configuredPath
	}
	if candidate := firstExistingFrontendPath(defaultFrontendCandidates()); candidate != "" {
		return candidate
	}
	return config.DefaultFrontendPath()
}

func defaultFrontendCandidates() []string {
	return []string{
		filepath.Join("sdn-js", "ui", "dist"),
		filepath.Join("..", "sdn-js", "ui", "dist"),
		filepath.Join("..", "..", "sdn-js", "ui", "dist"),
		filepath.Join("..", "..", "..", "sdn-js", "ui", "dist"),
		"/opt/spacedatanetwork/sdn-ui",
		"/opt/spacedatanetwork/frontend",
		config.DefaultFrontendPath(),
	}
}

func firstExistingFrontendPath(candidates []string) string {
	for _, candidate := range candidates {
		candidate = strings.TrimSpace(candidate)
		if candidate == "" {
			continue
		}
		info, err := os.Stat(filepath.Join(candidate, "index.html"))
		if err == nil && !info.IsDir() {
			return candidate
		}
	}
	return ""
}

// provisionFrontendDir creates the frontend directory with a default index.html
// if it doesn't already exist.
func provisionFrontendDir(dir string) error {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return fmt.Errorf("frontend path is empty")
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	indexPath := filepath.Join(dir, "index.html")
	if st, err := os.Stat(indexPath); err == nil {
		if st.IsDir() {
			return fmt.Errorf("%s is a directory", indexPath)
		}
		return nil
	}
	return os.WriteFile(indexPath, []byte(defaultFrontendHTML), 0644)
}

//go:embed default_frontend.html
var defaultFrontendHTML string

// makeFrontendHandler creates a static file server for the public frontend
// directory with SPA fallback and cross-origin isolation headers for OrbPro.
func makeFrontendHandler(frontendDir string) (http.Handler, error) {
	frontendDir = strings.TrimSpace(frontendDir)
	if frontendDir == "" {
		return nil, fmt.Errorf("frontend_path is empty")
	}

	info, err := os.Stat(frontendDir)
	if err != nil {
		return nil, fmt.Errorf("frontend_path %q: %w", frontendDir, err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("frontend_path %q: not a directory", frontendDir)
	}

	indexPath := filepath.Join(frontendDir, "index.html")
	if _, err := os.Stat(indexPath); err != nil {
		return nil, fmt.Errorf("frontend_path %q: missing index.html: %w", frontendDir, err)
	}

	fs := http.FileServer(http.Dir(frontendDir))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		// Cross-origin isolation for SharedArrayBuffer (required by OrbPro/WASM)
		w.Header().Set("Cross-Origin-Opener-Policy", "same-origin")
		w.Header().Set("Cross-Origin-Embedder-Policy", "require-corp")

		// Serve index.html with injected config for "/" and "/index.html"
		if r.URL.Path == "/" || r.URL.Path == "/index.html" {
			injectedHTML, err := loadInjectedFrontendIndex(indexPath)
			if err != nil {
				http.Error(w, "frontend unavailable", http.StatusServiceUnavailable)
				return
			}
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Header().Set("Cache-Control", "no-store")
			w.WriteHeader(http.StatusOK)
			if r.Method != http.MethodHead {
				_, _ = w.Write(injectedHTML)
			}
			return
		}

		// Serve existing files directly
		clean := path.Clean("/" + r.URL.Path)
		clean = strings.TrimPrefix(clean, "/")
		if clean != "" {
			full := filepath.Join(frontendDir, filepath.FromSlash(clean))
			if st, err := os.Stat(full); err == nil && !st.IsDir() {
				w.Header().Set("Cache-Control", "public, max-age=1800")
				fs.ServeHTTP(w, r)
				return
			}
		}

		// Asset paths (have extension) → 404
		if ext := path.Ext(r.URL.Path); ext != "" && r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}

		// SPA fallback — serve injected index.html
		injectedHTML, err := loadInjectedFrontendIndex(indexPath)
		if err != nil {
			http.Error(w, "frontend unavailable", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusOK)
		if r.Method != http.MethodHead {
			_, _ = w.Write(injectedHTML)
		}
	}), nil
}

func loadInjectedFrontendIndex(indexPath string) ([]byte, error) {
	indexHTML, err := os.ReadFile(indexPath)
	if err != nil {
		return nil, err
	}
	return injectFrontendConfig(indexHTML), nil
}

func makeFrontendSurfaceHandler(frontendHandler http.Handler, _ *auth.Handler, _ bool) http.Handler {
	return frontendHandler
}

// injectFrontendConfig injects SDN runtime configuration into index.html.
// This adds a <script> block before the closing </head> tag with the node's
// IPFS peer info so the frontend can connect over libp2p for key exchange.
// Plugin key exchange happens over encrypted IPFS/libp2p, NOT HTTP.
func injectFrontendConfig(html []byte) []byte {
	configScript := []byte(`<script>window.__SDN_CONFIG__={apiBase:"/api/v1",serverBaseUrl:window.location.origin,ipfsDashboardUrl:"/webui/"};</script>`)
	// Try to inject before </head>
	if idx := bytes.Index(html, []byte("</head>")); idx >= 0 {
		result := make([]byte, 0, len(html)+len(configScript))
		result = append(result, html[:idx]...)
		result = append(result, configScript...)
		result = append(result, html[idx:]...)
		return result
	}
	// Fallback: prepend to the whole document
	return append(configScript, html...)
}

// loadLandingPageFallback loads a custom landing page or returns the built-in default.
func loadLandingPageFallback(homepageFile string) []byte {
	html, err := loadLandingPage(homepageFile)
	if err != nil {
		if strings.TrimSpace(homepageFile) != "" {
			log.Warnf("Falling back to built-in landing page: %v", err)
		}
		return []byte(defaultFrontendHTML)
	}
	return html
}

// handleLibp2pJS serves a JavaScript module with the node's raw IP, peer ID,
// and ws:// multiaddr injected at request time. Browsers can load this script
// to connect to the node using the raw IP without DNS resolution.
//
//	GET /sdn/libp2p.js → application/javascript
func handleLibp2pJS(n *node.Node) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		peerID := n.PeerID().String()
		addrs := n.ListenAddrs()

		// Find the first public /ip4/<ip>/tcp/<port>/ws multiaddr.
		var wsMultiaddr string
		for _, a := range addrs {
			s := a.String()
			if strings.Contains(s, "/ws") &&
				!strings.Contains(s, "/ip4/127.") &&
				!strings.Contains(s, "/ip6/::1") {
				if !strings.HasSuffix(s, "/p2p/"+peerID) {
					s += "/p2p/" + peerID
				}
				wsMultiaddr = s
				break
			}
		}

		// Collect all listen address strings.
		addrStrings := make([]string, len(addrs))
		for i, a := range addrs {
			addrStrings[i] = a.String()
		}
		addrsJSON, _ := json.Marshal(addrStrings)

		js := fmt.Sprintf(
			`// Auto-generated by SpaceAware SDN server — do not edit.
// Connection parameters injected at request time.
export const SDN_PEER_ID = %q;
export const SDN_WS_MULTIADDR = %q;
export const SDN_LISTEN_ADDRS = %s;
`,
			peerID, wsMultiaddr, addrsJSON)

		w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Write([]byte(js))
	}
}

// handleNodeInfo returns an HTTP handler that serves the node's public identity info.
// The response is the full EPM JSON with runtime metadata overlaid.
func handleNodeInfo(n *node.Node, torRuntime *tor.Runtime) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		// Start with the full EPM JSON as the base response
		var info map[string]interface{}
		if epmSvc := n.EPMService(); epmSvc != nil {
			info = epmSvc.GetNodeEPMJSON()
		}
		if info == nil {
			info = make(map[string]interface{})
		}
		promoteNodeInfoKeyFields(info)

		// Overlay runtime metadata
		info["peer_id"] = n.PeerID().String()
		info["mode"] = n.Config().Mode
		info["version"] = versioninfo.AgentVersion
		info["agent_version"] = versioninfo.AgentVersion
		info["suite_version"] = versioninfo.SuiteVersion
		info["standards_version"] = versioninfo.SpaceDataStandardsVersion
		info["advertisement_flag"] = versioninfo.CurrentAdvertisementFlag
		// Peer populations, split: the raw libp2p/DHT swarm (ipfs) and the
		// subset that are real SDN nodes (sdn connected / sdn_known observed).
		info["peers"] = nodeInfoPeerCounts(n)

		addrs := n.ListenAddrs()
		addrStrings := make([]string, len(addrs))
		for i, a := range addrs {
			addrStrings[i] = a.String()
		}
		info["listen_addresses"] = addrStrings

		if torRuntime != nil && torRuntime.OnionHost() != "" {
			info["onion_address"] = torRuntime.OnionHost()
		}

		// Boot check surface (task sdn-licensing-module-load): every WASM
		// module that failed to load this boot, so a fail-closed capability
		// rejection is visible to operators without grepping the journal.
		// API-synthesized field: lowercase keys by convention.
		if failures := n.ModuleLoadFailures(); len(failures) > 0 {
			info["module_load_failures"] = failures
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(info)
	}
}

func promoteNodeInfoKeyFields(info map[string]interface{}) {
	if info == nil {
		return
	}

	keys, ok := info["keys"]
	if !ok {
		return
	}

	for _, key := range nodeInfoKeyEntries(keys) {
		keyType := strings.ToLower(strings.TrimSpace(nodeInfoStringValue(key["key_type"])))
		if keyType == "" {
			continue
		}

		pubKeyField := keyType + "_pubkey_hex"
		keyPathField := keyType + "_key_path"
		publicKey := nodeInfoStringValue(key["public_key"])
		keyPath := nodeInfoStringValue(key["key_path"])
		if keyPath == "" {
			keyPath = nodeInfoStringValue(key["key_address"])
		}

		if publicKey != "" && nodeInfoStringValue(info[pubKeyField]) == "" {
			info[pubKeyField] = publicKey
		}
		if keyPath != "" && nodeInfoStringValue(info[keyPathField]) == "" {
			info[keyPathField] = keyPath
		}
		if xpub := nodeInfoStringValue(key["xpub"]); xpub != "" && nodeInfoStringValue(info["xpub"]) == "" {
			info["xpub"] = xpub
		}
	}
}

func nodeInfoKeyEntries(raw interface{}) []map[string]interface{} {
	switch keys := raw.(type) {
	case []map[string]interface{}:
		return append([]map[string]interface{}(nil), keys...)
	case []interface{}:
		entries := make([]map[string]interface{}, 0, len(keys))
		for _, entry := range keys {
			if key, ok := entry.(map[string]interface{}); ok {
				entries = append(entries, key)
			}
		}
		return entries
	default:
		return nil
	}
}

func nodeInfoStringValue(value interface{}) string {
	text, _ := value.(string)
	return text
}

type providerDescriptorSource interface {
	PeerID() peer.ID
	ListenAddrs() []multiaddr.Multiaddr
	Host() libp2phost.Host
	EPMService() *epm.Service
}

type providerDescriptorIdentityAddress struct {
	Chain     string `json:"chain"`
	Address   string `json:"address"`
	KeyPath   string `json:"keyPath,omitempty"`
	PublicKey string `json:"publicKey,omitempty"`
}

type providerDescriptorIdentityResponse struct {
	XPub                string                              `json:"xpub,omitempty"`
	IdentityPublicKey   string                              `json:"identityPublicKey,omitempty"`
	SigningPublicKey    string                              `json:"signingPublicKey,omitempty"`
	EncryptionPublicKey string                              `json:"encryptionPublicKey,omitempty"`
	IPNSEntries         []string                            `json:"ipnsEntries,omitempty"`
	ENSNames            []string                            `json:"ensNames,omitempty"`
	Addresses           []providerDescriptorIdentityAddress `json:"addresses,omitempty"`
}

type providerDescriptorResponse struct {
	PublicKey      string                              `json:"publicKey"`
	PeerID         string                              `json:"peerId"`
	IPNS           string                              `json:"ipns,omitempty"`
	RelayAddresses []string                            `json:"relayAddresses,omitempty"`
	Identity       *providerDescriptorIdentityResponse `json:"identity,omitempty"`
}

type moduleDeliveryListingsResult struct {
	PluginID   string `json:"plugin_id,omitempty"`
	Version    string `json:"version,omitempty"`
	DataBase64 string `json:"data_base64"`
	Timestamp  string `json:"timestamp,omitempty"`
}

type moduleDeliveryListingsResponse struct {
	Results []moduleDeliveryListingsResult `json:"results"`
	Count   int                            `json:"count"`
}

func handleProviderDescriptor(src providerDescriptorSource) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
		w.Header().Set("Access-Control-Max-Age", "86400")

		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		payload, err := buildProviderDescriptor(src)
		if err != nil {
			http.Error(w, "provider descriptor unavailable", http.StatusServiceUnavailable)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(payload)
	}
}

func handleModuleDeliveryListings(reg *license.PluginRegistry) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
		w.Header().Set("Access-Control-Max-Age", "86400")

		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		listings, err := node.BuildModuleDeliveryListings(reg)
		if err != nil {
			http.Error(w, "module-delivery listings unavailable", http.StatusServiceUnavailable)
			return
		}

		results := make([]moduleDeliveryListingsResult, 0, len(listings))
		for _, listing := range listings {
			results = append(results, moduleDeliveryListingsResult{
				PluginID:   listing.PluginID,
				Version:    listing.Version,
				DataBase64: base64.StdEncoding.EncodeToString(listing.Payload),
				Timestamp:  listing.Timestamp,
			})
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(moduleDeliveryListingsResponse{
			Results: results,
			Count:   len(results),
		})
	}
}

func handleModuleRuntimeSnapshot(mgr *plugins.Manager, reg *license.PluginRegistry) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		snapshot := plugins.RuntimeSnapshot{
			GeneratedAt: time.Now().UTC().Format(time.RFC3339),
			Modules:     []plugins.RuntimeModuleEntry{},
		}
		if mgr != nil {
			snapshot = mgr.RuntimeSnapshot()
		}
		mergeModuleRuntimeCatalog(&snapshot, reg)
		snapshot.Count = len(snapshot.Modules)

		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-cache")
		_ = json.NewEncoder(w).Encode(snapshot)
	}
}

func handleModuleRuntimeMutation(mgr *plugins.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if mgr == nil {
			http.Error(w, "module runtime unavailable", http.StatusServiceUnavailable)
			return
		}

		moduleID, kind, key, ok := parseModuleRuntimeMutationPath(r.URL.Path)
		if !ok {
			http.NotFound(w, r)
			return
		}

		switch kind {
		case "options":
			if r.Method != http.MethodPatch && r.Method != http.MethodPost {
				http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
				return
			}
			var payload struct {
				Value string `json:"value"`
			}
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				http.Error(w, "invalid option payload", http.StatusBadRequest)
				return
			}
			option, err := mgr.UpdateRuntimeModuleOption(r.Context(), moduleID, key, payload.Value)
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Cache-Control", "no-cache")
			_ = json.NewEncoder(w).Encode(option)
		case "inputs":
			if r.Method != http.MethodPatch && r.Method != http.MethodPost {
				http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
				return
			}
			var payload struct {
				Values []plugins.RuntimeModuleInputValue `json:"values"`
			}
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				http.Error(w, "invalid input payload", http.StatusBadRequest)
				return
			}
			values, err := mgr.SaveRuntimeModuleInputValues(r.Context(), moduleID, payload.Values)
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Cache-Control", "no-cache")
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"moduleId":       moduleID,
				"restartPending": true,
				"inputValues":    values,
			})
		case "history":
			if r.Method != http.MethodGet {
				http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
				return
			}
			history, err := mgr.RuntimeModuleCommandHistory(moduleID)
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Cache-Control", "no-cache")
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"moduleId": moduleID,
				"history":  history,
			})
		case "schedules":
			if key == "" {
				http.NotFound(w, r)
				return
			}
			if strings.HasSuffix(key, "/run") {
				if r.Method != http.MethodPost {
					http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
					return
				}
				methodID := strings.TrimSuffix(key, "/run")
				run, err := mgr.RunRuntimeModuleScheduleNow(r.Context(), moduleID, methodID)
				if err != nil {
					http.Error(w, err.Error(), http.StatusBadRequest)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("Cache-Control", "no-cache")
				_ = json.NewEncoder(w).Encode(run)
				return
			}
			if r.Method != http.MethodPatch && r.Method != http.MethodPost {
				http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
				return
			}
			var payload plugins.RuntimeModuleScheduleConfig
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				http.Error(w, "invalid schedule payload", http.StatusBadRequest)
				return
			}
			schedule, err := mgr.SaveRuntimeModuleSchedule(r.Context(), moduleID, key, payload)
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Cache-Control", "no-cache")
			_ = json.NewEncoder(w).Encode(schedule)
		case "actions":
			if r.Method != http.MethodPost {
				http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
				return
			}
			if err := mgr.RunRuntimeModuleAction(r.Context(), moduleID, key); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Cache-Control", "no-cache")
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"ok":       true,
				"moduleId": moduleID,
				"actionId": key,
			})
		default:
			http.NotFound(w, r)
		}
	}
}

func parseModuleRuntimeMutationPath(pathValue string) (moduleID, kind, key string, ok bool) {
	rest := strings.TrimPrefix(pathValue, "/api/v1/modules/runtime/")
	if rest == pathValue || strings.TrimSpace(rest) == "" {
		return "", "", "", false
	}
	parts := strings.Split(rest, "/")
	if len(parts) < 2 {
		return "", "", "", false
	}
	decodedModuleID, err := url.PathUnescape(parts[0])
	if err != nil {
		return "", "", "", false
	}
	kind = strings.TrimSpace(parts[1])
	if kind != "options" && kind != "actions" && kind != "inputs" && kind != "history" && kind != "schedules" {
		return "", "", "", false
	}
	moduleID = strings.TrimSpace(decodedModuleID)
	if kind == "inputs" || kind == "history" {
		if len(parts) != 2 || moduleID == "" {
			return "", "", "", false
		}
		return moduleID, kind, "", true
	}
	if len(parts) < 3 {
		return "", "", "", false
	}
	decodedKey, err := url.PathUnescape(strings.Join(parts[2:], "/"))
	if err != nil {
		return "", "", "", false
	}
	key = strings.TrimSpace(decodedKey)
	if moduleID == "" || key == "" {
		return "", "", "", false
	}
	return moduleID, kind, key, true
}

func mergeModuleRuntimeCatalog(snapshot *plugins.RuntimeSnapshot, reg *license.PluginRegistry) {
	if snapshot == nil || reg == nil {
		return
	}
	seen := make(map[string]int, len(snapshot.Modules))
	for index, module := range snapshot.Modules {
		seen[module.ID] = index
	}
	for _, descriptor := range reg.ListPublic() {
		catalog := &plugins.RuntimeModuleCatalog{
			RequiredScope:   descriptor.RequiredScope,
			ContentType:     descriptor.ContentType,
			CacheControl:    descriptor.CacheControl,
			BundleSHA256:    descriptor.BundleSHA256,
			SizeBytes:       descriptor.SizeBytes,
			SignatureHex:    descriptor.SignatureHex,
			SignerPubKeyHex: descriptor.SignerPubKeyHex,
			UploadedAt:      descriptor.UploadedAt,
		}
		if index, ok := seen[descriptor.ID]; ok {
			snapshot.Modules[index].Catalog = catalog
			if snapshot.Modules[index].Version == "" {
				snapshot.Modules[index].Version = descriptor.Version
			}
			if snapshot.Modules[index].Status == "" || snapshot.Modules[index].Status == "registered" {
				snapshot.Modules[index].Status = descriptor.Status
				snapshot.Modules[index].StatusMessage = descriptor.StatusMessage
			}
			continue
		}
		snapshot.Modules = append(snapshot.Modules, plugins.RuntimeModuleEntry{
			ID:            descriptor.ID,
			Version:       descriptor.Version,
			Status:        descriptor.Status,
			StatusMessage: descriptor.StatusMessage,
			Catalog:       catalog,
		})
	}
}

func buildProviderDescriptor(src providerDescriptorSource) (*providerDescriptorResponse, error) {
	if src == nil {
		return nil, fmt.Errorf("provider descriptor source is nil")
	}

	publicKeyHex, err := providerPublicKeyHex(src.Host(), src.PeerID())
	if err != nil {
		return nil, err
	}

	peerID := src.PeerID().String()
	response := &providerDescriptorResponse{
		PublicKey: publicKeyHex,
		PeerID:    peerID,
		Identity:  buildProviderDescriptorIdentity(src, publicKeyHex, peerID),
	}
	if peerID != "" {
		response.IPNS = "/ipns/" + peerID
	}

	for _, addr := range src.ListenAddrs() {
		if addr == nil {
			continue
		}
		response.RelayAddresses = append(response.RelayAddresses, addr.String())
	}

	return response, nil
}

func buildProviderDescriptorIdentity(src providerDescriptorSource, defaultPublicKeyHex, peerID string) *providerDescriptorIdentityResponse {
	identity := &providerDescriptorIdentityResponse{}
	if strings.TrimSpace(defaultPublicKeyHex) != "" {
		identity.IdentityPublicKey = defaultPublicKeyHex
	}
	if strings.TrimSpace(peerID) != "" {
		identity.IPNSEntries = []string{"/ipns/" + peerID}
	}
	if src == nil || src.EPMService() == nil {
		return identity
	}

	info := src.EPMService().GetNodeEPMJSON()
	if len(info) == 0 {
		return identity
	}

	if xpub := nodeInfoStringValue(info["xpub"]); xpub != "" {
		identity.XPub = xpub
	}
	if value := nodeInfoStringValue(info["identity_pubkey_hex"]); value != "" {
		identity.IdentityPublicKey = value
	}
	if value := nodeInfoStringValue(info["signing_pubkey_hex"]); value != "" {
		identity.SigningPublicKey = value
	}
	if value := nodeInfoStringValue(info["encryption_pubkey_hex"]); value != "" {
		identity.EncryptionPublicKey = value
	}

	identity.IPNSEntries = uniqueTrimmedStrings(append(identity.IPNSEntries, providerDescriptorIPNSEntries(info)...))
	identity.ENSNames = uniqueTrimmedStrings(providerDescriptorENSNames(info))
	identity.Addresses = providerDescriptorIdentityAddresses(info)

	return identity
}

func providerDescriptorIPNSEntries(info map[string]interface{}) []string {
	entries := make([]string, 0)
	for _, value := range nodeInfoStringEntries(info["multiformat_address"]) {
		if strings.HasPrefix(strings.TrimSpace(value), "/ipns/") {
			entries = append(entries, value)
		}
	}
	return entries
}

func providerDescriptorENSNames(info map[string]interface{}) []string {
	candidates := []string{
		nodeInfoStringValue(info["dn"]),
		nodeInfoStringValue(info["legal_name"]),
	}
	candidates = append(candidates, nodeInfoStringEntries(info["alternate_names"])...)
	candidates = append(candidates, nodeInfoStringEntries(info["multiformat_address"])...)

	ensNames := make([]string, 0)
	for _, candidate := range candidates {
		if ensName := normalizeENSName(candidate); ensName != "" {
			ensNames = append(ensNames, ensName)
		}
	}
	return ensNames
}

func providerDescriptorIdentityAddresses(info map[string]interface{}) []providerDescriptorIdentityAddress {
	proofsByChain := make(map[string]providerDescriptorIdentityAddress)
	for _, proof := range nodeInfoObjectEntries(info["chain_proofs"]) {
		chain := strings.ToLower(strings.TrimSpace(nodeInfoStringValue(proof["chain"])))
		if chain == "" {
			continue
		}
		entry := proofsByChain[chain]
		entry.Chain = chain
		if entry.Address == "" {
			entry.Address = nodeInfoStringValue(proof["address"])
		}
		if entry.KeyPath == "" {
			entry.KeyPath = nodeInfoStringValue(proof["key_path"])
		}
		if entry.PublicKey == "" {
			entry.PublicKey = nodeInfoStringValue(proof["public_key"])
		}
		proofsByChain[chain] = entry
	}

	chainOrder := []string{"bitcoin", "ethereum", "solana"}
	addresses := make([]providerDescriptorIdentityAddress, 0, len(chainOrder))
	for _, chain := range chainOrder {
		entry := proofsByChain[chain]
		entry.Chain = chain
		if entry.Address == "" {
			entry.Address = nodeInfoStringValue(info[chain+"_address"])
		}
		if entry.KeyPath == "" {
			entry.KeyPath = nodeInfoStringValue(info[chain+"_key_path"])
		}
		if strings.TrimSpace(entry.Address) == "" {
			continue
		}
		addresses = append(addresses, entry)
	}
	return addresses
}

func nodeInfoObjectEntries(raw interface{}) []map[string]interface{} {
	switch entries := raw.(type) {
	case []map[string]interface{}:
		return append([]map[string]interface{}(nil), entries...)
	case []interface{}:
		ret := make([]map[string]interface{}, 0, len(entries))
		for _, entry := range entries {
			if value, ok := entry.(map[string]interface{}); ok {
				ret = append(ret, value)
			}
		}
		return ret
	default:
		return nil
	}
}

func nodeInfoStringEntries(raw interface{}) []string {
	switch entries := raw.(type) {
	case []string:
		return append([]string(nil), entries...)
	case []interface{}:
		ret := make([]string, 0, len(entries))
		for _, entry := range entries {
			if value, ok := entry.(string); ok {
				ret = append(ret, value)
			}
		}
		return ret
	default:
		return nil
	}
}

func uniqueTrimmedStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	ret := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			continue
		}
		if _, ok := seen[trimmed]; ok {
			continue
		}
		seen[trimmed] = struct{}{}
		ret = append(ret, trimmed)
	}
	return ret
}

func normalizeENSName(value string) string {
	trimmed := strings.TrimSpace(strings.ToLower(value))
	if trimmed == "" {
		return ""
	}

	if parsed, err := url.Parse(trimmed); err == nil && parsed.Hostname() != "" {
		trimmed = strings.ToLower(strings.TrimSpace(parsed.Hostname()))
	}

	trimmed = strings.Trim(trimmed, "[](){}<>\"'.,;")
	if strings.HasSuffix(trimmed, ".eth") {
		return trimmed
	}
	return ""
}

func providerPublicKeyHex(host libp2phost.Host, peerID peer.ID) (string, error) {
	if host == nil {
		return "", fmt.Errorf("provider host is required")
	}
	if peerID == "" {
		return "", fmt.Errorf("provider peer id is required")
	}

	pubKey := host.Peerstore().PubKey(peerID)
	if pubKey == nil {
		var err error
		pubKey, err = peerID.ExtractPublicKey()
		if err != nil {
			return "", fmt.Errorf("extract provider public key: %w", err)
		}
	}
	raw, err := pubKey.Raw()
	if err != nil {
		return "", fmt.Errorf("marshal provider public key: %w", err)
	}
	compressed, err := normalizeCompressedSecp256k1PublicKey(raw)
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(compressed), nil
}

func normalizeCompressedSecp256k1PublicKey(raw []byte) ([]byte, error) {
	if len(raw) != 33 {
		return nil, fmt.Errorf("expected 33-byte compressed secp256k1 public key, got %d bytes", len(raw))
	}
	pubKey, err := secp256k1.ParsePubKey(raw)
	if err != nil {
		return nil, fmt.Errorf("invalid compressed secp256k1 public key: %w", err)
	}
	return pubKey.SerializeCompressed(), nil
}

// handleRelayStatus returns relay connection load for client-side load balancing.
func handleRelayStatus(n *node.Node) http.HandlerFunc {
	type relayStatusResponse struct {
		PeerID            string  `json:"peer_id"`
		Connections       int     `json:"connections"`
		ConfiguredNodes   int     `json:"configured_nodes"`
		MaxConnections    int     `json:"max_connections"`
		Load              float64 `json:"load"`
		Mode              string  `json:"mode"`
		Version           string  `json:"version"`
		AgentVersion      string  `json:"agent_version"`
		SuiteVersion      string  `json:"suite_version"`
		StandardsVersion  string  `json:"standards_version"`
		AdvertisementFlag string  `json:"advertisement_flag"`
		UptimeSeconds     int64   `json:"uptime_seconds"`
	}

	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
		w.Header().Set("Access-Control-Max-Age", "86400")

		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		peers := n.Host().Network().Peers()
		maxConns := n.Config().Network.MaxConns
		if maxConns <= 0 {
			maxConns = 1000
		}

		load := float64(len(peers)) / float64(maxConns)
		if load > 1.0 {
			load = 1.0
		}

		status := relayStatusResponse{
			PeerID:            n.PeerID().String(),
			Connections:       len(peers),
			ConfiguredNodes:   configuredSDNSSHNodeCount(),
			MaxConnections:    maxConns,
			Load:              load,
			Mode:              n.Config().Mode,
			Version:           versioninfo.AgentVersion,
			AgentVersion:      versioninfo.AgentVersion,
			SuiteVersion:      versioninfo.SuiteVersion,
			StandardsVersion:  versioninfo.SpaceDataStandardsVersion,
			AdvertisementFlag: versioninfo.CurrentAdvertisementFlag,
			UptimeSeconds:     int64(time.Since(processStartTime).Seconds()),
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(status)
	}
}

func configuredSDNSSHNodeCount() int {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return 0
	}
	return countConfiguredSDNSSHHostStanzas(filepath.Join(home, ".ssh", "config"))
}

func countConfiguredSDNSSHHostStanzas(configPath string) int {
	file, err := os.Open(configPath)
	if err != nil {
		return 0
	}
	defer file.Close()

	count := 0
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 2 || !strings.EqualFold(fields[0], "host") {
			continue
		}
		for _, alias := range fields[1:] {
			if isConfiguredSDNSSHAlias(alias) {
				count++
				break
			}
		}
	}
	return count
}

func isConfiguredSDNSSHAlias(alias string) bool {
	alias = strings.TrimSpace(alias)
	if alias == "" || strings.ContainsAny(alias, "*?") {
		return false
	}
	return strings.HasPrefix(alias, "space-data-network-") ||
		alias == "sdn.spaceaware.io"
}

// handleNodeEPMJSON returns the node's EPM as JSON.
func handleNodeEPMJSON(n *node.Node) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		epmSvc := n.EPMService()
		if epmSvc == nil {
			http.Error(w, "EPM service not available", http.StatusServiceUnavailable)
			return
		}

		epmJSON := epmSvc.GetNodeEPMJSON()
		if epmJSON == nil {
			http.Error(w, "no EPM available", http.StatusNotFound)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(epmJSON)
	}
}

// handleNodeEPMVCard returns the node's EPM as a vCard 4.0 string.
func handleNodeEPMVCard(n *node.Node) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		epmSvc := n.EPMService()
		if epmSvc == nil {
			http.Error(w, "EPM service not available", http.StatusServiceUnavailable)
			return
		}

		vcardStr, err := epmSvc.GetNodeVCard()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "text/vcard")
		w.Header().Set("Content-Disposition", "attachment; filename=node.vcf")
		w.Write([]byte(vcardStr))
	}
}

// handleNodeEPMQR returns a QR code PNG of the node's vCard.
func handleNodeEPMQR(n *node.Node) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		epmSvc := n.EPMService()
		if epmSvc == nil {
			http.Error(w, "EPM service not available", http.StatusServiceUnavailable)
			return
		}

		qrData, err := epmSvc.GetNodeQR(256)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "image/png")
		w.Write(qrData)
	}
}

// handleNodeEPM handles GET (binary EPM) and PUT (update profile) for the node's EPM.
func handleNodeEPM(n *node.Node) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		epmSvc := n.EPMService()
		if epmSvc == nil {
			http.Error(w, "EPM service not available", http.StatusServiceUnavailable)
			return
		}

		switch r.Method {
		case http.MethodGet:
			epmData := epmSvc.GetNodeEPM()
			if epmData == nil {
				http.Error(w, "no EPM available", http.StatusNotFound)
				return
			}
			w.Header().Set("Content-Type", "application/x-flatbuffers")
			w.Write(epmData)

		case http.MethodPut:
			var profile epm.Profile
			if err := json.NewDecoder(r.Body).Decode(&profile); err != nil {
				http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
				return
			}
			if err := epmSvc.UpdateProfile(&profile); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			if err := n.IndexLocalNodeEPM(); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			if err := epmSvc.PublishEPM(r.Context(), n); err != nil {
				log.Warnf("Failed to publish updated EPM PNM: %v", err)
			}
			epmData := epmSvc.GetNodeEPM()
			if epmData == nil {
				http.Error(w, "no EPM available", http.StatusNotFound)
				return
			}
			w.Header().Set("Content-Type", "application/x-flatbuffers")
			w.Write(epmData)

		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	}
}

// handlePeerGraph returns the current peer graph as JSON.
func handlePeerGraph(n *node.Node) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		data, err := epm.GraphSnapshotJSON(n.Host(), n.PeerRegistry())
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		w.Write(data)
	}
}

// observedSDNPeerCounts reports how many of this node's peers are actual Space
// Data Network nodes — connected now (headline) and known overall — from the
// same evidence as the observed-peer list served at /api/peers/sdn, so the two
// surfaces can never disagree. Callers pair it with the raw libp2p/DHT swarm
// count (host.Network().Peers()) to show "SDN peers" beside "IPFS peers".
//
// It is read on anonymous, frequently-polled surfaces (/api/v1/stats,
// /api/node/info), so it is bounded: the peer registry and advertisement maps
// are mutex-guarded and a stalled writer elsewhere in the node must never turn
// a status poll into a hung request. On timeout the counts read as zero rather
// than blocking the response.
func observedSDNPeerCounts(n *node.Node) epm.SDNPeerCounts {
	if n == nil {
		return epm.SDNPeerCounts{}
	}
	host := n.Host()
	if host == nil {
		return epm.SDNPeerCounts{}
	}

	done := make(chan epm.SDNPeerCounts, 1)
	go func() {
		registry := n.PeerRegistry()
		var registryPeers []*peers.TrustedPeer
		if registry != nil {
			registryPeers = registry.ListPeers()
		}
		done <- epm.CountSDNPeers(
			epm.BuildGraphSnapshot(host, registry),
			registryPeers,
			n.SDNAdvertisementFlagsByPeer(),
			n.SDNAdvertisementAddrsByPeer(),
		)
	}()

	select {
	case counts := <-done:
		return counts
	case <-time.After(2 * time.Second):
		return epm.SDNPeerCounts{}
	}
}

// nodeInfoPeerCounts builds the /api/node/info peers block: the raw libp2p/DHT
// swarm count (ipfs) beside the SDN-node counts (sdn = connected, sdn_known =
// observed incl. advertisement-discovered peers not connected right now). Same
// shape as the peers block on /api/v1/stats.
func nodeInfoPeerCounts(n *node.Node) map[string]interface{} {
	ipfs := 0
	if n != nil {
		if host := n.Host(); host != nil {
			ipfs = len(host.Network().Peers())
		}
	}
	counts := observedSDNPeerCounts(n)
	return map[string]interface{}{
		"ipfs":      ipfs,
		"sdn":       counts.Connected,
		"sdn_known": counts.Known,
	}
}

// handleObservedSDNPeers returns the SDN-only peer list consumed by the root dashboard.
func handleObservedSDNPeers(n *node.Node) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		snapshot := epm.BuildGraphSnapshot(n.Host(), n.PeerRegistry())
		var registryPeers []*peers.TrustedPeer
		if registry := n.PeerRegistry(); registry != nil {
			registryPeers = registry.ListPeers()
		}
		data, err := json.Marshal(epm.BuildObservedSDNPeers(
			snapshot,
			registryPeers,
			n.SDNAdvertisementFlagsByPeer(),
			n.SDNAdvertisementAddrsByPeer(),
		))
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		w.Write(data)
	}
}

// handlePeerGraphSchema serves the PGR.fbs schema file.
func handlePeerGraphSchema(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	w.Header().Set("Content-Type", "text/plain")
	w.Write([]byte(epm.PGRSchema))
}

var defaultFaviconPNG = []byte{
	0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, 0x00, 0x00, 0x00, 0x0d,
	0x49, 0x48, 0x44, 0x52, 0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01,
	0x08, 0x04, 0x00, 0x00, 0x00, 0xb5, 0x1c, 0x0c, 0x02, 0x00, 0x00, 0x00,
	0x0b, 0x49, 0x44, 0x41, 0x54, 0x78, 0xda, 0x63, 0xfc, 0xff, 0x1f, 0x00,
	0x03, 0x03, 0x02, 0x00, 0xef, 0xbc, 0x7f, 0x44, 0x00, 0x00, 0x00, 0x00,
	0x49, 0x45, 0x4e, 0x44, 0xAE, 0x42, 0x60, 0x82,
}

func runInit(cmd *cobra.Command, args []string) error {
	cfg, err := config.Load(configPath)
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	if err := config.Save(configPath, cfg); err != nil {
		return fmt.Errorf("failed to save config: %w", err)
	}

	wp, err := resolveHDWalletWasmPath()
	if err != nil {
		return err
	}
	result, err := ensureNodeMnemonic(cmd.Context(), cfg, newHDWalletMnemonicGenerator(wp))
	if err != nil {
		return err
	}

	configFile := configPath
	if strings.TrimSpace(configFile) == "" {
		configFile = config.DefaultPath()
	}
	log.Infof("Initialized SDN configuration at %s", configFile)
	if result.Created {
		log.Infof("Initialized encrypted SDN node mnemonic at %s", result.Path)
	} else {
		log.Infof("SDN node mnemonic already exists at %s", result.Path)
	}
	return nil
}

type nodeMnemonicInitResult struct {
	Path    string
	Created bool
}

func ensureNodeMnemonic(ctx context.Context, cfg *config.Config, generateMnemonic func(context.Context) (string, error)) (nodeMnemonicInitResult, error) {
	if cfg == nil {
		return nodeMnemonicInitResult{}, fmt.Errorf("config is required")
	}
	if generateMnemonic == nil {
		return nodeMnemonicInitResult{}, fmt.Errorf("mnemonic generator is required")
	}

	keyDir := filepath.Join(filepath.Dir(cfg.Storage.Path), "keys")
	mnemonicPath := filepath.Join(keyDir, "mnemonic")
	if data, err := os.ReadFile(mnemonicPath); err == nil {
		if strings.TrimSpace(string(data)) == "" {
			return nodeMnemonicInitResult{}, fmt.Errorf("mnemonic file %s is empty", mnemonicPath)
		}
		return nodeMnemonicInitResult{Path: mnemonicPath, Created: false}, nil
	} else if !os.IsNotExist(err) {
		return nodeMnemonicInitResult{}, fmt.Errorf("read mnemonic file %s: %w", mnemonicPath, err)
	}

	mnemonic, err := generateMnemonic(ctx)
	if err != nil {
		return nodeMnemonicInitResult{}, fmt.Errorf("generate node mnemonic: %w", err)
	}
	if strings.TrimSpace(mnemonic) == "" {
		return nodeMnemonicInitResult{}, fmt.Errorf("generated mnemonic is empty")
	}

	keyPassword := os.Getenv("SDN_KEY_PASSWORD")
	if keyPassword == "" {
		keyPassword = cfg.Security.KeyPassword
	}
	if keyPassword == "" {
		keyPassword = keys.DeriveDefaultPassword()
	}
	encrypted, err := keys.EncryptMnemonic(strings.TrimSpace(mnemonic), keyPassword)
	if err != nil {
		return nodeMnemonicInitResult{}, fmt.Errorf("encrypt node mnemonic: %w", err)
	}
	if err := os.MkdirAll(keyDir, 0o700); err != nil {
		return nodeMnemonicInitResult{}, fmt.Errorf("create key directory %s: %w", keyDir, err)
	}
	if err := os.WriteFile(mnemonicPath, encrypted, 0o600); err != nil {
		return nodeMnemonicInitResult{}, fmt.Errorf("write mnemonic file %s: %w", mnemonicPath, err)
	}
	return nodeMnemonicInitResult{Path: mnemonicPath, Created: true}, nil
}

func newHDWalletMnemonicGenerator(wp string) func(context.Context) (string, error) {
	return func(ctx context.Context) (string, error) {
		hw, err := wasm.NewHDWalletModule(ctx, wp)
		if err != nil {
			return "", fmt.Errorf("failed to load HD wallet WASM: %w", err)
		}
		defer hw.Close(ctx)

		entropy := make([]byte, 64)
		if _, err := rand.Read(entropy); err != nil {
			return "", fmt.Errorf("read entropy: %w", err)
		}
		if err := hw.InjectEntropy(ctx, entropy); err != nil {
			return "", fmt.Errorf("inject entropy: %w", err)
		}

		mnemonic, _, err := hw.GenerateNewIdentity(ctx, 24)
		if err != nil {
			return "", err
		}
		return mnemonic, nil
	}
}

func runReindex(cmd *cobra.Command, args []string) error {
	cfg, err := config.Load(configPath)
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	validator, err := sds.NewValidator(nil)
	if err != nil {
		return fmt.Errorf("failed to initialize schema validator: %w", err)
	}

	store, err := storage.NewFlatSQLStore(cfg.Storage.Path, validator)
	if err != nil {
		if errors.Is(err, storage.ErrStoreLocked) {
			return fmt.Errorf("reindex rewrites the record index and needs EXCLUSIVE store access — stop the daemon first (read-flavored verbs like `search`/`sync status` work against a running daemon): %w", err)
		}
		return fmt.Errorf("failed to open storage: %w", err)
	}
	defer store.Close()

	summary, err := store.RebuildIndex()
	if err != nil {
		return fmt.Errorf("reindex failed: %w", err)
	}

	var total int64
	for schema, count := range summary {
		total += count
		log.Infof("Indexed %d records for %s", count, schema)
	}
	log.Infof("Reindex complete: %d total records indexed", total)

	return nil
}

func runDeriveXPub(cmd *cobra.Command, args []string) error {
	wp, err := resolveHDWalletWasmPath()
	if err != nil {
		return err
	}

	ctx := context.Background()
	hw, err := wasm.NewHDWalletModule(ctx, wp)
	if err != nil {
		return fmt.Errorf("failed to load HD wallet WASM: %w", err)
	}
	defer hw.Close(ctx)

	// Read mnemonic from stdin
	fmt.Fprint(os.Stderr, "Enter your BIP-39 mnemonic phrase: ")
	reader := bufio.NewReader(os.Stdin)
	mnemonic, err := reader.ReadString('\n')
	if err != nil {
		return fmt.Errorf("failed to read mnemonic: %w", err)
	}
	mnemonic = strings.TrimSpace(mnemonic)
	if mnemonic == "" {
		return fmt.Errorf("mnemonic cannot be empty")
	}

	valid, err := hw.ValidateMnemonic(ctx, mnemonic)
	if err != nil {
		return fmt.Errorf("failed to validate mnemonic: %w", err)
	}
	if !valid {
		return fmt.Errorf("invalid mnemonic phrase")
	}

	// Derive seed
	seed, err := hw.MnemonicToSeed(ctx, mnemonic, "")
	if err != nil {
		return fmt.Errorf("failed to derive seed: %w", err)
	}

	// Derive standard BIP-32 xpub at m/44'/0'/0' (account 0)
	xpubStr, err := hw.DeriveXPub(ctx, seed, 0)
	if err != nil {
		return fmt.Errorf("failed to derive xpub: %w", err)
	}

	fmt.Fprintf(os.Stderr, "\n--- SDN Identity ---\n")
	fmt.Fprintf(os.Stderr, "XPub (BIP-32):     %s\n", xpubStr)
	fmt.Fprintf(os.Stderr, "\nAdd to config.yaml:\n")
	fmt.Fprintf(os.Stderr, "users:\n  - xpub: \"%s\"\n    trust_level: \"admin\"\n    name: \"Operator\"\n", xpubStr)

	// Print just the xpub to stdout (for scripting)
	fmt.Println(xpubStr)

	return nil
}

func runShowIdentity(cmd *cobra.Command, args []string) error {
	// Load config for storage path and key password
	cfg, err := config.Load(configPath)
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	// Resolve key password: env > config > machine default
	keyPassword := os.Getenv("SDN_KEY_PASSWORD")
	if keyPassword == "" {
		keyPassword = cfg.Security.KeyPassword
	}
	if keyPassword == "" {
		keyPassword = keys.DeriveDefaultPassword()
	}

	// Locate mnemonic file
	keyDir := filepath.Join(filepath.Dir(cfg.Storage.Path), "keys")
	mnemonicPath := filepath.Join(keyDir, "mnemonic")

	data, err := os.ReadFile(mnemonicPath)
	if err != nil {
		return fmt.Errorf("failed to read mnemonic file %s: %w", mnemonicPath, err)
	}

	// Decrypt if encrypted, otherwise use as-is
	var mnemonic string
	if keys.IsMnemonicEncrypted(data) {
		mnemonic, err = keys.DecryptMnemonic(data, keyPassword)
		if err != nil {
			return fmt.Errorf("failed to decrypt mnemonic (wrong password?): %w", err)
		}
	} else {
		mnemonic = string(data)
	}

	// Resolve WASM path
	wp, err := resolveHDWalletWasmPath()
	if err != nil {
		return err
	}

	ctx := context.Background()
	hw, err := wasm.NewHDWalletModule(ctx, wp)
	if err != nil {
		return fmt.Errorf("failed to load HD wallet WASM: %w", err)
	}
	defer hw.Close(ctx)

	// Derive seed from mnemonic
	seed, err := hw.MnemonicToSeed(ctx, mnemonic, "")
	if err != nil {
		return fmt.Errorf("failed to derive seed: %w", err)
	}

	// Derive identity (account 0)
	identity, err := hw.DeriveIdentity(ctx, seed, 0)
	if err != nil {
		return fmt.Errorf("failed to derive identity: %w", err)
	}

	// Derive xpub
	xpubStr, err := hw.DeriveXPub(ctx, seed, 0)
	if err != nil {
		return fmt.Errorf("failed to derive xpub: %w", err)
	}

	info := identity.Info()

	fmt.Fprintf(os.Stderr, "\n--- SDN Node Identity ---\n")
	fmt.Fprintf(os.Stderr, "PeerID:         %s\n", info.PeerID)
	fmt.Fprintf(os.Stderr, "XPub:           %s\n", xpubStr)
	fmt.Fprintf(os.Stderr, "Signing Key:    %s  (path: %s)\n", info.SigningPubKeyHex, info.SigningKeyPath)
	fmt.Fprintf(os.Stderr, "Encryption Key: %s  (path: %s)\n", info.EncryptionPubHex, info.EncryptionKeyPath)
	fmt.Fprintf(os.Stderr, "Identity Path:  %s\n", info.IdentityKeyPath)
	fmt.Fprintf(os.Stderr, "Mnemonic File:  %s\n", mnemonicPath)

	if showMnemonic {
		fmt.Fprintf(os.Stderr, "\n*** MNEMONIC (SENSITIVE — DO NOT SHARE) ***\n")
		fmt.Fprintf(os.Stderr, "%s\n", mnemonic)
	}

	// Print PeerID to stdout (for scripting)
	fmt.Println(info.PeerID)

	return nil
}
