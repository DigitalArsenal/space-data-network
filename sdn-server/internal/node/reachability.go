package node

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/libp2p/go-libp2p/core/event"
	"github.com/libp2p/go-libp2p/core/host"
	ma "github.com/multiformats/go-multiaddr"
	manet "github.com/multiformats/go-multiaddr/net"
)

// Publisher reachability: can a peer outside this network dial this node
// directly, or only through a relay circuit? Discovery succeeds either way, but
// relay v2 circuits are capped (about 128 KiB and two minutes), so a
// relayed-only publisher is found and then never delivers a block.

// AdvertisedAddrClasses splits advertised addresses by how a remote dialer
// would reach them.
type AdvertisedAddrClasses struct {
	Direct  []string `json:"direct"`  // public IP or DNS, no relay hop
	Relayed []string `json:"relayed"` // p2p-circuit through another peer
	Local   []string `json:"local"`   // loopback, private or link-local
}

func classifyAdvertisedAddrs(addrs []ma.Multiaddr) AdvertisedAddrClasses {
	classes := AdvertisedAddrClasses{Direct: []string{}, Relayed: []string{}, Local: []string{}}
	for _, addr := range addrs {
		text := addr.String()
		if _, err := addr.ValueForProtocol(ma.P_CIRCUIT); err == nil {
			classes.Relayed = append(classes.Relayed, text)
			continue
		}
		first, _ := ma.SplitFirst(addr)
		if first == nil {
			continue
		}
		switch first.Protocol().Code {
		case ma.P_DNS, ma.P_DNS4, ma.P_DNS6, ma.P_DNSADDR:
			if strings.EqualFold(first.Value(), "localhost") {
				classes.Local = append(classes.Local, text)
			} else {
				classes.Direct = append(classes.Direct, text)
			}
		case ma.P_IP4, ma.P_IP6:
			if manet.IsPublicAddr(addr) {
				classes.Direct = append(classes.Direct, text)
			} else {
				classes.Local = append(classes.Local, text)
			}
		}
	}
	return classes
}

// reachabilityVerdict summarizes what a remote dialer gets. Relay circuits are
// only advertised once AutoNAT judged the node private, so unconfirmed direct
// addresses next to them are not dialable in practice.
func reachabilityVerdict(autonat string, reachableAddrs int, classes AdvertisedAddrClasses) string {
	switch {
	case reachableAddrs > 0 || strings.EqualFold(autonat, "public"):
		return "direct"
	case len(classes.Relayed) > 0:
		return "relayed"
	case len(classes.Direct) > 0:
		return "unconfirmed"
	default:
		return "unreachable"
	}
}

// SidecarReachability is the IPFS sidecar's own AutoNAT view; it serves the
// node's native files, so its reachability is the one block transfers see.
type SidecarReachability struct {
	Reachability string    `json:"reachability"`
	Reachable    []string  `json:"reachable"`
	Unreachable  []string  `json:"unreachable"`
	CheckedAt    time.Time `json:"checked_at"`
	Error        string    `json:"error,omitempty"`
}

// ReachabilitySnapshot is the node-info reachability block.
type ReachabilitySnapshot struct {
	Verdict     string                `json:"verdict"`
	AutoNAT     string                `json:"autonat"`
	Reachable   []string              `json:"reachable"`
	Unreachable []string              `json:"unreachable"`
	Advertised  AdvertisedAddrClasses `json:"advertised"`
	NAT         NATStatus             `json:"nat"`
	IPFS        *SidecarReachability  `json:"ipfs,omitempty"`
}

type reachabilityTracker struct {
	mu          sync.RWMutex
	autonat     string
	reachable   []string
	unreachable []string
	sidecar     *SidecarReachability
}

func (t *reachabilityTracker) run(ctx context.Context, h host.Host, ipfsAPIURL string) {
	t.mu.Lock()
	t.autonat = "unknown"
	t.mu.Unlock()
	sub, err := h.EventBus().Subscribe([]interface{}{
		new(event.EvtLocalReachabilityChanged),
		new(event.EvtHostReachableAddrsChanged),
	})
	if err != nil {
		log.Warnf("Reachability events unavailable: %v", err)
		return
	}
	defer sub.Close()
	poll := time.NewTicker(time.Minute)
	defer poll.Stop()
	if ipfsAPIURL != "" {
		t.pollSidecar(ctx, ipfsAPIURL)
	}
	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-sub.Out():
			if !ok {
				return
			}
			t.mu.Lock()
			switch e := ev.(type) {
			case event.EvtLocalReachabilityChanged:
				t.autonat = strings.ToLower(e.Reachability.String())
			case event.EvtHostReachableAddrsChanged:
				t.reachable = multiaddrStrings(e.Reachable)
				t.unreachable = multiaddrStrings(e.Unreachable)
			}
			t.mu.Unlock()
		case <-poll.C:
			if ipfsAPIURL != "" {
				t.pollSidecar(ctx, ipfsAPIURL)
			}
		}
	}
}

func (t *reachabilityTracker) pollSidecar(ctx context.Context, ipfsAPIURL string) {
	result := &SidecarReachability{Reachable: []string{}, Unreachable: []string{}, CheckedAt: time.Now().UTC()}
	reqCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, strings.TrimRight(ipfsAPIURL, "/")+"/api/v0/swarm/addrs/autonat", nil)
	if err == nil {
		var resp *http.Response
		resp, err = http.DefaultClient.Do(req)
		if err == nil {
			defer resp.Body.Close()
			var body struct {
				Reachability string   `json:"reachability"`
				Reachable    []string `json:"reachable"`
				Unreachable  []string `json:"unreachable"`
			}
			if resp.StatusCode != http.StatusOK {
				result.Error = resp.Status
			} else if err = json.NewDecoder(resp.Body).Decode(&body); err == nil {
				result.Reachability = strings.ToLower(body.Reachability)
				if body.Reachable != nil {
					result.Reachable = body.Reachable
				}
				if body.Unreachable != nil {
					result.Unreachable = body.Unreachable
				}
			}
		}
	}
	if err != nil {
		result.Error = err.Error()
	}
	t.mu.Lock()
	t.sidecar = result
	t.mu.Unlock()
}

func (t *reachabilityTracker) snapshot(advertised []ma.Multiaddr, nat NATStatus) ReachabilitySnapshot {
	t.mu.RLock()
	defer t.mu.RUnlock()
	classes := classifyAdvertisedAddrs(advertised)
	snap := ReachabilitySnapshot{
		AutoNAT:     t.autonat,
		Reachable:   append([]string{}, t.reachable...),
		Unreachable: append([]string{}, t.unreachable...),
		Advertised:  classes,
		NAT:         nat,
		IPFS:        t.sidecar,
	}
	if snap.AutoNAT == "" {
		snap.AutoNAT = "unknown"
	}
	snap.Verdict = reachabilityVerdict(snap.AutoNAT, len(snap.Reachable), classes)
	return snap
}

func multiaddrStrings(addrs []ma.Multiaddr) []string {
	out := make([]string, 0, len(addrs))
	for _, addr := range addrs {
		out = append(out, addr.String())
	}
	return out
}

// Reachability reports whether remote peers can dial this node directly.
func (n *Node) Reachability() ReachabilitySnapshot {
	nat := NATStatus{Mappings: []string{}, Unmapped: []string{}}
	if n.natWatchdog != nil {
		nat = n.natWatchdog.Status()
	}
	var advertised []ma.Multiaddr
	if n.host != nil {
		advertised = n.host.Addrs()
	}
	return n.reachability.snapshot(advertised, nat)
}
