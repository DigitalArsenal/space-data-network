package node

import (
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
func hostTransportOptions() []libp2p.Option {
	return []libp2p.Option{
		libp2p.Transport(tcp.NewTCPTransport),
		libp2p.Transport(libp2pquic.NewTransport),
		libp2p.Transport(websocket.New),
		libp2p.Transport(webtransport.New),
		libp2p.Transport(webrtc.New),
	}
}
