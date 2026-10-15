// Package dnsproof checks a publisher's domain-control claim against DNS
// (security audit SEC-037).
//
// An EPM DomainProof is signed by a key the EPM itself carries, so on its own
// it proves only that the record's owner CLAIMS a domain: anyone can sign
// "domain=noaa.gov". Control of the domain is shown by DNS: the domain's
// operator publishes a TXT record naming the key and the peer.
//
// Record name (the same one the node's PMM trust anchor uses):
//
//	_sdnkey.<domain>
//
// Record value, one TXT string, byte for byte:
//
//	sdn-domain-proof/1 key=<algorithm>:<public-key-hex> peerid=<peer-id>
//
// algorithm is the DomainProof's (lowercase, "ed25519" when absent) and
// public-key-hex is the DomainProof PUBLIC_KEY (lowercase hex). An Ed25519
// key and a secp256k1 peer ID keep the value under the 255-byte TXT string
// limit. A domain may carry several such strings (several nodes); one exact
// match is enough. Anything else in the record set is ignored.
//
// Lookups never run on a request path. Status answers from a bounded cache
// and, on a miss or an expired entry, schedules one background lookup per
// domain (singleflight, bounded concurrency, per-lookup timeout) and answers
// StatusUnknown meanwhile. Errors and timeouts are cached briefly as
// StatusUnknown: a failure never reads as verified. When more than one
// resolver is configured, every resolver must return the matching value.
//
// Not covered (owner decision): DNSSEC validation. The system resolver does
// not validate DNSSEC, so an on-path DNS attacker against this node can forge
// the TXT answer. Configure several independent resolvers (for example
// DNS-over-HTTPS endpoints wrapped as Resolver) to raise that bar.
package dnsproof

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"
)

// RecordPrefix is the label the TXT record lives under.
const RecordPrefix = "_sdnkey."

// ValuePrefix starts every proof value.
const ValuePrefix = "sdn-domain-proof/1"

// Status is the DNS answer for one (domain, key, peer) claim.
type Status int

const (
	// StatusUnknown: not looked up yet, lookup in flight, or the lookup
	// failed or timed out. Never treat as verified.
	StatusUnknown Status = iota
	// StatusNotVerified: DNS answered and no TXT value matches the claim
	// (including NXDOMAIN).
	StatusNotVerified
	// StatusVerified: every configured resolver returned the exact value.
	StatusVerified
)

func (s Status) String() string {
	switch s {
	case StatusVerified:
		return "verified"
	case StatusNotVerified:
		return "not-verified"
	default:
		return "unknown"
	}
}

// Resolver looks up TXT records. *net.Resolver satisfies it.
type Resolver interface {
	LookupTXT(ctx context.Context, name string) ([]string, error)
}

// RecordName is the TXT record name for domain.
func RecordName(domain string) string {
	return RecordPrefix + strings.TrimSuffix(strings.ToLower(strings.TrimSpace(domain)), ".")
}

// TXTValue is the exact TXT string a domain publishes to vouch for a key and
// peer. Producers print it for the operator; the verifier byte-matches it.
func TXTValue(algorithm, publicKeyHex, peerID string) string {
	algorithm = strings.ToLower(strings.TrimSpace(algorithm))
	if algorithm == "" {
		algorithm = "ed25519"
	}
	return fmt.Sprintf("%s key=%s:%s peerid=%s", ValuePrefix, algorithm, strings.ToLower(strings.TrimSpace(publicKeyHex)), strings.TrimSpace(peerID))
}

// Options tune a Verifier. Zero values take the defaults.
type Options struct {
	// LookupTimeout bounds one lookup on one resolver (default 3s).
	LookupTimeout time.Duration
	// PositiveTTL is how long a successful answer is cached (default 15m).
	PositiveTTL time.Duration
	// NegativeTTL is how long a failed lookup or NXDOMAIN is cached
	// (default 2m).
	NegativeTTL time.Duration
	// MaxEntries bounds the cache (default 4096 domains).
	MaxEntries int
	// MaxConcurrent bounds background lookups (default 8).
	MaxConcurrent int
	// Now is the clock (default time.Now).
	Now func() time.Time
}

type entry struct {
	values   []string // TXT strings every resolver agreed on
	ok       bool     // every resolver answered (NXDOMAIN counts as an answer)
	expires  time.Time
	inflight bool
}

// Verifier answers DNS domain-control checks from a cache filled off the
// request path.
type Verifier struct {
	resolvers []Resolver
	opts      Options
	sem       chan struct{}

	mu    sync.Mutex
	cache map[string]*entry
}

// NewVerifier builds a verifier over resolvers (all must agree). With no
// resolvers it uses the system resolver.
func NewVerifier(opts Options, resolvers ...Resolver) *Verifier {
	if len(resolvers) == 0 {
		resolvers = []Resolver{net.DefaultResolver}
	}
	if opts.LookupTimeout <= 0 {
		opts.LookupTimeout = 3 * time.Second
	}
	if opts.PositiveTTL <= 0 {
		opts.PositiveTTL = 15 * time.Minute
	}
	if opts.NegativeTTL <= 0 {
		opts.NegativeTTL = 2 * time.Minute
	}
	if opts.MaxEntries <= 0 {
		opts.MaxEntries = 4096
	}
	if opts.MaxConcurrent <= 0 {
		opts.MaxConcurrent = 8
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	return &Verifier{
		resolvers: append([]Resolver(nil), resolvers...),
		opts:      opts,
		sem:       make(chan struct{}, opts.MaxConcurrent),
		cache:     make(map[string]*entry),
	}
}

// Status answers from the cache without blocking. A missing or expired entry
// schedules a background lookup and reads StatusUnknown until it lands.
func (v *Verifier) Status(domain, algorithm, publicKeyHex, peerID string) Status {
	if v == nil {
		return StatusUnknown
	}
	name := RecordName(domain)
	want := TXTValue(algorithm, publicKeyHex, peerID)
	now := v.opts.Now()

	v.mu.Lock()
	e := v.cache[name]
	if e == nil || (!e.inflight && !now.Before(e.expires)) {
		if e == nil {
			if len(v.cache) >= v.opts.MaxEntries {
				v.evictLocked(now)
			}
			if len(v.cache) >= v.opts.MaxEntries {
				// Every slot is a lookup in flight: answer unknown rather
				// than queue unbounded work for a flood of claimed domains.
				v.mu.Unlock()
				return StatusUnknown
			}
			e = &entry{}
			v.cache[name] = e
		}
		if !e.inflight {
			e.inflight = true
			go v.refresh(name)
		}
	}
	status := StatusUnknown
	if !e.expires.IsZero() && now.Before(e.expires) {
		status = matchStatus(e, want)
	}
	v.mu.Unlock()
	return status
}

// Check looks the record up now (bounded by ctx and the lookup timeout),
// caches the answer, and returns the claim's status. Use it off the request
// path only.
func (v *Verifier) Check(ctx context.Context, domain, algorithm, publicKeyHex, peerID string) Status {
	if v == nil {
		return StatusUnknown
	}
	name := RecordName(domain)
	values, ok := v.lookup(ctx, name)
	e := v.store(name, values, ok)
	return matchStatus(e, TXTValue(algorithm, publicKeyHex, peerID))
}

func matchStatus(e *entry, want string) Status {
	if !e.ok {
		return StatusUnknown
	}
	for _, value := range e.values {
		if value == want {
			return StatusVerified
		}
	}
	return StatusNotVerified
}

func (v *Verifier) refresh(name string) {
	v.sem <- struct{}{}
	defer func() { <-v.sem }()
	values, ok := v.lookup(context.Background(), name)
	v.store(name, values, ok)
}

// lookup asks every resolver and keeps only the values all of them returned.
// ok is false when any resolver failed (other than a definite NXDOMAIN).
func (v *Verifier) lookup(ctx context.Context, name string) ([]string, bool) {
	var agreed map[string]bool
	for i, resolver := range v.resolvers {
		lookupCtx, cancel := context.WithTimeout(ctx, v.opts.LookupTimeout)
		values, err := resolver.LookupTXT(lookupCtx, name)
		cancel()
		if err != nil {
			var dnsErr *net.DNSError
			if errors.As(err, &dnsErr) && dnsErr.IsNotFound {
				values = nil
			} else {
				return nil, false
			}
		}
		got := make(map[string]bool, len(values))
		for _, value := range values {
			got[value] = true
		}
		if i == 0 {
			agreed = got
			continue
		}
		for value := range agreed {
			if !got[value] {
				delete(agreed, value)
			}
		}
	}
	out := make([]string, 0, len(agreed))
	for value := range agreed {
		out = append(out, value)
	}
	return out, true
}

func (v *Verifier) store(name string, values []string, ok bool) *entry {
	now := v.opts.Now()
	ttl := v.opts.PositiveTTL
	if !ok || len(values) == 0 {
		ttl = v.opts.NegativeTTL
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	answer := &entry{values: append([]string(nil), values...), ok: ok, expires: now.Add(ttl)}
	e := v.cache[name]
	if e == nil {
		if len(v.cache) >= v.opts.MaxEntries {
			v.evictLocked(now)
		}
		if len(v.cache) >= v.opts.MaxEntries {
			return answer
		}
		e = &entry{}
		v.cache[name] = e
	}
	e.values, e.ok, e.expires, e.inflight = answer.values, ok, answer.expires, false
	return answer
}

// evictLocked drops expired entries, then (if still full) arbitrary settled
// ones, so a flood of claimed domains cannot grow the cache without bound.
func (v *Verifier) evictLocked(now time.Time) {
	for name, e := range v.cache {
		if !e.inflight && !now.Before(e.expires) {
			delete(v.cache, name)
		}
	}
	for name, e := range v.cache {
		if len(v.cache) < v.opts.MaxEntries {
			return
		}
		if !e.inflight {
			delete(v.cache, name)
		}
	}
}
