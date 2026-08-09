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

// TestAnnouncedWSSNeverHijacksTheLocalProxyTarget guards a hazard created by
// advertising an address the node does not bind.
//
// resolveLocalLibp2pWsProxyTarget reads the node's ADVERTISED address list to
// find the cleartext loopback websocket listener it should reverse-proxy
// /p2p/<peerid> upgrades into. Once `network.announce` puts
// /dns4/sdn.spaceaware.io/tcp/443/wss on that list, the resolver sees an address
// that looks like a websocket listener and is not one — it is this very server's
// public TLS front door. Selecting it would make the node proxy its own :443
// into itself: an infinite loop dressed as a peering outage.
//
// The /wss rejection in plainLocalWsListener already prevents that; this test
// makes it non-negotiable.
func TestAnnouncedWSSNeverHijacksTheLocalProxyTarget(t *testing.T) {
	t.Parallel()

	advertised := []string{
		"/dns4/sdn.spaceaware.io/tcp/443/wss",
		"/ip4/104.131.11.220/tcp/4004/ws",
		"/ip4/127.0.0.1/tcp/18080/ws",
	}
	target, source := resolveLocalLibp2pWsProxyTarget(advertised)
	if target == nil {
		t.Fatalf("no proxy target selected from %v", advertised)
	}
	if got := target.String(); got != "http://127.0.0.1:18080" {
		t.Fatalf("proxy target = %s (from %q), want the cleartext LOOPBACK listener http://127.0.0.1:18080",
			got, source)
	}

	// And with only the announced wss plus the public cleartext listener, it
	// must fall back to the public listener's port on loopback — never to 443.
	target, _ = resolveLocalLibp2pWsProxyTarget([]string{
		"/dns4/sdn.spaceaware.io/tcp/443/wss",
		"/ip4/104.131.11.220/tcp/4004/ws",
	})
	if target == nil || target.String() != "http://127.0.0.1:4004" {
		t.Fatalf("fallback proxy target = %v, want http://127.0.0.1:4004", target)
	}

	// An announce list containing ONLY the TLS front door must select nothing.
	if target, _ := resolveLocalLibp2pWsProxyTarget([]string{"/dns4/sdn.spaceaware.io/tcp/443/wss"}); target != nil {
		t.Fatalf("selected %s as a proxy target; the node must never proxy its own TLS listener into itself", target)
	}
}
