package kubo

import (
	"context"
	"testing"
	"time"

	"github.com/ipfs/kubo/config"
	"github.com/ipfs/kubo/core"
	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/peerstore"
	"github.com/spacedatanetwork/sdn-server/internal/ops"
)

// StartHealthFixture pauses real outgoing streams through Kubo's public
// construction seam. Repository, identity and host construction are SDN's.
func StartHealthFixture(t *testing.T, cfg Config, wrap func(host.Host) host.Host) *Node {
	t.Helper()
	initial, err := cfg.initial()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := loadPlugins(cfg.RepoPath); err != nil {
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
		Online: true, Permanent: true, Repo: repo, Routing: cfg.DHT.routing(),
		Host: func(id peer.ID, ps peerstore.Peerstore, opts ...libp2p.Option) (host.Host, error) {
			h, err := buildHost(id, ps, opts...)
			if err != nil {
				return nil, err
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
			t.Error("health fixture did not stop")
		}
	})
	return out
}

// HealthProbeForTest drives the production sampler and state with a test clock.
func HealthProbeForTest(n *Node, alerts ops.Sink) func(context.Context, time.Time) (bool, bool, uint64) {
	s := &bitswapDemandSampler{node: n.ipfs}
	m := &bitswapSendMonitor{sample: s.sample, alerts: alerts}
	return func(ctx context.Context, now time.Time) (bool, bool, uint64) {
		sample := m.sample(ctx)
		m.observe(now, sample)
		return sample.known, sample.demand, sample.sent
	}
}

func RunHealthMonitorForTest(ctx context.Context, n *Node, alerts ops.Sink, ticks <-chan time.Time) {
	s := &bitswapDemandSampler{node: n.ipfs}
	m := &bitswapSendMonitor{sample: s.sample, alerts: alerts}
	m.run(ctx, ticks, time.Now)
}
