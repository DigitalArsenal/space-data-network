package main

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/crypto"
	libp2phost "github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/multiformats/go-multiaddr"

	"github.com/spacedatanetwork/sdn-server/internal/epm"
	"github.com/spacedatanetwork/sdn-server/internal/peers"
	"github.com/spacedatanetwork/sdn-server/internal/wasm"
)

func TestIsPublicAPIPathAllowsProviderDescriptorRoute(t *testing.T) {
	t.Parallel()

	if !isPublicAPIPath("/api/module-delivery/provider") {
		t.Fatal("expected provider descriptor route to be public")
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
