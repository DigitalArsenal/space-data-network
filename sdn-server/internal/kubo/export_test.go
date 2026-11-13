package kubo

import (
	"context"
	"testing"
	"time"

	"github.com/ipfs/kubo/config"
	"github.com/ipfs/kubo/core"
	kubolibp2p "github.com/ipfs/kubo/core/node/libp2p"
	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/peerstore"
	"github.com/libp2p/go-libp2p/core/routing"
	routed "github.com/libp2p/go-libp2p/p2p/host/routed"
)

// StartTransportFixture uses the same repository, identity and host builders
// as Start. Only the routing fault and the outbound barrier are test-owned.
func StartTransportFixture(t *testing.T, cfg Config, route kubolibp2p.RoutingOption, wrap func(host.Host) host.Host) *Node {
	t.Helper()
	initial, err := cfg.initial()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = loadPlugins(cfg.RepoPath); err != nil {
		t.Fatal(err)
	}
	id, err := peer.IDFromPrivateKey(cfg.Key)
	if err != nil {
		t.Fatal(err)
	}
	r, _, err := openRepo(cfg.RepoPath, id, initial, t.Logf)
	if err != nil {
		t.Fatal(err)
	}
	repo := &identityRepo{Repo: r, key: cfg.Key, id: id, settings: func(c *config.Config) {
		cfg.settings(c)
		c.HTTPRetrieval.Enabled = config.False
		c.Internal.Bitswap = &config.InternalBitswap{TaskWorkerCount: *config.NewOptionalInteger(8)}
	}}
	buildHost := hostOption(cfg.HostOptions, cfg.Gate)
	n, err := core.NewNode(context.Background(), &core.BuildCfg{
		Online: true, Permanent: true, Repo: repo, Routing: route,
		Host: func(id peer.ID, ps peerstore.Peerstore, opts ...libp2p.Option) (host.Host, error) {
			var options libp2p.Config
			if err := options.Apply(opts...); err != nil {
				return nil, err
			}
			original := options.Routing
			var router routing.PeerRouting
			// Observe Kubo's callback without replacing its routing output.
			opts = append(opts, func(c *libp2p.Config) error {
				c.Routing = func(h host.Host) (routing.PeerRouting, error) {
					var err error
					router, err = original(h)
					return router, err
				}
				return nil
			})
			h, err := buildHost(id, ps, opts...)
			if err != nil {
				return nil, err
			}
			if router == nil {
				// Reproduce Kubo's Host compatibility path here, before the
				// barrier, including in the unpatched control run. Kubo sees
				// its callback output populated and does not wrap a second time.
				router, err = original(h)
				if err != nil {
					_ = h.Close()
					return nil, err
				}
				h = routed.Wrap(h, router)
			}
			return wrap(h), nil
		},
	})
	if err != nil {
		_ = r.Close()
		t.Fatal(err)
	}
	out := &Node{ipfs: n}
	t.Cleanup(func() {
		done := make(chan error, 1)
		go func() { done <- out.Stop() }()
		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(15 * time.Second):
			t.Error("transport fixture did not stop")
		}
	})
	return out
}
