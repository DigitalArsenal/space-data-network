package node

import (
	"crypto/tls"

	"github.com/libp2p/go-libp2p"
	libp2pquic "github.com/libp2p/go-libp2p/p2p/transport/quic"
	"github.com/libp2p/go-libp2p/p2p/transport/tcp"
	webrtc "github.com/libp2p/go-libp2p/p2p/transport/webrtc"
	"github.com/libp2p/go-libp2p/p2p/transport/websocket"
	webtransport "github.com/libp2p/go-libp2p/p2p/transport/webtransport"
)

// hostTransportOptions returns the transports the full node registers.
// Registering explicit transports disables libp2p's defaults, so every
// listen-address family in the default config must appear here — including
// QUIC for /udp/4001/quic-v1.
//
// wsTLS, when non-nil, is the AutoTLS certificate source (see autotls.go).
// Without it the websocket transport REFUSES to listen on a `/tls/sni/…/ws`
// address, so the browser-dialable half of the node exists only when the
// certificate connector supplied its TLS config — it can never come up
// unauthenticated by accident.
func hostTransportOptions(wsTLS *tls.Config) []libp2p.Option {
	wsOptions := []interface{}{}
	if wsTLS != nil {
		wsOptions = append(wsOptions, websocket.WithTLSConfig(wsTLS))
	}
	opts := []libp2p.Option{
		libp2p.Transport(tcp.NewTCPTransport),
		libp2p.Transport(libp2pquic.NewTransport),
		libp2p.Transport(websocket.New, wsOptions...),
		libp2p.Transport(webtransport.New),
		libp2p.Transport(webrtc.New),
	}
	if wsTLS != nil {
		// The AutoTLS websocket address derives from an existing /tcp listener
		// and reuses its port; without a shared listener the second bind fails
		// with "address already in use" and the node loses plain TCP peering.
		opts = append(opts, libp2p.ShareTCPListener())
	}
	return opts
}
