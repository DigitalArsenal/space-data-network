package node

import (
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/network"
	basichost "github.com/libp2p/go-libp2p/p2p/host/basic"
	ma "github.com/multiformats/go-multiaddr"
)

func TestNATWatchDecisionRebuildsAfterMappingsStayLost(t *testing.T) {
	t0 := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	lost := natObservation{Discovered: true, Eligible: 2, Mapped: 0}
	var state natWatchState

	rebuild, state := natWatchDecision(state, lost, t0)
	if rebuild || !state.LostSince.Equal(t0) {
		t.Fatalf("first lost sample must only start the grace clock: rebuild=%v state=%+v", rebuild, state)
	}
	rebuild, state = natWatchDecision(state, lost, t0.Add(natLostGrace-time.Second))
	if rebuild {
		t.Fatal("rebuilt inside the grace period")
	}
	rebuild, state = natWatchDecision(state, lost, t0.Add(natLostGrace))
	if !rebuild || state.Rebuilds != 1 || !state.LostSince.IsZero() {
		t.Fatalf("expected a rebuild at the end of the grace period: rebuild=%v state=%+v", rebuild, state)
	}
	// Cooldown: a second rebuild waits even if the mappings are still lost.
	rebuild, state = natWatchDecision(state, lost, t0.Add(natLostGrace+natLostGrace+time.Second))
	if rebuild {
		t.Fatal("rebuilt again inside the cooldown")
	}
}

func TestNATWatchDecisionRecoveredMappingResetsTheClock(t *testing.T) {
	t0 := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	_, state := natWatchDecision(natWatchState{}, natObservation{Discovered: true, Eligible: 2}, t0)
	rebuild, state := natWatchDecision(state, natObservation{Discovered: true, Eligible: 2, Mapped: 1}, t0.Add(time.Minute))
	if rebuild || !state.LostSince.IsZero() {
		t.Fatalf("a live mapping must clear the lost clock: rebuild=%v state=%+v", rebuild, state)
	}
	rebuild, _ = natWatchDecision(state, natObservation{Discovered: true, Eligible: 2}, t0.Add(natLostGrace+time.Minute))
	if rebuild {
		t.Fatal("the grace period must restart after a recovery")
	}
}

func TestNATWatchDecisionNoListenersNeverRebuilds(t *testing.T) {
	t0 := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	var state natWatchState
	for i := 0; i < 100; i++ {
		var rebuild bool
		rebuild, state = natWatchDecision(state, natObservation{Discovered: true}, t0.Add(time.Duration(i)*time.Hour))
		if rebuild {
			t.Fatal("no eligible listener must never trigger a rebuild")
		}
		rebuild, state = natWatchDecision(state, natObservation{}, t0.Add(time.Duration(i)*time.Hour+time.Minute))
		if rebuild {
			t.Fatal("no gateway and no eligible listener must never retry discovery")
		}
	}
}

func TestNATWatchDecisionRetriesDiscoveryWithBackoff(t *testing.T) {
	t0 := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	noGateway := natObservation{Discovered: false, Eligible: 2}
	var state natWatchState
	var retries []time.Time
	for minute := 0; minute <= 240; minute++ {
		now := t0.Add(time.Duration(minute) * time.Minute)
		var rebuild bool
		rebuild, state = natWatchDecision(state, noGateway, now)
		if rebuild {
			retries = append(retries, now)
		}
	}
	// First retry after natRetryMin, then doubling gaps capped at natRetryMax.
	want := []time.Duration{5, 15, 35, 65, 95, 125, 155, 185, 215}
	if len(retries) != len(want) {
		t.Fatalf("retries at %v, want %d retries", retries, len(want))
	}
	for i, at := range retries {
		if got := at.Sub(t0); got != want[i]*time.Minute {
			t.Fatalf("retry %d at +%s, want +%dm", i, got, want[i])
		}
	}
}

func TestNATEligibleListenerMirrorsTheStockManager(t *testing.T) {
	cases := map[string]bool{
		"/ip4/0.0.0.0/tcp/16001":         true,
		"/ip4/0.0.0.0/udp/16001/quic-v1": true,
		"/ip4/203.0.113.7/tcp/4001":      true,
		"/ip6/::/tcp/4001":               true,
		"/ip4/127.0.0.1/tcp/18001":       false,
		"/ip6/::1/udp/4001/quic-v1":      false,
		"/ip4/169.254.1.1/tcp/4001":      false,
	}
	for text, want := range cases {
		if got := natEligibleListener(ma.StringCast(text)); got != want {
			t.Errorf("%s: eligible=%v want %v", text, got, want)
		}
	}
}

func TestClassifyAdvertisedAddrs(t *testing.T) {
	addrs := []ma.Multiaddr{
		ma.StringCast("/ip4/108.56.17.223/tcp/60999"),
		ma.StringCast("/dns4/108-56-17-223.k51qzi5uqu5dg7rxag5ay43i0wtjus7xvy81074a8vukm9xvgxejiebm7s3mde.libp2p.direct/tcp/60999/tls/ws"),
		ma.StringCast("/ip4/192.3.44.211/udp/4001/quic-v1/p2p/12D3KooWHp4a3P3RZTrohLzu3cb8ZNgtLsSjqwQw2tZRwM8HsyaL/p2p-circuit"),
		ma.StringCast("/ip4/127.0.0.1/tcp/16001"),
		ma.StringCast("/ip4/192.168.0.42/udp/16001/quic-v1"),
	}
	got := classifyAdvertisedAddrs(addrs)
	if len(got.Direct) != 2 || len(got.Relayed) != 1 || len(got.Local) != 2 {
		t.Fatalf("classes = %+v", got)
	}
	if v := reachabilityVerdict("private", 0, got); v != "relayed" {
		t.Fatalf("unconfirmed direct addresses beside relay circuits: verdict %q", v)
	}
	if v := reachabilityVerdict("unknown", 0, classifyAdvertisedAddrs(addrs[:2])); v != "unconfirmed" {
		t.Fatalf("direct but unconfirmed addresses: verdict %q", v)
	}
	if v := reachabilityVerdict("private", 1, got); v != "direct" {
		t.Fatalf("an AutoNAT-confirmed address: verdict %q", v)
	}
	if v := reachabilityVerdict("unknown", 0, classifyAdvertisedAddrs(addrs[3:])); v != "unreachable" {
		t.Fatalf("local-only addresses: verdict %q", v)
	}
}

type fakeNATManager struct {
	discovered bool
	mapped     bool
	closed     bool
}

func (f *fakeNATManager) GetMapping(ma.Multiaddr) ma.Multiaddr {
	if f.mapped {
		return ma.StringCast("/ip4/108.56.17.223/tcp/60999")
	}
	return nil
}
func (f *fakeNATManager) HasDiscoveredNAT() bool { return f.discovered }
func (f *fakeNATManager) Close() error           { f.closed = true; return nil }

type listenOnlyNetwork struct {
	network.Network
	addrs []ma.Multiaddr
}

func (l listenOnlyNetwork) ListenAddresses() []ma.Multiaddr { return l.addrs }

func TestNATWatchdogReplacesAStaleManager(t *testing.T) {
	stale := &fakeNATManager{discovered: true}
	fresh := &fakeNATManager{discovered: true, mapped: true}
	built := 0
	w := &natWatchdog{
		net: listenOnlyNetwork{addrs: []ma.Multiaddr{ma.StringCast("/ip4/0.0.0.0/tcp/16002"), ma.StringCast("/ip4/127.0.0.1/tcp/16051")}},
		newInner: func(network.Network) basichost.NATManager {
			built++
			return fresh
		},
		inner: stale,
	}
	t0 := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	w.check(t0)
	if status := w.Status(); len(status.Unmapped) != 1 || status.LostSince == nil {
		t.Fatalf("status before rebuild = %+v", status)
	}
	w.check(t0.Add(natLostGrace))
	if !stale.closed || built != 1 {
		t.Fatalf("stale manager closed=%v, rebuilt %d times", stale.closed, built)
	}
	status := w.Status()
	if status.Rebuilds != 1 || len(status.Mappings) != 1 || len(status.Unmapped) != 0 {
		t.Fatalf("status after rebuild = %+v", status)
	}
	if got := w.GetMapping(ma.StringCast("/ip4/0.0.0.0/tcp/16002")); got == nil {
		t.Fatal("GetMapping must delegate to the rebuilt manager")
	}
}
