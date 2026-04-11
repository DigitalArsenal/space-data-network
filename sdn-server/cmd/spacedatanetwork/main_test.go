package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/crypto"
	libp2phost "github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/multiformats/go-multiaddr"
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
}

type fakeProviderDescriptorSource struct {
	host  libp2phost.Host
	peer  peer.ID
	addrs []multiaddr.Multiaddr
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
