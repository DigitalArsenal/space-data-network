package kubo_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ipfs/boxo/files"
	"github.com/ipfs/boxo/path"
	"github.com/ipfs/go-cid"
	"github.com/ipfs/kubo/config"
	"github.com/ipfs/kubo/core/coreapi"
	coreiface "github.com/ipfs/kubo/core/coreiface"
	"github.com/ipfs/kubo/repo/fsrepo"
	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p-kad-dht/provider/buffered"
	ddhtprovider "github.com/libp2p/go-libp2p-kad-dht/provider/dual"
	pubsub "github.com/libp2p/go-libp2p-pubsub"
	"github.com/libp2p/go-libp2p/core/control"
	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	ma "github.com/multiformats/go-multiaddr"

	"github.com/spacedatanetwork/sdn-server/internal/kubo"
)

// refusePeers stands in for the node's trust gate.
type refusePeers struct {
	refuse  map[peer.ID]bool
	refused atomic.Int64
}

func (g *refusePeers) deny(p peer.ID) bool {
	if g.refuse[p] {
		g.refused.Add(1)
		return true
	}
	return false
}
func (g *refusePeers) InterceptPeerDial(p peer.ID) bool                 { return !g.deny(p) }
func (g *refusePeers) InterceptAddrDial(p peer.ID, _ ma.Multiaddr) bool { return !g.deny(p) }
func (g *refusePeers) InterceptAccept(network.ConnMultiaddrs) bool      { return true }
func (g *refusePeers) InterceptSecured(_ network.Direction, p peer.ID, _ network.ConnMultiaddrs) bool {
	return !g.deny(p)
}
func (g *refusePeers) InterceptUpgraded(network.Conn) (bool, control.DisconnectReason) {
	return true, 0
}

func newKey(t *testing.T) (crypto.PrivKey, peer.ID) {
	t.Helper()
	key, _, err := crypto.GenerateSecp256k1Key(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	id, err := peer.IDFromPrivateKey(key)
	if err != nil {
		t.Fatalf("peer id: %v", err)
	}
	return key, id
}

func start(t *testing.T, repo string, key crypto.PrivKey, gate *refusePeers, api string) *kubo.Node {
	t.Helper()
	cfg := kubo.Config{RepoPath: repo, Key: key, DHT: kubo.DHTClient, APIAddr: api}
	if gate != nil {
		cfg.Gate = gate
	}
	return startConfig(t, cfg)
}

func startConfig(t *testing.T, cfg kubo.Config) *kubo.Node {
	t.Helper()
	cfg.HostOptions = append([]libp2p.Option{
		libp2p.ListenAddrStrings("/ip4/127.0.0.1/tcp/0"),
		libp2p.UserAgent(kubo.AgentVersion("spacedatanetwork/0.0.0-test")),
	}, cfg.HostOptions...)
	n, err := kubo.Start(context.Background(), cfg)
	if err != nil {
		t.Fatalf("kubo.Start: %v", err)
	}
	t.Cleanup(func() { _ = n.Stop() })
	return n
}

func rpc(t *testing.T, base, cmd string) (map[string]any, int) {
	t.Helper()
	resp, err := http.Post(base+"/api/v0/"+cmd, "", nil)
	if err != nil {
		t.Fatalf("RPC %s: %v", cmd, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	out := map[string]any{}
	_ = json.Unmarshal(body, &out)
	return out, resp.StatusCode
}

func onDiskIdentity(t *testing.T, repo string) config.Identity {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(repo, "config"))
	if err != nil {
		t.Fatalf("read repo config: %v", err)
	}
	var c config.Config
	if err := json.Unmarshal(raw, &c); err != nil {
		t.Fatalf("decode repo config: %v", err)
	}
	return c.Identity
}

// TestOneProcessOnePeer is the owner's ruling end to end: Kubo runs on the
// node's key with no key on disk, the RPC answers as that same peer and never
// reveals the key, the node's gate still refuses a blocked peer, and an update
// announcement and its payload come from one peer (gossipsub, then bitswap).
func TestOneProcessOnePeer(t *testing.T) {
	keyA, idA := newKey(t)
	keyB, _ := newKey(t)
	keyC, idC := newKey(t)
	gateA := &refusePeers{refuse: map[peer.ID]bool{idC: true}}
	repoA := filepath.Join(t.TempDir(), "kubo")

	a := start(t, repoA, keyA, gateA, "127.0.0.1:0")
	b := start(t, filepath.Join(t.TempDir(), "kubo"), keyB, nil, "")
	c := start(t, filepath.Join(t.TempDir(), "kubo"), keyC, nil, "")

	if a.Host().ID() != idA || a.IPFS().Identity != idA {
		t.Fatalf("node runs as %s / %s, want the node key's %s", a.Host().ID(), a.IPFS().Identity, idA)
	}
	if got := onDiskIdentity(t, repoA); got.PeerID != idA.String() || got.PrivKey != "" {
		t.Fatalf("repo config on disk holds PeerID %q and a key of %d chars, want %s and no key", got.PeerID, len(got.PrivKey), idA)
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	infoA := peer.AddrInfo{ID: idA, Addrs: a.Host().Addrs()}
	if err := b.Host().Connect(ctx, infoA); err != nil {
		t.Fatalf("B connects to A: %v", err)
	}
	_ = c.Host().Connect(ctx, infoA)
	time.Sleep(500 * time.Millisecond)
	if a.Host().Network().Connectedness(idC) == network.Connected || gateA.refused.Load() == 0 {
		t.Fatalf("A's gate did not refuse C (refusals %d)", gateA.refused.Load())
	}
	if err := a.Host().Connect(ctx, peer.AddrInfo{ID: idC, Addrs: c.Host().Addrs()}); err == nil {
		t.Fatalf("A dialled C through its own gate")
	}

	// The node's gossipsub runs on the one host.
	psA, err := pubsub.NewGossipSub(ctx, a.Host())
	if err != nil {
		t.Fatalf("gossipsub on A: %v", err)
	}
	psB, err := pubsub.NewGossipSub(ctx, b.Host())
	if err != nil {
		t.Fatalf("gossipsub on B: %v", err)
	}
	const topic = "/sdn/updates/v1/test"
	ta, _ := psA.Join(topic)
	tb, _ := psB.Join(topic)
	sub, _ := tb.Subscribe()
	for i := 0; i < 100 && len(ta.ListPeers()) == 0; i++ {
		time.Sleep(100 * time.Millisecond)
	}
	payload := bytes.Repeat([]byte("sdn release payload "), 4096)
	added, err := mustAPI(t, a).Unixfs().Add(ctx, files.NewBytesFile(payload))
	if err != nil {
		t.Fatalf("add payload on A: %v", err)
	}
	if err := ta.Publish(ctx, []byte(added.RootCid().String())); err != nil {
		t.Fatalf("publish: %v", err)
	}
	msg, err := sub.Next(ctx)
	if err != nil || msg.GetFrom() != idA || msg.ReceivedFrom != idA {
		t.Fatalf("B's signal came from %v via %v (err %v), want A %s", msg.GetFrom(), msg.ReceivedFrom, err, idA)
	}
	p, err := path.NewPath("/ipfs/" + string(msg.Data))
	if err != nil {
		t.Fatalf("signal carries %q: %v", msg.Data, err)
	}
	nd, err := mustAPI(t, b).Unixfs().Get(ctx, p)
	if err != nil {
		t.Fatalf("B fetches the payload: %v", err)
	}
	got, _ := io.ReadAll(files.ToFile(nd))
	if !bytes.Equal(got, payload) {
		t.Fatalf("B fetched %d bytes, want the %d A published", len(got), len(payload))
	}

	id, _ := rpc(t, a.APIURL(), "id")
	agent, _ := id["AgentVersion"].(string)
	if id["ID"] != idA.String() {
		t.Fatalf("RPC id answers as %v, want %s", id["ID"], idA)
	}
	if !strings.HasPrefix(agent, "kubo/"+kubo.Version()) || !strings.Contains(agent, "spacedatanetwork") {
		t.Fatalf("agent %q, want kubo/%s carrying the spacedatanetwork suffix", agent, kubo.Version())
	}
	show, _ := rpc(t, a.APIURL(), "config/show")
	if ident, _ := show["Identity"].(map[string]any); ident == nil || ident["PrivKey"] != nil {
		t.Fatalf("config/show Identity = %v, want the PeerID without a key", show["Identity"])
	}
	for _, key := range []string{"Identity.PrivKey", "Identity"} {
		if _, code := rpc(t, a.APIURL(), "config?arg="+key); code == http.StatusOK {
			t.Fatalf("the RPC handed out %s", key)
		}
	}
}

func mustAPI(t *testing.T, n *kubo.Node) coreiface.CoreAPI {
	t.Helper()
	api, err := coreapi.NewCoreAPI(n.IPFS())
	if err != nil {
		t.Fatalf("core API: %v", err)
	}
	return api
}

func encode(raw []byte) string { return base64.StdEncoding.EncodeToString(raw) }

// TestExistingRepositoryIsTakenOver: a repository from the separate Kubo
// (its own random identity, key in the config) opens in place, runs as the
// node, and keeps its previous config, key included, as a backup. A second
// start finds nothing to take over.
func TestExistingRepositoryIsTakenOver(t *testing.T) {
	repo := filepath.Join(t.TempDir(), "kubo")
	oldKey, oldID := newKey(t)
	raw, err := crypto.MarshalPrivateKey(oldKey)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	cfg, err := config.InitWithIdentity(config.Identity{PeerID: oldID.String(), PrivKey: encode(raw)})
	if err != nil {
		t.Fatalf("init config: %v", err)
	}
	if err := fsrepo.Init(repo, cfg); err != nil {
		t.Fatalf("init repo: %v", err)
	}

	key, id := newKey(t)
	n := start(t, repo, key, nil, "")
	if n.Host().ID() != id {
		t.Fatalf("node runs as %s, want %s", n.Host().ID(), id)
	}
	if err := n.Stop(); err != nil {
		t.Fatalf("stop: %v", err)
	}
	if got := onDiskIdentity(t, repo); got.PeerID != id.String() || got.PrivKey != "" {
		t.Fatalf("repo identity %q (key %d chars), want %s and no key", got.PeerID, len(got.PrivKey), id)
	}
	backups, _ := filepath.Glob(filepath.Join(repo, "config-pre-sdn-identity-*"))
	if len(backups) != 1 {
		t.Fatalf("backups %v, want exactly one", backups)
	}
	kept, err := os.ReadFile(backups[0])
	if err != nil || !bytes.Contains(kept, []byte(oldID.String())) || !bytes.Contains(kept, []byte(encode(raw))) {
		t.Fatalf("backup %s does not hold the previous identity and key", backups[0])
	}

	start(t, repo, key, nil, "")
	if again, _ := filepath.Glob(filepath.Join(repo, "config-pre-sdn-identity-*")); len(again) != 1 {
		t.Fatalf("a second start made another backup: %v", again)
	}
}

// TestTakeoverAnnouncesExistingContentNow: content the separate Kubo held and
// had announced, on the schedule it keeps, is findable under the node's peer
// ID within seconds of the takeover. Resuming that schedule would leave every
// recently reprovided keyspace region waiting for its slot: up to
// Provide.DHT.Interval, 22 h.
//
// Loopback peers sit in Kubo's LAN DHT, so the DHT servers and the lookups are
// on the LAN side; on a public host the same reprovide runs on the WAN side.
// kad-dht never puts a loopback address in a provider record, and a node with
// no other address skips providing: on a public host the public address is
// that address, here both nodes also advertise an unroutable private one.
// Every connection still runs over loopback.
func TestTakeoverAnnouncesExistingContentNow(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	var servers []*kubo.Node
	var bootstrap []string
	for i := 0; i < 3; i++ {
		key, _ := newKey(t)
		s := startConfig(t, kubo.Config{RepoPath: filepath.Join(t.TempDir(), "kubo"), Key: key, DHT: kubo.DHTServer})
		for _, prev := range servers {
			if err := s.Host().Connect(ctx, peer.AddrInfo{ID: prev.Host().ID(), Addrs: prev.Host().Addrs()}); err != nil {
				t.Fatalf("connect DHT servers: %v", err)
			}
		}
		servers = append(servers, s)
		for _, addr := range s.Host().Addrs() {
			bootstrap = append(bootstrap, addr.String()+"/p2p/"+s.Host().ID().String())
		}
	}
	private := ma.StringCast("/ip4/10.255.255.1/tcp/4001")
	advertise := []libp2p.Option{libp2p.AddrsFactory(func(addrs []ma.Multiaddr) []ma.Multiaddr {
		return append(addrs, private)
	})}
	providedBy := func(c cid.Cid, want peer.ID) time.Duration {
		t.Helper()
		start := time.Now()
		for {
			for prov := range servers[0].IPFS().DHT.LAN.FindProvidersAsync(ctx, c, 10) {
				if prov.ID == want {
					return time.Since(start)
				}
			}
			if ctx.Err() != nil {
				t.Fatalf("%s was never announced under %s", c, want)
			}
			time.Sleep(250 * time.Millisecond)
		}
	}

	// The separate Kubo, on a 20 s interval so its regions are reprovided, and
	// recorded as recently reprovided, within seconds: the state weeks of
	// uptime leave on a fleet host.
	repo := filepath.Join(t.TempDir(), "kubo")
	oldKey, oldID := newKey(t)
	cfg, err := config.InitWithIdentity(config.Identity{PeerID: oldID.String()})
	if err != nil {
		t.Fatalf("config: %v", err)
	}
	cfg.Provide.DHT.Interval = config.NewOptionalDuration(20 * time.Second)
	if err := fsrepo.Init(repo, cfg); err != nil {
		t.Fatalf("init repo: %v", err)
	}
	old := startConfig(t, kubo.Config{RepoPath: repo, Key: oldKey, DHT: kubo.DHTServer, Bootstrap: bootstrap, HostOptions: advertise})
	var held []cid.Cid
	for i := 0; i < 16; i++ {
		block := make([]byte, 512)
		_, _ = rand.Read(block)
		added, err := mustAPI(t, old).Unixfs().Add(ctx, files.NewBytesFile(block))
		if err != nil {
			t.Fatalf("add content under the old identity: %v", err)
		}
		held = append(held, added.RootCid())
	}
	for _, c := range held {
		providedBy(c, oldID)
	}
	lan := old.IPFS().Provider.(*buffered.SweepingProvider).Provider.(*ddhtprovider.SweepingProvider).LAN
	for {
		if st, err := lan.Stats(ctx); err == nil && st.Operations.Past.RegionReprovidedLastCycle > 0 {
			break
		}
		if ctx.Err() != nil {
			t.Fatalf("the old Kubo never completed a reprovide cycle")
		}
		time.Sleep(500 * time.Millisecond)
	}
	if err := old.Stop(); err != nil {
		t.Fatalf("stop: %v", err)
	}
	// The node runs on Kubo's default interval, as on a fleet host.
	raw, err := os.ReadFile(filepath.Join(repo, "config"))
	if err != nil {
		t.Fatalf("read repo config: %v", err)
	}
	var onDisk map[string]any
	if err := json.Unmarshal(raw, &onDisk); err != nil {
		t.Fatalf("decode repo config: %v", err)
	}
	delete(onDisk["Provide"].(map[string]any)["DHT"].(map[string]any), "Interval")
	raw, _ = json.Marshal(onDisk)
	if err := os.WriteFile(filepath.Join(repo, "config"), raw, 0o600); err != nil {
		t.Fatalf("write repo config: %v", err)
	}

	key, id := newKey(t)
	startConfig(t, kubo.Config{RepoPath: repo, Key: key, DHT: kubo.DHTServer, Bootstrap: bootstrap, HostOptions: advertise})
	var slowest time.Duration
	for _, c := range held {
		slowest = max(slowest, providedBy(c, id))
	}
	if slowest > time.Minute {
		t.Fatalf("content took %s to be announced under %s", slowest, id)
	}
	t.Logf("all %d blocks the old Kubo had announced found under %s within %s", len(held), id, slowest.Round(time.Millisecond))
}
