package main

import (
	"testing"

	"github.com/multiformats/go-multiaddr"
)

// THE CONTRACT THIS FILE PINS
//
// /api/module-delivery/provider is the FIRST thing a browser reads and the only
// place it learns where to dial. On 2026-08-08 it advertised exactly two
// addresses, both plain `/ws`:
//
//	"relayAddresses":["/ip4/104.131.11.220/tcp/4004/ws",
//	                  "/ip4/159.203.150.8/tcp/4004/ws"]
//
// A page on an HTTPS origin may not open a cleartext WebSocket, so BOTH are
// discarded by the client before a dial is attempted (sdn-js
// isBrowserDialableAddr, OrbPro isBrowserDialableRelay). The descriptor
// therefore contributed ZERO usable addresses, and the lane worked only because
// one client library happens to carry a hardcoded fallback constant. A hardcoded
// constant in one client is not a contract: any other consumer of this
// descriptor gets nothing and fails on its first attempt.
//
// Meanwhile the node's own :443 HTTPS listener reverse-proxies /p2p/<peerid>
// websocket upgrades to the loopback libp2p listener, and
// `/dns4/sdn.spaceaware.io/tcp/443/wss` was verified live to complete a 101
// upgrade and negotiate multistream. The working address existed and was never
// advertised.

func TestProviderDescriptorDropsUnroutableAddresses(t *testing.T) {
	t.Parallel()

	// NO other host can dial these, whoever it is. Private ranges are
	// deliberately NOT in this list: on a LAN or a docker network they are the
	// only address that works (see dialableFromAnotherHost).
	unroutable := []string{
		// Both of these were served to remote browsers verbatim.
		"/ip4/127.0.0.1/tcp/4004/ws",
		"/ip4/127.0.0.1/tcp/18080/ws",
		"/ip4/0.0.0.0/tcp/4004/ws",
		"/ip4/169.254.10.1/tcp/4004/ws",
	}
	for _, addr := range unroutable {
		if dialableFromAnotherHost(multiaddr.StringCast(addr)) {
			t.Fatalf("%q must not be advertised to remote clients: it can only ever burn a dial attempt", addr)
		}
	}
}

func TestProviderDescriptorKeepsRoutableAddresses(t *testing.T) {
	t.Parallel()

	routable := []string{
		"/ip4/104.131.11.220/tcp/4004/ws",
		"/ip4/159.203.150.8/tcp/4004/ws",
		// The TLS-terminated browser lane. This is the entry that makes the
		// descriptor usable from an HTTPS page at all.
		"/dns4/sdn.spaceaware.io/tcp/443/wss",
		"/dns4/sdn.spaceaware.io/tcp/443/wss/p2p/16Uiu2HAm1LbvwjEHW2GDP2ZQZvwHLZrz2jbYoRLQmJEQ3wZ5Fm45",
		// AutoTLS libp2p.direct names must survive the filter too: they are the
		// other CA-authenticated, browser-dialable shape in the fleet.
		"/dns4/167-172-219-213.kzwfwjn5ji4purkaknrwvpronfvibl14m4uj4zvolmjz02gajewxm0fo7nsyagx.libp2p.direct/tcp/4001/tls/ws",
		// Private ranges are KEPT: a node whose descriptor is served on the same
		// network it listens on is a supported deployment.
		"/ip4/192.168.1.10/tcp/4004/ws",
	}
	for _, addr := range routable {
		if !dialableFromAnotherHost(multiaddr.StringCast(addr)) {
			t.Fatalf("%q must be advertised: it is dialable by a remote client", addr)
		}
	}
}
