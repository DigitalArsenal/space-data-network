package node

import (
	"strings"
	"testing"

	"github.com/libp2p/go-libp2p"
)

// The default config ships a /udp/4001/quic-v1 listen address; a host built
// with the node's transport set must be able to bind it (regression: QUIC
// was omitted from the explicit transport list, silently disabling it).
func TestHostTransportsBindDefaultListenAddressFamilies(t *testing.T) {
	options := append([]libp2p.Option{
		libp2p.ListenAddrStrings(
			"/ip4/127.0.0.1/tcp/0",
			"/ip4/127.0.0.1/tcp/0/ws",
			"/ip4/127.0.0.1/udp/0/quic-v1",
		),
	}, hostTransportOptions(nil)...)
	host, err := libp2p.New(options...)
	if err != nil {
		t.Fatalf("libp2p.New with node transports: %v", err)
	}
	defer host.Close()

	var hasTCP, hasWS, hasQUIC bool
	for _, addr := range host.Addrs() {
		s := addr.String()
		if strings.Contains(s, "/quic-v1") {
			hasQUIC = true
		} else if strings.Contains(s, "/ws") {
			hasWS = true
		} else if strings.Contains(s, "/tcp/") {
			hasTCP = true
		}
	}
	if !hasTCP || !hasWS || !hasQUIC {
		t.Fatalf("missing transport listen addrs (tcp=%t ws=%t quic=%t): %v", hasTCP, hasWS, hasQUIC, host.Addrs())
	}
}
