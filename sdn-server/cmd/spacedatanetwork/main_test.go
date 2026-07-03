package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/EPM"
	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/crypto"
	libp2phost "github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/multiformats/go-multiaddr"

	"github.com/spacedatanetwork/sdn-server/internal/api"
	"github.com/spacedatanetwork/sdn-server/internal/auth"
	"github.com/spacedatanetwork/sdn-server/internal/bundle"
	"github.com/spacedatanetwork/sdn-server/internal/channels"
	"github.com/spacedatanetwork/sdn-server/internal/config"
	"github.com/spacedatanetwork/sdn-server/internal/epm"
	"github.com/spacedatanetwork/sdn-server/internal/flatsqldrv"
	"github.com/spacedatanetwork/sdn-server/internal/keys"
	"github.com/spacedatanetwork/sdn-server/internal/license"
	"github.com/spacedatanetwork/sdn-server/internal/peers"
	"github.com/spacedatanetwork/sdn-server/internal/sds"
	"github.com/spacedatanetwork/sdn-server/internal/wasm"
	"github.com/spacedatanetwork/sdn-server/plugins"
)

func TestIsPublicAPIPathAllowsProviderDescriptorRoute(t *testing.T) {
	t.Parallel()

	if !isPublicAPIPath("/api/module-delivery/provider") {
		t.Fatal("expected provider descriptor route to be public")
	}
}

func TestIsPublicAPIPathAllowsModuleDeliveryListingsRoute(t *testing.T) {
	t.Parallel()

	if !isPublicAPIPath("/api/module-delivery/listings") {
		t.Fatal("expected module-delivery listings route to be public")
	}
}

func TestIsPublicAPIPathAllowsDirectoryRoutes(t *testing.T) {
	t.Parallel()

	if !isPublicAPIPath("/api/directory/nodes") {
		t.Fatal("expected directory nodes route to be public")
	}
	if !isPublicAPIPath("/api/directory/users") {
		t.Fatal("expected directory users route to be public")
	}
}

func TestNodeSecurityPublicAPIRequestPolicy(t *testing.T) {
	t.Parallel()

	tests := []struct {
		method string
		path   string
		public bool
	}{
		{http.MethodGet, "/api/node/info", true},
		{http.MethodGet, "/api/module-delivery/provider", true},
		{http.MethodGet, "/api/module-delivery/listings", true},
		{http.MethodGet, "/api/directory/nodes", true},
		{http.MethodPost, "/api/auth/challenge", true},
		{http.MethodPost, "/api/auth/verify", true},
		{http.MethodGet, "/api/auth/status", true},
		{http.MethodGet, "/api/storefront/listings", true},
		{http.MethodGet, "/api/storefront/listings/example", true},
		{http.MethodGet, "/api/storefront/listings/example/reviews", true},
		{http.MethodPost, "/api/storefront/listings/search", true},
		{http.MethodPost, "/api/storefront/payments/stripe/webhook", true},
		{http.MethodGet, "/api/v1/data/omm/bulk", true},
		{http.MethodGet, "/api/v1/data/secure/omm", true},
		{http.MethodGet, "/api/v1/channels", true},
		{http.MethodGet, "/api/v1/channels/spaceaware-OMM", true},
		{http.MethodGet, "/api/v1/channels/spaceaware-OMM/monitor", true},
		{http.MethodGet, "/api/v1/channels/spaceaware-OMM/pnm", true},
		{http.MethodHead, "/api/v1/channels/spaceaware-OMM/monitor", true},

		{http.MethodGet, "/api/v1/data/summary", false},
		{http.MethodPost, "/api/v1/data/query", false},
		{http.MethodPost, "/api/v1/search/providers", false},
		{http.MethodPost, "/api/v1/search/data", false},
		{http.MethodPost, "/api/v1/conjunction/screen", false},
		{http.MethodGet, "/api/v1/data/records/EPM.fbs/12D3KooW", false},
		{http.MethodPost, "/api/v1/channels/spaceaware-OMM/subscribe", false},
		{http.MethodPost, "/api/v1/channels/spaceaware-OMM/publish", false},
		{http.MethodPost, "/api/v1/channels/spaceaware-OMM/grants", false},
		{http.MethodPost, "/api/storefront/listings", false},
		{http.MethodPatch, "/api/storefront/listings/example", false},
		{http.MethodDelete, "/api/storefront/listings/example", false},
		{http.MethodGet, "/api/auth/users", false},
		{http.MethodPut, "/api/auth/users/xpub-admin", false},
		{http.MethodGet, "/api/auth/me", false},
		{http.MethodPost, "/api/auth/logout", false},
		{http.MethodPost, "/api/v0/id", false},
		{http.MethodPost, "/api/v0/pin/add", false},
		{http.MethodPost, "/api/v1/data/publish/OMM.fbs", false},
		{http.MethodPost, "/api/v1/data/publish/batch/OMM.fbs", false},
		{http.MethodPost, "/api/v1/modules/runtime/celestrak-provider/schedules/full/run", false},
		{http.MethodPost, "/api/v1/admin/dataset-updates/publish", false},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			t.Parallel()
			if got := isPublicAPIRequest(tc.method, tc.path); got != tc.public {
				t.Fatalf("isPublicAPIRequest(%q, %q) = %v, want %v", tc.method, tc.path, got, tc.public)
			}
		})
	}
}

func TestNodeSecurityAdminOnlyAPIPathPolicy(t *testing.T) {
	t.Parallel()

	adminPaths := []string{
		"/api/auth/users",
		"/api/auth/users/xpub-admin",
		"/api/v0/id",
		"/api/v0/pin/add",
		"/api/v1/data/summary",
		"/api/v1/data/query",
		"/api/v1/search/providers",
		"/api/v1/search/data",
		"/api/v1/conjunction/screen",
		"/api/v1/data/records/EPM.fbs/12D3KooW",
		"/api/v1/modules/runtime/celestrak-provider/schedules/full/run",
		"/api/v1/admin/dataset-updates/publish",
		"/api/admin/frontend/files",
		"/api/v1/plugins/upload",
		"/api/routing/config",
		"/api/streaming/sessions",
		"/api/relay/filters",
	}
	for _, path := range adminPaths {
		path := path
		t.Run(path, func(t *testing.T) {
			t.Parallel()
			if !isAdminOnlyAPIPath(path) {
				t.Fatalf("expected %q to require admin trust", path)
			}
		})
	}

	standardPaths := []string{
		"/api/auth/me",
		"/api/storefront/purchases",
		"/api/v1/data/publish/OMM.fbs",
	}
	for _, path := range standardPaths {
		path := path
		t.Run(path, func(t *testing.T) {
			t.Parallel()
			if isAdminOnlyAPIPath(path) {
				t.Fatalf("expected %q to require authentication without forcing admin trust", path)
			}
		})
	}
}

func TestCountConfiguredSDNSSHHostStanzasCountsDeploymentNodesOncePerHostLine(t *testing.T) {
	t.Parallel()

	configPath := filepath.Join(t.TempDir(), "config")
	if err := os.WriteFile(configPath, []byte(`
Host space-data-network-01 sdn.spaceaware.io
    HostName 159.203.150.8
    User root

Host space-data-network-02 celestrak.eth
    HostName 167.172.219.213
    User root

Host github.com
    HostName github.com

Host *.example.invalid
    HostName ignored.example.invalid
`), 0o600); err != nil {
		t.Fatalf("write ssh config: %v", err)
	}

	if got, want := countConfiguredSDNSSHHostStanzas(configPath), 2; got != want {
		t.Fatalf("countConfiguredSDNSSHHostStanzas() = %d, want %d", got, want)
	}
}

func TestCountConfiguredSDNSSHHostStanzasMissingFileIsZero(t *testing.T) {
	t.Parallel()

	if got := countConfiguredSDNSSHHostStanzas(filepath.Join(t.TempDir(), "missing")); got != 0 {
		t.Fatalf("countConfiguredSDNSSHHostStanzas(missing) = %d, want 0", got)
	}
}

func TestApplyPublicAPICORSHeadersUsesRequestOrigin(t *testing.T) {
	t.Parallel()

	header := http.Header{}
	applyPublicAPICORSHeaders(header, "https://spaceaware.io")

	if got := header.Get("Access-Control-Allow-Origin"); got != "https://spaceaware.io" {
		t.Fatalf("Access-Control-Allow-Origin = %q", got)
	}
	if got := header.Get("Access-Control-Allow-Methods"); got != "GET, POST, PUT, PATCH, DELETE, OPTIONS" {
		t.Fatalf("Access-Control-Allow-Methods = %q", got)
	}
	if got := header.Get("Vary"); got != "Origin" {
		t.Fatalf("Vary = %q", got)
	}
}

func TestApplyPublicAPICORSHeadersFallsBackToWildcard(t *testing.T) {
	t.Parallel()

	header := http.Header{}
	applyPublicAPICORSHeaders(header, "")

	if got := header.Get("Access-Control-Allow-Origin"); got != "*" {
		t.Fatalf("Access-Control-Allow-Origin = %q", got)
	}
}

func TestResolveHDWalletWasmPathUsesBundleLayout(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	walletPath := filepath.Join(root, "runtime", "modules", "hd-wallet-wasi.wasm")
	if err := os.MkdirAll(filepath.Dir(walletPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(walletPath, []byte("\x00asm"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := resolveHDWalletWasmPathFromInputs("", "", bundle.Layout{HDWalletWASM: walletPath}, nil)
	if err != nil {
		t.Fatalf("resolveHDWalletWasmPathFromInputs failed: %v", err)
	}
	if got != walletPath {
		t.Fatalf("wallet path = %q, want %q", got, walletPath)
	}
}

func TestEnsureNodeMnemonicCreatesEncryptedMnemonic(t *testing.T) {
	t.Parallel()

	cfg := config.Default()
	cfg.Storage.Path = filepath.Join(t.TempDir(), "data")

	result, err := ensureNodeMnemonic(context.Background(), cfg, func(context.Context) (string, error) {
		return "abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon about", nil
	})
	if err != nil {
		t.Fatalf("ensureNodeMnemonic failed: %v", err)
	}
	if !result.Created {
		t.Fatal("Created = false, want true")
	}
	if result.Path != filepath.Join(filepath.Dir(cfg.Storage.Path), "keys", "mnemonic") {
		t.Fatalf("Path = %q", result.Path)
	}
	data, err := os.ReadFile(result.Path)
	if err != nil {
		t.Fatalf("read mnemonic: %v", err)
	}
	if !keys.IsMnemonicEncrypted(data) {
		t.Fatalf("mnemonic file is not encrypted: %q", string(data))
	}
}

func TestEnsureNodeMnemonicPreservesExistingMnemonic(t *testing.T) {
	t.Parallel()

	cfg := config.Default()
	cfg.Storage.Path = filepath.Join(t.TempDir(), "data")
	mnemonicPath := filepath.Join(filepath.Dir(cfg.Storage.Path), "keys", "mnemonic")
	if err := os.MkdirAll(filepath.Dir(mnemonicPath), 0o700); err != nil {
		t.Fatal(err)
	}
	existing := []byte("existing encrypted mnemonic bytes")
	if err := os.WriteFile(mnemonicPath, existing, 0o600); err != nil {
		t.Fatal(err)
	}

	called := false
	result, err := ensureNodeMnemonic(context.Background(), cfg, func(context.Context) (string, error) {
		called = true
		return "abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon about", nil
	})
	if err != nil {
		t.Fatalf("ensureNodeMnemonic failed: %v", err)
	}
	if result.Created {
		t.Fatal("Created = true, want false")
	}
	if called {
		t.Fatal("generator was called for an existing mnemonic")
	}
	data, err := os.ReadFile(mnemonicPath)
	if err != nil {
		t.Fatalf("read mnemonic: %v", err)
	}
	if !bytes.Equal(data, existing) {
		t.Fatalf("mnemonic was overwritten: %q", string(data))
	}
}

func TestIPFSGatewayPathDoesNotUsePublicAPICORS(t *testing.T) {
	t.Parallel()

	if isPublicAPIPath("/ipfs/QmExample") {
		t.Fatal("/ipfs gateway responses must use only the normalized gateway CORS headers")
	}
}

func TestNodeEPMRoutesArePublicReadAPIPaths(t *testing.T) {
	t.Parallel()

	for _, path := range []string{"/api/node/epm", "/api/node/epm/json", "/api/node/epm/vcard", "/api/node/epm/qr"} {
		if !isPublicAPIRequest(http.MethodGet, path) {
			t.Fatalf("expected %q to be a public EPM read path", path)
		}
	}
}

func TestExportIdentityTextUsesNodeVCardEndpoint(t *testing.T) {
	t.Parallel()

	server := newIdentityExportTestServer(t)
	var out bytes.Buffer
	if err := exportIdentity(context.Background(), &out, server.URL, "text"); err != nil {
		t.Fatalf("exportIdentity failed: %v", err)
	}
	if got := out.String(); !strings.Contains(got, "BEGIN:VCARD") || !strings.Contains(got, "X-SDN-PEER-ID:12D3KooWExport") {
		t.Fatalf("text export = %q", got)
	}
}

func TestExportIdentityJSONUsesNodeEPMJSONEndpoint(t *testing.T) {
	t.Parallel()

	server := newIdentityExportTestServer(t)
	var out bytes.Buffer
	if err := exportIdentity(context.Background(), &out, server.URL, "json"); err != nil {
		t.Fatalf("exportIdentity failed: %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal(out.Bytes(), &payload); err != nil {
		t.Fatalf("json export is invalid JSON: %v", err)
	}
	if payload["peer_id"] != "12D3KooWExport" || payload["bitcoin_address"] != "bc1qexport" {
		t.Fatalf("json export = %#v", payload)
	}
}

func TestExportIdentityCSVUsesCommonEPMFields(t *testing.T) {
	t.Parallel()

	server := newIdentityExportTestServer(t)
	var out bytes.Buffer
	if err := exportIdentity(context.Background(), &out, server.URL, "csv"); err != nil {
		t.Fatalf("exportIdentity failed: %v", err)
	}
	records, err := csv.NewReader(strings.NewReader(out.String())).ReadAll()
	if err != nil {
		t.Fatalf("csv export is invalid CSV: %v", err)
	}
	if len(records) != 2 {
		t.Fatalf("csv records len = %d, want 2: %#v", len(records), records)
	}
	if records[0][0] != "peer_id" || records[1][0] != "12D3KooWExport" {
		t.Fatalf("csv export = %#v", records)
	}
	if records[0][2] != "legal_name" || records[1][2] != "Space Data Network Export" {
		t.Fatalf("csv export = %#v", records)
	}
}

func TestExportIdentityQRCodeUsesNodeVCardPayload(t *testing.T) {
	t.Parallel()

	server := newIdentityExportTestServer(t)
	var out bytes.Buffer
	if err := exportIdentity(context.Background(), &out, server.URL, "qrcode"); err != nil {
		t.Fatalf("exportIdentity failed: %v", err)
	}
	if out.Len() < 100 {
		t.Fatalf("qrcode export too small: %q", out.String())
	}
	if strings.Contains(out.String(), "BEGIN:VCARD") {
		t.Fatalf("qrcode export printed raw vCard: %q", out.String())
	}
}

func TestExportIdentityFlatBufferUsesNodeEPMEndpoint(t *testing.T) {
	t.Parallel()

	epmBytes := sds.NewEPMBuilder().WithDN("FlatBuffer Export").Build()
	if !EPM.SizePrefixedEPMBufferHasIdentifier(epmBytes) {
		t.Fatal("test EPM bytes are invalid")
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/api/node/epm", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-flatbuffers")
		_, _ = w.Write(epmBytes)
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	var out bytes.Buffer
	if err := exportIdentity(context.Background(), &out, server.URL, "flatbuffer"); err != nil {
		t.Fatalf("exportIdentity failed: %v", err)
	}
	if !bytes.Equal(out.Bytes(), epmBytes) {
		t.Fatalf("flatbuffer export bytes differ: got %x want %x", out.Bytes(), epmBytes)
	}
}

func TestNormalizeIPFSGatewayCORSHeadersCollapsesDuplicateValues(t *testing.T) {
	t.Parallel()

	header := http.Header{}
	header.Add("Access-Control-Allow-Origin", "*")
	header.Add("Access-Control-Allow-Origin", "*")
	header.Add("Access-Control-Allow-Methods", "GET, HEAD, OPTIONS")
	header.Add("Access-Control-Allow-Methods", "GET, HEAD, OPTIONS")
	header.Add("Access-Control-Allow-Headers", "Content-Type, Range, User-Agent, X-Requested-With")
	header.Add("Access-Control-Allow-Headers", "Content-Type, Range, User-Agent, X-Requested-With")
	header.Add("Access-Control-Expose-Headers", "Content-Length, Content-Range, X-Chunked-Output")
	header.Add("Access-Control-Expose-Headers", "Content-Length, Content-Range")

	normalizeIPFSGatewayCORSHeaders(header)

	if got := header.Values("Access-Control-Allow-Origin"); len(got) != 1 || got[0] != "*" {
		t.Fatalf("Access-Control-Allow-Origin values = %#v, want one wildcard", got)
	}
	if got := header.Values("Access-Control-Allow-Methods"); len(got) != 1 || got[0] != "GET, HEAD, OPTIONS" {
		t.Fatalf("Access-Control-Allow-Methods values = %#v", got)
	}
	if got := header.Values("Access-Control-Allow-Headers"); len(got) != 1 || got[0] != "Content-Type, Range, User-Agent, X-Requested-With" {
		t.Fatalf("Access-Control-Allow-Headers values = %#v", got)
	}
	if got := header.Values("Access-Control-Expose-Headers"); len(got) != 1 || got[0] != "Content-Length, Content-Range, X-Chunked-Output, X-Ipfs-Path, X-Ipfs-Roots, X-Stream-Output" {
		t.Fatalf("Access-Control-Expose-Headers values = %#v", got)
	}
}

func newIdentityExportTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/api/node/epm/vcard", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/vcard")
		_, _ = w.Write([]byte("BEGIN:VCARD\nVERSION:4.0\nFN:Export Node\nX-SDN-PEER-ID:12D3KooWExport\nEND:VCARD\n"))
	})
	mux.HandleFunc("/api/node/epm/json", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"peer_id":"12D3KooWExport","dn":"Export Node","legal_name":"Space Data Network Export","bitcoin_address":"bc1qexport","xpub":"xpub-export","signing_pubkey_hex":"signing","encryption_pubkey_hex":"encryption"}`))
	})
	return httptest.NewServer(mux)
}

func TestHandleProviderDescriptorReturnsBrowserSafeDescriptor(t *testing.T) {
	t.Parallel()

	privKey, _, err := crypto.GenerateSecp256k1Key(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateSecp256k1Key failed: %v", err)
	}

	host, err := libp2p.New(libp2p.NoListenAddrs, libp2p.Identity(privKey))
	if err != nil {
		t.Fatalf("libp2p.New failed: %v", err)
	}
	defer host.Close()

	addr, err := multiaddr.NewMultiaddr("/dns4/relay.example.com/tcp/443/wss")
	if err != nil {
		t.Fatalf("NewMultiaddr failed: %v", err)
	}

	request := httptest.NewRequest(http.MethodGet, "/api/module-delivery/provider", nil)
	recorder := httptest.NewRecorder()

	handleProviderDescriptor(fakeProviderDescriptorSource{
		host:  host,
		peer:  host.ID(),
		addrs: []multiaddr.Multiaddr{addr},
	})(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", recorder.Code)
	}
	if got := recorder.Header().Get("Access-Control-Allow-Origin"); got != "*" {
		t.Fatalf("Access-Control-Allow-Origin = %q", got)
	}

	var payload struct {
		PublicKey      string   `json:"publicKey"`
		PeerID         string   `json:"peerId"`
		IPNS           string   `json:"ipns"`
		RelayAddresses []string `json:"relayAddresses"`
		Identity       struct {
			IdentityPublicKey string   `json:"identityPublicKey"`
			XPub              string   `json:"xpub"`
			IPNSEntries       []string `json:"ipnsEntries"`
			Addresses         []struct {
				Chain   string `json:"chain"`
				Address string `json:"address"`
				KeyPath string `json:"keyPath"`
			} `json:"addresses"`
			ENSNames []string `json:"ensNames"`
		} `json:"identity"`
	}
	if err := json.NewDecoder(recorder.Body).Decode(&payload); err != nil {
		t.Fatalf("json decode failed: %v", err)
	}

	pubKey, err := host.ID().ExtractPublicKey()
	if err != nil {
		t.Fatalf("ExtractPublicKey failed: %v", err)
	}
	rawPubKey, err := pubKey.Raw()
	if err != nil {
		t.Fatalf("Raw failed: %v", err)
	}

	if got, want := payload.PublicKey, hex.EncodeToString(rawPubKey); got != want {
		t.Fatalf("publicKey = %q, want %q", got, want)
	}
	if got, want := payload.PeerID, host.ID().String(); got != want {
		t.Fatalf("peerId = %q, want %q", got, want)
	}
	if got, want := payload.IPNS, "/ipns/"+host.ID().String(); got != want {
		t.Fatalf("ipns = %q, want %q", got, want)
	}
	if len(payload.RelayAddresses) != 1 || payload.RelayAddresses[0] != addr.String() {
		t.Fatalf("relayAddresses = %#v", payload.RelayAddresses)
	}
	if got, want := payload.Identity.IdentityPublicKey, hex.EncodeToString(rawPubKey); got != want {
		t.Fatalf("identity.identityPublicKey = %q, want %q", got, want)
	}
	if got, want := payload.Identity.IPNSEntries, []string{"/ipns/" + host.ID().String()}; len(got) != len(want) || got[0] != want[0] {
		t.Fatalf("identity.ipnsEntries = %#v, want %#v", got, want)
	}
	if len(payload.Identity.Addresses) != 0 {
		t.Fatalf("identity.addresses = %#v, want empty for source without published wallet identity", payload.Identity.Addresses)
	}
}

func TestHandleModuleDeliveryListingsReturnsCanonicalPlgListings(t *testing.T) {
	t.Parallel()

	reg := writeMainTestPluginRegistry(
		t,
		license.PluginCatalogEntry{
			ID:            "licensing",
			Version:       "0.1.0",
			RequiredScope: "orbpro:runtime",
			EncryptedPath: "licensing.wasm.enc",
			KeyPath:       "licensing.key",
			ContentType:   "application/wasm+encrypted",
		},
		license.PluginCatalogEntry{
			ID:            "com.orbpro.sgp4",
			Version:       "1.0.0",
			RequiredScope: "orbpro:base",
			EncryptedPath: "sgp4.wasm.enc",
			KeyPath:       "sgp4.key",
			ContentType:   "application/wasm+encrypted",
		},
	)

	request := httptest.NewRequest(http.MethodGet, "/api/module-delivery/listings", nil)
	recorder := httptest.NewRecorder()

	handleModuleDeliveryListings(reg)(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", recorder.Code)
	}

	var payload struct {
		Results []struct {
			DataBase64 string `json:"data_base64"`
		} `json:"results"`
	}
	if err := json.NewDecoder(recorder.Body).Decode(&payload); err != nil {
		t.Fatalf("json decode failed: %v", err)
	}
	if len(payload.Results) != 2 {
		t.Fatalf("results len = %d, want 2", len(payload.Results))
	}
	if payload.Results[0].DataBase64 == "" || payload.Results[1].DataBase64 == "" {
		t.Fatalf("results = %#v, want base64-encoded PLG bytes", payload.Results)
	}
}

func TestHandleModuleRuntimeSnapshotMergesCatalogOnlyModules(t *testing.T) {
	t.Parallel()

	reg := writeMainTestPluginRegistry(
		t,
		license.PluginCatalogEntry{
			ID:            "com.space-data-network.analysis",
			Version:       "1.0.0",
			RequiredScope: "orbpro:base",
			EncryptedPath: "analysis.wasm.enc",
			KeyPath:       "analysis.key",
			ContentType:   "application/wasm+encrypted",
		},
	)

	request := httptest.NewRequest(http.MethodGet, "/api/v1/modules/runtime", nil)
	recorder := httptest.NewRecorder()

	handleModuleRuntimeSnapshot(plugins.New(), reg)(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", recorder.Code)
	}

	var payload struct {
		Count   int `json:"count"`
		Modules []struct {
			ID      string `json:"id"`
			Version string `json:"version"`
			Status  string `json:"status"`
			Catalog struct {
				RequiredScope string `json:"requiredScope"`
				ContentType   string `json:"contentType"`
			} `json:"catalog"`
		} `json:"modules"`
	}
	if err := json.NewDecoder(recorder.Body).Decode(&payload); err != nil {
		t.Fatalf("json decode failed: %v", err)
	}
	if payload.Count != 1 || len(payload.Modules) != 1 {
		t.Fatalf("payload count/modules = %d/%d, want 1/1", payload.Count, len(payload.Modules))
	}
	if got, want := payload.Modules[0].ID, "com.space-data-network.analysis"; got != want {
		t.Fatalf("module id = %q, want %q", got, want)
	}
	if got, want := payload.Modules[0].Catalog.RequiredScope, "orbpro:base"; got != want {
		t.Fatalf("required scope = %q, want %q", got, want)
	}
}

func TestHandleModuleRuntimeMutationUpdatesOptionsAndRunsActions(t *testing.T) {
	t.Parallel()

	mgr := plugins.New()
	plugin := &runtimeMutationTestPlugin{
		id: "licensing",
		descriptor: plugins.RuntimeModuleDescriptor{
			Manifest: &plugins.RuntimeModuleManifest{
				PluginID: "licensing",
				Timers: []plugins.RuntimeModuleTimer{
					{
						TimerID:           "refresh-grants",
						MethodID:          "refresh_grants",
						DefaultIntervalMs: 30000,
					},
				},
			},
		},
	}
	if err := mgr.Register(plugin); err != nil {
		t.Fatalf("Register failed: %v", err)
	}
	if err := mgr.StartAll(context.Background(), plugins.RuntimeContext{Mode: "test"}); err != nil {
		t.Fatalf("StartAll failed: %v", err)
	}

	optionReq := httptest.NewRequest(
		http.MethodPatch,
		"/api/v1/modules/runtime/licensing/options/timer.refresh-grants.interval",
		bytes.NewBufferString(`{"value":"45000"}`),
	)
	optionRecorder := httptest.NewRecorder()
	handleModuleRuntimeMutation(mgr)(optionRecorder, optionReq)
	if optionRecorder.Code != http.StatusOK {
		t.Fatalf("option status = %d, body = %s", optionRecorder.Code, optionRecorder.Body.String())
	}
	var optionPayload struct {
		Key         string `json:"key"`
		Value       string `json:"value"`
		Persistence string `json:"persistence"`
	}
	if err := json.NewDecoder(optionRecorder.Body).Decode(&optionPayload); err != nil {
		t.Fatalf("decode option payload: %v", err)
	}
	if optionPayload.Key != "timer.refresh-grants.interval" || optionPayload.Value != "45000" || optionPayload.Persistence != "persisted" {
		t.Fatalf("option payload = %#v", optionPayload)
	}

	mgr.RunRuntimeModuleAction(context.Background(), "licensing", "stop")
	actionReq := httptest.NewRequest(http.MethodPost, "/api/v1/modules/runtime/licensing/actions/clear-error", nil)
	actionRecorder := httptest.NewRecorder()
	handleModuleRuntimeMutation(mgr)(actionRecorder, actionReq)
	if actionRecorder.Code != http.StatusOK {
		t.Fatalf("action status = %d, body = %s", actionRecorder.Code, actionRecorder.Body.String())
	}
}

func TestHandleModuleRuntimeMutationSavesInputsAndReturnsHistory(t *testing.T) {
	t.Parallel()

	mgr := plugins.New()
	plugin := &runtimeMutationTestPlugin{
		id: "licensing",
		descriptor: plugins.RuntimeModuleDescriptor{
			Manifest: &plugins.RuntimeModuleManifest{
				PluginID: "licensing",
				Methods: []plugins.RuntimeModuleMethod{
					{
						MethodID: "server_configure_runtime",
						InputPorts: []plugins.RuntimeModulePort{
							{
								PortID: "request",
							},
						},
					},
				},
			},
		},
	}
	if err := mgr.Register(plugin); err != nil {
		t.Fatalf("Register failed: %v", err)
	}
	if err := mgr.StartAll(context.Background(), plugins.RuntimeContext{Mode: "test"}); err != nil {
		t.Fatalf("StartAll failed: %v", err)
	}

	inputReq := httptest.NewRequest(
		http.MethodPatch,
		"/api/v1/modules/runtime/licensing/inputs",
		bytes.NewBufferString(`{"values":[{"methodId":"server_configure_runtime","portId":"request","wireFormat":"FLATBUFFER_JSON","encoding":"json","schemaName":"MODULE.fbs","rootType":"ConfigureRuntimeRequest","value":"{\"refreshIntervalMs\":45000}"}]}`),
	)
	inputRecorder := httptest.NewRecorder()
	handleModuleRuntimeMutation(mgr)(inputRecorder, inputReq)
	if inputRecorder.Code != http.StatusOK {
		t.Fatalf("input status = %d, body = %s", inputRecorder.Code, inputRecorder.Body.String())
	}
	var inputPayload struct {
		ModuleID       string                            `json:"moduleId"`
		RestartPending bool                              `json:"restartPending"`
		InputValues    []plugins.RuntimeModuleInputValue `json:"inputValues"`
	}
	if err := json.NewDecoder(inputRecorder.Body).Decode(&inputPayload); err != nil {
		t.Fatalf("decode input payload: %v", err)
	}
	if inputPayload.ModuleID != "licensing" || !inputPayload.RestartPending || len(inputPayload.InputValues) != 1 {
		t.Fatalf("input payload = %#v", inputPayload)
	}

	historyReq := httptest.NewRequest(http.MethodGet, "/api/v1/modules/runtime/licensing/history", nil)
	historyRecorder := httptest.NewRecorder()
	handleModuleRuntimeMutation(mgr)(historyRecorder, historyReq)
	if historyRecorder.Code != http.StatusOK {
		t.Fatalf("history status = %d, body = %s", historyRecorder.Code, historyRecorder.Body.String())
	}
	var historyPayload struct {
		ModuleID string                                     `json:"moduleId"`
		History  []plugins.RuntimeModuleCommandHistoryEntry `json:"history"`
	}
	if err := json.NewDecoder(historyRecorder.Body).Decode(&historyPayload); err != nil {
		t.Fatalf("decode history payload: %v", err)
	}
	if historyPayload.ModuleID != "licensing" || len(historyPayload.History) != 1 || historyPayload.History[0].Command != "save-inputs" {
		t.Fatalf("history payload = %#v", historyPayload)
	}
}

func TestHandleModuleRuntimeMutationSavesAndRunsSchedule(t *testing.T) {
	t.Parallel()

	mgr := plugins.New()
	plugin := &runtimeMutationTestPlugin{id: "celestrak-provider"}
	if err := mgr.Register(plugin); err != nil {
		t.Fatalf("Register failed: %v", err)
	}
	if err := mgr.StartAll(context.Background(), plugins.RuntimeContext{Mode: "test"}); err != nil {
		t.Fatalf("StartAll failed: %v", err)
	}

	scheduleReq := httptest.NewRequest(
		http.MethodPatch,
		"/api/v1/modules/runtime/celestrak-provider/schedules/sync_full_catalog",
		bytes.NewBufferString(`{"enabled":true,"interval":"45m","timezone":"UTC"}`),
	)
	scheduleRecorder := httptest.NewRecorder()
	handleModuleRuntimeMutation(mgr)(scheduleRecorder, scheduleReq)
	if scheduleRecorder.Code != http.StatusBadRequest {
		t.Fatalf("short cadence status = %d, body = %s, want 400", scheduleRecorder.Code, scheduleRecorder.Body.String())
	}

	scheduleReq = httptest.NewRequest(
		http.MethodPatch,
		"/api/v1/modules/runtime/celestrak-provider/schedules/sync_full_catalog",
		bytes.NewBufferString(`{"enabled":true,"interval":"3h","cronExpression":"0 */3 * * *","timezone":"UTC","retryBudget":2,"maxRuntime":"30m"}`),
	)
	scheduleRecorder = httptest.NewRecorder()
	handleModuleRuntimeMutation(mgr)(scheduleRecorder, scheduleReq)
	if scheduleRecorder.Code != http.StatusOK {
		t.Fatalf("schedule status = %d, body = %s", scheduleRecorder.Code, scheduleRecorder.Body.String())
	}
	var schedulePayload plugins.RuntimeModuleSchedule
	if err := json.NewDecoder(scheduleRecorder.Body).Decode(&schedulePayload); err != nil {
		t.Fatalf("decode schedule payload: %v", err)
	}
	if schedulePayload.MethodID != "sync_full_catalog" || schedulePayload.Interval != "3h0m0s" || schedulePayload.MinInterval != "3h0m0s" {
		t.Fatalf("schedule payload = %#v", schedulePayload)
	}

	runReq := httptest.NewRequest(http.MethodPost, "/api/v1/modules/runtime/celestrak-provider/schedules/sync_full_catalog/run", nil)
	runRecorder := httptest.NewRecorder()
	handleModuleRuntimeMutation(mgr)(runRecorder, runReq)
	if runRecorder.Code != http.StatusOK {
		t.Fatalf("run status = %d, body = %s", runRecorder.Code, runRecorder.Body.String())
	}
	var runPayload plugins.RuntimeModuleScheduleRun
	if err := json.NewDecoder(runRecorder.Body).Decode(&runPayload); err != nil {
		t.Fatalf("decode run payload: %v", err)
	}
	if runPayload.MethodID != "sync_full_catalog" || runPayload.Trigger != "manual" || runPayload.Status != "ok" {
		t.Fatalf("run payload = %#v", runPayload)
	}
}

func TestMakeWebUIHandlerServesIndexAndAssetsUnderWebUI(t *testing.T) {
	t.Parallel()

	buildDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(buildDir, "index.html"), []byte("<!doctype html><html><body>webui</body></html>"), 0o644); err != nil {
		t.Fatalf("write index.html failed: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(buildDir, "assets"), 0o755); err != nil {
		t.Fatalf("mkdir assets failed: %v", err)
	}
	if err := os.WriteFile(filepath.Join(buildDir, "assets", "main.js"), []byte("console.log('ok')"), 0o644); err != nil {
		t.Fatalf("write asset failed: %v", err)
	}

	handler, err := makeWebUIHandler(buildDir, "/webui")
	if err != nil {
		t.Fatalf("makeWebUIHandler failed: %v", err)
	}

	t.Run("serves index at mount root", func(t *testing.T) {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodGet, "/webui/", nil)

		http.StripPrefix("/webui", handler).ServeHTTP(recorder, request)

		if recorder.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", recorder.Code)
		}
		if got := recorder.Body.String(); got != "<!doctype html><html><body>webui</body></html>" {
			t.Fatalf("body = %q, want index.html contents", got)
		}
	})

	t.Run("serves static assets", func(t *testing.T) {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodGet, "/webui/assets/main.js", nil)

		http.StripPrefix("/webui", handler).ServeHTTP(recorder, request)

		if recorder.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", recorder.Code)
		}
		if got := recorder.Body.String(); got != "console.log('ok')" {
			t.Fatalf("body = %q, want asset contents", got)
		}
	})
}

func TestDefaultFrontendHTMLIsCleanLandingPage(t *testing.T) {
	t.Parallel()

	if !bytes.Contains([]byte(defaultFrontendHTML), []byte(">Space Data Network<")) {
		t.Fatal("default frontend should present Space Data Network as the primary page title")
	}
	if !bytes.Contains([]byte(defaultFrontendHTML), []byte(`href="/admin/"`)) {
		t.Fatal("default frontend should link to the admin page")
	}
	if !bytes.Contains([]byte(defaultFrontendHTML), []byte(`href="https://spacedatanetwork.org"`)) {
		t.Fatal("default frontend should link to spacedatanetwork.org documentation")
	}
	if !bytes.Contains([]byte(defaultFrontendHTML), []byte(`class="landing-card"`)) {
		t.Fatal("default frontend should use a single simple landing content block")
	}
	if !bytes.Contains([]byte(defaultFrontendHTML), []byte(`data:image/svg+xml`)) {
		t.Fatal("default frontend should include an inline static globe background image")
	}
	if bytes.Contains([]byte(defaultFrontendHTML), []byte(`class="orbit"`)) {
		t.Fatal("default frontend should not include extra decorative orbit markup")
	}
	if bytes.Contains([]byte(defaultFrontendHTML), []byte("/api/v1/data/")) {
		t.Fatal("default frontend should not expose API sample links")
	}
}

func TestDesktopIntroMatchesDefaultFrontendHTML(t *testing.T) {
	t.Parallel()

	desktopIntro, err := os.ReadFile(filepath.Join("..", "..", "..", "desktop", "assets", "pages", "sdn-intro.html"))
	if err != nil {
		t.Fatalf("read desktop intro failed: %v", err)
	}
	if !bytes.Equal(bytes.TrimSpace(desktopIntro), bytes.TrimSpace([]byte(defaultFrontendHTML))) {
		t.Fatal("desktop intro page must match the server default frontend exactly")
	}
}

func TestPublicHomepageFileIgnoresDeprecatedHomepageWhenFrontendPathIsSet(t *testing.T) {
	t.Parallel()

	if got := publicHomepageFile("/var/lib/spacedatanetwork/frontend", "/opt/spacedatanetwork/spaceaware/index.html"); got != "" {
		t.Fatalf("public homepage file = %q, want embedded default landing page", got)
	}
}

func TestDefaultFrontendCandidatesPreferBuiltSdnUIBeforeManagedFrontend(t *testing.T) {
	t.Parallel()

	candidates := defaultFrontendCandidates()
	sdnUIIndex := -1
	defaultIndex := -1
	for index, candidate := range candidates {
		if strings.Contains(filepath.ToSlash(candidate), "sdn-js/ui/dist") && sdnUIIndex == -1 {
			sdnUIIndex = index
		}
		if candidate == config.DefaultFrontendPath() {
			defaultIndex = index
		}
	}

	if sdnUIIndex == -1 {
		t.Fatalf("default frontend candidates = %#v, want sdn-js/ui/dist candidate", candidates)
	}
	if defaultIndex == -1 {
		t.Fatalf("default frontend candidates = %#v, want managed default frontend fallback", candidates)
	}
	if sdnUIIndex > defaultIndex {
		t.Fatalf("sdn-js/ui/dist candidate index %d should precede managed fallback index %d", sdnUIIndex, defaultIndex)
	}
}

func TestFirstExistingFrontendPathRequiresIndexHTML(t *testing.T) {
	t.Parallel()

	withoutIndex := t.TempDir()
	withIndex := t.TempDir()
	if err := os.WriteFile(filepath.Join(withIndex, "index.html"), []byte("<!doctype html>"), 0o644); err != nil {
		t.Fatalf("write index.html failed: %v", err)
	}

	if got := firstExistingFrontendPath([]string{withoutIndex, withIndex}); got != withIndex {
		t.Fatalf("first existing frontend path = %q, want %q", got, withIndex)
	}
}

func TestResolveFrontendPathRespectsExplicitConfiguredPath(t *testing.T) {
	t.Parallel()

	if got := resolveFrontendPath("/var/lib/sdn/custom-ui"); got != "/var/lib/sdn/custom-ui" {
		t.Fatalf("resolved frontend path = %q, want explicit configured path", got)
	}
}

func TestUserFacingCLICommandsAreRegistered(t *testing.T) {
	want := []string{"daemon", "init", "status", "open", "update", "sync", "version", "config", "start", "stop", "restart", "remove", "service", "providers", "marketplace", "conjunction"}
	for _, name := range want {
		requireCommand(t, []string{name}, name)
	}
	requireCommand(t, []string{"update", "check"}, "check")
	requireCommand(t, []string{"update", "apply"}, "apply")
	requireCommand(t, []string{"sync", "status"}, "status")
	requireCommand(t, []string{"sync", "watch"}, "watch")
	requireCommand(t, []string{"service", "status"}, "status")
	requireCommand(t, []string{"service", "install"}, "install")
	requireCommand(t, []string{"service", "uninstall"}, "uninstall")
}

func TestMigrationOnlyCommandsAreHiddenFromUserFacingHelp(t *testing.T) {
	cmd, _, err := rootCmd.Find([]string{"import-legacy-sqlite"})
	if err != nil {
		t.Fatalf("find import-legacy-sqlite: %v", err)
	}
	if cmd == nil {
		t.Fatal("import-legacy-sqlite command should remain available for explicit migration use")
	}
	if !cmd.Hidden {
		t.Fatal("import-legacy-sqlite should be hidden from user-facing root help")
	}
	if usage := rootCmd.UsageString(); strings.Contains(usage, "import-legacy-sqlite") {
		t.Fatalf("root help exposes migration-only command:\n%s", usage)
	}
}

func TestChannelHandlerOptionsForIdentityWiresEncryptedStreamDecryptor(t *testing.T) {
	t.Parallel()

	identity := &wasm.DerivedIdentity{
		EncryptionKey: decodeMainHexFixture(t, "b096fac6064d1777e18c58179c10386d11ba04f9fc155bf1888fed9fab2cea7c"),
		EncryptionPub: decodeMainHexFixture(t, "cc177d127ed2a18629a71361b7a5ac1b53eb5a924eb8b6d59f85ede09f1e736e"),
	}
	options := channelHandlerOptionsForIdentity(identity)
	if options.EncryptedStreams == nil {
		t.Fatal("channel handler options did not wire encrypted stream decryptor")
	}

	plaintext, err := options.EncryptedStreams.DecryptNativeStream(api.EncryptedNativeStreamDecryptRequest{
		Channel: channels.ChannelID{
			ChannelID:    "spaceaware-OMM",
			SourceID:     "spaceaware",
			StandardCode: "OMM",
		},
		Header: api.EncryptedNativeStreamHeader{
			Algorithm:       "x25519",
			Context:         "spaceaware-OMM",
			SenderPublicKey: "5f8bfd2b52f392a5bd000509945ac8ff840974f0bab1c918cbec18869f79b75c",
			NonceStart:      "00112233445566778899aabb",
		},
		RecordIndex: 7,
		Ciphertext:  decodeMainHexFixture(t, "bbd30fac58a41b0a11ee4c"),
	})
	if err != nil {
		t.Fatalf("DecryptNativeStream failed: %v", err)
	}
	if got, want := hex.EncodeToString(plaintext), "070000004f4d4d31010203"; got != want {
		t.Fatalf("plaintext = %s, want %s", got, want)
	}
}

func decodeMainHexFixture(t *testing.T, value string) []byte {
	t.Helper()
	decoded, err := hex.DecodeString(value)
	if err != nil {
		t.Fatalf("decode hex fixture: %v", err)
	}
	return decoded
}

func TestAdminURLUsesHTTPSWhenTLSIsEnabled(t *testing.T) {
	cfg := config.Default()
	cfg.Admin.ListenAddr = "127.0.0.1:9443"
	cfg.Admin.TLSEnabled = true

	if got := adminURL(cfg); got != "https://127.0.0.1:9443/" {
		t.Fatalf("adminURL = %q, want https URL", got)
	}
}

func TestAdminURLUsesHTTPWhenTLSIsDisabled(t *testing.T) {
	cfg := config.Default()
	cfg.Admin.ListenAddr = "127.0.0.1:5001"
	cfg.Admin.TLSEnabled = false
	cfg.Admin.TLSMode = "disabled"

	if got := adminURL(cfg); got != "http://127.0.0.1:5001/" {
		t.Fatalf("adminURL = %q, want http URL", got)
	}
}

func TestApplyBundleDefaultsUsesBundledAssetsWhenConfigIsEmpty(t *testing.T) {
	root := t.TempDir()
	layout := bundle.Layout{
		Root:        root,
		KuboBinary:  filepath.Join(root, "runtime", "kubo", "ipfs"),
		SDNUIPath:   filepath.Join(root, "runtime", "ui", "sdn"),
		WebUIPath:   filepath.Join(root, "runtime", "ui", "webui"),
		UpdaterWASM: filepath.Join(root, "runtime", "modules", "org.spacedatanetwork.updater.wasm"),
	}
	if err := os.MkdirAll(layout.SDNUIPath, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(layout.WebUIPath, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.Admin.FrontendPath = ""
	cfg.Admin.WebuiPath = ""
	cfg.Admin.IPFSAPIURL = ""
	cfg.Admin.IPFSGatewayURL = ""

	applyBundleDefaults(cfg, layout)

	if cfg.Admin.FrontendPath != layout.SDNUIPath {
		t.Fatalf("FrontendPath = %q, want %q", cfg.Admin.FrontendPath, layout.SDNUIPath)
	}
	if cfg.Admin.WebuiPath != layout.WebUIPath {
		t.Fatalf("WebuiPath = %q, want %q", cfg.Admin.WebuiPath, layout.WebUIPath)
	}
}

func TestApplyBundleDefaultsPreservesExplicitConfig(t *testing.T) {
	root := t.TempDir()
	layout := bundle.Layout{
		Root:      root,
		SDNUIPath: filepath.Join(root, "runtime", "ui", "sdn"),
		WebUIPath: filepath.Join(root, "runtime", "ui", "webui"),
	}
	if err := os.MkdirAll(layout.SDNUIPath, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(layout.WebUIPath, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.Admin.FrontendPath = "/custom/sdn"
	cfg.Admin.WebuiPath = "/custom/webui"

	applyBundleDefaults(cfg, layout)

	if cfg.Admin.FrontendPath != "/custom/sdn" {
		t.Fatalf("FrontendPath changed to %q", cfg.Admin.FrontendPath)
	}
	if cfg.Admin.WebuiPath != "/custom/webui" {
		t.Fatalf("WebuiPath changed to %q", cfg.Admin.WebuiPath)
	}
}

func requireCommand(t *testing.T, args []string, wantUse string) {
	t.Helper()
	cmd, remaining, err := rootCmd.Find(args)
	if err != nil {
		t.Fatalf("command %v is not registered: %v", args, err)
	}
	if cmd == nil {
		t.Fatalf("command %v resolved to nil", args)
	}
	if cmd.Use != wantUse {
		t.Fatalf("command %v resolved to %q, want %q", args, cmd.Use, wantUse)
	}
	if len(remaining) != 0 {
		t.Fatalf("command %v left remaining args %v", args, remaining)
	}
}

func TestProvisionFrontendDirWritesDefaultIndexInExistingDirectory(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	if err := provisionFrontendDir(dir); err != nil {
		t.Fatalf("provisionFrontendDir failed: %v", err)
	}
	body, err := os.ReadFile(filepath.Join(dir, "index.html"))
	if err != nil {
		t.Fatalf("read provisioned index: %v", err)
	}
	if !bytes.Contains(body, []byte("Space Data Network")) {
		t.Fatalf("provisioned index = %q, want default SDN frontend", string(body))
	}
}

func TestMakeFrontendSurfaceHandlerServesUnauthenticatedRoot(t *testing.T) {
	t.Parallel()

	handler := makeFrontendSurfaceHandler(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("public frontend"))
		}),
		nil,
		true,
	)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if body := rec.Body.String(); body != "public frontend" {
		t.Fatalf("body = %q, want %q", body, "public frontend")
	}
}

func TestMakeFrontendSurfaceHandlerServesFrontendWhenAuthenticated(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	sdb, closer, err := flatsqldrv.OpenStandalone(filepath.Join(dir, "sessions.sdnj"))
	if err != nil {
		t.Fatalf("OpenStandalone: %v", err)
	}
	defer closer()

	sessions, err := auth.NewSessionStore(sdb)
	if err != nil {
		t.Fatalf("NewSessionStore: %v", err)
	}

	token, err := sessions.CreateSession(
		"xpub-standard-user",
		peers.Standard,
		"127.0.0.1",
		"test-agent",
		time.Hour,
	)
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	handler := makeFrontendSurfaceHandler(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("frontend"))
		}),
		auth.NewHandler(nil, sessions, time.Hour, "", ""),
		true,
	)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(&http.Cookie{Name: "sdn_wallet_session", Value: token})
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if body := rec.Body.String(); body != "frontend" {
		t.Fatalf("body = %q, want %q", body, "frontend")
	}
}

func TestMakeFrontendHandlerReloadsIndexAfterBuildChange(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	indexPath := filepath.Join(dir, "index.html")
	firstHTML := []byte(`<!doctype html><html><head><script type="module" src="./assets/index-old.js"></script></head><body><div id="app"></div></body></html>`)
	if err := os.WriteFile(indexPath, firstHTML, 0o644); err != nil {
		t.Fatalf("write initial index.html failed: %v", err)
	}

	handler, err := makeFrontendHandler(dir)
	if err != nil {
		t.Fatalf("makeFrontendHandler failed: %v", err)
	}

	firstReq := httptest.NewRequest(http.MethodGet, "/", nil)
	firstRec := httptest.NewRecorder()
	handler.ServeHTTP(firstRec, firstReq)
	if firstRec.Code != http.StatusOK {
		t.Fatalf("initial status = %d, want 200", firstRec.Code)
	}
	if !bytes.Contains(firstRec.Body.Bytes(), []byte("index-old.js")) {
		t.Fatalf("initial body = %q, want old asset reference", firstRec.Body.String())
	}

	secondHTML := []byte(`<!doctype html><html><head><script type="module" src="./assets/index-new.js"></script></head><body><div id="app"></div></body></html>`)
	if err := os.WriteFile(indexPath, secondHTML, 0o644); err != nil {
		t.Fatalf("write updated index.html failed: %v", err)
	}

	secondReq := httptest.NewRequest(http.MethodGet, "/", nil)
	secondRec := httptest.NewRecorder()
	handler.ServeHTTP(secondRec, secondReq)
	if secondRec.Code != http.StatusOK {
		t.Fatalf("updated status = %d, want 200", secondRec.Code)
	}
	if !bytes.Contains(secondRec.Body.Bytes(), []byte("index-new.js")) {
		t.Fatalf("updated body = %q, want new asset reference after rebuild", secondRec.Body.String())
	}
}

func TestMakeFrontendHandlerDoesNotCacheHTMLEntryPoints(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte(`<!doctype html><html><body><div id="app"></div></body></html>`), 0o644); err != nil {
		t.Fatalf("write index.html failed: %v", err)
	}

	handler, err := makeFrontendHandler(dir)
	if err != nil {
		t.Fatalf("makeFrontendHandler failed: %v", err)
	}

	for _, requestPath := range []string{"/", "/network"} {
		t.Run(requestPath, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, requestPath, nil)
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200", rec.Code)
			}
			if got := rec.Header().Get("Cache-Control"); got != "no-store" {
				t.Fatalf("Cache-Control = %q, want %q", got, "no-store")
			}
		})
	}
}

func TestBuildProviderDescriptorIncludesPublishedIdentityAddresses(t *testing.T) {
	t.Parallel()

	identity, err := testProviderDerivedIdentity()
	if err != nil {
		t.Fatalf("testProviderDerivedIdentity failed: %v", err)
	}

	dataDir := t.TempDir()
	if err := epm.SaveProfile(dataDir, &epm.Profile{
		AlternateNames: []string{"operator.eth"},
	}); err != nil {
		t.Fatalf("SaveProfile failed: %v", err)
	}

	epmService := epm.NewService(identity, peers.NewRegistry(false, nil), identity.PeerID, "xpub-provider", dataDir)
	if err := epmService.Init(); err != nil {
		t.Fatalf("Init failed: %v", err)
	}

	host, err := libp2p.New(libp2p.NoListenAddrs, libp2p.Identity(identity.IdentityPrivKey))
	if err != nil {
		t.Fatalf("libp2p.New failed: %v", err)
	}
	defer host.Close()

	addr, err := multiaddr.NewMultiaddr("/ip4/127.0.0.1/tcp/4001/ws")
	if err != nil {
		t.Fatalf("NewMultiaddr failed: %v", err)
	}

	payload, err := buildProviderDescriptor(fakeProviderDescriptorSource{
		host:       host,
		peer:       host.ID(),
		addrs:      []multiaddr.Multiaddr{addr},
		epmService: epmService,
	})
	if err != nil {
		t.Fatalf("buildProviderDescriptor failed: %v", err)
	}

	if got, want := payload.Identity.XPub, "xpub-provider"; got != want {
		t.Fatalf("identity.xpub = %q, want %q", got, want)
	}
	if got, want := payload.Identity.SigningPublicKey, identity.Info().SigningPubKeyHex; got != want {
		t.Fatalf("identity.signingPublicKey = %q, want %q", got, want)
	}
	if got, want := payload.Identity.EncryptionPublicKey, identity.Info().EncryptionPubHex; got != want {
		t.Fatalf("identity.encryptionPublicKey = %q, want %q", got, want)
	}
	if got, want := payload.Identity.ENSNames, []string{"operator.eth"}; len(got) != len(want) || got[0] != want[0] {
		t.Fatalf("identity.ensNames = %#v, want %#v", got, want)
	}
	if len(payload.Identity.Addresses) != 3 {
		t.Fatalf("identity.addresses len = %d, want 3", len(payload.Identity.Addresses))
	}
	if got, want := payload.Identity.Addresses[0].Chain, "bitcoin"; got != want {
		t.Fatalf("identity.addresses[0].chain = %q, want %q", got, want)
	}
	if got, want := payload.Identity.Addresses[0].Address, "bc1qprovideridentityaddress000000000000000000"; got != want {
		t.Fatalf("identity.addresses[0].address = %q, want %q", got, want)
	}
}

func TestPromoteNodeInfoKeyFieldsPromotesSigningAndEncryptionKeys(t *testing.T) {
	t.Parallel()

	info := map[string]interface{}{
		"keys": []interface{}{
			map[string]interface{}{
				"key_type":    "signing",
				"public_key":  "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIexample",
				"key_address": "sdn@node",
			},
			map[string]interface{}{
				"key_type":   "encryption",
				"public_key": "302a300506032b6570032100feedface",
			},
		},
	}

	promoteNodeInfoKeyFields(info)

	if got, want := info["signing_pubkey_hex"], "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIexample"; got != want {
		t.Fatalf("signing_pubkey_hex = %#v, want %q", got, want)
	}
	if got, want := info["signing_key_path"], "sdn@node"; got != want {
		t.Fatalf("signing_key_path = %#v, want %q", got, want)
	}
	if got, want := info["encryption_pubkey_hex"], "302a300506032b6570032100feedface"; got != want {
		t.Fatalf("encryption_pubkey_hex = %#v, want %q", got, want)
	}
}

func TestStorefrontSigningKeyFromRawAcceptsSeedOrPrivateKey(t *testing.T) {
	t.Parallel()

	seed := bytes.Repeat([]byte{0x51}, ed25519.SeedSize)
	privateKey := ed25519.NewKeyFromSeed(seed)

	fromSeed, err := storefrontSigningKeyFromRaw(seed)
	if err != nil {
		t.Fatalf("storefrontSigningKeyFromRaw(seed) failed: %v", err)
	}
	if !bytes.Equal(fromSeed, privateKey) {
		t.Fatal("storefrontSigningKeyFromRaw(seed) did not expand to the expected private key")
	}

	fromPrivate, err := storefrontSigningKeyFromRaw(privateKey)
	if err != nil {
		t.Fatalf("storefrontSigningKeyFromRaw(privateKey) failed: %v", err)
	}
	if !bytes.Equal(fromPrivate, privateKey) {
		t.Fatal("storefrontSigningKeyFromRaw(privateKey) changed key bytes")
	}

	if _, err := storefrontSigningKeyFromRaw([]byte{1, 2, 3}); err == nil {
		t.Fatal("storefrontSigningKeyFromRaw should reject invalid key lengths")
	}
}

func TestDatasetPublicationSigningKeyCreatesPersistentLegacyIdentity(t *testing.T) {
	dir := t.TempDir()
	cfg := &config.Config{
		Storage: config.StorageConfig{Path: filepath.Join(dir, "store")},
		Setup:   config.SetupConfig{DataPath: dir},
	}

	first, err := datasetPublicationSigningKey(cfg, nil)
	if err != nil {
		t.Fatalf("datasetPublicationSigningKey first call failed: %v", err)
	}
	if len(first) != ed25519.PrivateKeySize {
		t.Fatalf("first key length = %d, want %d", len(first), ed25519.PrivateKeySize)
	}

	second, err := datasetPublicationSigningKey(cfg, nil)
	if err != nil {
		t.Fatalf("datasetPublicationSigningKey second call failed: %v", err)
	}
	if !bytes.Equal(first, second) {
		t.Fatal("dataset publication signing key was not persisted")
	}
}

func writeMainTestPluginRegistry(t *testing.T, entries ...license.PluginCatalogEntry) *license.PluginRegistry {
	t.Helper()

	root := t.TempDir()
	rawCatalog, err := json.Marshal(map[string]any{
		"plugins": entries,
	})
	if err != nil {
		t.Fatalf("Marshal catalog failed: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "catalog.json"), rawCatalog, 0o600); err != nil {
		t.Fatalf("WriteFile(catalog.json) failed: %v", err)
	}
	for _, entry := range entries {
		encryptedPath := filepath.Join(root, entry.EncryptedPath)
		keyPath := filepath.Join(root, entry.KeyPath)
		if err := os.WriteFile(encryptedPath, []byte{0x00, 0x61, 0x73, 0x6d}, 0o600); err != nil {
			t.Fatalf("WriteFile(%s) failed: %v", encryptedPath, err)
		}
		if err := os.WriteFile(keyPath, bytes.Repeat([]byte("a"), 64), 0o600); err != nil {
			t.Fatalf("WriteFile(%s) failed: %v", keyPath, err)
		}
	}

	reg, err := license.LoadPluginRegistry(root)
	if err != nil {
		t.Fatalf("LoadPluginRegistry failed: %v", err)
	}
	return reg
}

type runtimeMutationTestPlugin struct {
	id         string
	descriptor plugins.RuntimeModuleDescriptor
}

func (p *runtimeMutationTestPlugin) ID() string { return p.id }

func (p *runtimeMutationTestPlugin) Start(context.Context, plugins.RuntimeContext) error {
	return nil
}

func (p *runtimeMutationTestPlugin) RegisterRoutes(*http.ServeMux) {}

func (p *runtimeMutationTestPlugin) Close() error { return nil }

func (p *runtimeMutationTestPlugin) RuntimeDescriptor() plugins.RuntimeModuleDescriptor {
	return p.descriptor
}

func (p *runtimeMutationTestPlugin) CronMethods() []plugins.CronMethodSpec {
	if p.id == "celestrak-provider" {
		return []plugins.CronMethodSpec{
			{
				Method:          "sync_full_catalog",
				Description:     "Sync CelesTrak full catalog",
				DefaultInterval: "3h",
				Input:           "json",
				Output:          "json",
			},
		}
	}
	return []plugins.CronMethodSpec{
		{
			Method:          "refresh_grants",
			Description:     "Refresh grant cache",
			DefaultInterval: "30s",
			Input:           "none",
			Output:          "json",
		},
	}
}

func (p *runtimeMutationTestPlugin) InvokeCron(context.Context, string, []byte) ([]byte, error) {
	return nil, nil
}

type fakeProviderDescriptorSource struct {
	host       libp2phost.Host
	peer       peer.ID
	addrs      []multiaddr.Multiaddr
	epmService *epm.Service
}

func (f fakeProviderDescriptorSource) PeerID() peer.ID {
	return f.peer
}

func (f fakeProviderDescriptorSource) ListenAddrs() []multiaddr.Multiaddr {
	return append([]multiaddr.Multiaddr(nil), f.addrs...)
}

func (f fakeProviderDescriptorSource) Host() libp2phost.Host {
	return f.host
}

func (f fakeProviderDescriptorSource) EPMService() *epm.Service {
	return f.epmService
}

func testProviderDerivedIdentity() (*wasm.DerivedIdentity, error) {
	identityPrivKey, _, err := crypto.GenerateSecp256k1Key(bytes.NewReader(bytes.Repeat([]byte{0x41}, 64)))
	if err != nil {
		return nil, err
	}
	signingPrivKey, signingPubKey, err := crypto.GenerateEd25519Key(bytes.NewReader(bytes.Repeat([]byte{0x42}, 64)))
	if err != nil {
		return nil, err
	}
	peerID, err := peer.IDFromPublicKey(identityPrivKey.GetPublic())
	if err != nil {
		return nil, err
	}

	return &wasm.DerivedIdentity{
		IdentityPrivKey:   identityPrivKey,
		IdentityPubKey:    identityPrivKey.GetPublic(),
		SigningPrivKey:    signingPrivKey,
		SigningPubKey:     signingPubKey,
		EncryptionKey:     bytes.Repeat([]byte{0x43}, 32),
		EncryptionPub:     bytes.Repeat([]byte{0x44}, 32),
		PeerID:            peerID,
		IdentityKeyPath:   "m/44'/0'/0'",
		SigningKeyPath:    "m/44'/0'/0'/0'/0'",
		EncryptionKeyPath: "m/44'/0'/0'/1'/0'",
		BitcoinKeyPath:    "m/44'/0'/0'/0/0",
		EthereumKeyPath:   "m/44'/60'/0'/0/0",
		SolanaKeyPath:     "m/44'/501'/0'/0'",
		Addresses: &wasm.CoinAddresses{
			Bitcoin: &wasm.CoinAddress{
				Address: "bc1qprovideridentityaddress000000000000000000",
				Path:    "m/44'/0'/0'/0/0",
			},
			Ethereum: &wasm.CoinAddress{
				Address: "0x1111111111111111111111111111111111111111",
				Path:    "m/44'/60'/0'/0/0",
			},
			Solana: &wasm.CoinAddress{
				Address: "So1anaProviderIdentity111111111111111111111111",
				Path:    "m/44'/501'/0'/0'",
			},
		},
	}, nil
}
