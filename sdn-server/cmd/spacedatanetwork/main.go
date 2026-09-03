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
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	logging "github.com/ipfs/go-log/v2"
	"github.com/libp2p/go-libp2p/core/event"
	libp2phost "github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/multiformats/go-multiaddr"
	qrgen "github.com/skip2/go-qrcode"
	"github.com/spf13/cobra"

	"github.com/spacedatanetwork/sdn-server/internal/adminui"
	"github.com/spacedatanetwork/sdn-server/internal/api"
	"github.com/spacedatanetwork/sdn-server/internal/assetpin"
	"github.com/spacedatanetwork/sdn-server/internal/auth"
	"github.com/spacedatanetwork/sdn-server/internal/bootstrap"
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
	"github.com/spacedatanetwork/sdn-server/internal/modulert/caps"
	"github.com/spacedatanetwork/sdn-server/internal/node"
	"github.com/spacedatanetwork/sdn-server/internal/peers"
	"github.com/spacedatanetwork/sdn-server/internal/sds"
	"github.com/spacedatanetwork/sdn-server/internal/sourcemetrics"
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

	// wsProxyFailures counts /p2p/ websocket-upgrade proxy failures, i.e. every
	// public 502 this daemon has served on that route. It is reported in the
	// ERROR line the proxy's ErrorHandler now emits, so the journal alone tells
	// an operator whether a 502 was a one-off or a sustained outage — the
	// distinction that previously required dumping production's goroutines.
	wsProxyFailures atomic.Uint64
)

var errLocalSDNDaemonUnavailable = errors.New("local SDN daemon unavailable")

var rootCmd = &cobra.Command{
	Use:   "spacedatanetwork",
	Short: "Space Data Network - FlatBuffer-native P2P for space data",
	Long: `spacedatanetwork is a specialized fork of IPFS tailored for the Space Data Network.
It replaces generic content-addressed storage with FlatBuffer-native data handling
and SQLite-based structured storage, optimized for space data standards.`,
	PersistentPreRun: func(cmd *cobra.Command, _ []string) { configureCLILogging(cmd) },
}

// longRunningCommands are the ones an operator EXPECTS to narrate themselves:
// they run until stopped, and their progress log IS the output.
var longRunningCommands = map[string]bool{
	"daemon": true, "start": true, "restart": true, "service": true,
	"ingest": true, "sync": true, "monitor": true, "stream": true, "watch": true,
}

// configureCLILogging picks the library log level AFTER cobra has parsed flags.
//
// Two bugs are fixed here. First, this used to run in main() before parsing, so
// `debug` was always false and --debug did nothing. Second, everything ran at
// Info, which spilled library noise like
//
//	2026-07-28T06:32:51Z WARN storage storage/flatsql.go:275 FlatSQL engine ...
//
// into the output of one-shot commands. That is jargon leaking into an
// operator-facing surface: `key export --format xpub` should print an xpub and
// nothing else, so it can be piped.
//
// A long-running command still narrates at Info — its log IS its output. A
// one-shot command is quiet unless something actually failed, and --debug
// always wins.
func configureCLILogging(cmd *cobra.Command) {
	if debug {
		logging.SetAllLoggers(logging.LevelDebug)
		return
	}
	name := ""
	if cmd != nil {
		name = cmd.Name()
		for c := cmd; c != nil; c = c.Parent() {
			if longRunningCommands[c.Name()] {
				name = c.Name()
				break
			}
		}
	}
	if longRunningCommands[name] {
		logging.SetAllLoggers(logging.LevelInfo)
		return
	}
	logging.SetAllLoggers(logging.LevelError)
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
		// This command's ENTIRE JOB is to say which config is in use. Printing
		// the home default while the daemon runs from /etc was the most
		// misleading output in the whole CLI — it answered the operator's
		// question wrongly and confidently.
		res, err := config.ResolvePath(configPath)
		if err != nil {
			return err
		}
		fmt.Fprintln(cmd.OutOrStdout(), res.Path)
		fmt.Fprintf(cmd.ErrOrStderr(), "resolved from: %s\n", res.Source)
		if !res.Exists {
			fmt.Fprintf(cmd.ErrOrStderr(),
				"warning: this file does not exist; defaults are in use. %s\n",
				config.OverrideHint)
		}
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
		cfg, _, err := config.LoadResolved(configPath)
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
Password is resolved from SDN_KEY_PASSWORD, SDN_KEY_PASSWORD_FILE, config, or machine default.`,
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
	daemonCmd.Flags().BoolVar(&allowMultiDaemonFlag, "allow-multi-daemon", false,
		"DEVELOPMENT ONLY: start even if another SDN daemon is running on this box (owner law: one instance per box)")
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
	// Log level is set in rootCmd's PersistentPreRun, NOT here: at this point
	// cobra has not parsed flags yet, so `debug` is always false and --debug
	// never took effect. See configureCLILogging.
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func runStatus(cmd *cobra.Command) error {
	cfg, _, err := config.LoadResolved(configPath)
	if err != nil {
		return err
	}
	out := cmd.OutOrStdout()
	baseURL := adminURL(cfg)
	fmt.Fprintf(out, "admin_url=%s\n", baseURL)
	return writeDaemonStatus(cmd.Context(), out, baseURL)
}

// daemonHealthPayload tolerates both health shapes the daemon has shipped:
// the data-api liveness handler emits {"status":"ok"} and has never emitted
// the "healthy" boolean this CLI historically decoded — which made every node
// report unhealthy forever. An explicit "healthy" field, when present, wins.
type daemonHealthPayload struct {
	Healthy *bool          `json:"healthy"`
	Status  string         `json:"status"`
	Details map[string]any `json:"details"`
}

func (h daemonHealthPayload) ok() bool {
	if h.Healthy != nil {
		return *h.Healthy
	}
	return strings.EqualFold(h.Status, "ok")
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

	var health daemonHealthPayload
	if err := json.NewDecoder(resp.Body).Decode(&health); err != nil {
		fmt.Fprintln(out, "daemon_status=unhealthy")
		fmt.Fprintln(out, "data_health=unhealthy")
		fmt.Fprintf(out, "data_message=%s\n", strings.ReplaceAll(err.Error(), "\n", " "))
		return nil
	}

	if health.ok() {
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
	cfg, _, err := config.LoadResolved(configPath)
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
	// The local EPM identity is a RECORD (QueryRawRecords over EPM.fbs), so the
	// record catalog must be hydrated.
	store, err := openStoreForReading(cfg.Storage.Path, validator, storeReadNeeds{recordCatalog: true})
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
			// The config holds a BIND address. A wildcard bind (0.0.0.0, ::)
			// is not a destination and no cert can be valid for it, so map it
			// to loopback before it becomes a URL. See DialAddrForListenAddr.
			addr = DialAddrForListenAddr(cfg.Admin.ListenAddr)
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
	// Say WHERE we looked. "not found" without a search list makes the
	// operator guess, and on a service host the answer is rarely obvious —
	// the same honesty rule as the config resolver's errors (§20).
	searched := make([]string, 0, len(candidates)+1)
	if layout.HDWalletWASM != "" {
		searched = append(searched, layout.HDWalletWASM+"  (bundle layout)")
	}
	searched = append(searched, candidates...)
	return "", fmt.Errorf(
		"hd-wallet-wasi.wasm not found.\nSearched:\n  %s\nSet --wasm <path> or HD_WALLET_WASM_PATH to point at it",
		strings.Join(searched, "\n  "))
}

// executableRelativeHDWalletCandidates looks for the HD-wallet wasm NEXT TO THE
// BINARY. The CLI signs in through the §19 root ceremony, which derives the
// node's root key through this module — so on a service host the module is not
// optional tooling, it is part of the daemon's own install. Before this, a
// deploy directory holding only the binary (e.g. /opt/sdn-retriever) fell
// through to hard-coded absolute paths belonging to OTHER installs, and the CLI
// worked only by accident of a retired node's leftovers still being on disk.
func executableRelativeHDWalletCandidates() []string {
	exe, err := os.Executable()
	if err != nil {
		return nil
	}
	if resolved, rerr := filepath.EvalSymlinks(exe); rerr == nil {
		exe = resolved
	}
	dir := filepath.Dir(exe)
	return []string{
		filepath.Join(dir, "wasm", "hd-wallet-wasi.wasm"),
		filepath.Join(dir, "hd-wallet-wasi.wasm"),
		filepath.Join(filepath.Dir(dir), "wasm", "hd-wallet-wasi.wasm"),
	}
}

// userLocalHDWalletCandidates covers the only layout an UNPRIVILEGED operator
// can install: ~/.local. On a host with no passwordless sudo there is nowhere
// else to put the artifact, and without these entries every identity-dependent
// command failed unless HD_WALLET_WASM_PATH was exported by a wrapper script —
// which made the bare binary unusable on its own (sdn-cli-user-local-wasm-search-path).
func userLocalHDWalletCandidates() []string {
	home, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(home) == "" {
		return nil
	}
	return []string{
		filepath.Join(home, ".local", "lib", "spacedatanetwork", "hd-wallet-wasi.wasm"),
		filepath.Join(home, ".local", "lib", "hd-wallet-wasi.wasm"),
	}
}

func defaultHDWalletWasmCandidates() []string {
	candidates := executableRelativeHDWalletCandidates()
	candidates = append(candidates, userLocalHDWalletCandidates()...)
	return append(candidates,
		"sdn-js/node_modules/hd-wallet-wasm/dist/hd-wallet-wasi.wasm",
		"node_modules/hd-wallet-wasm/dist/hd-wallet-wasi.wasm",
		"../../sdn-js/node_modules/hd-wallet-wasm/dist/hd-wallet-wasi.wasm",
		"../../../sdn-js/node_modules/hd-wallet-wasm/dist/hd-wallet-wasi.wasm",
		"../../hd-wallet-wasm/build-wasi/wasm/hd-wallet-wasi.wasm",
		"../hd-wallet-wasm/build-wasi/wasm/hd-wallet-wasi.wasm",
		"/opt/spacedatanetwork/wasm/hd-wallet-wasi.wasm",
		"/usr/local/lib/hd-wallet-wasi.wasm",
	)
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
	// One node per box (owner law 2026-07-28). Checked BEFORE any config load,
	// port bind or store open, so a second daemon fails immediately and without
	// touching the running node's files.
	if err := enforceSingleDaemonPerBox(); err != nil {
		return err
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	updateShutdown := make(chan struct{}, 1)

	// Load configuration
	cfg, _, err := config.LoadResolved(configPath)
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

			// Read-surface caches that survive a restart. host-01 spends
			// 60-100 minutes hydrating under the store lock after every daemon
			// restart; with RAM-only caches the anonymous surfaces answered
			// STORE_BUSY / SNAPSHOT_COLD for that whole window, i.e. the node
			// forgot what it had been serving a minute earlier. The caches
			// write their last-known-good answers into this directory and load
			// them at boot, serving them marked stale with an as_of. A failure
			// to create the directory is NOT fatal: caching is an
			// optimization, and "" degrades to exactly the old RAM-only
			// behavior.
			uiCacheDir, uiCacheErr := config.UICacheDir(cfg.Storage.Path)
			if uiCacheErr != nil {
				log.Warnf("UI read caches will not survive restart: %v", uiCacheErr)
				uiCacheDir = ""
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
					// NEVER discard this error. Every public /p2p/<peerid> 502 is
					// emitted here, and until 2026-07-30 the cause was dropped on
					// the floor — so a total peering outage presented as a bare
					// 502 with no diagnosis anywhere in the journal, and finding
					// it required a SIGQUIT goroutine dump of production. The
					// upstream is our OWN loopback libp2p listener, so a failure
					// is always a real node-side defect worth an ERROR line.
					wsProxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
						wsProxyFailures.Add(1)
						log.Errorf(
							"libp2p websocket upgrade proxy FAILED (total=%d): %s %s -> %s: %v",
							wsProxyFailures.Load(), r.Method, r.URL.Path, wsTarget.String(), err,
						)
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
			dataAPI := api.NewDataQueryHandlerWithUICache(n.Store(), uiCacheDir)
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

				// Auto-publish trigger (sdn-rfb-publish-to-consumer-node): the
				// configured ingest lanes republish themselves as dataset
				// publications when a batch lands, instead of waiting for a
				// module that happens to carry a publish node or an operator
				// with curl. Fail-closed — no configured lane, no publisher.
				if autoPublisher := api.NewAutoPublisher(publicationService, cfg.Publishing.AutoPublish); autoPublisher != nil {
					autoPublisher.Start(ctx)
					removeObserver := caps.AddIngestObserver(func(obs caps.IngestObservation) {
						autoPublisher.ObserveIngest(api.IngestedBatch{
							Schema:     obs.Schema,
							ProviderID: obs.ProviderID,
							SourceName: obs.SourceName,
							BatchID:    obs.BatchID,
							Inserted:   obs.Inserted,
						})
					})
					defer func() {
						removeObserver()
						autoPublisher.Stop()
					}()
					for _, lane := range autoPublisher.Lanes() {
						log.Infof("Auto-publish lane armed: schema %s provider %q source %q",
							lane.Schema, lane.ProviderID, lane.SourceName)
					}
				}
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
					// $STF.PRIMARY_CATEGORY comes from the node's module
					// registry join, not from a storefront-local table: one
					// catalog is the authority for the shelf on $PLG, $PMM and
					// $STF alike. Injected here because internal/storefront
					// must not grow its own catalog reader.
					sfStore.SetModuleCategoryResolver(n.ModuleCapabilityClass)
					sfSigningKey, err := storefrontSigningKeyFromRaw(n.SigningKey())
					if err != nil {
						log.Warnf("Storefront listings will be unsigned; node signing key unavailable: %v", err)
					}
					// Grants sign with the DERIVED GRANT CHILD, never the publisher
					// root (owner ruling 2026-08-07,
					// graph/tasks/sdn-grant-verifier-key-domain-separation.md). Nil
					// here means grants go out unsigned and visibly so — it never
					// falls back to sfSigningKey.
					sfGrantKey, err := storefrontSigningKeyFromRaw(n.GrantSigningKey())
					if err != nil {
						log.Warnf("Storefront grants will be unsigned; node grant signing key unavailable: %v", err)
					}
					sfSvc, err := storefront.NewService(sfStore, n.PeerID().String(), sfSigningKey, sfGrantKey, nil)
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

			// The node's own runtime facts (uptime, store, disk, service
			// state/mode, libp2p bandwidth + sparkline) for the dashboard's
			// NODE HEALTH / SERVICE / NETWORK THROUGHPUT widgets. Admin-only
			// via isAdminOnlyAPIPath's "/api/node/runtime" prefix; read-only by
			// construction (node_runtime_api.go). NOT a control surface — the
			// supervisor read lives on its own path below.
			adminMux.HandleFunc("/api/node/runtime", handleNodeRuntime(n))

			// The node's own activity ring (peer connects, publications,
			// record stores, grant issuance) for the dashboard's ACTIVITY LOG
			// widget. Admin-only via the "/api/node/activity" prefix; calls the
			// node_activity_read capability's OWN assembler, so the page cannot
			// render a shape no module sees (node_activity_api.go).
			adminMux.HandleFunc("/api/node/activity", handleNodeActivity(n))

			// What this node's process supervisor says about it — unit,
			// active/sub state, AUTOSTART and restart policy, all READ from
			// systemd (internal/hostsvc). The read half of the lifecycle
			// capability the owner approved 2026-07-30; Admin-only via the
			// "/api/node/service" prefix, GET/HEAD only, and it is what makes
			// the SERVICE panel's AUTOSTART cell a measurement instead of the
			// hardcoded literal it used to be (node_service_api.go).
			adminMux.HandleFunc("/api/node/service", handleNodeService())

			// The DESTRUCTIVE half (node_service_control.go): the owner
			// authorized RESTART/STOP on 2026-07-30 and the Seal Council set the
			// conditions. Admin-classified by the same prefix as the read above;
			// each verb additionally requires the unit-level opt-in, a PROVEN
			// supervisor, a single-use nonce bound to (verb, unit, identity) and
			// a FRESH wallet signature over it — Hephaestus dissented from
			// accepting the session cookie alone as authority to darken a live
			// host. Default-off: with SDN_SERVICE_CONTROL unset, every one of
			// these answers a logged refusal and the dashboard renders no
			// buttons at all.
			serviceControl := newServiceControlState()
			adminMux.HandleFunc("/api/node/service/nonce", handleServiceNonce(serviceControl, authHandler))
			adminMux.HandleFunc("/api/node/service/restart", handleServiceAction(serviceControl, authHandler))
			adminMux.HandleFunc("/api/node/service/stop", handleServiceAction(serviceControl, authHandler))

			// Security-bond attestation (owner 2026-08-03): the embedded
			// bond-attestation WASM module queries free chain services over
			// the generic http cap; this host schedules it and serves the
			// cached answer anonymously (bond_attestation.go).
			bondAtt := &bondAttestor{}
			bondAtt.start(ctx, n)
			adminMux.HandleFunc("/api/v1/trust/bond", bondAtt.handleBond)
			// Trust rules engine (`$TRP` policies → signed `$TRV` verdicts) and
			// the trust graph API over it (trust_engine.go).
			if err := startTrustRulesEngine(ctx, n, adminMux, cfg.Storage.Path, cfg.Admin.RequireAuth, func() *auth.Handler { return authHandler }, bondAtt); err != nil {
				log.Warnf("Trust rules engine not started: %v", err)
			}

			// EPM (Entity Profile Message) API endpoints
			adminMux.HandleFunc("/api/node/epm/json", handleNodeEPMJSON(n))
			adminMux.HandleFunc("/api/node/epm/vcard", handleNodeEPMVCard(n))
			adminMux.HandleFunc("/api/node/epm/qr", handleNodeEPMQR(n))
			// §18 key slots: read the derivation paths in effect and compute
			// what GEN KEY would rotate them to. Admin-only (it describes the
			// node's identity layout); derives server-side and returns PATHS
			// ONLY — the seed never leaves the process and no key material is
			// returned, because the public key is reconstructible by anyone
			// from xpub + path.
			adminMux.HandleFunc("/api/node/epm/keys", gateNodeEPMWrite(
				handleNodeEPMKeySlots(n),
				cfg.Admin.RequireAuth,
				func() *auth.Handler { return authHandler },
			))
			// The auth handler is constructed further below (it needs the
			// storage path), so the gate resolves it per request rather than
			// capturing a nil pointer at mount time.
			// The node identity wire is the raw $EPM FlatBuffer (owner
			// 2026-09-03: "everything should be flatbuffers"); JSON stays a
			// derived read projection on /api/node/epm/json.
			adminMux.HandleFunc("/api/node/epm", gateNodeEPMWrite(
				api.NewNodeEPMHandler(nodeEPMWireService(n), func(ctx context.Context, _ []byte) error {
					if err := n.IndexLocalNodeEPM(); err != nil {
						return err
					}
					if svc := n.EPMService(); svc != nil {
						if err := svc.PublishEPM(ctx, n); err != nil {
							log.Warnf("Failed to publish updated EPM PNM: %v", err)
						}
					}
					return nil
				}).ServeHTTP,
				cfg.Admin.RequireAuth,
				func() *auth.Handler { return authHandler },
			))

			// Peer graph API endpoints
			// Unified ACCOUNTS surface (owner directive 2026-07-27: a node and
			// a login account are the same thing). Read-only; management keeps
			// using /api/peers and /api/auth/users.
			adminMux.HandleFunc("/api/accounts", handleAccounts(n, func() *auth.Handler { return authHandler }))
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

			// HD wallet authentication.
			//
			// THE ADMIT POINT IS UNCONDITIONAL (owner order 2026-07-28, "the CLI
			// must work against EVERY daemon"). This construction used to sit
			// inside `if cfg.Admin.RequireAuth`, so a daemon configured with
			// admin.require_auth:false — the retriever profile, loopback admin
			// listener — served /api/apps but 404'd /api/auth/challenge. The CLI
			// signs in through the §19 root ceremony before every Admin-gated
			// command, so on that shape `apps run` could not work at all: not
			// because the route was missing, but because the DOOR was missing.
			//
			// require_auth no longer decides WHETHER an admit point exists. It
			// decides only how wide the wall around the read surface is (see
			// serveAdminMuxRequest): operator-authority paths are Admin-gated
			// either way, because §14/§19 root recognition means a session is
			// always OBTAINABLE on a node holding its own seed — which is the
			// precondition that was missing when "no auth" had to mean "no gate".
			//
			// The user store owns the private auth database; the session
			// store shares that same handle via userStore.DB().
			// Owner law 2026-07-27: the auth store is SQLite in its OWN
			// file, kept out of the standards store. resolveAuthDBPath
			// applies the default and refuses a path inside the record
			// store rather than silently co-locating them.
			//
			// legacyWalletUIPath is resolved here (not in the branch below) so
			// the wallet-static mount inside the RequireAuth branch still sees it.
			// The generic in-process wallet UI is a development-only legacy
			// surface. Shipped conjunction mode uses the isolated typed wallet
			// presenter and must not expose the legacy asset path or advertise it
			// through /api/auth/status.
			legacyWalletUIPath := legacyWalletUIPathForMode(frontendUIMode, cfg.Admin.WalletUIPath)
			if authErr := func() error {
				authDBPath, aerr := resolveAuthDBPath(cfg)
				if aerr != nil {
					return aerr
				}
				userStore, uerr := auth.NewUserStore(authDBPath, cfg.Users)
				if uerr != nil {
					return fmt.Errorf("create user store: %w", uerr)
				}

				sessionStore, serr := auth.NewSessionStore(userStore.DB())
				if serr != nil {
					_ = userStore.Close()
					return fmt.Errorf("create session store: %w", serr)
				}

				sessionTTL, _ := time.ParseDuration(cfg.Admin.SessionExpiry)
				if sessionTTL == 0 {
					sessionTTL = 24 * time.Hour
				}

				cfgDisplayPath := configPath
				if cfgDisplayPath == "" {
					cfgDisplayPath = config.DefaultPath()
				}
				authHandler = auth.NewHandler(userStore, sessionStore, sessionTTL, legacyWalletUIPath, cfgDisplayPath)
				authHandler.SetTLSManager(tlsManager)
				if cfg.Admin.DevAutoAdmin {
					// Loopback-bound listeners only: dev_auto_admin on a
					// reachable bind would BE an open admin surface, so the
					// daemon refuses to start instead of degrading.
					if !isLoopbackListenAddr(cfg.Admin.ListenAddr) {
						return fmt.Errorf("admin.dev_auto_admin requires a loopback listen_addr (got %q)", cfg.Admin.ListenAddr)
					}
					authHandler.EnableDevAutoAdmin()
					log.Warnf("admin.dev_auto_admin is ON: loopback requests are auto-admitted as the first Admin user (dev only)")
				}
				// Bind the operator profile-photo object store to the IPFS lane
				// this node already runs. When admin.ipfs_api_url is unset the
				// port stays nil and the endpoint refuses with 501 — a node
				// without object storage says so instead of losing pictures.
				if ipfsAPIURL := strings.TrimSpace(cfg.Admin.IPFSAPIURL); ipfsAPIURL != "" {
					authHandler.SetProfilePhotoStore(ipfsProfilePhotoStore{apiURL: ipfsAPIURL})
				}

				// ACCOUNT EPMs (owner directive 2026-08-28). Two connectors and
				// no application logic: the node's EPM service is the builder
				// (same builder as the node's own record, different subject),
				// and the record+pin lane is the FlatSQL store plus — when
				// admin.ipfs_api_url is set — the local blockstore. A node
				// without a store binds neither and /api/auth/epm refuses with
				// 501 rather than losing somebody's identity.
				if store := n.Store(); store != nil {
					var blockstore api.AccountEPMBlockstore
					if ipfsAPIURL := strings.TrimSpace(cfg.Admin.IPFSAPIURL); ipfsAPIURL != "" {
						blockstore = ipfsAccountEPMBlockstore{apiURL: ipfsAPIURL}
					}
					accountEPMStore := api.NewAccountEPMStore(store, n.PeerID().String(), blockstore)
					authHandler.SetAccountEPMServices(n.EPMService(), accountEPMStore)

					// THE FLEET LAW: "All SDN nodes need to be able to pin all
					// the EPMs that are created by accounts tied to them"
					// (owner 2026-08-28). The store is ready here, so the
					// reconciler's boot pass runs against a live lane; it then
					// repeats every six hours and stops when ctx is cancelled
					// on shutdown.
					auth.NewAccountEPMReconciler(userStore, accountEPMStore).Start(ctx)
				}
				return nil
			}(); authErr != nil {
				// An operator who asked for authentication gets a hard failure,
				// exactly as before. An operator who did not still loses the
				// admit point — so the wall below refuses every Admin-only path
				// with 503 rather than falling open.
				if cfg.Admin.RequireAuth {
					return fmt.Errorf("admin authentication required: %w", authErr)
				}
				log.Errorf("Admin admit point unavailable; Admin-only APIs will refuse (503): %v", authErr)
			}
			if authHandler != nil {
				// OWNER DIRECTIVE 2026-07-27: the node's own root account is
				// always accepted as the admin sign-in, with no error and no
				// config seeding. The handler recognises it by comparing the
				// presented key against keys derived HERE from the node's own
				// seed — nothing client-supplied participates. Both the
				// SLIP-10 (§2 / v2) and the legacy bip32-scalar key are
				// registered, because the wallet's identity scheme decides
				// which one signs.
				if rootXPub, rootKeys, rerr := n.RootAuthPublicKeys(); rerr != nil {
					log.Warnf("Node root admin sign-in unavailable: %v", rerr)
				} else {
					authHandler.SetNodeRootIdentity(&auth.RootIdentity{
						XPub:        rootXPub,
						Name:        "Node Root",
						SigningKeys: rootKeys,
					})
				}
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
				log.Infof("Admin admit point at %s://%s/api/auth/{challenge,verify} (require_auth: %v)",
					adminScheme, adminAddr, cfg.Admin.RequireAuth)
			}

			// Everything below is the AUTHENTICATED-PROFILE surface, still keyed
			// to require_auth. It is deliberately NOT part of the admit point:
			// the authenticated publish routes registered here collide on the
			// mux with the unauthenticated ones registered further down when
			// require_auth is false, and the rest are operator surfaces a
			// loopback ingest daemon has never mounted.
			if cfg.Admin.RequireAuth && authHandler != nil {
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
			// Operator-entered third-party credentials for ANY service,
			// stored encrypted at rest under the node's own key material
			// (internal/credstore) — NEVER as an SDS record, which would
			// replicate the credential to every peer.
			//
			// Lane ids are operator-defined (owner 2026-08-04). The verifier
			// map below is NOT the lane list: it is only the set of lanes this
			// node can actively PROBE. A lane with no entry here is stored and
			// reported as "saved, unverified" — honestly, and forever.
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
				api.NewAIProvidersHandler(credStore, authHandler, cfg.Admin.RequireAuth).RegisterRoutes(adminMux)
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
				// Point the stats cache and the dashboard stats lane at the
				// restart-surviving cache directory. MUST precede
				// StartDashboardSnapshots — the lane loads its persisted frame
				// at Start.
				coreAPI.SetUICacheDir(uiCacheDir)
				// SDN peer counts for /api/v1/stats — the anonymous read
				// surface app boards poll. Same evidence as the dashboard's
				// /api/peers/sdn (epm.BuildObservedSDNPeers), so "SDN peers"
				// on a board can never disagree with the peer list.
				coreAPI.SetSDNPeerCounter(func() api.SDNPeerCounts {
					counts := observedSDNPeerCounts(n)
					return api.SDNPeerCounts{Connected: counts.Connected, Known: counts.Known}
				})

				// Background dashboard data plane: the stats lane rebuilds the
				// store numbers every 5s off the request path, so both
				// /api/v1/stats and the binary /api/v1/dashboard/stats answer
				// from RAM while an ingest holds the write lock.
				coreAPI.StartDashboardSnapshots()
				defer coreAPI.StopDashboardSnapshots()

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
				// Sandboxed read-only SELECT (docs Phase G.5) — the data
				// explorer's uniform lane, admin-gated.
				coreAPI.RegisterSandboxQueryRoute(adminMux)
				// Server-side table pages over the same sandbox: pagination,
				// sort, per-column filters, search, and the network-source
				// selector ("_source") — the data-engineering page's lane.
				coreAPI.RegisterTableRoutes(adminMux)
				log.Infof("Core API available at %s://%s/api/v1/{id,version,stats,peers,pubsub}", adminScheme, adminAddr)

				// $APPS feed (anonymous read): what apps this node runs and
				// what each has retrieved. Apps are read from the FLOW SERVICE
				// registry — timer-served flow bundles registered with the
				// plugin manager — not the legacy module list, which is empty
				// on a flow-only node. Retrieval metrics come from the node's
				// operational ledger; last-PNM is refreshed from the record
				// store's dataset-publication index and written back through.
				{
					metricsStore := n.SourceMetrics()
					var (
						metricsSource api.AppsMetricsSource
						pnmSink       func(string, string, sourcemetrics.PNM)
					)
					if metricsStore != nil {
						metricsSource = metricsStore.Sources
						pnmSink = metricsStore.RecordPNM
					}
					appsAPI := api.NewAppsHandler(
						n.PluginManager().RuntimeSnapshot,
						metricsSource,
						n.Store(),
						pnmSink,
					).WithSelfPeerID(n.PeerID().String())
					appsAPI.RegisterRoutes(adminMux)
					log.Infof("Apps feed available at %s://%s/api/apps (anonymous read)", adminScheme, adminAddr)
				}

				// DEFAULT-$APP surface (anonymous read): which $APP each
				// runtime class opens — the Dashboard for this server, the
				// declared console for a browser client — each carrying a
				// link to the other (owner ruling 2026-08-04). Distinct from
				// /api/apps above: that reports what the node RUNS, this
				// reports what it OFFERS to open.
				//
				// A failed registry build is NOT fatal. The node's job is to
				// run; a bad apps.* declaration must be loud and must not take
				// the daemon down with it.
				if appRegistry, err := buildAppRegistry(cfg.Apps, dashboardHTML); err != nil {
					log.Errorf("Default-$APP registry not available: %v", err)
				} else {
					api.NewDefaultAppsHandler(appRegistry).
						WithNodePeerID(n.PeerID().String()).
						RegisterRoutes(adminMux)
					log.Infof("Default $APP surface available at %s://%s%s (anonymous read); registered apps: %s",
						adminScheme, adminAddr, api.AppsDefaultPath, strings.Join(appRegistry.IDs(), ", "))
				}

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
				// The dashboard stats frame rides the SAME socket as an
				// additional binary message; clients tell the two apart by the
				// FlatBuffer file identifier ($NST vs $NDS). Existing $NST
				// consumers are unaffected.
				statusBroadcaster.AddFrameSource("stats", coreAPI.DashboardStatsFrame)
				statusBroadcaster.Start()
				adminMux.HandleFunc("/ws/status", statusBroadcaster.ServeHTTP)
				log.Infof("Status feed available at %s://%s/ws/status (public read-only)", adminScheme, adminAddr)

				// Operator-enrolled peer EPMs (peers.epm_dir): a fleet
				// peer's signed identity is held from provisioning, even
				// while the peer is offline (owner directive 2026-07-31).
				if dir := strings.TrimSpace(cfg.Peers.EPMDir); dir != "" {
					if count := loadEnrolledPeerEPMs(dir, n.PeerRegistry()); count > 0 {
						log.Infof("peer-epm-enrolment: %d signed peer EPM(s) loaded from config epm_dir", count)
					}
				}

				// On-identify EPM exchange (owner directive 2026-07-31: the
				// identity is fetchable the moment a connection is
				// instantiated): the instant identify completes for a peer
				// speaking the exchange protocol, request its EPM if we do
				// not hold one. The timer pump below stays as the sweep for
				// missed events and offline pinned peers.
				if sub, err := n.Host().EventBus().Subscribe(new(event.EvtPeerIdentificationCompleted)); err == nil {
					go func() {
						defer sub.Close()
						for {
							select {
							case <-ctx.Done():
								return
							case e, ok := <-sub.Out():
								if !ok {
									return
								}
								evt, ok := e.(event.EvtPeerIdentificationCompleted)
								if !ok {
									continue
								}
								pid := evt.Peer
								if pid == n.Host().ID() {
									continue
								}
								epmSvc := n.EPMService()
								registry := n.PeerRegistry()
								if epmSvc == nil || registry == nil {
									continue
								}
								if tp, err := registry.GetPeer(pid); err == nil && tp != nil && len(tp.EPMData) > 0 {
									continue
								}
								protos, err := n.Host().Peerstore().GetProtocols(pid)
								if err != nil {
									continue
								}
								speaks := false
								for _, proto := range protos {
									if proto == epm.EPMExchangeProtocolID {
										speaks = true
										break
									}
								}
								if !speaks {
									continue
								}
								go func(target peer.ID) {
									if err := epmSvc.RequestPeerEPM(ctx, n.Host(), target); err != nil {
										log.Debugf("epm-exchange: on-identify request to %s failed: %v", target.ShortString(), err)
									}
								}(pid)
							}
						}
					}()
				} else {
					log.Warnf("epm-exchange: identify-event subscription failed, timer pump only: %v", err)
				}

				// EPM exchange pump: RequestPeerEPM was previously never
				// invoked, so observed peers' vCards stayed the sparse
				// registry fallback (graph task nst-qr-identity-verify).
				// Periodically request the EPM of every connected registry
				// peer that has not published one to us yet; each stored
				// EPM upgrades that peer's feed vCard, downloads and QR.
				go func() {
					const retryCooldown = time.Hour
					// Refreshing an already-connected peer is one stream frame
					// answered by a change-only append — cheap enough to bound
					// EPM staleness in minutes instead of an hour.
					const connectedRefreshCooldown = 10 * time.Minute
					lastAttempt := map[peer.ID]time.Time{}
					ticker := time.NewTicker(2 * time.Minute)
					defer ticker.Stop()
					for {
						select {
						case <-ctx.Done():
							return
						case <-ticker.C:
						}
						epmSvc := n.EPMService()
						registry := n.PeerRegistry()
						if epmSvc == nil || registry == nil {
							continue
						}
						// Pass 1: registry peers without an EPM. Pass 2: CONNECTED
						// peers that advertise the EPM-exchange protocol but have
						// no registry row at all — the registry projection can
						// come up empty of learned rows after a restart, and a
						// row-less peer would otherwise never be swept, leaving
						// its identity surface dark (owner law 2026-07-31).
						candidates := registry.ListPeers()
						known := make(map[peer.ID]bool, len(candidates))
						for _, tp := range candidates {
							if tp != nil {
								known[tp.ID] = true
							}
						}
						for _, pid := range n.Host().Network().Peers() {
							if known[pid] || pid == n.Host().ID() {
								continue
							}
							protos, err := n.Host().Peerstore().GetProtocols(pid)
							if err != nil {
								continue
							}
							for _, proto := range protos {
								if proto == epm.EPMExchangeProtocolID {
									candidates = append(candidates, &peers.TrustedPeer{ID: pid})
									break
								}
							}
						}
						for _, tp := range candidates {
							if tp == nil {
								continue
							}
							// A registry peer with known addresses but no live
							// connection is DIALLED first: pinned peers (e.g.
							// config-enrolled fleet nodes) may never dial us,
							// and without their EPM the identity surface can
							// serve no QR card at all (owner law 2026-07-31).
							connected := n.Host().Network().Connectedness(tp.ID) == network.Connected
							if len(tp.EPMData) > 0 && !connected {
								// A stored EPM is only refreshed opportunistically
								// from live connections — never worth a dial.
								continue
							}
							if !connected && len(tp.Addrs) == 0 {
								continue
							}
							// CONNECTED peers are re-requested even when a row
							// already has EPMData: the projection can restore a
							// STALE EPM (observed 2026-08-01: a DN-less default
							// EPM from host-02's orphaned-profile era resurfaced
							// after hydration reload and the pump skipped the row
							// forever). The exchange's change-only append makes an
							// identical re-request a no-op, so the refresh runs on
							// a short cooldown; the full cooldown still bounds
							// DIALS, which are the expensive path.
							cooldown := retryCooldown
							if connected {
								cooldown = connectedRefreshCooldown
							}
							if last, ok := lastAttempt[tp.ID]; ok && time.Since(last) < cooldown {
								continue
							}
							lastAttempt[tp.ID] = time.Now()
							go func(target peer.ID, addrs []multiaddr.Multiaddr, connected bool) {
								if !connected {
									dialCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
									err := n.Host().Connect(dialCtx, peer.AddrInfo{ID: target, Addrs: addrs})
									cancel()
									if err != nil {
										log.Debugf("epm-exchange: dial %s for EPM failed: %v", target.ShortString(), err)
										return
									}
								}
								if err := epmSvc.RequestPeerEPM(ctx, n.Host(), target); err != nil {
									log.Debugf("epm-exchange: request to %s failed: %v", target.ShortString(), err)
								}
							}(tp.ID, append([]multiaddr.Multiaddr(nil), tp.Addrs...), connected)
						}
					}
				}()
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
			// The browser engine artifact (flatsql.wasm + integrity.json)
			// the dashboard's local FlatSQL window runs on, same-origin.
			adminMux.Handle("/sdn-js/", makeSdnJsAssetsHandler())
			// The node's own documentation, shipped in the binary (owner
			// 2026-08-28): version-exact guides + PDF at /docs/.
			adminMux.Handle("/docs", makeDocsHandler())
			adminMux.Handle("/docs/", makeDocsHandler())
			// Semantic-search assets for the dashboard's embedding search
			// (model + tokenizer + ort wasm runtime), same-origin and
			// fail-open: absent assets 404 and the dashboard keeps its
			// always-on substring search.
			adminMux.Handle("/embedding/", makeEmbeddingHandler(cfg.Embedding.AssetsDir))
			// hd-wallet-wasm, served SAME-ORIGIN for the dashboard's wallet
			// sign-in (its CSP is default-src 'self'). Staged by
			// deployment/wallet-wasm/stage-wallet-wasm.sh; fail-open —
			// absent assets 404 and the dashboard reports sign-in
			// unavailable rather than reaching a CDN.
			adminMux.Handle("/wallet-wasm/", makeWalletWasmHandler(cfg.WalletWasm.AssetsDir))
			// hd-wallet-ui — the actual wallet sign-in experience, mounted
			// IN-PAGE by the dashboard. Owner law 2026-07-27: "we do NOT load
			// anything from a site", so it is served from here, never from
			// wallet.spacedatanetwork.org. Same fail-open terms.
			adminMux.Handle("/wallet-ui/", makeWalletUIHandler(cfg.WalletWasm.UIAssetsDir))
			// Anonymous identity downloads for the dashboard modal:
			// /identity/<peerId>.vcf|.epm — the same data the public
			// status feed already streams (vCards) or that peers publish
			// (signed EPM records).
			peerLookup := func(peerID string) *peers.TrustedPeer {
				reg := n.PeerRegistry()
				if reg == nil {
					return nil
				}
				pid, err := peer.Decode(peerID)
				if err != nil {
					return nil
				}
				tp, err := reg.GetPeer(pid)
				if err != nil {
					return nil
				}
				return tp
			}
			adminMux.Handle("/identity/", makeIdentityHandler(identitySource{
				SelfID: n.Host().ID().String(),
				SelfVCard: func() (string, error) {
					if svc := n.EPMService(); svc != nil {
						return svc.GetNodeVCard()
					}
					return "", fmt.Errorf("EPM service not available")
				},
				SelfEPM: func() []byte {
					if svc := n.EPMService(); svc != nil {
						return svc.GetNodeEPM()
					}
					return nil
				},
				SelfQRVCard: func() (string, error) {
					if svc := n.EPMService(); svc != nil {
						return svc.GetNodeQRVCard()
					}
					return "", fmt.Errorf("EPM service not available")
				},
				PeerVCard: func(peerID string) (string, bool) {
					tp := peerLookup(peerID)
					if tp == nil {
						return "", false
					}
					if v := strings.TrimSpace(tp.VCardData); v != "" {
						return v, true
					}
					if len(tp.EPMData) > 0 {
						if v, err := sdnvcard.EPMToVCard(tp.EPMData); err == nil {
							return v, true
						}
					}
					return peers.TrustedPeerToVCard(tp), true
				},
				PeerEPM: func(peerID string) ([]byte, bool) {
					tp := peerLookup(peerID)
					if tp == nil || len(tp.EPMData) == 0 {
						return nil, false
					}
					return tp.EPMData, true
				},
				PeerQRVCard: func(peerID string) (string, bool) {
					// OWNER LAW 2026-07-31: a scannable card MUST carry the
					// full crypto identity — xpub, sign/encrypt HD paths and
					// the EPM signature chain. A peer we hold no signed EPM
					// for gets NO QR card at all (fail closed), never a
					// name-and-peer-id-only card.
					tp := peerLookup(peerID)
					if tp == nil || len(tp.EPMData) == 0 {
						return "", false
					}
					card, err := sdnvcard.CompactQRVCard(tp.EPMData)
					if err != nil || !sdnvcard.CardCarriesCryptoIdentity(card) {
						return "", false
					}
					return card, true
				},
			}))
			adminMux.Handle("/", makeRootHandler())
			log.Infof("Node status dashboard at %s://%s/ (fed by /ws/status; admin portal remains at /admin)", adminScheme, adminAddr)

			adminServer = &http.Server{
				Addr:              adminAddr,
				ReadHeaderTimeout: 10 * time.Second,
				ReadTimeout:       30 * time.Second,
				WriteTimeout:      10 * time.Minute,
				IdleTimeout:       120 * time.Second,
				Handler: newAdminUpgradeRouter(
					wsUpgradeProxy,
					adminSecurityMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						serveAdminMuxRequest(w, r, adminMux, cfg.Admin.RequireAuth, assetOIDCCapabilityMounted, authHandler, publicAPIRequest)
					}), tlsManager.Mode(), publicAPIRequest),
				),
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

	// SHUTDOWN WATCHDOG. Before this existed, a wedged shutdown was SILENT:
	// "Shutting down..." was the last line in the journal and the next thing
	// systemd logged was SIGKILL at TimeoutStopSec, with no way to tell which
	// step had blocked. On host-01, 15 of 22 stops on 2026-08-09 ended that
	// way, each one a 2-4 minute public outage.
	//
	// Fire before systemd's TimeoutStopSec (90s on this fleet) so the
	// goroutine dump reaches the journal instead of being lost to the kill,
	// then re-raise SIGKILL. Re-raising the signal systemd would have sent
	// keeps supervisor semantics byte-identical on boxes with Restart=always
	// AND on boxes with Restart=on-failure — an invented exit code would not
	// be honest on both. SIGKILL here is state-safe: every durable write is
	// committed at write time through CRC-framed, fsync-per-append journals
	// with CRC-validated prefix replay on boot, so an abrupt exit costs a
	// slower next boot, never data.
	shutdownWatchdog := time.AfterFunc(shutdownWatchdogTimeout, func() {
		buf := make([]byte, 4<<20)
		n := runtime.Stack(buf, true)
		log.Errorf("SHUTDOWN WATCHDOG: still shutting down after %s — killing. Goroutine dump follows:\n%s",
			shutdownWatchdogTimeout, buf[:n])
		_ = syscall.Kill(syscall.Getpid(), syscall.SIGKILL)
	})
	defer shutdownWatchdog.Stop()

	// The HTTP drains must NOT use ctx: ctx is the live daemon context and is
	// still open here (its cancel is deferred at the top of runDaemon), so
	// Shutdown(ctx) is semantically Shutdown(context.Background()) — it waits
	// forever for in-flight handlers. On a box where a single store read can
	// queue for tens of seconds behind the engine lock, one slow request held
	// the whole shutdown. Bound it, then force the sockets closed.
	shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), httpShutdownTimeout)
	defer cancelShutdown()
	for _, srv := range []struct {
		name string
		srv  *http.Server
	}{
		{"admin", adminServer},
		{"http-challenge", httpChallengeServer},
		{"local-publish", localPublishServer},
	} {
		if srv.srv == nil {
			continue
		}
		if err := srv.srv.Shutdown(shutdownCtx); err != nil {
			// Deadline hit: Shutdown leaves in-flight requests running, so
			// close the listeners outright rather than wait on a handler
			// parked behind the engine queue. Abandoning a read is free and
			// an abandoned write is already journal-fsynced or never started.
			log.Warnf("%s server graceful shutdown exceeded %s (%v); force-closing", srv.name, httpShutdownTimeout, err)
			_ = srv.srv.Close()
		}
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

	// Bounded drain. On timeout StopContext deliberately leaves the store open
	// rather than closing it out from under goroutines that are still running;
	// the watchdog above then kills the process, which the journals make safe.
	nodeStopCtx, cancelNodeStop := context.WithTimeout(context.Background(), nodeDrainTimeout)
	defer cancelNodeStop()
	return n.StopContext(nodeStopCtx)
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

// Shutdown budgets. These are deliberately well inside systemd's
// TimeoutStopSec (90s on this fleet) so the daemon decides how it dies and
// leaves evidence, rather than being SIGKILLed with an empty journal.
//
//	httpShutdownTimeout     — graceful drain of the three HTTP listeners.
//	nodeDrainTimeout        — background goroutines (n.wg) after n.cancel().
//	shutdownWatchdogTimeout — total budget; must exceed the two above plus
//	                          store/engine teardown, and stay under 90s.
const (
	httpShutdownTimeout     = 15 * time.Second
	nodeDrainTimeout        = 30 * time.Second
	shutdownWatchdogTimeout = 75 * time.Second
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
			// Loopback self-gated admin paths (update/dataset control) carry
			// their OWN designed gate — loopback RemoteAddr + a one-time
			// control token consumed on use — and that gate never ran on
			// require_auth nodes because this default-deny fired first
			// (ops-update-shutdown-401: an unattended fleet update reported
			// success while the OLD binary kept running). Let a request that
			// is BOTH self-gated AND actually from loopback reach its
			// handler; everything remote still hits the wallet wall.
			if isLoopbackSelfGatedAdminPath(requestPath) && isLoopbackRequestAddr(r.RemoteAddr) {
				adminMux.ServeHTTP(w, r)
				return
			}
			minTrust := peers.Standard
			switch {
			case isAdminOnlyAPIPath(requestPath):
				minTrust = peers.Admin
			case isAnyTierAuthenticatedAPIPath(requestPath):
				minTrust = anyTierAuthenticatedTrust
			}
			authHandler.RequireAuth(minTrust, func(w http.ResponseWriter, r *http.Request) {
				adminMux.ServeHTTP(w, r)
			})(w, r)
			return
		}
		adminMux.ServeHTTP(w, r)
		return
	}

	// require_auth is OFF: the node's READ surface is open, and stays exactly as
	// open as it was. OPERATOR AUTHORITY does not (owner order 2026-07-28).
	//
	// "No auth" once had to mean "no gate", because on a node with no seeded
	// admin there was no way to satisfy a gate; §14/§19 root recognition removed
	// that — a node holding its own seed can always mint an Admin session
	// through the admit point, and the admit point is now mounted
	// unconditionally. So Admin-only paths keep the gate the authenticated
	// profile applies, and driving this node's schedules costs a real session.
	//
	// METHOD-GRANULAR, deliberately. isAdminOnlyAPIPath is prefix-only and
	// classifies plenty of READS (/api/v1/data/records/, /api/peers/…) that an
	// operator-disabled-auth node has always served openly; gating those would
	// be closing the read surface, which is not what was asked and broke the
	// plugin-demo record read on the first attempt. Only state-changing methods
	// are gated here, which is precisely the set "run-now must stay behind the
	// same auth the sidecar uses" names.
	adminOnlyPath := isAdminOnlyAPIPath(r.URL.Path) &&
		!isLoopbackSelfGatedAdminPath(r.URL.Path) &&
		isStateChangingMethod(r.Method)
	if adminOnlyPath && !publicAPIRequest(r.Method, r.URL.Path) {
		if authHandler == nil {
			// Fail closed: no admit point, no operator action.
			http.Error(w, "authentication unavailable", http.StatusServiceUnavailable)
			return
		}
		authHandler.RequireAuth(peers.Admin, func(w http.ResponseWriter, r *http.Request) {
			adminMux.ServeHTTP(w, r)
		})(w, r)
		return
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
		// $APPS feed: which apps this node runs and what each has retrieved
		// (last_retrieved_at, debounce window, last pull size, last PNM). Every
		// field is an operational fact about PUBLIC data retrieval — the same
		// class of disclosure as /api/v1/stats, which is already anonymous.
		"/api/apps",
		// DEFAULT-$APP surface. A browser client asking "what do I open?"
		// has no session and no identity yet — it is asking before it can be
		// anybody — so this read must never be a privilege. What it discloses
		// is app identity, where each app is served, and page content hashes:
		// all already public (the dashboard's own bytes are served at "/" to
		// anyone). The record route below serves those same bytes wrapped in
		// their $APP envelope.
		"/api/v1/apps/default",
		"/api/storefront/listings",
		"/api/v1/catalog",
		"/api/v1/data/health",
		// Anonymous per-record index page for explicitly selected schemas.
		"/api/v1/data/index",
		"/sdn/libp2p.js",
		"/api/v1/id",
		"/api/v1/version",
		"/api/v1/stats",
		// The same numbers as /api/v1/stats, pre-serialized as a $NDS frame.
		// Same disclosure, same anonymous posture as the /ws/status feed that
		// also carries it.
		"/api/v1/dashboard/stats",
		"/api/v1/pubsub/topics",
		// The embedded Space Data Standards registry (names + descriptions):
		// public by design, the same text the schema files carry.
		"/api/v1/standards",
		"/api/v1/pubsub/messages",
		// The node's security bond: public BY DESIGN — peers price trust by
		// a bond anyone can verify (owner 2026-08-03; bond_attestation.go).
		"/api/v1/trust/bond",
		// Policies are the evaluator's published rules and verdicts its
		// signed public opinions: both read as openly as the bond.
		"/api/v1/trust/policies",
		"/api/v1/trust/verdicts",
		"/api/v1/peers":
		return true
	}

	// Flow-served per-schema record retrieval (loop C.4: the data-retrieval
	// flow mounted at /api/v1/data/ owns routing/format/ETag inside wasm).
	//
	// PER-SCHEMA, not per-literal (sdn-rfb-public-read-allowlist). This used
	// to be the single literal "/api/v1/data/omm/bulk", which is why the $RFB
	// read 401'd in a browser before CORS could answer, and why every future
	// standard would have done the same. The anonymous data plane is now a
	// property of the STANDARD (sds.IsPublicReadSchema) — an allow-list, so an
	// unlisted or unknown standard stays behind the gate.
	if code, ok := dataPlaneBulkSchema(path); ok {
		return sds.IsPublicReadSchema(code)
	}

	return strings.HasPrefix(path, "/api/directory/") ||
		strings.HasPrefix(path, "/api/v1/docs/") ||
		// Engine DDL per standard (/api/v1/standards/{CODE}.fbs): the same
		// text the schema files carry, anonymous like the registry above.
		strings.HasPrefix(path, "/api/v1/standards/") ||
		// $APP record bytes for an app this node offers — same anonymity
		// argument as /api/v1/apps/default above.
		strings.HasPrefix(path, "/api/v1/apps/records/") ||
		path == "/api/v1/channels" ||
		strings.HasPrefix(path, "/api/v1/channels/") ||
		strings.HasPrefix(path, "/api/v1/demo/") ||
		strings.HasPrefix(path, "/api/storefront/listings/") ||
		strings.HasPrefix(path, "/api/storefront/trust/") ||
		strings.HasPrefix(path, "/api/v1/log/")
}

// dataPlaneBulkSchema matches the flow-served per-schema bulk read
// "/api/v1/data/<code>/bulk" and returns <code> (e.g. "rfb").
//
// Deliberately exact: only the /bulk verb of the default data mount is
// classified here. A record-by-CID read (/api/v1/data/records/<cid>), the SQL
// query route and the datasync scan/stream surface each keep their own
// classification — this function must never widen them by accident.
func dataPlaneBulkSchema(path string) (string, bool) {
	const prefix = "/api/v1/data/"
	if !strings.HasPrefix(path, prefix) {
		return "", false
	}
	// Exactly two segments: "<code>/bulk". Trimming a "/bulk" suffix instead
	// would read "/api/v1/data/bulk" as the schema "bulk", because the prefix
	// and the suffix overlap on the same slash.
	segments := strings.Split(strings.TrimPrefix(path, prefix), "/")
	if len(segments) != 2 || segments[0] == "" || segments[1] != "bulk" {
		return "", false
	}
	return segments[0], true
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

// newAdminUpgradeRouter is the admin listener's TOP-LEVEL handler: it decides,
// before anything else runs, whether a request is a libp2p websocket dial to be
// tunnelled or an ordinary admin/dashboard request.
//
// THIS ORDER IS THE CONTRACT, and it is why this is a named function with tests
// (TestAdminRootPathWebSocketUpgradeReturns101) rather than an anonymous
// closure inside a 5000-line RunE. The dashboard is mounted at the catch-all
// "/", so if the mux — or any middleware in front of it — ever gets to see a
// root-path `Connection: Upgrade` first, the node answers a browser's libp2p
// dial with 200 and a page of HTML, and every browser on the network silently
// loses its data path while the site itself looks perfectly healthy. That
// failure mode is invisible to every check that only asks "is the homepage up".
//
// Three rules, all load-bearing:
//
//  1. A websocket upgrade on ANY path other than the admin mux's own websocket
//     endpoints is proxied to the local libp2p /ws listener, bypassing
//     adminSecurityMiddleware entirely — it is a raw passthrough, not an admin
//     response, and the libp2p security handshake is the authentication.
//  2. /ws (pubsub bridge) and /ws/status (telemetry) are NEVER tunnelled;
//     without that exemption every ws client on a TLS admin listener gets a
//     libp2p multistream banner instead of the endpoint it dialled.
//  3. With no tunnel configured (adminTLS off, or no local /ws listener found)
//     every request falls through to the admin surface unchanged.
//
// NOTE FOR ANYONE PROBING THIS IN PRODUCTION: sdn.spaceaware.io sits behind a
// CDN that speaks HTTP/2 to clients, and HTTP/2 cannot carry `Connection:
// Upgrade` at all — it is a connection-specific header the protocol forbids. A
// curl probe that negotiates h2 therefore reaches this handler as a PLAIN GET
// and correctly receives the dashboard with 200. That is a property of the
// probe, not of the node: probe with --http1.1 (RFC 8441 h2 websockets are not
// in play here). Mistaking the h2 200 for a broken tunnel cost this task its
// first hour.
func newAdminUpgradeRouter(wsUpgradeProxy http.Handler, admin http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if wsUpgradeProxy != nil && isWebSocketUpgradeRequest(r) && !isAdminWebSocketPath(r.URL.Path) {
			wsUpgradeProxy.ServeHTTP(w, r)
			return
		}
		admin.ServeHTTP(w, r)
	})
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

// resolveLocalLibp2pWsProxyTarget picks the PLAINTEXT, LOOPBACK-REACHABLE
// libp2p websocket listener that the admin listener's upgrade tunnel proxies
// into (see newAdminUpgradeRouter).
//
// "Plaintext" is the part that is easy to get wrong and expensive to get wrong.
// The tunnel speaks cleartext HTTP to 127.0.0.1: TLS is terminated once, at the
// admin listener. So any listen address that terminates TLS ITSELF is a wrong
// answer, and since the AutoTLS connector landed (internal/node/autotls.go) the
// node can publish exactly such an address:
//
//	/ip4/167.172.219.213/tcp/4001/tls/sni/*.libp2p.direct/ws
//
// It contains "/ws", it does not contain "/wss", and it SHARES its TCP port
// with the plain TCP transport (libp2p.ShareTCPListener) — so the previous
// substring scan selected it, derived port 4001, and pointed the browser tunnel
// at a TLS listener over cleartext. Every browser upgrade would 502 while the
// node logged nothing but a proxy error. Parsing the multiaddr instead of
// scanning it for "/ws" removes the whole class.
//
// Preference order, and both preferences are load-bearing:
//  1. an explicitly LOOPBACK plain /ws listener (host-01's /ip4/127.0.0.1/tcp/
//     18080/ws) — guaranteed reachable at the 127.0.0.1 address the proxy dials;
//  2. any other plain /ws listener, whose port is reachable on loopback when it
//     is bound to 0.0.0.0 (which go-libp2p reports expanded per interface).
func resolveLocalLibp2pWsProxyTarget(listenAddrs []string) (*url.URL, string) {
	var fallbackPort, fallbackAddr string

	for _, rawAddr := range listenAddrs {
		addr := strings.TrimSpace(rawAddr)
		if addr == "" {
			continue
		}
		port, loopback, ok := plainLocalWsListener(addr)
		if !ok {
			continue
		}
		if loopback {
			if target, err := url.Parse("http://127.0.0.1:" + port); err == nil {
				return target, addr
			}
			continue
		}
		if fallbackPort == "" {
			fallbackPort, fallbackAddr = port, addr
		}
	}

	if fallbackPort != "" {
		if target, err := url.Parse("http://127.0.0.1:" + fallbackPort); err == nil {
			return target, fallbackAddr
		}
	}
	return nil, ""
}

// plainLocalWsListener reports the TCP port of a CLEARTEXT websocket listen
// address and whether it is bound to loopback. ok is false for anything the
// tunnel must not proxy into: a TLS-terminating listener (/wss, or the AutoTLS
// /tls/sni/.../ws form), a non-websocket transport, or an address with no TCP
// port.
func plainLocalWsListener(addr string) (port string, loopback bool, ok bool) {
	ma, err := multiaddr.NewMultiaddr(addr)
	if err != nil {
		return "", false, false
	}

	sawWS := false
	for _, proto := range ma.Protocols() {
		switch proto.Code {
		case multiaddr.P_WS:
			sawWS = true
		case multiaddr.P_WSS, multiaddr.P_TLS, multiaddr.P_QUIC, multiaddr.P_QUIC_V1,
			multiaddr.P_WEBTRANSPORT, multiaddr.P_WEBRTC_DIRECT, multiaddr.P_CIRCUIT:
			// TLS is terminated at the admin listener, once. A listener that
			// terminates it again cannot be spoken to in cleartext.
			return "", false, false
		}
	}
	if !sawWS {
		return "", false, false
	}

	port, err = ma.ValueForProtocol(multiaddr.P_TCP)
	if err != nil || strings.TrimSpace(port) == "" {
		return "", false, false
	}

	for _, code := range []int{multiaddr.P_IP4, multiaddr.P_IP6} {
		if host, err := ma.ValueForProtocol(code); err == nil {
			if ip := net.ParseIP(strings.TrimSpace(host)); ip != nil && ip.IsLoopback() {
				return port, true, true
			}
		}
	}
	return port, false, true
}

// anyTierAuthenticatedTrust is the trust floor for the session-introspection
// and session-termination endpoints below: a VALID SESSION is required (these
// paths are NOT on the anonymous allow-list), but no trust TIER is imposed on
// top of it. peers.Never is the bottom of the scale, so every session that
// authenticates at all clears it — including a session whose operator has been
// demoted or locked out, which is exactly the case that must still work.
const anyTierAuthenticatedTrust = peers.Never

// isAnyTierAuthenticatedAPIPath reports whether path is reachable by ANY
// authenticated session regardless of trust tier.
//
// # Ruling (graph task nst-node-admin-contract, Hermes 2026-07-26)
//
// The top-level wall's default floor is peers.Standard. That is right for
// reads of node data, but wrong for these two, because neither is a privileged
// read of anything:
//
//   - /api/auth/me is SESSION INTROSPECTION. It returns the caller's OWN xpub,
//     name and trust level — nothing the caller does not already hold, since
//     they signed a challenge with the matching private key to obtain the
//     session. Gating it at Standard means an operator at unknown/marginal tier
//     cannot discover that they ARE at unknown/marginal tier: the one state a
//     UI most needs to render legibly ("signed in, insufficient permissions")
//     is the one state the endpoint refuses to describe. Worse, the client
//     cannot even tell "signed in but low tier" from "not signed in".
//   - /api/auth/logout is SESSION TERMINATION. Refusing to let a principal end
//     their own session strands a live credential; logout must never be harder
//     to reach than login. A low-tier or revoked operator ending their session
//     is a security IMPROVEMENT, never a risk.
//
// Deliberately NOT moved here:
//
//   - /api/auth/attest stays at the Standard default. It is not the caller's
//     own session: the request names an ARBITRARY xpub and, on success, reports
//     that user's name and trust level. It is a proof endpoint, not
//     introspection, and no surface needs it before sign-in. Widening its reach
//     without a driving requirement would grow the pre-session attack surface
//     for nothing.
//   - Neither path joins the ANONYMOUS allow-list (isPublicReadAPIPath /
//     isPublicAPIRequest). Anonymous paths also skip CSRF validation in
//     adminSecurityMiddleware, which would turn POST /api/auth/logout into a
//     cross-origin forced-logout vector. Requiring a session keeps CSRF
//     protection on. An anonymous or expired caller therefore gets 401 from
//     logout, which correctly means "already not signed in".
//
// Exact matches only — a prefix rule would admit look-alike paths.
// /api/auth/me/photo joins this set for the same reason /api/auth/me is in it:
// it is the caller acting on THEIR OWN row, self-scoped by construction (the
// row written is session.XPub — there is no identifier in the request to
// tamper with). Gating a profile picture at Standard would restore exactly the
// hole the owner named on 2026-07-30: the operators most likely to be below
// Standard are the ones who need to fill in who they are.
func isAnyTierAuthenticatedAPIPath(path string) bool {
	switch path {
	case "/api/auth/me", "/api/auth/me/photo", "/api/auth/logout":
		return true
	default:
		return false
	}
}

// authStoreFileName is the default SQLite file for operator trust-matrix
// entries and sessions.
const authStoreFileName = "auth.db"

// resolveAuthDBPath returns the SQLite file the auth store must use.
//
// OWNER DIRECTIVE 2026-07-27: "use the entries in an sqlite database to handle
// that. Also I think it should probably be in a separate database file that the
// other standards for safety." The default has always satisfied that — a
// distinct auth.db file beside the record store — so the default is UNCHANGED
// and no deployed node's store moves. What is new is that the location is now
// configurable and that co-locating it with the standards store is refused
// rather than silently accepted.
func resolveAuthDBPath(cfg *config.Config) (string, error) {
	if cfg == nil {
		return "", fmt.Errorf("resolve auth db path: nil config")
	}
	storagePath := strings.TrimSpace(cfg.Storage.Path)
	configured := strings.TrimSpace(cfg.Admin.AuthDBPath)
	if configured == "" {
		return filepath.Join(storagePath, authStoreFileName), nil
	}
	if !filepath.IsAbs(configured) {
		configured = filepath.Join(storagePath, configured)
	}
	if err := validateAuthDBPathSeparation(configured, storagePath); err != nil {
		return "", err
	}
	return configured, nil
}

// validateAuthDBPathSeparation refuses an auth database that would live inside
// the standards / FlatSQL record store directory. "Separate database file ...
// for safety" is only true if a record-store rebuild cannot take the auth
// database with it.
func validateAuthDBPathSeparation(authPath, storagePath string) error {
	storagePath = strings.TrimSpace(storagePath)
	if storagePath == "" {
		return nil
	}
	recordStore := filepath.Clean(filepath.Join(storagePath, "store"))
	cleanAuth := filepath.Clean(authPath)
	rel, err := filepath.Rel(recordStore, cleanAuth)
	if err != nil {
		return nil
	}
	if rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf(
			"admin.auth_db_path %q is inside the standards record store %q; the auth database must be a separate file so a record-store rebuild cannot take operator credentials with it",
			authPath, recordStore)
	}
	return nil
}

// isStateChangingMethod reports whether a request can alter node state. GET,
// HEAD and OPTIONS cannot; everything else is treated as if it can, including
// methods this daemon does not implement — an unknown verb is not a safe one.
func isStateChangingMethod(method string) bool {
	switch strings.ToUpper(strings.TrimSpace(method)) {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return false
	}
	return true
}

// isLoopbackSelfGatedAdminPath names the /api/v1/admin/ routes whose HANDLER
// enforces its own loopback-only gate, independently of any session. They are
// machine-local control surfaces, not operator-authority-by-session:
//
//	/api/v1/admin/dataset-updates/publish  api/dataset_publication.go:120
//	/api/v1/admin/update/shutdown          update/control.go:68
//
// This matters only on a require_auth:false daemon, where the wall would
// otherwise start demanding a session on a door that is already correctly hung
// — and would break the §19 publish trigger, which is a FLOW calling its own
// node over loopback with the `http` capability and no credential by design
// (graph/tasks/sdn-producer-publish-trigger.md). Under require_auth:true these
// paths stay behind the session wall exactly as before; this predicate is not
// consulted there.
func isLoopbackSelfGatedAdminPath(path string) bool {
	switch path {
	case "/api/v1/admin/dataset-updates/publish",
		"/api/v1/admin/update/shutdown":
		return true
	}
	return false
}

// isLoopbackRequestAddr mirrors the update control handler's own RemoteAddr
// gate (internal/update/control.go isLoopbackRemoteAddr): the wall may only
// wave a self-gated request through when it demonstrably originates on this
// box — the handler then enforces its one-time token on top.
// isLoopbackListenAddr reports whether a configured listen address binds a
// loopback interface — the gate admin.dev_auto_admin must pass. "localhost" is
// accepted alongside literal loopback IPs; an empty or wildcard host is NOT
// loopback (it binds every interface).
func isLoopbackListenAddr(listenAddr string) bool {
	host, _, err := net.SplitHostPort(strings.TrimSpace(listenAddr))
	if err != nil {
		host = strings.TrimSpace(listenAddr)
	}
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func isLoopbackRequestAddr(remoteAddr string) bool {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		host = remoteAddr
	}
	ip := net.ParseIP(strings.TrimSpace(host))
	return ip != nil && ip.IsLoopback()
}

func isAdminOnlyAPIPath(path string) bool {
	return strings.HasPrefix(path, "/api/accounts") ||
		strings.HasPrefix(path, "/api/peers") ||
		strings.HasPrefix(path, "/api/groups") ||
		strings.HasPrefix(path, "/api/blocklist") ||
		strings.HasPrefix(path, "/api/settings") ||
		strings.HasPrefix(path, "/api/export") ||
		strings.HasPrefix(path, "/api/import") ||
		strings.HasPrefix(path, "/api/admin/") ||
		strings.HasPrefix(path, "/api/auth/users") ||
		// The node's own EPM identity. Reads (/api/node/epm[/json|/vcard|/qr])
		// are on the anonymous surface via isPublicReadAPIPath and never reach
		// this check; every OTHER method — the PUT that rewrites who this node
		// says it is — is an operator action. isAdminOnlyAPIPath is prefix-only
		// with no method granularity, which is exactly why the write also
		// carries its own method-granular Admin gate where /api/node/epm is
		// mounted on adminMux.
		strings.HasPrefix(path, "/api/node/epm") ||
		// The node's own runtime state: storage path, disk capacity, libp2p
		// bandwidth totals and the sparkline history. These describe the HOST,
		// not public data, so unlike /api/v1/stats they are NOT on the
		// anonymous surface. Read-only either way — see node_runtime_api.go.
		strings.HasPrefix(path, "/api/node/runtime") ||
		// The node's own activity ring. Every row names a peer this host talked
		// to and when — public peer ids, but the PATTERN is a host fact, so the
		// same classification as the bandwidth totals above.
		strings.HasPrefix(path, "/api/node/activity") ||
		// The node's own supervisor state, and — once the Council-ruled
		// destructive half lands — every path beneath it. Prefix-classified
		// deliberately: a lifecycle verb must never be able to appear on a path
		// this list does not already cover (node_service_api.go).
		strings.HasPrefix(path, "/api/node/service") ||
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

// gateNodeEPMWrite splits /api/node/epm by method: reads pass through to inner
// (GET is part of the anonymous read surface — isPublicReadAPIPath — so the
// node's published identity stays fetchable without a session, exactly like
// /api/node/epm/json|vcard|qr), while PUT — the write that rewrites who this
// node says it is — requires an authenticated session at peers.Admin.
//
// This is the SECOND of two independent locks on that write. The first is the
// top-level auth wall (serveAdminMuxRequest + isAdminOnlyAPIPath), which is
// prefix-only and therefore cannot express "GET public, PUT admin" for a single
// path on its own. Both are keyed off the same cfg.Admin.RequireAuth policy: a
// node deliberately run with authentication disabled has no identity system to
// gate against and keeps its pre-existing open-admin behavior, matching
// gateHandlerWithTrust. When authentication IS required the gate fails closed —
// a missing auth handler admits nobody (auth.Handler.RequireTrust on a nil
// receiver writes 401).
//
// resolveAuth is a function rather than a value because the auth handler is
// constructed after this route is mounted.
func gateNodeEPMWrite(inner http.HandlerFunc, requireAuth bool, resolveAuth func() *auth.Handler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut && requireAuth {
			var handler *auth.Handler
			if resolveAuth != nil {
				handler = resolveAuth()
			}
			if !handler.RequireTrust(w, r, peers.Admin) {
				return
			}
		}
		inner(w, r)
	}
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
	// GrantVerifierPublicKeyHex is the ed25519 key clients verify licensing
	// grants against. "" means this node issues no grants.
	GrantVerifierPublicKeyHex() string
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
	PublicKey string `json:"publicKey"`
	PeerID    string `json:"peerId"`
	// GrantVerifierPublicKeys is what a client loads into
	// trustedGrantVerifierPublicKeys and cross-checks every grant's
	// GRANT_VERIFIER_PUBKEY against. A LIST, not a scalar, so a key rotation can
	// advertise old and new together instead of breaking every client mid-flight.
	// Field name matches sdn-js ServerDescriptor.grantVerifierPublicKeys exactly.
	//
	// This is the grant CHILD (m/44'/0'/<account>'/2'/0'), never the fleet
	// update/publisher root — advertising the root here was refused by the Seal
	// Council and the refusal was lifted only because the key is now dedicated.
	GrantVerifierPublicKeys []string                            `json:"grantVerifierPublicKeys,omitempty"`
	IPNS                    string                              `json:"ipns,omitempty"`
	RelayAddresses          []string                            `json:"relayAddresses,omitempty"`
	Identity                *providerDescriptorIdentityResponse `json:"identity,omitempty"`
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
	if grantVerifier := strings.TrimSpace(src.GrantVerifierPublicKeyHex()); grantVerifier != "" {
		response.GrantVerifierPublicKeys = []string{grantVerifier}
	}
	if peerID != "" {
		response.IPNS = "/ipns/" + peerID
	}

	for _, addr := range src.ListenAddrs() {
		if addr == nil {
			continue
		}
		if !dialableFromAnotherHost(addr) {
			continue
		}
		response.RelayAddresses = append(response.RelayAddresses, addr.String())
	}

	return response, nil
}

// dialableFromAnotherHost reports whether a listen address is worth publishing
// in the PUBLIC provider descriptor.
//
// This descriptor is not documentation. When relayAddresses is non-empty a
// client uses it as its ENTIRE candidate list and never falls back to DHT
// discovery (sdn-js module-delivery.ts:676-678), and it walks those candidates
// SEQUENTIALLY re-sending the same stamped request frame (node.ts:657,673-683).
// So an address that cannot possibly resolve to this node from the caller's
// vantage point is not merely noise: it spends the real candidate's connect
// budget before the real candidate is ever tried. host-01 was publishing
// /ip4/127.0.0.1/tcp/4004/ws and /ip4/127.0.0.1/tcp/18080/ws on the open
// internet — half of a four-candidate list that every remote browser had to
// exhaust first.
//
// The filter is deliberately narrow: loopback, link-local and unspecified
// (0.0.0.0, ::) are addresses NO other host can ever dial, whoever it is.
// PRIVATE ranges are KEPT — on a LAN or a docker network they are the only
// address that works, and a node whose descriptor is served on the same network
// it listens on is a supported deployment. Anything that is not an IP literal
// at all (dns4/dns6/p2p-circuit) is kept untouched: resolving it is the
// client's job and this host cannot rule on it.
//
// An empty result is safe by construction — it is exactly the pre-descriptor
// state, and the client falls back to DHT discovery.
func dialableFromAnotherHost(addr multiaddr.Multiaddr) bool {
	literal := ""
	if v, err := addr.ValueForProtocol(multiaddr.P_IP4); err == nil {
		literal = v
	} else if v, err := addr.ValueForProtocol(multiaddr.P_IP6); err == nil {
		literal = v
	}
	if literal == "" {
		return true
	}
	ip := net.ParseIP(literal)
	if ip == nil {
		return true
	}
	return !ip.IsLoopback() && !ip.IsUnspecified() && !ip.IsLinkLocalUnicast() && !ip.IsLinkLocalMulticast()
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
// nodeEPMWireService adapts the node's EPM service to the wire handler
// without leaking a typed nil into the interface.
func nodeEPMWireService(n *node.Node) api.NodeEPMService {
	if svc := n.EPMService(); svc != nil {
		return svc
	}
	return nil
}

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
				// §18: a rejected derivation path is OPERATOR INPUT, not a
				// server fault. It must come back 400 with the validation text
				// verbatim — that text explains the hardening rule, which is
				// not guessable from a form field — and nothing may have been
				// applied (UpdateProfile validates before it mutates).
				if epm.IsKeyPathValidationError(err) {
					http.Error(w, err.Error(), http.StatusBadRequest)
					return
				}
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
	cfg, _, err := config.LoadResolved(configPath)
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
	keys.WarnKeyDirPermissions(keyDir)
	mnemonicPath := filepath.Join(keyDir, "mnemonic")
	if err := keys.EnforceKeyFilePermissions(mnemonicPath); err != nil {
		return nodeMnemonicInitResult{}, err
	}
	if data, err := os.ReadFile(mnemonicPath); err == nil {
		if strings.TrimSpace(string(data)) == "" {
			return nodeMnemonicInitResult{}, fmt.Errorf("mnemonic file %s is empty", mnemonicPath)
		}
		return nodeMnemonicInitResult{Path: mnemonicPath, Created: false}, nil
	} else if !os.IsNotExist(err) {
		return nodeMnemonicInitResult{}, fmt.Errorf("read mnemonic file %s: %w", mnemonicPath, err)
	}

	// Resolve before generating anything. A resealed box can legitimately have
	// no mnemonic at this instant; if its mounted password file is unavailable,
	// generating first creates key material that must never be sealed under a
	// fallback.
	keyPassword, err := resolveKeyCLIPassword(cfg)
	if err != nil {
		return nodeMnemonicInitResult{}, err
	}

	mnemonic, err := generateMnemonic(ctx)
	if err != nil {
		return nodeMnemonicInitResult{}, fmt.Errorf("generate node mnemonic: %w", err)
	}
	if strings.TrimSpace(mnemonic) == "" {
		return nodeMnemonicInitResult{}, fmt.Errorf("generated mnemonic is empty")
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
	cfg, _, err := config.LoadResolved(configPath)
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
	cfg, cfgRes, err := config.LoadResolved(configPath)
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}
	fmt.Fprintf(os.Stderr, "config: %s\n", cfgRes.Describe())

	keyPassword, err := resolveKeyCLIPassword(cfg)
	if err != nil {
		return err
	}

	// Locate mnemonic file
	// Shared derivation — the same functions internal/node uses, so the CLI
	// can never look somewhere the daemon does not write.
	keyDir := config.KeyDir(cfg)
	mnemonicPath := config.MnemonicPathResolved(cfg)

	keys.WarnKeyDirPermissions(keyDir)
	if err := keys.EnforceKeyFilePermissions(mnemonicPath); err != nil {
		return err
	}
	data, err := os.ReadFile(mnemonicPath)
	if err != nil {
		if os.IsNotExist(err) {
			return config.DescribeMissingNodeState("node identity (mnemonic)", mnemonicPath, cfgRes)
		}
		if os.IsPermission(err) {
			return config.DescribePermissionDenied("the node mnemonic", mnemonicPath, keyDirOwner(keyDir), cfgRes)
		}
		return fmt.Errorf("read mnemonic %s (config: %s): %w", mnemonicPath, cfgRes.Describe(), err)
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

	// ONE encryption path, everywhere (owner rule). Show the key the node
	// actually ADVERTISES on its card/QR — the xpub-derivable secp256k1 key at
	// the effective path — not the identity's hardened X25519 key. Printing the
	// hardened one here is what made a single node look like it had two
	// encryption paths depending on where you looked. The X25519 key stays an
	// internal decryption detail.
	var profile *epm.Profile
	if p, err := epm.LoadProfile(cfg.Storage.Path); err == nil {
		profile = p
	}
	if encPub, encPath, ok := epm.AdvertisedEncryptionKey(xpubStr, 0, profile); ok {
		fmt.Fprintf(os.Stderr, "Encryption Key: %s  (path: %s)\n", encPub, encPath)
	} else {
		// Never invent a key: say plainly that it could not be derived.
		fmt.Fprintf(os.Stderr, "Encryption Key: (could not derive from xpub)\n")
	}
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
