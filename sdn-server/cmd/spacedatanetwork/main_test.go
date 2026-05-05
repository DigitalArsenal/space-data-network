package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/crypto"
	libp2phost "github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"
	_ "github.com/mattn/go-sqlite3"
	"github.com/multiformats/go-multiaddr"

	"github.com/spacedatanetwork/sdn-server/internal/auth"
	"github.com/spacedatanetwork/sdn-server/internal/epm"
	"github.com/spacedatanetwork/sdn-server/internal/license"
	"github.com/spacedatanetwork/sdn-server/internal/peers"
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
	if optionPayload.Key != "timer.refresh-grants.interval" || optionPayload.Value != "45000" || optionPayload.Persistence != "live-only" {
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
	if !bytes.Contains([]byte(defaultFrontendHTML), []byte(`href="https://spacedatanet.org"`)) {
		t.Fatal("default frontend should link to spacedatanet.org documentation")
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
	sdb, err := sql.Open("sqlite3", filepath.Join(dir, "sessions.db"))
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	defer sdb.Close()

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
