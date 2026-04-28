package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/peerstore"
	libp2pprotocol "github.com/libp2p/go-libp2p/core/protocol"
	"github.com/libp2p/go-libp2p/p2p/transport/tcp"
	"github.com/libp2p/go-libp2p/p2p/transport/websocket"
	"github.com/multiformats/go-multiaddr"
	"github.com/spf13/cobra"

	"github.com/spacedatanetwork/sdn-server/internal/license"
)

const defaultModulePublishProviderURL = "https://sdn.spaceaware.io/api/module-delivery/provider"

var (
	modulePublishPluginRoot   string
	modulePublishProviderURL  string
	modulePublishTargetAddr   string
	modulePublishTokenEnv     string
	modulePublishModules      []string
	modulePublishAllowDomains []string
	modulePublishTimeout      time.Duration
)

var pluginsCmd = &cobra.Command{
	Use:   "plugins",
	Short: "Manage SDN module-delivery plugins",
}

var pluginsPublishOrbProCmd = &cobra.Command{
	Use:   "publish-orbpro",
	Short: "Publish encrypted OrbPro module artifacts over libp2p",
	Long: `Publish encrypted OrbPro module artifacts from a generated catalog root to an
SDN provider using the /space-data-network/module-publish/1.0.0 libp2p
protocol. The publish request is authenticated with an HMAC token read from
SDN_MODULE_PUBLISH_TOKEN by default.`,
	RunE: runPluginsPublishOrbPro,
}

func init() {
	pluginsPublishOrbProCmd.Flags().StringVar(&modulePublishPluginRoot, "plugin-root", "", "plugin root containing catalog.json plus encrypted module artifacts")
	pluginsPublishOrbProCmd.Flags().StringVar(&modulePublishProviderURL, "provider-url", defaultModulePublishProviderURL, "provider descriptor URL used to resolve peer id when --target is omitted")
	pluginsPublishOrbProCmd.Flags().StringVar(&modulePublishTargetAddr, "target", "", "explicit provider multiaddr including /p2p/<peer-id>")
	pluginsPublishOrbProCmd.Flags().StringVar(&modulePublishTokenEnv, "token-env", "SDN_MODULE_PUBLISH_TOKEN", "environment variable containing the module publish token")
	pluginsPublishOrbProCmd.Flags().StringArrayVar(&modulePublishModules, "module", nil, "module id to publish; repeat to publish a subset")
	pluginsPublishOrbProCmd.Flags().StringArrayVar(&modulePublishAllowDomains, "allowed-domain", nil, "allowed requester domain to apply to every published module; repeat for multiple domains")
	pluginsPublishOrbProCmd.Flags().DurationVar(&modulePublishTimeout, "timeout", 60*time.Second, "publish timeout")
	pluginsCmd.AddCommand(pluginsPublishOrbProCmd)
	rootCmd.AddCommand(pluginsCmd)
}

func runPluginsPublishOrbPro(cmd *cobra.Command, args []string) error {
	if strings.TrimSpace(modulePublishPluginRoot) == "" {
		return errors.New("--plugin-root is required")
	}
	nonce, err := randomHex(16)
	if err != nil {
		return err
	}
	req, err := buildModulePublishRequestFromPluginRoot(
		modulePublishPluginRoot,
		modulePublishModules,
		time.Now().UnixMilli(),
		nonce,
	)
	if err != nil {
		return err
	}
	applyPublishAllowedDomains(&req, modulePublishAllowDomains)

	token := resolveModulePublishToken(modulePublishTokenEnv)
	if err := license.SignModulePublishRequest(&req, token); err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(cmd.Context(), modulePublishTimeout)
	defer cancel()

	target, err := resolveModulePublishTarget(ctx, modulePublishProviderURL, modulePublishTargetAddr)
	if err != nil {
		return err
	}
	resp, err := publishModuleRequest(ctx, target, req)
	if err != nil {
		return err
	}
	if !resp.OK {
		return fmt.Errorf("module publish failed: %s", resp.Error)
	}
	for _, result := range resp.Results {
		fmt.Fprintf(cmd.OutOrStdout(), "published %s@%s %s (%d bytes)\n", result.ID, result.Version, result.BundleSHA256, result.SizeBytes)
	}
	return nil
}

func buildModulePublishRequestFromPluginRoot(pluginRoot string, moduleIDs []string, issuedAtMs int64, nonce string) (license.ModulePublishRequest, error) {
	root, err := filepath.Abs(strings.TrimSpace(pluginRoot))
	if err != nil {
		return license.ModulePublishRequest{}, err
	}
	catalogBytes, err := os.ReadFile(filepath.Join(root, "catalog.json"))
	if err != nil {
		return license.ModulePublishRequest{}, fmt.Errorf("read catalog.json: %w", err)
	}
	var catalog license.PluginCatalogFile
	if err := json.Unmarshal(catalogBytes, &catalog); err != nil {
		return license.ModulePublishRequest{}, fmt.Errorf("decode catalog.json: %w", err)
	}

	selected := normalizeModuleIDSet(moduleIDs)
	modules := make([]license.ModulePublishEntry, 0, len(catalog.Plugins))
	for _, entry := range catalog.Plugins {
		id := strings.TrimSpace(entry.ID)
		if id == "" {
			continue
		}
		if len(selected) > 0 {
			if _, ok := selected[id]; !ok {
				continue
			}
		}
		if strings.TrimSpace(entry.EncryptedPath) == "" || strings.TrimSpace(entry.KeyPath) == "" {
			return license.ModulePublishRequest{}, fmt.Errorf("catalog entry %q is not encrypted", id)
		}
		encryptedBundle, err := os.ReadFile(resolveCatalogPath(root, entry.EncryptedPath))
		if err != nil {
			return license.ModulePublishRequest{}, fmt.Errorf("read encrypted bundle for %q: %w", id, err)
		}
		keyMaterial, err := os.ReadFile(resolveCatalogPath(root, entry.KeyPath))
		if err != nil {
			return license.ModulePublishRequest{}, fmt.Errorf("read key material for %q: %w", id, err)
		}
		modules = append(modules, license.ModulePublishEntry{
			ID:                id,
			Version:           entry.Version,
			RequiredScope:     entry.RequiredScope,
			EncryptedBundle:   encryptedBundle,
			KeyMaterial:       keyMaterial,
			ContentType:       entry.ContentType,
			CacheControl:      entry.CacheControl,
			AllowedDomains:    append([]string(nil), entry.AllowedDomains...),
			MaxGrantTimeoutMs: entry.MaxGrantTimeoutMs,
			SignatureHex:      entry.SignatureHex,
			SignerPubKeyHex:   entry.SignerPubKeyHex,
		})
	}
	if len(modules) == 0 {
		if len(selected) > 0 {
			return license.ModulePublishRequest{}, fmt.Errorf("no selected modules found in catalog: %s", strings.Join(sortedKeys(selected), ", "))
		}
		return license.ModulePublishRequest{}, errors.New("catalog contains no encrypted modules")
	}
	return license.ModulePublishRequest{
		Type:       "module-publish.v1",
		IssuedAtMs: issuedAtMs,
		Nonce:      nonce,
		Modules:    modules,
	}, nil
}

func resolveCatalogPath(root, rel string) string {
	clean := filepath.Clean(strings.TrimSpace(rel))
	return filepath.Join(root, clean)
}

func normalizeModuleIDSet(moduleIDs []string) map[string]struct{} {
	if len(moduleIDs) == 0 {
		return nil
	}
	out := make(map[string]struct{}, len(moduleIDs))
	for _, id := range moduleIDs {
		trimmed := strings.TrimSpace(id)
		if trimmed != "" {
			out[trimmed] = struct{}{}
		}
	}
	return out
}

func sortedKeys(values map[string]struct{}) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func applyPublishAllowedDomains(req *license.ModulePublishRequest, domains []string) {
	if req == nil || len(domains) == 0 {
		return
	}
	normalized := make([]string, 0, len(domains))
	seen := map[string]struct{}{}
	for _, domain := range domains {
		trimmed := strings.TrimSpace(domain)
		if trimmed == "" {
			continue
		}
		if _, ok := seen[trimmed]; ok {
			continue
		}
		seen[trimmed] = struct{}{}
		normalized = append(normalized, trimmed)
	}
	if len(normalized) == 0 {
		return
	}
	for i := range req.Modules {
		req.Modules[i].AllowedDomains = append([]string(nil), normalized...)
	}
}

func resolveModulePublishToken(envName string) string {
	if name := strings.TrimSpace(envName); name != "" {
		if token := strings.TrimSpace(os.Getenv(name)); token != "" {
			return token
		}
	}
	if token := strings.TrimSpace(os.Getenv("SDN_MODULE_PUBLISH_TOKEN")); token != "" {
		return token
	}
	return strings.TrimSpace(os.Getenv("SDN_LICENSE_ADMIN_TOKEN"))
}

func resolveModulePublishTarget(ctx context.Context, providerURL, explicitTarget string) (string, error) {
	if target := strings.TrimSpace(explicitTarget); target != "" {
		return target, nil
	}
	descriptor, err := fetchProviderDescriptor(ctx, providerURL)
	if err != nil {
		return "", err
	}
	target, err := targetFromProviderURL(providerURL, descriptor.PeerID)
	if err != nil {
		return "", err
	}
	return target, nil
}

func fetchProviderDescriptor(ctx context.Context, providerURL string) (*providerDescriptorResponse, error) {
	urlText := strings.TrimSpace(providerURL)
	if urlText == "" {
		return nil, errors.New("provider URL is required when --target is omitted")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, urlText, nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch provider descriptor: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("fetch provider descriptor: %s", resp.Status)
	}
	var descriptor providerDescriptorResponse
	if err := json.NewDecoder(resp.Body).Decode(&descriptor); err != nil {
		return nil, fmt.Errorf("decode provider descriptor: %w", err)
	}
	if strings.TrimSpace(descriptor.PeerID) == "" {
		return nil, errors.New("provider descriptor is missing peerId")
	}
	return &descriptor, nil
}

func targetFromProviderURL(providerURL, peerID string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(providerURL))
	if err != nil {
		return "", err
	}
	host := parsed.Hostname()
	if host == "" {
		return "", errors.New("provider URL is missing host")
	}
	port := parsed.Port()
	transport := "wss"
	if parsed.Scheme == "http" {
		transport = "ws"
		if port == "" {
			port = "80"
		}
	} else if port == "" {
		port = "443"
	}
	hostComponent := "dns4"
	if ip := net.ParseIP(host); ip != nil {
		if ip.To4() != nil {
			hostComponent = "ip4"
		} else {
			hostComponent = "ip6"
		}
	}
	return fmt.Sprintf("/%s/%s/tcp/%s/%s/p2p/%s", hostComponent, host, port, transport, strings.TrimSpace(peerID)), nil
}

func publishModuleRequest(ctx context.Context, target string, req license.ModulePublishRequest) (*license.ModulePublishResponse, error) {
	host, err := libp2p.New(
		libp2p.Transport(tcp.NewTCPTransport),
		libp2p.Transport(websocket.New),
	)
	if err != nil {
		return nil, fmt.Errorf("create libp2p host: %w", err)
	}
	defer host.Close()

	addr, err := multiaddr.NewMultiaddr(target)
	if err != nil {
		return nil, fmt.Errorf("parse target multiaddr: %w", err)
	}
	info, err := peer.AddrInfoFromP2pAddr(addr)
	if err != nil {
		return nil, fmt.Errorf("target must include /p2p/<peer-id>: %w", err)
	}
	host.Peerstore().AddAddrs(info.ID, info.Addrs, peerstore.PermanentAddrTTL)

	stream, err := host.NewStream(ctx, info.ID, libp2pprotocol.ID(license.ModulePublishProtocolID))
	if err != nil {
		return nil, fmt.Errorf("open module publish stream: %w", err)
	}
	defer stream.Close()
	_ = stream.SetDeadline(time.Now().Add(modulePublishTimeout))

	payload, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	if _, err := stream.Write(payload); err != nil {
		return nil, fmt.Errorf("write module publish request: %w", err)
	}
	if closeWriter, ok := any(stream).(interface{ CloseWrite() error }); ok {
		if err := closeWriter.CloseWrite(); err != nil {
			return nil, fmt.Errorf("close module publish request writer: %w", err)
		}
	}

	var response license.ModulePublishResponse
	if err := json.NewDecoder(stream).Decode(&response); err != nil {
		return nil, fmt.Errorf("decode module publish response: %w", err)
	}
	return &response, nil
}

func randomHex(byteCount int) (string, error) {
	if byteCount <= 0 {
		return "", errors.New("byte count must be positive")
	}
	buf := make([]byte, byteCount)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}
