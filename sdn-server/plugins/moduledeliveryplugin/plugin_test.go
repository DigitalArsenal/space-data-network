package moduledeliveryplugin

import (
	"context"
	"crypto/rand"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/protocol"

	"github.com/spacedatanetwork/sdn-server/plugins"
)

func TestPluginStartInitializesModuleDeliveryService(t *testing.T) {
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

	ipfsServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"Hash":"bafy-plugin-cid","Size":"1"}`))
	}))
	defer ipfsServer.Close()

	plugin := New()
	if err := plugin.Start(context.Background(), plugins.RuntimeContext{
		Host:         host,
		BaseDataPath: t.TempDir(),
		PeerID:       host.ID().String(),
		IPFSAPIURL:   ipfsServer.URL,
	}); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer plugin.Close()

	if plugin.Service() == nil {
		t.Fatal("expected module delivery service")
	}
	if plugin.LicenseService() == nil {
		t.Fatal("expected embedded license service")
	}
	if plugin.TokenVerifier() == nil {
		t.Fatal("expected token verifier")
	}
	if plugin.DiscoveryCID() == "" {
		t.Fatal("expected discovery CID")
	}
}

func TestPluginStartRegistersModuleDeliveryProtocolHandler(t *testing.T) {
	t.Parallel()

	privKey, _, err := crypto.GenerateSecp256k1Key(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateSecp256k1Key failed: %v", err)
	}

	serverHost, err := libp2p.New(libp2p.Identity(privKey))
	if err != nil {
		t.Fatalf("libp2p.New(server) failed: %v", err)
	}
	defer serverHost.Close()

	clientHost, err := libp2p.New()
	if err != nil {
		t.Fatalf("libp2p.New(client) failed: %v", err)
	}
	defer clientHost.Close()

	ipfsServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"Hash":"bafy-plugin-cid","Size":"1"}`))
	}))
	defer ipfsServer.Close()

	plugin := New()
	if err := plugin.Start(context.Background(), plugins.RuntimeContext{
		Host:         serverHost,
		BaseDataPath: t.TempDir(),
		PeerID:       serverHost.ID().String(),
		IPFSAPIURL:   ipfsServer.URL,
	}); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer plugin.Close()

	if err := clientHost.Connect(context.Background(), peer.AddrInfo{ID: serverHost.ID(), Addrs: serverHost.Addrs()}); err != nil {
		t.Fatalf("client connect failed: %v", err)
	}

	stream, err := clientHost.NewStream(context.Background(), serverHost.ID(), protocol.ID("/space-data-network/module-delivery/1.0.0"))
	if err != nil {
		t.Fatalf("NewStream failed: %v", err)
	}
	if err := stream.CloseWrite(); err != nil {
		t.Fatalf("CloseWrite failed: %v", err)
	}
	responseBytes, err := io.ReadAll(stream)
	if err != nil {
		t.Fatalf("ReadAll failed: %v", err)
	}
	if len(responseBytes) == 0 {
		t.Fatal("expected protocol handler response bytes")
	}
}
