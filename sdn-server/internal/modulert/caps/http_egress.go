package caps

import (
	"context"
	"net/url"
	"strings"
	"sync"
	"time"
)

// Outbound egress pacing — a HOST connector policy, not application logic.
//
// The host owns the network hook, so the host owns how hard that hook is
// allowed to hit a third party. This pacer knows nothing about what is being
// fetched or why; it knows only "requests to destination host H are serialized
// and separated by at least D". A wasm flow cannot opt out of it, cannot see
// it, and needs no knowledge of it.
//
// CelesTrak fetch policy (binding owner law): requests to CelesTrak are SERIAL
// with 2.5 s between them. That floor is compiled in, not merely defaulted —
// configuration may make the node politer, never ruder. The satcat ingest flow
// alone emits two CelesTrak fetches from a single timer tick, and the GP and
// SATCAT flows run on independent timers that will collide; without a
// process-wide pacer those go out back-to-back.

// CelesTrakMinRequestInterval is the non-negotiable spacing between outbound
// requests to a CelesTrak host.
const CelesTrakMinRequestInterval = 2500 * time.Millisecond

// celestrakHostSuffixes are the destinations the CelesTrak floor applies to.
var celestrakHostSuffixes = []string{"celestrak.org", "celestrak.com"}

// isCelesTrakHost reports whether host (already lowercased, port stripped) is
// CelesTrak or a subdomain of it.
func isCelesTrakHost(host string) bool {
	for _, suffix := range celestrakHostSuffixes {
		if host == suffix || strings.HasSuffix(host, "."+suffix) {
			return true
		}
	}
	return false
}

// egressHostKey extracts the pacing key (lowercased hostname, no port) from a
// request URL. An unparseable URL returns "" and is not paced — the request
// will fail in the transport anyway.
func egressHostKey(rawURL string) string {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	return strings.ToLower(parsed.Hostname())
}

// egressPacer serializes and spaces outbound requests per destination host.
type egressPacer struct {
	mu    sync.Mutex
	hosts map[string]*egressHostGate

	// overrides are operator-configured minimum intervals by host. The
	// CelesTrak floor is applied on top and always wins when it is larger.
	overrides map[string]time.Duration
}

type egressHostGate struct {
	// sem is a 1-slot semaphore: holding it means "this host is mine right
	// now", which is what makes the requests SERIAL rather than merely spaced.
	sem    chan struct{}
	mu     sync.Mutex
	lastAt time.Time
}

// sharedEgressPacer is process-wide on purpose: the pacing contract is with the
// remote publisher, so every flow, every module and every timer in this process
// must queue behind the same gate.
var sharedEgressPacer = &egressPacer{hosts: map[string]*egressHostGate{}}

// SetEgressMinIntervals installs operator-configured per-host minimum spacing
// (host → interval). Compiled-in floors still apply, so this can only ever slow
// the node down. Passing nil clears the overrides.
func SetEgressMinIntervals(intervals map[string]time.Duration) {
	sharedEgressPacer.mu.Lock()
	defer sharedEgressPacer.mu.Unlock()
	if len(intervals) == 0 {
		sharedEgressPacer.overrides = nil
		return
	}
	normalized := make(map[string]time.Duration, len(intervals))
	for host, interval := range intervals {
		host = strings.ToLower(strings.TrimSpace(host))
		if host == "" || interval <= 0 {
			continue
		}
		normalized[host] = interval
	}
	sharedEgressPacer.overrides = normalized
}

// minInterval resolves the effective spacing for a destination host: the larger
// of the operator override and any compiled-in floor.
func (p *egressPacer) minInterval(host string) time.Duration {
	p.mu.Lock()
	configured := p.overrides[host]
	p.mu.Unlock()

	floor := time.Duration(0)
	if isCelesTrakHost(host) {
		floor = CelesTrakMinRequestInterval
	}
	if configured > floor {
		return configured
	}
	return floor
}

func (p *egressPacer) gate(host string) *egressHostGate {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.hosts == nil {
		p.hosts = map[string]*egressHostGate{}
	}
	gate, ok := p.hosts[host]
	if !ok {
		gate = &egressHostGate{sem: make(chan struct{}, 1)}
		p.hosts[host] = gate
	}
	return gate
}

// acquire blocks until this caller may issue a request to host: it takes the
// per-host slot (serializing) and then waits out the remaining spacing since
// the previous request to that host finished. The returned release function
// stamps the completion time and frees the slot; it is always non-nil.
//
// A cancelled context aborts the wait and releases cleanly — the pacer never
// outlives the request budget it is pacing.
func (p *egressPacer) acquire(ctx context.Context, host string) (release func(), waited time.Duration, err error) {
	if host == "" {
		return func() {}, 0, nil
	}
	interval := p.minInterval(host)
	if interval <= 0 {
		return func() {}, 0, nil
	}
	gate := p.gate(host)

	start := time.Now()
	select {
	case gate.sem <- struct{}{}:
	case <-ctx.Done():
		return func() {}, time.Since(start), ctx.Err()
	}

	gate.mu.Lock()
	last := gate.lastAt
	gate.mu.Unlock()

	if !last.IsZero() {
		if remaining := interval - time.Since(last); remaining > 0 {
			timer := time.NewTimer(remaining)
			select {
			case <-timer.C:
			case <-ctx.Done():
				timer.Stop()
				<-gate.sem
				return func() {}, time.Since(start), ctx.Err()
			}
		}
	}

	released := false
	return func() {
		if released {
			return
		}
		released = true
		gate.mu.Lock()
		gate.lastAt = time.Now()
		gate.mu.Unlock()
		<-gate.sem
	}, time.Since(start), nil
}
