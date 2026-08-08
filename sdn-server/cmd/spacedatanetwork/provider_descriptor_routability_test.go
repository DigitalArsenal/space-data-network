package main

import (
	"crypto/rand"
	"testing"

	libp2p "github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/crypto"
	libp2phost "github.com/libp2p/go-libp2p/core/host"
	"github.com/multiformats/go-multiaddr"
)

// The public provider descriptor is a DIAL PLAN, not a status page: when
// relayAddresses is non-empty a client uses it as its entire candidate list and
// skips DHT discovery, then walks the candidates sequentially re-sending the
// same stamped request frame. host-01 was serving
// /ip4/127.0.0.1/tcp/4004/ws and /ip4/127.0.0.1/tcp/18080/ws on the open
// internet — two of four candidates that every remote browser had to fail
// through before reaching a real one, inside a challenge window that was itself
// only five seconds wide.

func TestProviderDescriptorDropsAddressesNoOtherHostCanDial(t *testing.T) {
	t.Parallel()

	host := newSecp256k1TestHost(t)

	addrs := mustMultiaddrs(t,
		"/ip4/159.203.150.8/tcp/4004/ws",      // public: keep
		"/ip4/127.0.0.1/tcp/4004/ws",          // loopback: drop
		"/ip4/127.0.0.1/tcp/18080/ws",         // loopback: drop
		"/ip6/::1/tcp/4004/ws",                // loopback: drop
		"/ip4/0.0.0.0/tcp/4004/ws",            // unspecified: drop
		"/ip6/::/tcp/4004/ws",                 // unspecified: drop
		"/ip4/169.254.10.4/tcp/4004/ws",       // link-local: drop
		"/ip4/10.17.0.5/tcp/4004/ws",          // private: KEEP, dialable on a LAN
		"/dns4/relay.example.com/tcp/443/wss", // name: KEEP, the client resolves it
	)

	descriptor, err := buildProviderDescriptor(fakeProviderDescriptorSource{
		host:  host,
		peer:  host.ID(),
		addrs: addrs,
	})
	if err != nil {
		t.Fatalf("buildProviderDescriptor failed: %v", err)
	}

	want := []string{
		"/ip4/159.203.150.8/tcp/4004/ws",
		"/ip4/10.17.0.5/tcp/4004/ws",
		"/dns4/relay.example.com/tcp/443/wss",
	}
	if len(descriptor.RelayAddresses) != len(want) {
		t.Fatalf("relayAddresses = %#v, want %#v", descriptor.RelayAddresses, want)
	}
	for i, addr := range want {
		if descriptor.RelayAddresses[i] != addr {
			t.Fatalf("relayAddresses[%d] = %q, want %q (order must follow ListenAddrs)", i, descriptor.RelayAddresses[i], addr)
		}
	}
}

// A node that listens ONLY on loopback must publish an EMPTY candidate list
// rather than a list of lies. Empty is the safe answer: the client falls back
// to DHT discovery, which is exactly what it did before descriptors carried
// addresses at all.
func TestProviderDescriptorPublishesNothingRatherThanLoopbackOnly(t *testing.T) {
	t.Parallel()

	host := newSecp256k1TestHost(t)

	descriptor, err := buildProviderDescriptor(fakeProviderDescriptorSource{
		host:  host,
		peer:  host.ID(),
		addrs: mustMultiaddrs(t, "/ip4/127.0.0.1/tcp/4004/ws", "/ip6/::1/tcp/4004/ws"),
	})
	if err != nil {
		t.Fatalf("buildProviderDescriptor failed: %v", err)
	}
	if len(descriptor.RelayAddresses) != 0 {
		t.Fatalf("relayAddresses = %#v, want none", descriptor.RelayAddresses)
	}
}

// The descriptor derives its publicKey from a secp256k1 peer identity, which is
// what the node actually runs; a default Ed25519 test host fails before the
// address filter is ever reached.
func newSecp256k1TestHost(t *testing.T) libp2phost.Host {
	t.Helper()

	privKey, _, err := crypto.GenerateSecp256k1Key(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateSecp256k1Key failed: %v", err)
	}
	host, err := libp2p.New(libp2p.NoListenAddrs, libp2p.Identity(privKey))
	if err != nil {
		t.Fatalf("libp2p.New failed: %v", err)
	}
	t.Cleanup(func() { _ = host.Close() })
	return host
}

func mustMultiaddrs(t *testing.T, values ...string) []multiaddr.Multiaddr {
	t.Helper()

	out := make([]multiaddr.Multiaddr, 0, len(values))
	for _, value := range values {
		addr, err := multiaddr.NewMultiaddr(value)
		if err != nil {
			t.Fatalf("NewMultiaddr(%q) failed: %v", value, err)
		}
		out = append(out, addr)
	}
	return out
}
