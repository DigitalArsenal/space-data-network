package node

import (
	"testing"

	"github.com/multiformats/go-multiaddr"
)

const testAnnounceWSS = "/dns4/sdn.spaceaware.io/tcp/443/wss"

// TestAnnounceAddrsAreAppendedToBoundAddrs pins the mechanism that lets a
// TLS-fronted node tell anyone where to reach it.
//
// This node's browser-reachable endpoint is NOT a libp2p listener: TLS
// terminates on its own :443 HTTPS server, which reverse-proxies
// /p2p/<peerid> websocket upgrades to the loopback libp2p listener. libp2p has
// no way to infer that, so identify, delegated routing and the provider
// descriptor all advertised only `/ws` — mixed content, unusable from every
// HTTPS page on the internet.
func TestAnnounceAddrsAreAppendedToBoundAddrs(t *testing.T) {
	t.Parallel()

	factory, rendered := announceAddrsFactory([]string{testAnnounceWSS})
	if factory == nil {
		t.Fatalf("announceAddrsFactory returned nil for a valid announce address")
	}
	if len(rendered) != 1 || rendered[0] != testAnnounceWSS {
		t.Fatalf("rendered = %v, want [%s]", rendered, testAnnounceWSS)
	}

	bound := mustMultiaddrs(t, "/ip4/0.0.0.0/tcp/4004/ws", "/ip4/127.0.0.1/tcp/18080/ws")
	got := factory(bound)

	if len(got) != 3 {
		t.Fatalf("factory must APPEND, never replace: got %d addrs, want 3 (%v)", len(got), got)
	}
	if !containsAddr(got, testAnnounceWSS) {
		t.Fatalf("announced address %s missing from %v", testAnnounceWSS, got)
	}
	for _, want := range bound {
		if !containsAddr(got, want.String()) {
			t.Fatalf("bound address %s was dropped; announce must never remove a real listener", want)
		}
	}
}

// TestAnnounceAddrsDeduplicate guards against advertising the same address
// twice when an operator announces something libp2p already binds.
func TestAnnounceAddrsDeduplicate(t *testing.T) {
	t.Parallel()

	factory, _ := announceAddrsFactory([]string{"/ip4/104.131.11.220/tcp/4004/ws"})
	if factory == nil {
		t.Fatalf("announceAddrsFactory returned nil")
	}
	got := factory(mustMultiaddrs(t, "/ip4/104.131.11.220/tcp/4004/ws"))
	if len(got) != 1 {
		t.Fatalf("duplicate announce entry must collapse, got %v", got)
	}
}

// TestAnnounceAddrsIgnoreMalformedEntries: a typo must not take the node's
// whole address list down with it, and it must not pass silently either — an
// announce address is the ONLY browser-dialable path on a TLS-fronted node.
func TestAnnounceAddrsIgnoreMalformedEntries(t *testing.T) {
	t.Parallel()

	factory, rendered := announceAddrsFactory([]string{"  ", "not-a-multiaddr", testAnnounceWSS})
	if factory == nil {
		t.Fatalf("one bad entry must not discard the good ones")
	}
	if len(rendered) != 1 || rendered[0] != testAnnounceWSS {
		t.Fatalf("rendered = %v, want only the valid entry", rendered)
	}
}

// TestNoAnnounceLeavesDefaultBehaviour: with nothing configured the node must
// keep go-libp2p's own address behaviour, not an identity factory of ours.
func TestNoAnnounceLeavesDefaultBehaviour(t *testing.T) {
	t.Parallel()

	if factory, rendered := announceAddrsFactory(nil); factory != nil || rendered != nil {
		t.Fatalf("no announce config must install no AddrsFactory, got factory=%v rendered=%v", factory != nil, rendered)
	}
	if factory, _ := announceAddrsFactory([]string{"", "   "}); factory != nil {
		t.Fatalf("blank announce entries must install no AddrsFactory")
	}
}

func mustMultiaddrs(t *testing.T, addrs ...string) []multiaddr.Multiaddr {
	t.Helper()
	out := make([]multiaddr.Multiaddr, 0, len(addrs))
	for _, a := range addrs {
		ma, err := multiaddr.NewMultiaddr(a)
		if err != nil {
			t.Fatalf("multiaddr.NewMultiaddr(%q): %v", a, err)
		}
		out = append(out, ma)
	}
	return out
}

func containsAddr(addrs []multiaddr.Multiaddr, want string) bool {
	for _, a := range addrs {
		if a.String() == want {
			return true
		}
	}
	return false
}
