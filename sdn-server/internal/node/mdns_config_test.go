package node

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/zeroconf/v2"

	"github.com/spacedatanetwork/sdn-server/internal/config"
)

// mdnsAnswers reports whether an mDNS query for this network's service name
// gets, within wait, an answer naming id (the libp2p mDNS TXT records carry
// each address as dnsaddr=<multiaddr>/p2p/<id>).
func mdnsAnswers(t *testing.T, id peer.ID, wait time.Duration) bool {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), wait)
	defer cancel()
	entries := make(chan *zeroconf.ServiceEntry, 64)
	found := make(chan struct{}, 1)
	go func() {
		for entry := range entries {
			for _, txt := range entry.Text {
				if strings.Contains(txt, id.String()) {
					select {
					case found <- struct{}{}:
					default:
					}
				}
			}
		}
	}()
	browsed := make(chan error, 1)
	go func() { browsed <- zeroconf.Browse(ctx, MDNSServiceName, "local", entries) }()
	select {
	case <-found:
		return true
	case <-ctx.Done():
		if err := <-browsed; err != nil && ctx.Err() == nil {
			t.Logf("mDNS query could not run here: %v", err)
		}
		return false
	}
}

// A node configured with peers.enable_mdns: false starts no mDNS service:
// runMDNS returns at once instead of serving until the node stops, and an
// mDNS query for the network's service name gets no answer naming the node.
// (Before this gate the flag was read by nothing: the node announced itself
// and dialed every SDN node it heard on the local network.)
func TestDisabledMDNSStartsNoServiceAndAnswersNoQuery(t *testing.T) {
	h, err := libp2p.New(libp2p.ListenAddrStrings("/ip4/127.0.0.1/tcp/0"))
	if err != nil {
		t.Fatalf("libp2p host: %v", err)
	}
	defer h.Close()

	cfg := config.Default()
	cfg.Peers.EnableMDNS = false
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	n := &Node{config: cfg, host: h, ctx: ctx}

	n.wg.Add(1)
	returned := make(chan struct{})
	go func() {
		n.runMDNS()
		close(returned)
	}()
	select {
	case <-returned:
	case <-time.After(2 * time.Second):
		t.Fatal("runMDNS with peers.enable_mdns: false is still running: it started an mDNS service")
	}
	n.wg.Wait()

	if mdnsAnswers(t, h.ID(), 3*time.Second) {
		t.Fatal("a node with peers.enable_mdns: false answered an mDNS query")
	}
}

// The default stays on: a node whose configuration does not mention mDNS
// still discovers peers on its local network.
func TestMDNSStaysOnByDefault(t *testing.T) {
	if !config.Default().Peers.EnableMDNS {
		t.Fatal("peers.enable_mdns no longer defaults to true")
	}
}
