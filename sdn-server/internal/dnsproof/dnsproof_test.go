package dnsproof

import (
	"context"
	"errors"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type fakeResolver struct {
	mu      sync.Mutex
	records map[string][]string
	err     error
	calls   atomic.Int32
	block   chan struct{}
}

func (f *fakeResolver) LookupTXT(ctx context.Context, name string) ([]string, error) {
	f.calls.Add(1)
	if f.block != nil {
		select {
		case <-f.block:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return nil, f.err
	}
	values, ok := f.records[name]
	if !ok {
		return nil, &net.DNSError{Err: "no such host", Name: name, IsNotFound: true}
	}
	return append([]string(nil), values...), nil
}

const (
	testKey  = "6f1f0d1c2b3a49586776859493a2b1c0d0e1f2031425364758697a8b9cadbecf"
	testPeer = "16Uiu2HAmAPZQ22WpSgQeUxStqr5ah3cJYrtpMQBJiTH3oQRHD31L"
)

func TestCheckVerifiesOnlyTheExactTXTValue(t *testing.T) {
	r := &fakeResolver{records: map[string][]string{
		"_sdnkey.observatory.example": {"v=spf1 -all", TXTValue("ed25519", testKey, testPeer)},
	}}
	v := NewVerifier(Options{}, r)
	if got := v.Check(context.Background(), "observatory.example", "", testKey, testPeer); got != StatusVerified {
		t.Fatalf("exact TXT value: status %v, want verified", got)
	}
	if got := v.Check(context.Background(), "observatory.example", "ed25519", testKey, "16Uiu2HAmOtherPeer"); got != StatusNotVerified {
		t.Fatalf("other peer: status %v, want not-verified", got)
	}
	if got := v.Check(context.Background(), "noaa.gov", "ed25519", testKey, testPeer); got != StatusNotVerified {
		t.Fatalf("NXDOMAIN: status %v, want not-verified", got)
	}
}

func TestLookupFailureIsNeverVerified(t *testing.T) {
	r := &fakeResolver{err: errors.New("SERVFAIL")}
	v := NewVerifier(Options{}, r)
	if got := v.Check(context.Background(), "noaa.gov", "ed25519", testKey, testPeer); got != StatusUnknown {
		t.Fatalf("resolver error: status %v, want unknown", got)
	}
}

func TestLookupTimesOut(t *testing.T) {
	r := &fakeResolver{block: make(chan struct{})}
	v := NewVerifier(Options{LookupTimeout: 20 * time.Millisecond}, r)
	start := time.Now()
	if got := v.Check(context.Background(), "slow.example", "ed25519", testKey, testPeer); got != StatusUnknown {
		t.Fatalf("timeout: status %v, want unknown", got)
	}
	if time.Since(start) > 2*time.Second {
		t.Fatal("lookup was not bounded by LookupTimeout")
	}
}

func TestEveryResolverMustAgree(t *testing.T) {
	value := TXTValue("ed25519", testKey, testPeer)
	honest := &fakeResolver{records: map[string][]string{"_sdnkey.observatory.example": {value}}}
	other := &fakeResolver{records: map[string][]string{"_sdnkey.observatory.example": {"something else"}}}
	if got := NewVerifier(Options{}, honest, other).Check(context.Background(), "observatory.example", "ed25519", testKey, testPeer); got != StatusNotVerified {
		t.Fatalf("disagreeing resolvers: status %v, want not-verified", got)
	}
	failing := &fakeResolver{err: errors.New("timeout")}
	if got := NewVerifier(Options{}, honest, failing).Check(context.Background(), "observatory.example", "ed25519", testKey, testPeer); got != StatusUnknown {
		t.Fatalf("one resolver failing: status %v, want unknown", got)
	}
}

func TestStatusNeverBlocksAndCachesOneLookupPerDomain(t *testing.T) {
	r := &fakeResolver{records: map[string][]string{
		"_sdnkey.observatory.example": {TXTValue("ed25519", testKey, testPeer)},
	}, block: make(chan struct{})}
	now := time.Unix(1_800_000_000, 0)
	var clock atomic.Int64
	clock.Store(now.UnixNano())
	v := NewVerifier(Options{Now: func() time.Time { return time.Unix(0, clock.Load()) }}, r)

	// First sight: unknown, one background lookup, which is blocked.
	for i := 0; i < 5; i++ {
		if got := v.Status("observatory.example", "ed25519", testKey, testPeer); got != StatusUnknown {
			t.Fatalf("before the lookup lands: status %v, want unknown", got)
		}
	}
	close(r.block)
	deadline := time.Now().Add(2 * time.Second)
	for v.Status("observatory.example", "ed25519", testKey, testPeer) != StatusVerified {
		if time.Now().After(deadline) {
			t.Fatal("background lookup never landed")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if calls := r.calls.Load(); calls != 1 {
		t.Fatalf("resolver called %d times, want one lookup per domain", calls)
	}

	// After the positive TTL the record is re-checked; a removed record
	// stops verifying.
	r.mu.Lock()
	delete(r.records, "_sdnkey.observatory.example")
	r.mu.Unlock()
	clock.Add(int64(16 * time.Minute))
	if got := v.Status("observatory.example", "ed25519", testKey, testPeer); got == StatusVerified {
		t.Fatal("an expired positive answer still verified")
	}
	deadline = time.Now().Add(2 * time.Second)
	for v.Status("observatory.example", "ed25519", testKey, testPeer) != StatusNotVerified {
		if time.Now().After(deadline) {
			t.Fatal("refresh never landed")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestCacheIsBounded(t *testing.T) {
	r := &fakeResolver{block: make(chan struct{})}
	defer close(r.block)
	v := NewVerifier(Options{MaxEntries: 3, MaxConcurrent: 1, LookupTimeout: time.Hour}, r)
	for _, domain := range []string{"a.example", "b.example", "c.example", "d.example", "e.example"} {
		v.Status(domain, "ed25519", testKey, testPeer)
	}
	v.mu.Lock()
	n := len(v.cache)
	v.mu.Unlock()
	if n > 3 {
		t.Fatalf("cache holds %d entries, want at most 3", n)
	}
}

func TestTXTValueFitsOneTXTString(t *testing.T) {
	if got := len(TXTValue("ed25519", testKey, testPeer)); got > 255 {
		t.Fatalf("TXT value is %d bytes, over the 255-byte string limit", got)
	}
}
