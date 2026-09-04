package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/EPM"
	"github.com/gorilla/websocket"
	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/crypto"
	libp2phost "github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/multiformats/go-multiaddr"

	"github.com/spacedatanetwork/sdn-server/internal/api"
	"github.com/spacedatanetwork/sdn-server/internal/assetpin"
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
	"github.com/spacedatanetwork/sdn-server/internal/storage"
	"github.com/spacedatanetwork/sdn-server/internal/wasm"
	"github.com/spacedatanetwork/sdn-server/plugins"
)

// testVectorMnemonic returns the canonical public BIP-39 all-zeros test
// vector. Built at runtime so the source never contains a 12-word wordlist
// run, which the check-no-mnemonics pre-commit guard (correctly) blocks.
func testVectorMnemonic() string {
	return strings.TrimSpace(strings.Repeat("abandon ", 11)) + " about"
}

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
		// Retired native endpoint (loop C.4): not a per-schema bulk read, so
		// it is not on the data plane and stays gated.
		{http.MethodGet, "/api/v1/data/secure/omm", false},
		// Per-schema data plane (sdn-rfb-public-read-allowlist): anonymity now
		// follows the STANDARD, not a literal path. $MPE is public
		// catalogue-derived data, so its bulk read is anonymous whether or not
		// the mounted flow implements the route yet (an unimplemented route
		// answers 404 from inside the flow — it must not answer 401).
		{http.MethodGet, "/api/v1/data/mpe/bulk", true},
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
		{http.MethodPost, "/api/v1/modules/runtime/catalogfixture-provider/schedules/full/run", false},
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

func TestIsAssetOIDCCapabilityRequestIsLiteralAndPOSTOnly(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		method string
		path   string
		want   bool
	}{
		{name: "pin", method: http.MethodPost, path: "/api/v1/assets/pin", want: true},
		{name: "reference state", method: http.MethodPost, path: "/api/v1/assets/reference-state", want: true},
		{name: "get", method: http.MethodGet, path: "/api/v1/assets/pin"},
		{name: "head", method: http.MethodHead, path: "/api/v1/assets/pin"},
		{name: "options", method: http.MethodOptions, path: "/api/v1/assets/pin"},
		{name: "lowercase method", method: "post", path: "/api/v1/assets/pin"},
		{name: "case variant", method: http.MethodPost, path: "/api/v1/assets/Pin"},
		{name: "trailing slash", method: http.MethodPost, path: "/api/v1/assets/pin/"},
		{name: "suffix", method: http.MethodPost, path: "/api/v1/assets/pin/extra"},
		{name: "prefix", method: http.MethodPost, path: "/prefix/api/v1/assets/pin"},
		{name: "encoded letter", method: http.MethodPost, path: "/api/v1/assets/%70in"},
		{name: "encoded slash", method: http.MethodPost, path: "/api/v1/assets%2Fpin"},
		{name: "reference trailing slash", method: http.MethodPost, path: "/api/v1/assets/reference-state/"},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := isAssetOIDCCapabilityRequest(test.method, test.path); got != test.want {
				t.Fatalf("isAssetOIDCCapabilityRequest(%q, %q) = %v, want %v", test.method, test.path, got, test.want)
			}
		})
	}

	for _, path := range []string{"/api/v1/assets/pin", "/api/v1/assets/reference-state"} {
		if isPublicAPIRequest(http.MethodPost, path) {
			t.Fatalf("%s must remain outside the public API policy", path)
		}
	}
}

func TestValidateAssetPinPreNodeConfigRequiresAdminListener(t *testing.T) {
	if err := validateAssetPinPreNodeConfig(nil); err == nil {
		t.Fatal("validateAssetPinPreNodeConfig(nil) succeeded")
	}
	tests := []struct {
		name    string
		admin   bool
		assets  bool
		wantErr bool
	}{
		{name: "disabled capability with disabled admin", admin: false, assets: false},
		{name: "disabled capability with enabled admin", admin: true, assets: false},
		{name: "enabled capability with enabled admin", admin: true, assets: true},
		{name: "enabled capability with disabled admin", admin: false, assets: true, wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cfg := config.Default()
			cfg.Admin.Enabled = test.admin
			cfg.AssetPins.Enabled = test.assets
			err := validateAssetPinPreNodeConfig(cfg)
			if (err != nil) != test.wantErr {
				t.Fatalf("validateAssetPinPreNodeConfig() error = %v, wantErr %v", err, test.wantErr)
			}
		})
	}
}

func TestValidateAssetPinAdminUIAvailabilityFailsClosed(t *testing.T) {
	if err := validateAssetPinAdminUIAvailability(nil, true); err == nil {
		t.Fatal("validateAssetPinAdminUIAvailability(nil) succeeded")
	}
	tests := []struct {
		name        string
		assets      bool
		uiAvailable bool
		wantErr     bool
	}{
		{name: "disabled capability without UI", assets: false, uiAvailable: false},
		{name: "enabled capability with UI", assets: true, uiAvailable: true},
		{name: "enabled capability without UI", assets: true, uiAvailable: false, wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cfg := config.Default()
			cfg.AssetPins.Enabled = test.assets
			err := validateAssetPinAdminUIAvailability(cfg, test.uiAvailable)
			if (err != nil) != test.wantErr {
				t.Fatalf("validateAssetPinAdminUIAvailability() error = %v, wantErr %v", err, test.wantErr)
			}
		})
	}
}

func TestAdminWalletWallBypassesOnlyExactAssetOIDCCapabilities(t *testing.T) {
	authHandler, _ := newAdminSession(t, peers.Standard)
	adminMux := http.NewServeMux()
	calls := 0
	capabilityHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusNoContent)
	})
	registerAssetPinCapabilityRoutes(adminMux, capabilityHandler)

	for _, path := range []string{"/api/v1/assets/pin", "/api/v1/assets/reference-state"} {
		req := httptest.NewRequest(http.MethodPost, path, nil)
		rec := httptest.NewRecorder()
		serveAdminMuxRequest(rec, req, adminMux, true, true, authHandler, notPublicAPI)
		if rec.Code != http.StatusNoContent {
			t.Fatalf("exact POST %s status = %d, want %d", path, rec.Code, http.StatusNoContent)
		}
	}
	if calls != 2 {
		t.Fatalf("exact capability calls = %d, want 2", calls)
	}

	variants := []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/api/v1/assets/pin"},
		{http.MethodHead, "/api/v1/assets/pin"},
		{http.MethodOptions, "/api/v1/assets/pin"},
		{http.MethodPost, "/api/v1/assets/pin/"},
		{http.MethodPost, "/api/v1/assets/pin/extra"},
		{http.MethodPost, "/api/v1/assets/Pin"},
		{http.MethodPost, "/api/v1/assets/%70in"},
		{http.MethodPost, "/api/v1/assets%2Fpin"},
		{http.MethodPost, "/api/v1/assets/reference-state/"},
	}
	for _, variant := range variants {
		req := httptest.NewRequest(variant.method, variant.path, nil)
		rec := httptest.NewRecorder()
		serveAdminMuxRequest(rec, req, adminMux, true, true, authHandler, notPublicAPI)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("variant %s %s status = %d, want wallet-gated %d", variant.method, variant.path, rec.Code, http.StatusUnauthorized)
		}
	}
	if calls != 2 {
		t.Fatalf("variant reached capability handler; calls = %d, want 2", calls)
	}
}

func TestAdminWalletWallExactAssetOIDCCapabilityDoesNotRequireWalletBackend(t *testing.T) {
	adminMux := http.NewServeMux()
	calls := 0
	registerAssetPinCapabilityRoutes(adminMux, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusNoContent)
	}))

	for _, path := range []string{"/api/v1/assets/pin", "/api/v1/assets/reference-state"} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, path, nil)
		serveAdminMuxRequest(rec, req, adminMux, true, true, nil, notPublicAPI)
		if rec.Code != http.StatusNoContent {
			t.Fatalf("exact POST %s with no wallet backend status = %d, want %d", path, rec.Code, http.StatusNoContent)
		}
	}
	if calls != 2 {
		t.Fatalf("exact capability calls = %d, want 2", calls)
	}

	for _, variant := range []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/api/v1/assets/pin"},
		{http.MethodPost, "/api/v1/assets/pin/"},
		{http.MethodPost, "/api/v1/assets/%70in"},
		{http.MethodPost, "/api/v1/assets/reference-state/"},
	} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(variant.method, variant.path, nil)
		serveAdminMuxRequest(rec, req, adminMux, true, true, nil, notPublicAPI)
		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("variant %s %s with no wallet backend status = %d, want legacy %d", variant.method, variant.path, rec.Code, http.StatusServiceUnavailable)
		}
	}
	if calls != 2 {
		t.Fatalf("variant reached capability handler; calls = %d, want 2", calls)
	}
}

func TestAdminWalletWallDoesNotBypassUnmountedAssetOIDCPaths(t *testing.T) {
	authHandler, _ := newAdminSession(t, peers.Standard)
	adminMux := http.NewServeMux()
	catchAllCalls := 0
	adminMux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		catchAllCalls++
		w.WriteHeader(http.StatusNoContent)
	})

	for _, path := range []string{"/api/v1/assets/pin", "/api/v1/assets/reference-state"} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, path, nil)
		serveAdminMuxRequest(rec, req, adminMux, true, false, authHandler, notPublicAPI)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("unmounted POST %s status = %d, want wallet-gated %d", path, rec.Code, http.StatusUnauthorized)
		}
	}
	if catchAllCalls != 0 {
		t.Fatalf("unmounted capability paths reached broad API handler %d times", catchAllCalls)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/assets/pin", nil)
	serveAdminMuxRequest(rec, req, adminMux, true, false, nil, notPublicAPI)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("unmounted path without wallet backend status = %d, want legacy %d", rec.Code, http.StatusServiceUnavailable)
	}
	if catchAllCalls != 0 {
		t.Fatalf("unmounted path reached broad API handler with no wallet backend")
	}
}

func TestComposeAssetPinCapabilityHealthyMountUsesNodeInputs(t *testing.T) {
	now := time.Date(2026, 7, 13, 19, 20, 21, 123, time.FixedZone("offset", -4*60*60))
	store := &fakeAssetPinCapabilityStore{path: filepath.Join(t.TempDir(), "sdn.db")}
	cfg := config.Default().AssetPins
	cfg.Enabled = true
	pinner := &fakeAssetPinCapabilityPinner{}
	gate := assetpin.NewMutationGate()
	verifier := &fakeAssetPinCapabilityVerifier{}
	routes := &fakeAssetPinCapabilityRoutes{}
	var consumed assetpin.TokenReceiptConsumer
	dependencies := assetPinCapabilityDependencies{
		clock: func() time.Time { return now },
		probeKubo: func(_ context.Context, apiURL string) error {
			if apiURL != "http://127.0.0.1:5001" {
				t.Fatalf("Kubo probe URL = %q", apiURL)
			}
			return nil
		},
		newPinner: func(apiURL string) (api.AssetPinPinner, error) {
			if apiURL != "http://127.0.0.1:5001" {
				t.Fatalf("Kubo URL = %q", apiURL)
			}
			return pinner, nil
		},
		newHandler: func(options api.AssetPinHandlerOptions) (assetPinCapabilityRoutes, error) {
			if options.Store != store || options.Pinner != pinner || options.Verifier == nil || options.Gate != gate {
				t.Fatalf("handler options not composed from store/pinner/verifier: %#v", options)
			}
			if options.Config != cfg {
				t.Fatalf("handler config = %#v, want %#v", options.Config, cfg)
			}
			if options.DataDir != filepath.Dir(store.Path()) {
				t.Fatalf("handler data dir = %q, want %q", options.DataDir, filepath.Dir(store.Path()))
			}
			return routes, nil
		},
		newVerifier: func(_ context.Context, got config.AssetPinConfig, consumer assetpin.TokenReceiptConsumer) (api.AssetPinVerifier, error) {
			if got != cfg {
				t.Fatalf("verifier config = %#v, want %#v", got, cfg)
			}
			consumed = consumer
			return verifier, nil
		},
	}

	capability, err := composeAssetPinCapability(context.Background(), store, "http://127.0.0.1:5001", cfg, gate, dependencies)
	if err != nil {
		t.Fatalf("composeAssetPinCapability() error = %v", err)
	}
	if capability.HealthErr != nil {
		t.Fatalf("capability health error = %v", capability.HealthErr)
	}
	if consumed == nil {
		t.Fatal("token receipt consumer was not supplied to verifier")
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/unrelated", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusAccepted) })
	registerAssetPinCapabilityRoutes(mux, capability.Handler)
	for _, path := range []string{"/api/v1/assets/pin", "/api/v1/assets/reference-state"} {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, path, nil))
		if rec.Code != http.StatusNoContent {
			t.Fatalf("POST %s status = %d, want %d", path, rec.Code, http.StatusNoContent)
		}
	}
	if routes.calls != 2 {
		t.Fatalf("healthy route calls = %d, want 2", routes.calls)
	}
	for _, path := range []string{"/api/v1/assets/pin/", "/api/v1/assets/pin/extra", "/api/v1/assets/reference-state/"} {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, path, nil))
		if rec.Code != http.StatusNotFound {
			t.Fatalf("POST %s status = %d, want exact-registration 404", path, rec.Code)
		}
	}
	if routes.calls != 2 {
		t.Fatalf("non-exact route reached handler; calls = %d", routes.calls)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/unrelated", nil))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("unrelated route status = %d, want %d", rec.Code, http.StatusAccepted)
	}
}

func TestAssetPinTokenReceiptConsumerMapsStorageReceiptAndReplay(t *testing.T) {
	now := time.Date(2026, 7, 13, 23, 24, 25, 456, time.FixedZone("offset", 3*60*60))
	expiresAt := now.Add(5 * time.Minute).UTC()
	claims := assetpin.Claims{
		Repository: "DigitalArsenal/asset-models", Ref: "refs/heads/main",
		WorkflowRef: "DigitalArsenal/asset-models/.github/workflows/asset-loop.yml@refs/heads/main",
		Actor:       "review-bot", RunID: "101", RunAttempt: "2", SHA: strings.Repeat("a", 40),
	}
	store := &fakeAssetPinCapabilityStore{}
	consumer := newAssetPinTokenReceiptConsumer(store, func() time.Time { return now })
	if err := consumer(context.Background(), strings.Repeat("b", 64), expiresAt, claims); err != nil {
		t.Fatalf("consume receipt: %v", err)
	}
	want := storage.AssetOIDCReceipt{
		Digest: strings.Repeat("b", 64), ExpiresAt: expiresAt,
		Repository: claims.Repository, Ref: claims.Ref, WorkflowRef: claims.WorkflowRef,
		Actor: claims.Actor, RunID: claims.RunID, RunAttempt: claims.RunAttempt, SHA: claims.SHA,
		ConsumedAt: now.UTC(),
	}
	if store.receipt != want {
		t.Fatalf("stored receipt = %#v, want %#v", store.receipt, want)
	}

	store.consumeErr = fmt.Errorf("wrapped: %w", storage.ErrAssetOIDCTokenReplay)
	if err := consumer(context.Background(), strings.Repeat("c", 64), expiresAt, claims); !errors.Is(err, assetpin.ErrTokenReplay) {
		t.Fatalf("replay error = %v, want assetpin.ErrTokenReplay", err)
	}
	store.consumeErr = errors.New("ledger unavailable")
	if err := consumer(context.Background(), strings.Repeat("d", 64), expiresAt, claims); err == nil || errors.Is(err, assetpin.ErrTokenReplay) {
		t.Fatalf("ledger error = %v, want original non-replay failure", err)
	}
}

func TestComposeAssetPinCapabilityRejectsStaticFailuresBeforeDiscovery(t *testing.T) {
	cfg := config.Default().AssetPins
	cfg.Enabled = true
	validStore := &fakeAssetPinCapabilityStore{path: filepath.Join(t.TempDir(), "sdn.db")}
	discoveryCalls := 0
	probeCalls := 0
	dependencies := assetPinCapabilityDependencies{
		clock: func() time.Time { return time.Now().UTC() },
		probeKubo: func(context.Context, string) error {
			probeCalls++
			return nil
		},
		newPinner: func(string) (api.AssetPinPinner, error) { return &fakeAssetPinCapabilityPinner{}, nil },
		newHandler: func(api.AssetPinHandlerOptions) (assetPinCapabilityRoutes, error) {
			return &fakeAssetPinCapabilityRoutes{}, nil
		},
		newVerifier: func(context.Context, config.AssetPinConfig, assetpin.TokenReceiptConsumer) (api.AssetPinVerifier, error) {
			discoveryCalls++
			return &fakeAssetPinCapabilityVerifier{}, nil
		},
	}
	var typedNilEdgeStore *fakeAssetPinCapabilityStore

	tests := []struct {
		name       string
		store      assetPinCapabilityStore
		apiURL     string
		config     config.AssetPinConfig
		customDeps *assetPinCapabilityDependencies
	}{
		{name: "nil edge store", store: nil, apiURL: "http://127.0.0.1:5001", config: cfg},
		{name: "typed nil edge store", store: typedNilEdgeStore, apiURL: "http://127.0.0.1:5001", config: cfg},
		{name: "disabled", store: validStore, apiURL: "http://127.0.0.1:5001", config: func() config.AssetPinConfig { c := cfg; c.Enabled = false; return c }()},
		{name: "empty Kubo URL", store: validStore, apiURL: "", config: cfg},
		{name: "invalid Kubo URL", store: validStore, apiURL: "unix:///var/run/kubo.sock", config: cfg},
		{name: "invalid issuer", store: validStore, apiURL: "http://127.0.0.1:5001", config: func() config.AssetPinConfig { c := cfg; c.Issuer = "://bad"; return c }()},
		{name: "invalid handler data", store: validStore, apiURL: "http://127.0.0.1:5001", config: cfg, customDeps: func() *assetPinCapabilityDependencies {
			d := dependencies
			d.newHandler = func(api.AssetPinHandlerOptions) (assetPinCapabilityRoutes, error) {
				return nil, errors.New("invalid data directory")
			}
			return &d
		}()},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			deps := dependencies
			if test.customDeps != nil {
				deps = *test.customDeps
			}
			before := discoveryCalls
			probeBefore := probeCalls
			if _, err := composeAssetPinCapability(context.Background(), test.store, test.apiURL, test.config, assetpin.NewMutationGate(), deps); err == nil {
				t.Fatal("composeAssetPinCapability() succeeded, want static failure")
			}
			if discoveryCalls != before {
				t.Fatalf("OIDC discovery called for static failure: before=%d after=%d", before, discoveryCalls)
			}
			if probeCalls != probeBefore {
				t.Fatalf("Kubo readiness probe called for static failure: before=%d after=%d", probeBefore, probeCalls)
			}
		})
	}
}

func TestComposeAssetPinCapabilityRejectsNoncanonicalSecurityInputsBeforeEffects(t *testing.T) {
	baseConfig := config.Default().AssetPins
	baseConfig.Enabled = true
	store := &fakeAssetPinCapabilityStore{path: filepath.Join(t.TempDir(), "sdn.db")}

	tests := []struct {
		name   string
		apiURL string
		mutate func(*config.AssetPinConfig)
	}{
		{name: "Kubo leading whitespace", apiURL: " http://127.0.0.1:5001"},
		{name: "Kubo trailing whitespace", apiURL: "http://127.0.0.1:5001 "},
		{name: "Kubo hostless", apiURL: "http://:5001"},
		{name: "Kubo IPv4 empty port", apiURL: "http://127.0.0.1:"},
		{name: "Kubo hostname nonnumeric port", apiURL: "http://localhost:notaport"},
		{name: "Kubo hostname zero port", apiURL: "http://localhost:0"},
		{name: "Kubo IPv4 out of range port", apiURL: "http://127.0.0.1:65536"},
		{name: "Kubo IPv6 empty port", apiURL: "http://[::1]:"},
		{name: "Kubo IPv6 out of range port", apiURL: "http://[::1]:70000"},
		{name: "Kubo userinfo", apiURL: "http://user:secret@127.0.0.1:5001"},
		{name: "Kubo query", apiURL: "http://127.0.0.1:5001?token=secret"},
		{name: "Kubo empty query", apiURL: "http://127.0.0.1:5001?"},
		{name: "Kubo fragment", apiURL: "http://127.0.0.1:5001#fragment"},
		{name: "Kubo encoded path", apiURL: "http://127.0.0.1:5001/%61pi"},
		{name: "issuer leading whitespace", apiURL: "http://127.0.0.1:5001", mutate: func(c *config.AssetPinConfig) { c.Issuer = " " + c.Issuer }},
		{name: "issuer query", apiURL: "http://127.0.0.1:5001", mutate: func(c *config.AssetPinConfig) { c.Issuer += "?tenant=secret" }},
		{name: "issuer fragment", apiURL: "http://127.0.0.1:5001", mutate: func(c *config.AssetPinConfig) { c.Issuer += "#fragment" }},
		{name: "issuer userinfo", apiURL: "http://127.0.0.1:5001", mutate: func(c *config.AssetPinConfig) { c.Issuer = "https://user:secret@issuer.example" }},
		{name: "issuer hostname empty port", apiURL: "http://127.0.0.1:5001", mutate: func(c *config.AssetPinConfig) { c.Issuer = "https://issuer.example:" }},
		{name: "issuer hostname nonnumeric port", apiURL: "http://127.0.0.1:5001", mutate: func(c *config.AssetPinConfig) { c.Issuer = "https://issuer.example:notaport" }},
		{name: "issuer hostname zero port", apiURL: "http://127.0.0.1:5001", mutate: func(c *config.AssetPinConfig) { c.Issuer = "https://issuer.example:0" }},
		{name: "issuer hostname out of range port", apiURL: "http://127.0.0.1:5001", mutate: func(c *config.AssetPinConfig) { c.Issuer = "https://issuer.example:65536" }},
		{name: "issuer IPv6 out of range port", apiURL: "http://127.0.0.1:5001", mutate: func(c *config.AssetPinConfig) { c.Issuer = "https://[::1]:70000" }},
		{name: "audience trailing whitespace", apiURL: "http://127.0.0.1:5001", mutate: func(c *config.AssetPinConfig) { c.Audience += " " }},
		{name: "repository leading whitespace", apiURL: "http://127.0.0.1:5001", mutate: func(c *config.AssetPinConfig) { c.Repository = " " + c.Repository }},
		{name: "ref trailing whitespace", apiURL: "http://127.0.0.1:5001", mutate: func(c *config.AssetPinConfig) { c.Ref += "\n" }},
		{name: "pin workflow leading whitespace", apiURL: "http://127.0.0.1:5001", mutate: func(c *config.AssetPinConfig) { c.PinWorkflow = "\t" + c.PinWorkflow }},
		{name: "decision workflow trailing whitespace", apiURL: "http://127.0.0.1:5001", mutate: func(c *config.AssetPinConfig) { c.DecisionWorkflow += " " }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cfg := baseConfig
			if test.mutate != nil {
				test.mutate(&cfg)
			}
			calls := 0
			dependencies := assetPinCapabilityDependencies{
				clock: func() time.Time { return time.Now().UTC() },
				newPinner: func(string) (api.AssetPinPinner, error) {
					calls++
					return &fakeAssetPinCapabilityPinner{}, nil
				},
				newHandler: func(api.AssetPinHandlerOptions) (assetPinCapabilityRoutes, error) {
					calls++
					return &fakeAssetPinCapabilityRoutes{}, nil
				},
				probeKubo: func(context.Context, string) error {
					calls++
					return nil
				},
				newVerifier: func(context.Context, config.AssetPinConfig, assetpin.TokenReceiptConsumer) (api.AssetPinVerifier, error) {
					calls++
					return &fakeAssetPinCapabilityVerifier{}, nil
				},
			}
			if _, err := composeAssetPinCapability(context.Background(), store, test.apiURL, cfg, assetpin.NewMutationGate(), dependencies); err == nil {
				t.Fatal("composeAssetPinCapability() accepted noncanonical static input")
			}
			if calls != 0 {
				t.Fatalf("noncanonical static input triggered %d constructor/probe/discovery calls", calls)
			}
		})
	}
}

func TestAssetPinURLExplicitPortValidationAcceptsIPv4HostnameAndIPv6Bounds(t *testing.T) {
	validKuboURLs := []string{
		"http://127.0.0.1:1",
		"https://kubo.example:65535/reverse-proxy",
		"http://[::1]:5001",
	}
	for _, raw := range validKuboURLs {
		if _, err := canonicalAssetPinKuboAPIURL(raw); err != nil {
			t.Fatalf("canonicalAssetPinKuboAPIURL(%q) error = %v", raw, err)
		}
	}

	validIssuers := []string{
		"https://127.0.0.1:1",
		"https://issuer.example:65535/tenant",
		"https://[::1]:443",
	}
	for _, issuer := range validIssuers {
		cfg := config.Default().AssetPins
		cfg.Issuer = issuer
		if err := validateAssetPinCapabilityOIDCConfig(cfg); err != nil {
			t.Fatalf("validateAssetPinCapabilityOIDCConfig(%q) error = %v", issuer, err)
		}
	}
}

func TestComposeAssetPinCapabilityUsesOneCanonicalPathOnlyKuboBase(t *testing.T) {
	store := &fakeAssetPinCapabilityStore{path: filepath.Join(t.TempDir(), "sdn.db")}
	cfg := config.Default().AssetPins
	cfg.Enabled = true
	const canonicalURL = "http://127.0.0.1:5001/reverse-proxy"
	var pinnerURL, probeURL string
	dependencies := assetPinCapabilityDependencies{
		clock: func() time.Time { return time.Now().UTC() },
		newPinner: func(apiURL string) (api.AssetPinPinner, error) {
			pinnerURL = apiURL
			return &fakeAssetPinCapabilityPinner{}, nil
		},
		newHandler: func(api.AssetPinHandlerOptions) (assetPinCapabilityRoutes, error) {
			return &fakeAssetPinCapabilityRoutes{}, nil
		},
		probeKubo: func(_ context.Context, apiURL string) error {
			probeURL = apiURL
			return nil
		},
		newVerifier: func(context.Context, config.AssetPinConfig, assetpin.TokenReceiptConsumer) (api.AssetPinVerifier, error) {
			return &fakeAssetPinCapabilityVerifier{}, nil
		},
	}

	capability, err := composeAssetPinCapability(context.Background(), store, canonicalURL+"/", cfg, assetpin.NewMutationGate(), dependencies)
	if err != nil {
		t.Fatalf("composeAssetPinCapability() error = %v", err)
	}
	if capability.HealthErr != nil {
		t.Fatalf("capability health error = %v", capability.HealthErr)
	}
	if pinnerURL != canonicalURL || probeURL != canonicalURL {
		t.Fatalf("canonical Kubo URLs = pinner %q, probe %q; want both %q", pinnerURL, probeURL, canonicalURL)
	}
}

func TestComposeAssetPinCapabilityDegradesOnlyCapabilityRoutes(t *testing.T) {
	store := &fakeAssetPinCapabilityStore{path: filepath.Join(t.TempDir(), "sdn.db")}
	cfg := config.Default().AssetPins
	cfg.Enabled = true
	secretDiscoveryError := errors.New("provider secret-discovery-detail")
	dependencies := assetPinCapabilityDependencies{
		clock:     func() time.Time { return time.Now().UTC() },
		probeKubo: func(context.Context, string) error { return nil },
		newPinner: func(string) (api.AssetPinPinner, error) { return &fakeAssetPinCapabilityPinner{}, nil },
		newHandler: func(api.AssetPinHandlerOptions) (assetPinCapabilityRoutes, error) {
			return &fakeAssetPinCapabilityRoutes{}, nil
		},
		newVerifier: func(context.Context, config.AssetPinConfig, assetpin.TokenReceiptConsumer) (api.AssetPinVerifier, error) {
			return nil, secretDiscoveryError
		},
	}

	capability, err := composeAssetPinCapability(context.Background(), store, "http://127.0.0.1:5001", cfg, assetpin.NewMutationGate(), dependencies)
	if err != nil {
		t.Fatalf("composeAssetPinCapability() static error = %v", err)
	}
	if !errors.Is(capability.HealthErr, secretDiscoveryError) {
		t.Fatalf("health error = %v, want wrapped discovery error", capability.HealthErr)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/unrelated", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) })
	registerAssetPinCapabilityRoutes(mux, capability.Handler)

	for _, path := range []string{"/api/v1/assets/pin", "/api/v1/assets/reference-state"} {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, path, strings.NewReader("sensitive request")))
		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("degraded POST %s status = %d, want %d", path, rec.Code, http.StatusServiceUnavailable)
		}
		if got := rec.Header().Get("Content-Type"); got != "application/json" {
			t.Fatalf("degraded Content-Type = %q", got)
		}
		if body := rec.Body.String(); body != assetPinCapabilityUnavailableJSON || strings.Contains(body, "secret-discovery-detail") {
			t.Fatalf("degraded body = %q, want fixed sanitized JSON", body)
		}
		if rec.Body.Len() > 256 {
			t.Fatalf("degraded body length = %d, want bounded", rec.Body.Len())
		}
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/unrelated", nil))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("unrelated route status = %d, want %d", rec.Code, http.StatusNoContent)
	}
}

func TestComposeAssetPinCapabilityKuboOutageDegradesWithoutOIDCDiscovery(t *testing.T) {
	store := &fakeAssetPinCapabilityStore{path: filepath.Join(t.TempDir(), "sdn.db")}
	cfg := config.Default().AssetPins
	cfg.Enabled = true
	secretProbeError := errors.New("kubo secret-readiness-detail")
	discoveryCalls := 0
	dependencies := assetPinCapabilityDependencies{
		clock:     func() time.Time { return time.Now().UTC() },
		probeKubo: func(context.Context, string) error { return secretProbeError },
		newPinner: func(string) (api.AssetPinPinner, error) { return &fakeAssetPinCapabilityPinner{}, nil },
		newHandler: func(api.AssetPinHandlerOptions) (assetPinCapabilityRoutes, error) {
			return &fakeAssetPinCapabilityRoutes{}, nil
		},
		newVerifier: func(context.Context, config.AssetPinConfig, assetpin.TokenReceiptConsumer) (api.AssetPinVerifier, error) {
			discoveryCalls++
			return &fakeAssetPinCapabilityVerifier{}, nil
		},
	}

	capability, err := composeAssetPinCapability(context.Background(), store, "http://127.0.0.1:5001", cfg, assetpin.NewMutationGate(), dependencies)
	if err != nil {
		t.Fatalf("composeAssetPinCapability() static error = %v", err)
	}
	if !errors.Is(capability.HealthErr, secretProbeError) {
		t.Fatalf("health error = %v, want wrapped Kubo probe error", capability.HealthErr)
	}
	if discoveryCalls != 0 {
		t.Fatalf("OIDC discovery calls = %d, want 0 while Kubo is unavailable", discoveryCalls)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/unrelated", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) })
	registerAssetPinCapabilityRoutes(mux, capability.Handler)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/assets/pin", nil))
	if rec.Code != http.StatusServiceUnavailable || rec.Body.String() != assetPinCapabilityUnavailableJSON {
		t.Fatalf("Kubo-degraded response = %d %q", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "secret-readiness-detail") {
		t.Fatalf("Kubo probe error leaked into response: %q", rec.Body.String())
	}
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/unrelated", nil))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("unrelated route status = %d, want %d", rec.Code, http.StatusNoContent)
	}
}

func TestProbeAssetPinKuboReadinessUsesBoundedOfficialVersionRPC(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodPost || r.URL.Path != "/api/v0/version" || r.URL.RawQuery != "" {
				t.Fatalf("probe request = %s %s", r.Method, r.URL.RequestURI())
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"Version":"0.36.0","Commit":"fixture","Repo":"18"}`))
		}))
		defer server.Close()
		if err := probeAssetPinKuboReadiness(context.Background(), server.URL); err != nil {
			t.Fatalf("probeAssetPinKuboReadiness() error = %v", err)
		}
	})

	tests := []struct {
		name string
		body string
		code int
	}{
		{name: "non success", code: http.StatusBadGateway, body: "secret upstream detail"},
		{name: "empty body", code: http.StatusOK},
		{name: "malformed JSON", code: http.StatusOK, body: `{"Version":`},
		{name: "trailing JSON", code: http.StatusOK, body: `{"Version":"0.36.0"}{}`},
		{name: "missing version", code: http.StatusOK, body: `{"Repo":"18"}`},
		{name: "oversized body", code: http.StatusOK, body: `{"Version":"` + strings.Repeat("x", assetPinKuboProbeMaxResponseBytes) + `"}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(test.code)
				_, _ = w.Write([]byte(test.body))
			}))
			defer server.Close()
			err := probeAssetPinKuboReadiness(context.Background(), server.URL)
			if err == nil {
				t.Fatal("probeAssetPinKuboReadiness() succeeded, want failure")
			}
			if strings.Contains(err.Error(), "secret upstream detail") {
				t.Fatalf("probe error leaked response body: %v", err)
			}
		})
	}

	t.Run("caller deadline", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			<-r.Context().Done()
		}))
		defer server.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
		defer cancel()
		started := time.Now()
		if err := probeAssetPinKuboReadiness(ctx, server.URL); err == nil {
			t.Fatal("probeAssetPinKuboReadiness() ignored caller deadline")
		}
		if elapsed := time.Since(started); elapsed > time.Second {
			t.Fatalf("probe returned after %s, want bounded caller cancellation", elapsed)
		}
	})
}

func TestComposeAssetPinCapabilityValidatesProductionHandlerBeforeOIDCDiscovery(t *testing.T) {
	cfg := config.Default().AssetPins
	cfg.Enabled = true
	store := &fakeAssetPinCapabilityStore{path: filepath.Join(t.TempDir(), "missing", "sdn.db")}
	discoveryCalls := 0
	dependencies := defaultAssetPinCapabilityDependencies()
	dependencies.newVerifier = func(context.Context, config.AssetPinConfig, assetpin.TokenReceiptConsumer) (api.AssetPinVerifier, error) {
		discoveryCalls++
		return &fakeAssetPinCapabilityVerifier{}, nil
	}

	if _, err := composeAssetPinCapability(context.Background(), store, "http://127.0.0.1:5001", cfg, assetpin.NewMutationGate(), dependencies); err == nil {
		t.Fatal("composeAssetPinCapability() accepted a missing data directory")
	}
	if discoveryCalls != 0 {
		t.Fatalf("OIDC discovery calls = %d, want 0 before static handler validation", discoveryCalls)
	}
}

func TestComposeAssetPinCapabilityRejectsTypedNilDependenciesBeforeExposure(t *testing.T) {
	newFixture := func(t *testing.T) (*fakeAssetPinCapabilityStore, config.AssetPinConfig, assetPinCapabilityDependencies, *int, *int) {
		t.Helper()
		store := &fakeAssetPinCapabilityStore{path: filepath.Join(t.TempDir(), "sdn.db")}
		cfg := config.Default().AssetPins
		cfg.Enabled = true
		probeCalls := 0
		verifierCalls := 0
		dependencies := assetPinCapabilityDependencies{
			clock: func() time.Time { return time.Now().UTC() },
			probeKubo: func(context.Context, string) error {
				probeCalls++
				return nil
			},
			newPinner: func(string) (api.AssetPinPinner, error) {
				return &fakeAssetPinCapabilityPinner{}, nil
			},
			newHandler: func(api.AssetPinHandlerOptions) (assetPinCapabilityRoutes, error) {
				return &fakeAssetPinCapabilityRoutes{}, nil
			},
			newVerifier: func(context.Context, config.AssetPinConfig, assetpin.TokenReceiptConsumer) (api.AssetPinVerifier, error) {
				verifierCalls++
				return &fakeAssetPinCapabilityVerifier{}, nil
			},
		}
		return store, cfg, dependencies, &probeCalls, &verifierCalls
	}

	t.Run("pinner", func(t *testing.T) {
		store, cfg, dependencies, probeCalls, verifierCalls := newFixture(t)
		handlerCalls := 0
		var typedNil *fakeAssetPinCapabilityPinner
		dependencies.newPinner = func(string) (api.AssetPinPinner, error) { return typedNil, nil }
		dependencies.newHandler = func(api.AssetPinHandlerOptions) (assetPinCapabilityRoutes, error) {
			handlerCalls++
			return &fakeAssetPinCapabilityRoutes{}, nil
		}

		if _, err := composeAssetPinCapability(context.Background(), store, "http://127.0.0.1:5001", cfg, assetpin.NewMutationGate(), dependencies); err == nil {
			t.Fatal("composeAssetPinCapability() accepted a typed-nil pinner")
		}
		if handlerCalls != 0 || *probeCalls != 0 || *verifierCalls != 0 {
			t.Fatalf("typed-nil pinner triggered later effects: handler=%d probe=%d verifier=%d", handlerCalls, *probeCalls, *verifierCalls)
		}
	})

	t.Run("routes", func(t *testing.T) {
		store, cfg, dependencies, probeCalls, verifierCalls := newFixture(t)
		var typedNil *fakeAssetPinCapabilityRoutes
		dependencies.newHandler = func(api.AssetPinHandlerOptions) (assetPinCapabilityRoutes, error) { return typedNil, nil }

		if _, err := composeAssetPinCapability(context.Background(), store, "http://127.0.0.1:5001", cfg, assetpin.NewMutationGate(), dependencies); err == nil {
			t.Fatal("composeAssetPinCapability() accepted typed-nil routes")
		}
		if *probeCalls != 0 || *verifierCalls != 0 {
			t.Fatalf("typed-nil routes triggered later effects: probe=%d verifier=%d", *probeCalls, *verifierCalls)
		}
	})

	t.Run("verifier", func(t *testing.T) {
		store, cfg, dependencies, probeCalls, verifierCalls := newFixture(t)
		routes := &fakeAssetPinCapabilityRoutes{}
		dependencies.newHandler = func(api.AssetPinHandlerOptions) (assetPinCapabilityRoutes, error) { return routes, nil }
		var typedNil *fakeAssetPinCapabilityVerifier
		dependencies.newVerifier = func(context.Context, config.AssetPinConfig, assetpin.TokenReceiptConsumer) (api.AssetPinVerifier, error) {
			(*verifierCalls)++
			return typedNil, nil
		}

		if _, err := composeAssetPinCapability(context.Background(), store, "http://127.0.0.1:5001", cfg, assetpin.NewMutationGate(), dependencies); err == nil {
			t.Fatal("composeAssetPinCapability() accepted a typed-nil verifier")
		}
		if *probeCalls != 1 || *verifierCalls != 1 {
			t.Fatalf("typed-nil verifier construction order: probe=%d verifier=%d, want 1 each", *probeCalls, *verifierCalls)
		}
		if routes.registrations != 0 {
			t.Fatalf("typed-nil verifier exposed routes with %d registration calls", routes.registrations)
		}
	})
}

type fakeAssetPinCapabilityStore struct {
	path       string
	receipt    storage.AssetOIDCReceipt
	consumeErr error
}

func (s *fakeAssetPinCapabilityStore) Path() string { return s.path }

func (s *fakeAssetPinCapabilityStore) ConsumeAssetOIDCToken(_ context.Context, receipt storage.AssetOIDCReceipt) error {
	s.receipt = receipt
	return s.consumeErr
}

func (*fakeAssetPinCapabilityStore) FindAssetPinReferenceByCandidateKey(context.Context, string) (storage.AssetPinReference, bool, error) {
	return storage.AssetPinReference{}, false, nil
}

func (*fakeAssetPinCapabilityStore) FindAssetBySHA256(context.Context, string) (storage.AssetPinReference, bool, error) {
	return storage.AssetPinReference{}, false, nil
}

func (*fakeAssetPinCapabilityStore) UpsertAssetPinReference(context.Context, storage.AssetPinReference, storage.AssetPinAuditEvent) error {
	return nil
}

func (*fakeAssetPinCapabilityStore) TransitionAssetPinReference(context.Context, storage.AssetPinReferenceTransition, storage.AssetPinAuditEvent) error {
	return nil
}

type fakeAssetPinCapabilityPinner struct{}

func (*fakeAssetPinCapabilityPinner) IsAssetCIDPinned(context.Context, string) (bool, error) {
	return true, nil
}

func (*fakeAssetPinCapabilityPinner) CalculateAssetGLBCID(context.Context, string) (string, error) {
	return "", nil
}

func (*fakeAssetPinCapabilityPinner) PinAssetGLB(context.Context, string) (string, error) {
	return "", nil
}

func (*fakeAssetPinCapabilityPinner) UnpinAssetCID(context.Context, string) error { return nil }

type fakeAssetPinCapabilityVerifier struct{}

func (*fakeAssetPinCapabilityVerifier) VerifyAndConsume(context.Context, string, assetpin.WorkflowKind) (assetpin.Claims, error) {
	return assetpin.Claims{}, nil
}

type fakeAssetPinCapabilityRoutes struct {
	calls         int
	registrations int
}

func (h *fakeAssetPinCapabilityRoutes) ServeHTTP(w http.ResponseWriter, _ *http.Request) {
	h.calls++
	w.WriteHeader(http.StatusNoContent)
}

func (h *fakeAssetPinCapabilityRoutes) RegisterRoutes(mux *http.ServeMux) {
	h.registrations++
	mux.Handle("POST /api/v1/assets/pin", h)
	mux.Handle("POST /api/v1/assets/reference-state", h)
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
		"/api/v1/modules/runtime/catalogfixture-provider/schedules/full/run",
		"/api/v1/admin/dataset-updates/publish",
		"/api/admin/frontend/files",
		"/api/v1/plugins/upload",
		"/api/routing/config",
		"/api/streaming/sessions",
		"/api/relay/filters",
		"/api/v1/diag",
		"/api/modules/capabilities",
		"/api/modules/capabilities/approve",
		"/api/modules/capabilities/revoke",
		"/api/modules/capabilities/tiers",
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

Host space-data-network-02 provider.example
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

// notPublicAPI is a publicAPIRequest stub that treats nothing as public, so
// adminSecurityMiddleware tests exercise the CSRF gate in isolation from the
// real isPublicAPIRequest allowlist.
func notPublicAPI(string, string) bool { return false }

// TestAdminSecurityMiddlewareSetsSecurityHeaders locks in that every
// response served through adminSecurityMiddleware — the wrapper main.go
// puts in front of adminMux — carries the baseline security headers, and
// that the wrapper actually forwards to the wrapped handler (next).
func TestAdminSecurityMiddlewareSetsSecurityHeaders(t *testing.T) {
	t.Parallel()

	called := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	})

	handler := adminSecurityMiddleware(next, "static", notPublicAPI)
	req := httptest.NewRequest(http.MethodGet, "/admin/", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if !called {
		t.Fatal("adminSecurityMiddleware did not forward the request to the wrapped admin handler")
	}
	if got := rec.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Fatalf("X-Content-Type-Options = %q, want nosniff", got)
	}
	if got := rec.Header().Get("X-Frame-Options"); got != "DENY" {
		t.Fatalf("X-Frame-Options = %q, want DENY", got)
	}
	if got := rec.Header().Get("Referrer-Policy"); got != "strict-origin-when-cross-origin" {
		t.Fatalf("Referrer-Policy = %q, want strict-origin-when-cross-origin", got)
	}
	if got := rec.Header().Get("Strict-Transport-Security"); got == "" {
		t.Fatal("Strict-Transport-Security missing for tlsMode=static")
	}
}

// TestAdminSecurityMiddlewareOmitsHSTSWhenTLSIsNotStatic guards the
// tlsMode-conditional half of the header logic.
func TestAdminSecurityMiddlewareOmitsHSTSWhenTLSIsNotStatic(t *testing.T) {
	t.Parallel()

	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	handler := adminSecurityMiddleware(next, "disabled", notPublicAPI)
	req := httptest.NewRequest(http.MethodGet, "/admin/", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if got := rec.Header().Get("Strict-Transport-Security"); got != "" {
		t.Fatalf("Strict-Transport-Security = %q, want empty when tlsMode != static", got)
	}
}

// TestAdminSecurityMiddlewareBlocksCrossOriginStateChange locks in the CSRF
// gate: a state-changing request carrying a session cookie and a
// cross-origin Origin header must be rejected before it ever reaches the
// admin mux.
func TestAdminSecurityMiddlewareBlocksCrossOriginStateChange(t *testing.T) {
	t.Parallel()

	called := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	})

	handler := adminSecurityMiddleware(next, "disabled", notPublicAPI)
	req := httptest.NewRequest(http.MethodPost, "/admin/api/settings", nil)
	req.Host = "node.example"
	req.Header.Set("Origin", "https://evil.example")
	req.AddCookie(&http.Cookie{Name: "sdn_wallet_session", Value: "token"})
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if called {
		t.Fatal("admin mux was reached for a cross-origin, cookie-authenticated state-changing request")
	}
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusForbidden)
	}
}

// TestAdminSecurityMiddlewareBlocksMissingOriginStateChange covers the
// "no Origin/Referer and not an AJAX request" CSRF branch.
func TestAdminSecurityMiddlewareBlocksMissingOriginStateChange(t *testing.T) {
	t.Parallel()

	called := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	})

	handler := adminSecurityMiddleware(next, "disabled", notPublicAPI)
	req := httptest.NewRequest(http.MethodPost, "/admin/api/settings", nil)
	req.Host = "node.example"
	req.AddCookie(&http.Cookie{Name: "sdn_wallet_session", Value: "token"})
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if called {
		t.Fatal("admin mux was reached for a cookie-authenticated state-changing request with no Origin/Referer/X-Requested-With")
	}
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusForbidden)
	}
}

// TestAdminSecurityMiddlewareAllowsSameOriginStateChange is the positive
// control proving the CSRF gate is a genuine pass-through wrapper (not
// merely always-block): a same-origin, cookie-authenticated state-changing
// request reaches the admin mux.
func TestAdminSecurityMiddlewareAllowsSameOriginStateChange(t *testing.T) {
	t.Parallel()

	called := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	})

	handler := adminSecurityMiddleware(next, "disabled", notPublicAPI)
	req := httptest.NewRequest(http.MethodPost, "/admin/api/settings", nil)
	req.Host = "node.example"
	req.Header.Set("Origin", "https://node.example")
	req.AddCookie(&http.Cookie{Name: "sdn_wallet_session", Value: "token"})
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if !called {
		t.Fatal("admin mux was not reached for a same-origin, cookie-authenticated state-changing request")
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
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
		return testVectorMnemonic(), nil
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

func TestEnsureNodeMnemonicHonorsKeyPasswordFile(t *testing.T) {
	passwordPath := filepath.Join(t.TempDir(), "key-password")
	const password = "mounted-file-password"
	if err := os.WriteFile(passwordPath, []byte(password+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(config.EnvKeyPassword, "")
	t.Setenv(config.EnvKeyPasswordFile, passwordPath)

	cfg := config.Default()
	cfg.Storage.Path = filepath.Join(t.TempDir(), "data")
	result, err := ensureNodeMnemonic(context.Background(), cfg, func(context.Context) (string, error) {
		return testVectorMnemonic(), nil
	})
	if err != nil {
		t.Fatalf("ensureNodeMnemonic failed: %v", err)
	}
	sealed, err := os.ReadFile(result.Path)
	if err != nil {
		t.Fatal(err)
	}
	got, err := keys.DecryptMnemonic(sealed, password)
	if err != nil {
		t.Fatalf("mnemonic was not sealed under %s: %v", config.EnvKeyPasswordFile, err)
	}
	if got != testVectorMnemonic() {
		t.Fatalf("decrypted mnemonic mismatch")
	}
}

func TestEnsureNodeMnemonicUnreadablePasswordFileCannotMintIdentity(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "not-mounted")
	t.Setenv(config.EnvKeyPassword, "")
	t.Setenv(config.EnvKeyPasswordFile, missing)

	cfg := config.Default()
	cfg.Storage.Path = filepath.Join(t.TempDir(), "data")
	generated := false
	_, err := ensureNodeMnemonic(context.Background(), cfg, func(context.Context) (string, error) {
		generated = true
		return testVectorMnemonic(), nil
	})
	if err == nil || !strings.Contains(err.Error(), config.EnvKeyPasswordFile) {
		t.Fatalf("expected password-file error, got %v", err)
	}
	if generated {
		t.Fatal("mnemonic generator ran despite an unreadable configured password file")
	}
	mnemonicPath := filepath.Join(filepath.Dir(cfg.Storage.Path), "keys", "mnemonic")
	if _, statErr := os.Stat(mnemonicPath); !os.IsNotExist(statErr) {
		t.Fatalf("mnemonic file exists after fail-closed init: %v", statErr)
	}
}

func TestLocalEPMWriteRejectsUnreadableKeyPasswordFile(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "not-mounted")
	t.Setenv(config.EnvKeyPassword, "")
	t.Setenv(config.EnvKeyPasswordFile, missing)
	t.Setenv("SDN_EPM_STORE_PASSWORD", "")

	validator, err := sds.NewValidator(nil)
	if err != nil {
		t.Fatal(err)
	}
	store, err := storage.NewFlatSQLStore(t.TempDir(), validator)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	err = store.SaveLocalEPM("12D3KooWFailClosed", []byte("epm-bytes"))
	if err == nil || !strings.Contains(err.Error(), config.EnvKeyPasswordFile) {
		t.Fatalf("expected password-file error from local EPM write, got %v", err)
	}
	if _, readErr := store.LoadLocalEPM("12D3KooWFailClosed"); readErr == nil {
		t.Fatal("local EPM row was written despite an unreadable password file")
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
		return testVectorMnemonic(), nil
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
	plugin := &runtimeMutationTestPlugin{id: "catalogfixture-provider"}
	if err := mgr.Register(plugin); err != nil {
		t.Fatalf("Register failed: %v", err)
	}
	if err := mgr.StartAll(context.Background(), plugins.RuntimeContext{Mode: "test"}); err != nil {
		t.Fatalf("StartAll failed: %v", err)
	}

	scheduleReq := httptest.NewRequest(
		http.MethodPatch,
		"/api/v1/modules/runtime/catalogfixture-provider/schedules/sync_full_catalog",
		bytes.NewBufferString(`{"enabled":true,"interval":"45m","timezone":"UTC"}`),
	)
	scheduleRecorder := httptest.NewRecorder()
	handleModuleRuntimeMutation(mgr)(scheduleRecorder, scheduleReq)
	if scheduleRecorder.Code != http.StatusBadRequest {
		t.Fatalf("short cadence status = %d, body = %s, want 400", scheduleRecorder.Code, scheduleRecorder.Body.String())
	}

	scheduleReq = httptest.NewRequest(
		http.MethodPatch,
		"/api/v1/modules/runtime/catalogfixture-provider/schedules/sync_full_catalog",
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

	runReq := httptest.NewRequest(http.MethodPost, "/api/v1/modules/runtime/catalogfixture-provider/schedules/sync_full_catalog/run", nil)
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
	layout.WalletWASMDir = filepath.Join(root, "runtime", "ui", "wallet-wasm")
	layout.WalletUIDir = filepath.Join(root, "runtime", "ui", "wallet-ui")
	for _, dir := range []string{layout.WalletWASMDir, layout.WalletUIDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	cfg := config.Default()
	cfg.Admin.FrontendPath = ""
	cfg.Admin.WebuiPath = ""
	cfg.Admin.IPFSAPIURL = ""
	cfg.Admin.IPFSGatewayURL = ""
	// The config defaults name <data>/wallet-wasm and <data>/wallet-ui, which a
	// fresh install has never staged; the bundle's copies must take over.
	cfg.WalletWasm.AssetsDir = filepath.Join(root, "data", "wallet-wasm")
	cfg.WalletWasm.UIAssetsDir = filepath.Join(root, "data", "wallet-ui")

	applyBundleDefaults(cfg, layout)

	if cfg.Admin.FrontendPath != layout.SDNUIPath {
		t.Fatalf("FrontendPath = %q, want %q", cfg.Admin.FrontendPath, layout.SDNUIPath)
	}
	if cfg.Admin.WebuiPath != layout.WebUIPath {
		t.Fatalf("WebuiPath = %q, want %q", cfg.Admin.WebuiPath, layout.WebUIPath)
	}
	if cfg.WalletWasm.AssetsDir != layout.WalletWASMDir {
		t.Fatalf("WalletWasm.AssetsDir = %q, want the bundled %q", cfg.WalletWasm.AssetsDir, layout.WalletWASMDir)
	}
	if cfg.WalletWasm.UIAssetsDir != layout.WalletUIDir {
		t.Fatalf("WalletWasm.UIAssetsDir = %q, want the bundled %q", cfg.WalletWasm.UIAssetsDir, layout.WalletUIDir)
	}

	// An operator-staged tree that exists keeps winning over the bundle's copy.
	staged := filepath.Join(root, "data", "wallet-wasm")
	if err := os.MkdirAll(staged, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg.WalletWasm.AssetsDir = staged
	applyBundleDefaults(cfg, layout)
	if cfg.WalletWasm.AssetsDir != staged {
		t.Fatalf("WalletWasm.AssetsDir = %q, want the operator-staged %q", cfg.WalletWasm.AssetsDir, staged)
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
	sdb, closer, err := flatsqldrv.OpenStandalone(filepath.Join(dir, "sessions.db"))
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

// newAdminSession creates a real session-store-backed *auth.Handler plus a
// valid session token at the given trust level, for tests exercising
// gateAdminOnlyHandler's real RequireAuth path (not a mock).
func newAdminSession(t *testing.T, trust peers.TrustLevel) (*auth.Handler, string) {
	t.Helper()

	dir := t.TempDir()
	sdb, closer, err := flatsqldrv.OpenStandalone(filepath.Join(dir, "sessions.db"))
	if err != nil {
		t.Fatalf("OpenStandalone: %v", err)
	}
	t.Cleanup(func() { _ = closer() })

	sessions, err := auth.NewSessionStore(sdb)
	if err != nil {
		t.Fatalf("NewSessionStore: %v", err)
	}

	token, err := sessions.CreateSession("xpub-test-user", trust, "127.0.0.1", "test-agent", time.Hour)
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	return auth.NewHandler(nil, sessions, time.Hour, "", ""), token
}

// TestGateAdminOnlyHandlerAllowsUnauthenticatedWhenAuthNotRequired locks in
// that gateAdminOnlyHandler is a no-op pass-through when cfg.Admin.RequireAuth
// is false, matching how the flow editor mount behaves when the admin server
// runs without authentication configured.
func TestGateAdminOnlyHandlerAllowsUnauthenticatedWhenAuthNotRequired(t *testing.T) {
	t.Parallel()

	called := false
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/flow-editor/debug/", nil)
	rec := httptest.NewRecorder()
	gateAdminOnlyHandler(rec, req, inner, nil, false)

	if !called {
		t.Fatal("inner handler was not invoked when requireAuth=false")
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
}

// TestGateAdminOnlyHandlerRejectsWhenAuthHandlerUnavailable locks in the
// fail-closed behavior when RequireAuth is set but no auth handler could be
// constructed (e.g. auth store initialization failure): the request must be
// rejected, never routed to the inner handler.
func TestGateAdminOnlyHandlerRejectsWhenAuthHandlerUnavailable(t *testing.T) {
	t.Parallel()

	called := false
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/flow-editor/debug/", nil)
	rec := httptest.NewRecorder()
	gateAdminOnlyHandler(rec, req, inner, nil, true)

	if called {
		t.Fatal("inner handler was invoked despite a nil auth handler with requireAuth=true")
	}
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusServiceUnavailable)
	}
}

// TestGateAdminOnlyHandlerRejectsRequestWithoutSession locks in that an
// unauthenticated request to a gated mount (like the flow editor's /debug/
// stub) is rejected rather than reaching the inner handler — this is the
// regression this fix closes: the editor mount previously bypassed the
// top-level auth wall entirely because it does not live under /api/.
func TestGateAdminOnlyHandlerRejectsRequestWithoutSession(t *testing.T) {
	t.Parallel()

	authHandler, _ := newAdminSession(t, peers.Admin)

	called := false
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/flow-editor/debug/", nil)
	req.Header.Set("Accept", "application/json")
	rec := httptest.NewRecorder()
	gateAdminOnlyHandler(rec, req, inner, authHandler, true)

	if called {
		t.Fatal("inner handler (debug stub) was invoked for an unauthenticated request")
	}
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}
}

// TestGateAdminOnlyHandlerRejectsInsufficientTrust locks in that a session
// below Admin trust (e.g. a Standard user) cannot reach the gated mount —
// the editor requires the same trust level as /admin, not merely "any
// logged-in session".
func TestGateAdminOnlyHandlerRejectsInsufficientTrust(t *testing.T) {
	t.Parallel()

	authHandler, token := newAdminSession(t, peers.Standard)

	called := false
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/flow-editor/debug/", nil)
	req.Header.Set("Accept", "application/json")
	req.AddCookie(&http.Cookie{Name: "sdn_wallet_session", Value: token})
	rec := httptest.NewRecorder()
	gateAdminOnlyHandler(rec, req, inner, authHandler, true)

	if called {
		t.Fatal("inner handler (debug stub) was invoked for a Standard-trust session on an Admin-gated mount")
	}
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusForbidden)
	}
}

// TestGateAdminOnlyHandlerAllowsAuthenticatedAdminSession locks in the
// success path: an Admin-trust session reaches the inner handler.
func TestGateAdminOnlyHandlerAllowsAuthenticatedAdminSession(t *testing.T) {
	t.Parallel()

	authHandler, token := newAdminSession(t, peers.Admin)

	called := false
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/flow-editor/debug/", nil)
	req.AddCookie(&http.Cookie{Name: "sdn_wallet_session", Value: token})
	rec := httptest.NewRecorder()
	gateAdminOnlyHandler(rec, req, inner, authHandler, true)

	if !called {
		t.Fatal("inner handler was not invoked for an authenticated Admin session")
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
}

// TestGateHandlerWithTrustRejectsStandardMountWithoutSession locks in gap
// B10.3(a): a Standard-trust self-gate (the pattern now used for /ws, mirror
// of /webui's existing gate) must reject a request with no session before
// the inner handler is ever invoked.
func TestGateHandlerWithTrustRejectsStandardMountWithoutSession(t *testing.T) {
	t.Parallel()

	authHandler, _ := newAdminSession(t, peers.Admin)

	called := false
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/ws", nil)
	req.Header.Set("Accept", "application/json")
	rec := httptest.NewRecorder()
	gateHandlerWithTrust(rec, req, inner, authHandler, true, peers.Standard)

	if called {
		t.Fatal("inner handler (ws bridge) was invoked for an unauthenticated request")
	}
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}
}

// TestGateHandlerWithTrustAllowsAuthenticatedStandardSession locks in the
// success path for the Standard-trust self-gate: a Standard session reaches
// the inner handler (unlike gateAdminOnlyHandler, this must not require
// Admin trust — the /ws bridge is meant for any logged-in operator client).
func TestGateHandlerWithTrustAllowsAuthenticatedStandardSession(t *testing.T) {
	t.Parallel()

	authHandler, token := newAdminSession(t, peers.Standard)

	called := false
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/ws", nil)
	req.AddCookie(&http.Cookie{Name: "sdn_wallet_session", Value: token})
	rec := httptest.NewRecorder()
	gateHandlerWithTrust(rec, req, inner, authHandler, true, peers.Standard)

	if !called {
		t.Fatal("inner handler was not invoked for an authenticated Standard session")
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
}

// wsGateTestServer builds the exact self-gate pattern registered for /ws in
// runDaemon (gateHandlerWithTrust wrapping api.NewWSHandler at Standard
// trust) behind a real httptest.Server, for end-to-end WebSocket dial tests.
func wsGateTestServer(t *testing.T, authHandler *auth.Handler) *httptest.Server {
	t.Helper()
	wsHandler := api.NewWSHandler(nil, nil)
	serveWS := func(w http.ResponseWriter, r *http.Request) {
		gateHandlerWithTrust(w, r, wsHandler, authHandler, true, peers.Standard)
	}
	srv := httptest.NewServer(http.HandlerFunc(serveWS))
	t.Cleanup(srv.Close)
	return srv
}

// TestWSBridgeGateRejectsUnauthenticatedUpgrade dials a real WebSocket
// handshake against the /ws self-gate with no session cookie: the upgrade
// must fail (no 101 Switching Protocols reaches the bridge).
func TestWSBridgeGateRejectsUnauthenticatedUpgrade(t *testing.T) {
	t.Parallel()

	authHandler, _ := newAdminSession(t, peers.Standard)
	srv := wsGateTestServer(t, authHandler)

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http")
	_, resp, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err == nil {
		t.Fatal("unauthenticated WebSocket dial succeeded, want rejection")
	}
	if resp == nil {
		t.Fatal("expected an HTTP response on dial failure")
	}
	if resp.StatusCode == http.StatusSwitchingProtocols {
		t.Fatal("unauthenticated dial got 101 Switching Protocols, want a non-upgrade response")
	}
}

// TestWSBridgeGateAllowsAuthenticatedSameOriginSubscribeRoundTrip is the
// success path required by gap B10.3: with a valid session cookie, the /ws
// self-gate lets the connection through and the existing subscribe/publish
// round trip still works.
func TestWSBridgeGateAllowsAuthenticatedSameOriginSubscribeRoundTrip(t *testing.T) {
	t.Parallel()

	authHandler, token := newAdminSession(t, peers.Standard)
	srv := wsGateTestServer(t, authHandler)

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http")
	header := http.Header{}
	header.Set("Cookie", (&http.Cookie{Name: "sdn_wallet_session", Value: token}).String())

	conn, resp, err := websocket.DefaultDialer.Dial(wsURL, header)
	if err != nil {
		t.Fatalf("authenticated WebSocket dial failed: %v", err)
	}
	defer conn.Close()
	if resp.StatusCode != http.StatusSwitchingProtocols {
		t.Fatalf("dial status = %d, want %d", resp.StatusCode, http.StatusSwitchingProtocols)
	}

	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	if err := conn.WriteJSON(map[string]string{"type": "subscribe", "schema": "OMM.fbs"}); err != nil {
		t.Fatalf("write subscribe: %v", err)
	}

	var ack struct {
		Type   string `json:"type"`
		Schema string `json:"schema"`
	}
	if err := conn.ReadJSON(&ack); err != nil {
		t.Fatalf("read subscribe ack: %v", err)
	}
	if ack.Type != "subscribed" || ack.Schema != "OMM.fbs" {
		t.Fatalf("subscribe ack = %+v, want type=subscribed schema=OMM.fbs", ack)
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

	// §21 (owner ruling 2026-08-19, memory identity-literal-pubkeys-policy):
	// the PUBLISHED identity is the literal sign/encrypt public keys; the xpub
	// and the derivation paths are PRIVATE. The node EPM projection stopped
	// carrying "xpub" when the flip landed (internal/epm/service_test.go:339
	// asserts info["xpub"] == nil), so the provider descriptor — a public,
	// unauthenticated read — must carry no xpub either. This assertion used to
	// demand "xpub-provider" here and was simply never updated, which left the
	// REQUIRED go-host-tier gate red on main.
	if got := payload.Identity.XPub; got != "" {
		t.Fatalf("identity.xpub = %q, want %q (xpub is PRIVATE under §21)", got, "")
	}
	// The wire form is the surface that matters: `omitempty` must actually
	// drop the key, so a client can never read an xpub off this endpoint.
	wire, err := json.Marshal(payload.Identity)
	if err != nil {
		t.Fatalf("marshal identity: %v", err)
	}
	var wireKeys map[string]json.RawMessage
	if err := json.Unmarshal(wire, &wireKeys); err != nil {
		t.Fatalf("unmarshal identity: %v", err)
	}
	if _, present := wireKeys["xpub"]; present {
		t.Fatalf("provider descriptor identity JSON carries an \"xpub\" key: %s", wire)
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
	if p.id == "catalogfixture-provider" {
		return []plugins.CronMethodSpec{
			{
				Method:          "sync_full_catalog",
				Description:     "Sync CatalogFixture full catalog",
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
	host             libp2phost.Host
	peer             peer.ID
	addrs            []multiaddr.Multiaddr
	epmService       *epm.Service
	grantVerifierHex string
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

func (f fakeProviderDescriptorSource) GrantVerifierPublicKeyHex() string {
	return f.grantVerifierHex
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

// TestIdentityAdvertisesPublicationKey pins the dataset-publication key
// advertisement decision: HD-identity nodes whose publication key is the
// identity Ed25519 signing key already advertise it on the wire EPM (no
// runtime-key injection); every other configuration still injects the key
// via SetRuntimeSigningKey.
func TestIdentityAdvertisesPublicationKey(t *testing.T) {
	t.Parallel()

	_, identityPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey failed: %v", err)
	}
	_, otherPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey failed: %v", err)
	}

	// HD identity, publication key == identity signing key (raw 64-byte form,
	// as node.SigningKey returns): already advertised via the EPM KEYS vector.
	if !identityAdvertisesPublicationKey(true, []byte(identityPriv), identityPriv) {
		t.Fatal("HD identity with matching publication key should be advertised via EPM")
	}
	// Seed form of the same key must also match.
	if !identityAdvertisesPublicationKey(true, identityPriv.Seed(), identityPriv) {
		t.Fatal("HD identity seed-form signing key should match publication key")
	}
	// HD identity but publication key differs: must inject runtime key.
	if identityAdvertisesPublicationKey(true, []byte(identityPriv), otherPriv) {
		t.Fatal("mismatched publication key must not be treated as advertised")
	}
	// HD identity with no exportable signing key: must inject runtime key.
	if identityAdvertisesPublicationKey(true, nil, identityPriv) {
		t.Fatal("missing identity signing key must not be treated as advertised")
	}
	// No HD identity: unchanged legacy behavior, inject runtime key.
	if identityAdvertisesPublicationKey(false, []byte(identityPriv), identityPriv) {
		t.Fatal("non-HD node must keep injecting the runtime signing key")
	}
}

// TestProviderDescriptorAdvertisesTheGrantVerifierKey — the advertisement the Seal
// Council refused while the grant key WAS the fleet update root, and un-refused
// once it became a dedicated hardened child (HEPHAESTUS, 2026-08-07,
// graph/tasks/sdn-grant-verifier-key-domain-separation.md).
//
// It is what makes a client's trustedGrantVerifierPublicKeys cross-check possible
// at all: without it the list is empty and the check is a no-op.
func TestProviderDescriptorAdvertisesTheGrantVerifierKey(t *testing.T) {
	const grantVerifier = "5f1c0a3e9b2d4c8a7e6f0d1b2c3a4e5f60718293a4b5c6d7e8f9012a3b4c5d6e"

	privKey, _, err := crypto.GenerateSecp256k1Key(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateSecp256k1Key failed: %v", err)
	}
	host, err := libp2p.New(libp2p.NoListenAddrs, libp2p.Identity(privKey))
	if err != nil {
		t.Fatalf("libp2p.New failed: %v", err)
	}
	defer host.Close()

	descriptor, err := buildProviderDescriptor(fakeProviderDescriptorSource{
		host:             host,
		peer:             host.ID(),
		grantVerifierHex: grantVerifier,
	})
	if err != nil {
		t.Fatalf("buildProviderDescriptor: %v", err)
	}
	if len(descriptor.GrantVerifierPublicKeys) != 1 || descriptor.GrantVerifierPublicKeys[0] != grantVerifier {
		t.Fatalf("grantVerifierPublicKeys = %v, want [%s]", descriptor.GrantVerifierPublicKeys, grantVerifier)
	}

	// The JSON key must be spelled exactly as sdn-js reads it
	// (ServerDescriptor.grantVerifierPublicKeys). A synthesized API field, so
	// lowerCamel — not an SDS record key.
	encoded, err := json.Marshal(descriptor)
	if err != nil {
		t.Fatalf("marshal descriptor: %v", err)
	}
	if !strings.Contains(string(encoded), `"grantVerifierPublicKeys":["`+grantVerifier+`"]`) {
		t.Fatalf("descriptor JSON does not carry grantVerifierPublicKeys as sdn-js spells it: %s", encoded)
	}

	// A node that cannot sign grants advertises NO key rather than an empty
	// string: an empty entry would be pinned by a client as a real key and would
	// reject every grant.
	silent, err := buildProviderDescriptor(fakeProviderDescriptorSource{
		host: host,
		peer: host.ID(),
	})
	if err != nil {
		t.Fatalf("buildProviderDescriptor (no grant key): %v", err)
	}
	if len(silent.GrantVerifierPublicKeys) != 0 {
		t.Fatalf("a node with no grant key advertised %v", silent.GrantVerifierPublicKeys)
	}
	encodedSilent, err := json.Marshal(silent)
	if err != nil {
		t.Fatalf("marshal silent descriptor: %v", err)
	}
	if strings.Contains(string(encodedSilent), "grantVerifierPublicKeys") {
		t.Fatalf("the field should be omitted entirely when absent: %s", encodedSilent)
	}
}
