// Command js-interop-host is the Go half of the sdn-js <-> go-libp2p interop gate.
//
// WHY THIS EXISTS. sdn-js is the BROWSER CLIENT for a network whose servers run
// go-libp2p. Every other test in sdn-js mocks libp2p or replays recorded bytes,
// so a build can compile, type-check and pass the whole unit suite while being
// completely unable to dial a node. This binary closes that hole: it stands up a
// real go-libp2p host, on the same go-libp2p release sdn-server pins, speaking
// the real SDN protocol IDs, and lets a JS test dial it over a real socket.
//
// It is a TEST FIXTURE, not a product surface: it stores nothing, signs nothing
// and serves canned deterministic replies. sdn-js/src/go-libp2p-interop.test.ts
// spawns it, reads the JSON handshake line from stdout, dials it, and asserts on
// the reply bytes.
//
// Protocol shape, matching how every SDN request/response protocol behaves
// (see internal/datasync/datasync.go and internal/modulert/module.go): the
// dialer writes one payload, half-closes the write side, the handler reads to
// EOF, writes one reply and closes. That exercises multistream-select, the noise
// handshake, the yamux muxer and stream half-close semantics end to end.
//
// Usage:
//
//	go run ./cmd/js-interop-host            # listens on 127.0.0.1, every
//	                                        # transport the node offers
//
// Stdout, first line, exactly one JSON object:
//
//	{"peerId":"12D3Koo...","addrs":[...],"wsAddr":"...","tcpAddr":"...",
//	 "webTransportAddr":"...","webRtcDirectAddr":"..."}
//
// The webtransport and webrtc-direct addresses are the DIRECT ones — a browser
// dials them with no relay and no reverse proxy, which is how a browser client
// is supposed to reach an SDN node. They carry /certhash components because
// both transports authenticate a self-signed certificate by hash rather than
// through a CA, and those components are what make a browser accept the
// connection.
// The process runs until stdin reaches EOF or it is signalled.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/protocol"
	noise "github.com/libp2p/go-libp2p/p2p/security/noise"
	libp2ptls "github.com/libp2p/go-libp2p/p2p/security/tls"
	"github.com/multiformats/go-multiaddr"

	"github.com/spacedatanetwork/sdn-server/internal/libp2ptransports"
)

// The SDN request/response protocol IDs a browser client dials. Kept as literals
// on purpose: if either side ever renames one, this fixture must be updated
// deliberately rather than following a shared constant silently.
const (
	flatSQLSyncProtocol    = protocol.ID("/space-data-network/flatsql-sync/1.0.0")
	moduleDeliveryProtocol = protocol.ID("/space-data-network/module-delivery/1.0.0")
	idExchangeProtocol     = protocol.ID("/space-data-network/id-exchange/1.0.0")
	echoProtocol           = protocol.ID("/space-data-network/interop-echo/1.0.0")
)

// replyPrefix is prepended to the request payload so the JS side can prove the
// bytes it read came from THIS handler and were not echoed by its own transport.
const replyPrefix = "go-libp2p:"

type handshake struct {
	PeerID           string   `json:"peerId"`
	Addrs            []string `json:"addrs"`
	WSAddr           string   `json:"wsAddr"`
	TCPAddr          string   `json:"tcpAddr"`
	WebTransportAddr string   `json:"webTransportAddr"`
	WebRtcDirectAddr string   `json:"webRtcDirectAddr"`
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "js-interop-host: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	// libp2ptransports.Options is the node's OWN transport list, not a copy of
	// it (internal/node re-exports this very function). A browser interop test
	// that registers its own transports proves only that the test's transports
	// work. The list lives in a leaf package so THIS fixture builds with a
	// plain `go build`, with no WasmEdge headers on the machine.
	opts := append([]libp2p.Option{
		libp2p.ListenAddrStrings(
			"/ip4/127.0.0.1/tcp/0/ws",
			"/ip4/127.0.0.1/tcp/0",
			// The two transports a browser can dial DIRECTLY.
			"/ip4/127.0.0.1/udp/0/quic-v1/webtransport",
			"/ip4/127.0.0.1/udp/0/webrtc-direct",
		),
		// Mirror sdn-server/internal/node/node.go: offer BOTH tls and noise and
		// let the dialer choose, so the JS client's noise-only stack has to
		// negotiate rather than be handed its only option.
		libp2p.Security(libp2ptls.ID, libp2ptls.New),
		libp2p.Security(noise.ID, noise.New),
		libp2p.DisableRelay(),
	}, libp2ptransports.Options(nil)...)

	h, err := libp2p.New(opts...)
	if err != nil {
		return fmt.Errorf("create host: %w", err)
	}
	defer h.Close()

	for _, id := range []protocol.ID{
		flatSQLSyncProtocol,
		moduleDeliveryProtocol,
		idExchangeProtocol,
		echoProtocol,
	} {
		h.SetStreamHandler(id, handleExchange)
	}

	hs, err := describe(h)
	if err != nil {
		return err
	}
	line, err := json.Marshal(hs)
	if err != nil {
		return fmt.Errorf("marshal handshake: %w", err)
	}
	fmt.Println(string(line))
	_ = os.Stdout.Sync()

	// Exit when the parent closes stdin, so a crashed test never leaks a host.
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = io.Copy(io.Discard, os.Stdin)
	}()

	select {
	case <-ctx.Done():
	case <-done:
	}
	return nil
}

func describe(h host.Host) (handshake, error) {
	hs := handshake{PeerID: h.ID().String()}
	for _, addr := range h.Addrs() {
		full := fmt.Sprintf("%s/p2p/%s", addr.String(), h.ID().String())
		hs.Addrs = append(hs.Addrs, full)
		if strings.HasSuffix(addr.String(), "/ws") {
			if hs.WSAddr == "" {
				hs.WSAddr = full
			}
			continue
		}
		if strings.Contains(addr.String(), "/webtransport") {
			if hs.WebTransportAddr == "" {
				hs.WebTransportAddr = full
			}
			continue
		}
		if strings.Contains(addr.String(), "/webrtc-direct") {
			if hs.WebRtcDirectAddr == "" {
				hs.WebRtcDirectAddr = full
			}
			continue
		}
		if hs.TCPAddr == "" && isPlainTCP(addr) {
			hs.TCPAddr = full
		}
	}
	// Both DIRECT addresses are required, not best-effort: a browser that
	// cannot dial them has no unrelayed path to a node, and a fixture that
	// quietly omits one turns its interop test into a no-op.
	if hs.WebTransportAddr == "" {
		return hs, fmt.Errorf("no webtransport listen address; got %v", hs.Addrs)
	}
	if hs.WebRtcDirectAddr == "" {
		return hs, fmt.Errorf("no webrtc-direct listen address; got %v", hs.Addrs)
	}
	if hs.WSAddr == "" {
		return hs, fmt.Errorf("no websocket listen address; got %v", hs.Addrs)
	}
	return hs, nil
}

func isPlainTCP(addr multiaddr.Multiaddr) bool {
	s := addr.String()
	return strings.Contains(s, "/tcp/") && !strings.Contains(s, "/ws")
}

// handleExchange implements the SDN request/response shape: read the request to
// EOF (the dialer half-closes), write one reply, close.
func handleExchange(s network.Stream) {
	defer s.Close()

	req, err := io.ReadAll(s)
	if err != nil {
		_ = s.Reset()
		return
	}
	reply := append([]byte(replyPrefix), req...)
	if _, err := s.Write(reply); err != nil {
		_ = s.Reset()
		return
	}
	_ = s.CloseWrite()
}
