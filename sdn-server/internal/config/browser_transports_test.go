package config

import (
	"strings"
	"testing"
)

// A browser has exactly two unrelayed routes to an SDN node: webtransport and
// webrtc-direct. Registering the transports is not enough — a transport with
// no listen address never listens, and webtransport was in
// node.HostTransportOptions from the day it was written while no config
// anywhere declared an address for it, so no node ever offered a browser that
// path. This asserts the DEFAULT config declares both.
func TestDefaultConfigListensOnBothBrowserDirectTransports(t *testing.T) {
	listen := Default().Network.Listen

	var webTransport, webRTCDirect string
	for _, addr := range listen {
		if strings.Contains(addr, "/webtransport") {
			webTransport = addr
		}
		if strings.Contains(addr, "/webrtc-direct") {
			webRTCDirect = addr
		}
	}
	if webTransport == "" {
		t.Fatalf("no webtransport listen address in the default config; got %v", listen)
	}
	if webRTCDirect == "" {
		t.Fatalf("no webrtc-direct listen address in the default config; got %v", listen)
	}

	// Webtransport must RIDE the QUIC port rather than claim another one:
	// go-libp2p multiplexes them, and an extra open UDP port on every node is
	// a firewall conversation nobody asked for.
	quic := ""
	for _, addr := range listen {
		if strings.Contains(addr, "/quic-v1") && !strings.Contains(addr, "/webtransport") {
			quic = addr
			break
		}
	}
	if quic == "" {
		t.Fatalf("no plain quic-v1 listen address to share; got %v", listen)
	}
	if !strings.HasPrefix(webTransport, quic+"/webtransport") {
		t.Fatalf("webtransport %q does not share the quic port %q", webTransport, quic)
	}
}
