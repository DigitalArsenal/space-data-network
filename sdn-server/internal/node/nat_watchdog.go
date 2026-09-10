package node

import (
	"context"
	"net"
	"sync"
	"time"

	"github.com/libp2p/go-libp2p/config"
	"github.com/libp2p/go-libp2p/core/network"
	basichost "github.com/libp2p/go-libp2p/p2p/host/basic"
	ma "github.com/multiformats/go-multiaddr"
)

// NAT port-mapping watchdog.
//
// go-libp2p's NAT manager discovers the gateway once and only rediscovers it
// when a lease renewal fails with "connection refused". A miniupnpd router that
// reboots keeps answering on the same port but serves its control URL under a
// new random path, so every renewal fails with UPnP fault 401 "Invalid Action"
// instead, the failure counter resets, and the mappings never come back. The
// node keeps advertising its observed public address with no forwarding behind
// it, remote dialers fall back to relay circuits, and block transfers stall at
// the relay limits. Observed on the local fleet on 2026-09-10 after a router
// WAN restart: only processes started after the restart held mappings.
//
// The watchdog wraps the stock manager and rebuilds it (a rebuild runs a fresh
// gateway discovery) when the gateway is known but no eligible listener has
// held a mapping for natLostGrace, and retries discovery with backoff when
// none was found at boot.

const (
	natCheckInterval   = time.Minute
	natLostGrace       = 2 * time.Minute
	natRetryMin        = 5 * time.Minute
	natRetryMax        = 30 * time.Minute
	natRebuildCooldown = 5 * time.Minute
)

// natObservation is one watchdog sample of the wrapped manager.
type natObservation struct {
	Discovered bool
	Eligible   int // listeners the manager should map
	Mapped     int // listeners with a live external mapping
}

// natWatchState is the watchdog's memory between samples.
type natWatchState struct {
	LostSince   time.Time // first sample with a known gateway and no mapping
	NextRetry   time.Time // next discovery retry while no gateway is known
	RetryDelay  time.Duration
	LastRebuild time.Time
	Rebuilds    int
}

// natWatchDecision decides whether to rebuild the NAT manager for obs at now
// and returns the updated state. Rebuilding is the only remedy: it closes the
// stale gateway client and runs discovery again.
func natWatchDecision(state natWatchState, obs natObservation, now time.Time) (bool, natWatchState) {
	if !state.LastRebuild.IsZero() && now.Sub(state.LastRebuild) < natRebuildCooldown {
		return false, state
	}
	if !obs.Discovered {
		state.LostSince = time.Time{}
		if obs.Eligible == 0 {
			return false, state
		}
		if state.RetryDelay == 0 {
			state.RetryDelay = natRetryMin
			state.NextRetry = now.Add(state.RetryDelay)
			return false, state
		}
		if now.Before(state.NextRetry) {
			return false, state
		}
		state.RetryDelay *= 2
		if state.RetryDelay > natRetryMax {
			state.RetryDelay = natRetryMax
		}
		state.NextRetry = now.Add(state.RetryDelay)
		state.LastRebuild = now
		state.Rebuilds++
		return true, state
	}
	state.RetryDelay = 0
	state.NextRetry = time.Time{}
	if obs.Eligible == 0 || obs.Mapped > 0 {
		state.LostSince = time.Time{}
		return false, state
	}
	if state.LostSince.IsZero() {
		state.LostSince = now
		return false, state
	}
	if now.Sub(state.LostSince) < natLostGrace {
		return false, state
	}
	state.LostSince = time.Time{}
	state.LastRebuild = now
	state.Rebuilds++
	return true, state
}

// natEligibleListener mirrors the stock manager's rule: a TCP or UDP listener
// on a global unicast or unspecified IP address.
func natEligibleListener(addr ma.Multiaddr) bool {
	ipComponent, rest := ma.SplitFirst(addr)
	if ipComponent == nil || len(rest) == 0 {
		return false
	}
	switch ipComponent.Protocol().Code {
	case ma.P_IP4, ma.P_IP6:
	default:
		return false
	}
	ip := net.IP(ipComponent.RawValue())
	if !ip.IsGlobalUnicast() && !ip.IsUnspecified() {
		return false
	}
	transport, _ := ma.SplitFirst(rest)
	if transport == nil {
		return false
	}
	code := transport.Protocol().Code
	return code == ma.P_TCP || code == ma.P_UDP
}

// NATStatus is the watchdog's operator-facing state.
type NATStatus struct {
	Enabled     bool       `json:"enabled"`
	Discovered  bool       `json:"gateway_discovered"`
	Mappings    []string   `json:"mappings"`
	Unmapped    []string   `json:"unmapped_listeners"`
	Rebuilds    int        `json:"rebuilds"`
	LastRebuild *time.Time `json:"last_rebuild,omitempty"`
	LostSince   *time.Time `json:"mapping_lost_since,omitempty"`
}

type natWatchdog struct {
	net      network.Network
	newInner func(network.Network) basichost.NATManager

	mu    sync.RWMutex
	inner basichost.NATManager
	state natWatchState

	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{}
}

func newNATWatchdog(n network.Network, newInner func(network.Network) basichost.NATManager) *natWatchdog {
	ctx, cancel := context.WithCancel(context.Background())
	w := &natWatchdog{net: n, newInner: newInner, inner: newInner(n), ctx: ctx, cancel: cancel, done: make(chan struct{})}
	go w.run()
	return w
}

// natWatchdogConstructor returns a libp2p NAT manager constructor that keeps
// the created watchdog reachable through slot for health reporting.
func natWatchdogConstructor(slot **natWatchdog) config.NATManagerC {
	return func(n network.Network) basichost.NATManager {
		w := newNATWatchdog(n, basichost.NewNATManager)
		*slot = w
		return w
	}
}

func (w *natWatchdog) GetMapping(addr ma.Multiaddr) ma.Multiaddr {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return w.inner.GetMapping(addr)
}

func (w *natWatchdog) HasDiscoveredNAT() bool {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return w.inner.HasDiscoveredNAT()
}

func (w *natWatchdog) Close() error {
	w.cancel()
	<-w.done
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.inner.Close()
}

func (w *natWatchdog) run() {
	defer close(w.done)
	ticker := time.NewTicker(natCheckInterval)
	defer ticker.Stop()
	for {
		select {
		case <-w.ctx.Done():
			return
		case now := <-ticker.C:
			w.check(now)
		}
	}
}

func (w *natWatchdog) observe() (natObservation, []string, []string) {
	w.mu.RLock()
	defer w.mu.RUnlock()
	obs := natObservation{Discovered: w.inner.HasDiscoveredNAT()}
	var mapped, unmapped []string
	for _, addr := range w.net.ListenAddresses() {
		if !natEligibleListener(addr) {
			continue
		}
		obs.Eligible++
		if external := w.inner.GetMapping(addr); external != nil {
			obs.Mapped++
			mapped = append(mapped, addr.String()+" -> "+external.String())
		} else {
			unmapped = append(unmapped, addr.String())
		}
	}
	return obs, mapped, unmapped
}

func (w *natWatchdog) check(now time.Time) {
	obs, _, _ := w.observe()
	w.mu.Lock()
	rebuild, next := natWatchDecision(w.state, obs, now)
	w.state = next
	if !rebuild {
		w.mu.Unlock()
		return
	}
	// Close first: the old client may still reach the router (a false
	// positive), and closing after the rebuild would delete the new mappings.
	old := w.inner
	w.mu.Unlock()
	_ = old.Close()
	fresh := w.newInner(w.net)
	w.mu.Lock()
	w.inner = fresh
	w.mu.Unlock()
	if obs.Discovered {
		log.Warnf("NAT port mappings lost for %s on %d listener(s); rebuilt the NAT manager to rediscover the gateway (rebuild %d)", natLostGrace, obs.Eligible, next.Rebuilds)
	} else {
		log.Infof("No NAT gateway discovered; retrying discovery (attempt %d, next in %s)", next.Rebuilds, next.RetryDelay)
	}
}

// Status reports the current mappings and watchdog history.
func (w *natWatchdog) Status() NATStatus {
	obs, mapped, unmapped := w.observe()
	w.mu.RLock()
	defer w.mu.RUnlock()
	status := NATStatus{Enabled: true, Discovered: obs.Discovered, Mappings: mapped, Unmapped: unmapped, Rebuilds: w.state.Rebuilds}
	if !w.state.LastRebuild.IsZero() {
		t := w.state.LastRebuild
		status.LastRebuild = &t
	}
	if !w.state.LostSince.IsZero() {
		t := w.state.LostSince
		status.LostSince = &t
	}
	if status.Mappings == nil {
		status.Mappings = []string{}
	}
	if status.Unmapped == nil {
		status.Unmapped = []string{}
	}
	return status
}
