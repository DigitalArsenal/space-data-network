package license

import (
	"context"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	lch "github.com/DigitalArsenal/spacedatastandards.org/lib/go/LCH"
	lpf "github.com/DigitalArsenal/spacedatastandards.org/lib/go/LPF"
	flatbuffers "github.com/google/flatbuffers/go"
	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/protocol"
)

// These tests drive the gate the way a browser meets it: real libp2p hosts on
// loopback, one stream per frame, the provider's handler installed through
// WrapDeliveryHost exactly as the plugin manager installs the licensing
// module's. The handler stands in for the key server only in what it answers:
// an $LCH Response for every $LCH request it is handed, an empty answer for a
// proof. The clock is a test clock, so the bucket and the TTL are exact.

const (
	openModule = "com.orbpro.wasm-engine"
	paidModule = "com.orbpro.hpop"
)

type testClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *testClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *testClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

type deliveryRig struct {
	t        *testing.T
	gate     *DeliveryAdmission
	clock    *testClock
	provider host.Host
	handled  atomic.Int64
}

func newDeliveryRig(t *testing.T, limits DeliveryLimits) *deliveryRig {
	t.Helper()
	clock := &testClock{now: time.Unix(1_760_000_000, 0)}
	gate := NewDeliveryAdmission(limits, clock.Now)
	gate.Configure(limits, func(moduleID string) bool { return moduleID == paidModule })

	provider, err := libp2p.New(libp2p.ListenAddrStrings("/ip4/127.0.0.1/tcp/0"))
	if err != nil {
		t.Fatalf("libp2p.New(provider): %v", err)
	}
	t.Cleanup(func() { _ = provider.Close() })

	rig := &deliveryRig{t: t, gate: gate, clock: clock, provider: provider}
	wrapped := WrapDeliveryHost(provider, gate)
	wrapped.SetStreamHandler(protocol.ID(ModuleDeliveryProtocolID), func(s network.Stream) {
		defer s.Close()
		request, err := io.ReadAll(io.LimitReader(s, 16385))
		if err != nil || len(request) == 0 {
			return
		}
		rig.handled.Add(1)
		frame := decodeDeliveryFrame(request)
		if frame.kind == deliveryFrameChallengeRequest {
			_, _ = s.Write(buildTestChallengeResponse(frame.requestID, frame.moduleID))
		}
	})
	return rig
}

// client is one browser: one host, one connection to the provider.
func (r *deliveryRig) client() host.Host {
	r.t.Helper()
	c, err := libp2p.New(libp2p.NoListenAddrs)
	if err != nil {
		r.t.Fatalf("libp2p.New(client): %v", err)
	}
	r.t.Cleanup(func() { _ = c.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := c.Connect(ctx, peer.AddrInfo{ID: r.provider.ID(), Addrs: r.provider.Addrs()}); err != nil {
		r.t.Fatalf("Connect: %v", err)
	}
	return c
}

func (r *deliveryRig) exchange(c host.Host, frame []byte) []byte {
	r.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	s, err := c.NewStream(ctx, r.provider.ID(), protocol.ID(ModuleDeliveryProtocolID))
	if err != nil {
		r.t.Fatalf("NewStream: %v", err)
	}
	defer s.Close()
	if _, err := s.Write(frame); err != nil {
		r.t.Fatalf("Write: %v", err)
	}
	if err := s.CloseWrite(); err != nil {
		r.t.Fatalf("CloseWrite: %v", err)
	}
	answer, _ := io.ReadAll(s)
	return answer
}

// challenge sends an $LCH request and returns "" when a challenge was issued,
// else the refusal's ERROR_CODE.
func (r *deliveryRig) challenge(c host.Host, requestID, moduleID string) string {
	r.t.Helper()
	answer := r.exchange(c, buildTestChallengeRequest(requestID, moduleID, "1.0.0"))
	if len(answer) < 8 || !lch.LCHBufferHasIdentifier(answer) {
		r.t.Fatalf("challenge %s: answer is not an $LCH frame (%d bytes)", requestID, len(answer))
	}
	msg := lch.GetRootAsLCH(answer, 0)
	if string(msg.REQUEST_ID()) != requestID {
		r.t.Fatalf("challenge %s: answer echoes request %q", requestID, msg.REQUEST_ID())
	}
	switch int(msg.MESSAGE_TYPE()) {
	case challengeMessageTypeResponse:
		return ""
	case challengeMessageTypeError:
		return string(msg.ERROR_CODE())
	}
	r.t.Fatalf("challenge %s: unexpected message type %d", requestID, msg.MESSAGE_TYPE())
	return ""
}

// prove sends an $LPF and reports whether the handler was reached.
func (r *deliveryRig) prove(c host.Host, requestID, moduleID string) bool {
	r.t.Helper()
	before := r.handled.Load()
	r.exchange(c, buildTestProof(requestID, moduleID, "1.0.0"))
	return r.handled.Load() > before
}

func buildTestChallengeRequest(requestID, moduleID, version string) []byte {
	b := flatbuffers.NewBuilder(256)
	rid, mid, ver := b.CreateString(requestID), b.CreateString(moduleID), b.CreateString(version)
	lch.LCHStart(b)
	lch.LCHAddMESSAGE_TYPE(b, lch.EnumValueslicensingChallengeMessageType["Request"])
	lch.LCHAddREQUEST_ID(b, rid)
	lch.LCHAddMODULE_ID(b, mid)
	lch.LCHAddMODULE_VERSION(b, ver)
	lch.FinishLCHBuffer(b, lch.LCHEnd(b))
	return b.FinishedBytes()
}

func buildTestChallengeResponse(requestID, moduleID string) []byte {
	b := flatbuffers.NewBuilder(256)
	rid, mid := b.CreateString(requestID), b.CreateString(moduleID)
	lch.LCHStart(b)
	lch.LCHAddMESSAGE_TYPE(b, lch.EnumValueslicensingChallengeMessageType["Response"])
	lch.LCHAddROLE(b, lch.EnumValueslicensingChallengeRole["Provider"])
	lch.LCHAddREQUEST_ID(b, rid)
	lch.LCHAddMODULE_ID(b, mid)
	lch.FinishLCHBuffer(b, lch.LCHEnd(b))
	return b.FinishedBytes()
}

func buildTestProof(requestID, moduleID, version string) []byte {
	b := flatbuffers.NewBuilder(256)
	rid, mid, ver := b.CreateString(requestID), b.CreateString(moduleID), b.CreateString(version)
	lpf.LPFStart(b)
	lpf.LPFAddREQUEST_ID(b, rid)
	lpf.LPFAddMODULE_ID(b, mid)
	lpf.LPFAddMODULE_VERSION(b, ver)
	lpf.FinishLPFBuffer(b, lpf.LPFEnd(b))
	return b.FinishedBytes()
}

func TestDeliveryLimitDefaults(t *testing.T) {
	limits := DefaultDeliveryLimits()
	if limits.OpenRatePerSecond != 50 || limits.OpenBurst != 200 {
		t.Fatalf("default open bucket = %v/s burst %d, want 50/s burst 200", limits.OpenRatePerSecond, limits.OpenBurst)
	}
	if OpenPendingSlots != 256 || KeyServerPendingSlots != 512 {
		t.Fatalf("open share = %d of %d, want 256 of 512", OpenPendingSlots, KeyServerPendingSlots)
	}
	if MaxInFlightHandshakesPerConnection != 32 {
		t.Fatalf("per-connection cap = %d, want 32", MaxInFlightHandshakesPerConnection)
	}
	if DeliveryChallengeTTL != 30*time.Second {
		t.Fatalf("challenge TTL = %s, want 30s", DeliveryChallengeTTL)
	}

	cfg := &GrantPolicyConfig{DeliveryLimits: &DeliveryLimitsConfig{OpenRatePerSecond: 7, OpenBurst: 9}}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if got := cfg.EffectiveDeliveryLimits(); got.OpenRatePerSecond != 7 || got.OpenBurst != 9 {
		t.Fatalf("tuned limits = %+v, want 7/s burst 9", got)
	}
	if got := (&GrantPolicyConfig{}).EffectiveDeliveryLimits(); got != DefaultDeliveryLimits() {
		t.Fatalf("unset delivery_limits = %+v, want the defaults", got)
	}
	bad := &GrantPolicyConfig{DeliveryLimits: &DeliveryLimitsConfig{OpenRatePerSecond: -1}}
	if err := bad.Validate(); err == nil {
		t.Fatal("a negative open rate validated")
	}
}

// 1. The global bucket: at the defaults, 200 open frames pass at once, the
// 201st is refused, and the bucket refills at 50 per second.
func TestOpenBucketRefusesPastTheBurstAndRecovers(t *testing.T) {
	rig := newDeliveryRig(t, DefaultDeliveryLimits())
	browser := rig.client()

	for i := 0; i < DefaultOpenDeliveryBurst; i++ {
		if !rig.prove(browser, fmt.Sprintf("p-%d", i), openModule) {
			t.Fatalf("open proof %d of the burst was refused", i)
		}
	}
	if rig.prove(browser, "p-over", openModule) {
		t.Fatal("open proof 201 reached the key server; the bucket did not refuse it")
	}
	// An open $LCH draws from the same bucket and is refused with a frame.
	if code := rig.challenge(browser, "c-over", openModule); code != DeliveryRefusedRateLimited {
		t.Fatalf("open challenge past the burst: code %q, want %q", code, DeliveryRefusedRateLimited)
	}

	// 50/s: one token every 20 ms.
	rig.clock.Advance(20 * time.Millisecond)
	if code := rig.challenge(browser, "c-after", openModule); code != "" {
		t.Fatalf("open challenge after one refill interval: refused %q", code)
	}
	if code := rig.challenge(browser, "c-after-2", openModule); code != DeliveryRefusedRateLimited {
		t.Fatalf("second challenge in the same interval: code %q, want %q", code, DeliveryRefusedRateLimited)
	}
	rig.clock.Advance(time.Second)
	for i := 0; i < 50; i++ {
		if !rig.prove(browser, fmt.Sprintf("p-refill-%d", i), openModule) {
			t.Fatalf("proof %d of the 50 refilled in one second was refused", i)
		}
	}
}

// 2. Open handshakes hold at most half of the key server's 512 slots.
func TestOpenHandshakesHoldAtMostHalfThePendingTable(t *testing.T) {
	rig := newDeliveryRig(t, DeliveryLimits{OpenRatePerSecond: 1000, OpenBurst: 1000})

	// 256 open challenges on eight browsers (32 each, the per-connection cap).
	var browsers []host.Host
	n := 0
	for len(browsers) < OpenPendingSlots/MaxInFlightHandshakesPerConnection {
		b := rig.client()
		browsers = append(browsers, b)
		for i := 0; i < MaxInFlightHandshakesPerConnection; i++ {
			if code := rig.challenge(b, fmt.Sprintf("open-%d", n), openModule); code != "" {
				t.Fatalf("open challenge %d refused %q before the open share was full", n, code)
			}
			n++
		}
	}
	if got := rig.gate.Stats().OpenPending; got != OpenPendingSlots {
		t.Fatalf("open pending = %d, want %d", got, OpenPendingSlots)
	}

	fresh := rig.client()
	if code := rig.challenge(fresh, "open-over", openModule); code != DeliveryRefusedProviderBusy {
		t.Fatalf("open challenge 257: code %q, want %q", code, DeliveryRefusedProviderBusy)
	}
	// The other half is the paid lane's.
	if code := rig.challenge(fresh, "paid-while-open-full", paidModule); code != "" {
		t.Fatalf("paid challenge refused %q while only the open share was full", code)
	}

	// A proof frees its slot at once.
	if !rig.prove(browsers[0], "open-0", openModule) {
		t.Fatal("the proof for open-0 was refused")
	}
	if code := rig.challenge(fresh, "open-after-proof", openModule); code != "" {
		t.Fatalf("open challenge after a proof freed a slot: refused %q", code)
	}
	if code := rig.challenge(fresh, "open-over-again", openModule); code != DeliveryRefusedProviderBusy {
		t.Fatalf("open share full again: code %q, want %q", code, DeliveryRefusedProviderBusy)
	}

	// Unproven challenges expire with the key server's 30 s TTL, and not before.
	rig.clock.Advance(DeliveryChallengeTTL - time.Millisecond)
	if code := rig.challenge(fresh, "open-at-ttl-edge", openModule); code != DeliveryRefusedProviderBusy {
		t.Fatalf("a challenge 1 ms short of the TTL was dropped: code %q", code)
	}
	rig.clock.Advance(time.Millisecond)
	if got := rig.gate.Stats().OpenPending; got != 0 {
		t.Fatalf("open pending after the TTL = %d, want 0", got)
	}
	if code := rig.challenge(fresh, "open-after-ttl", openModule); code != "" {
		t.Fatalf("open challenge after the TTL: refused %q", code)
	}
}

// 3. At most 32 handshakes in flight per connection, open or paid.
func TestPerConnectionHandshakeCap(t *testing.T) {
	rig := newDeliveryRig(t, DefaultDeliveryLimits())
	first := rig.client()

	for i := 0; i < MaxInFlightHandshakesPerConnection; i++ {
		module := openModule
		if i%2 == 1 {
			module = paidModule
		}
		if code := rig.challenge(first, fmt.Sprintf("h-%d", i), module); code != "" {
			t.Fatalf("challenge %d on one connection refused %q", i, code)
		}
	}
	for _, module := range []string{openModule, paidModule} {
		if code := rig.challenge(first, "h-over-"+module, module); code != DeliveryRefusedTooManyInFlight {
			t.Fatalf("challenge 33 (%s): code %q, want %q", module, code, DeliveryRefusedTooManyInFlight)
		}
	}

	// Another connection is not affected.
	second := rig.client()
	if code := rig.challenge(second, "other-conn", paidModule); code != "" {
		t.Fatalf("challenge on a second connection refused %q", code)
	}

	// A proof is always admitted past the cap, and frees a slot.
	if !rig.prove(first, "h-1", paidModule) {
		t.Fatal("a proof on a full connection was refused")
	}
	if code := rig.challenge(first, "h-after-proof", paidModule); code != "" {
		t.Fatalf("challenge after a proof freed a slot: refused %q", code)
	}
	if code := rig.challenge(first, "h-over-again", paidModule); code != DeliveryRefusedTooManyInFlight {
		t.Fatalf("full again: code %q, want %q", code, DeliveryRefusedTooManyInFlight)
	}

	// Expiry frees the connection.
	rig.clock.Advance(DeliveryChallengeTTL)
	if got := rig.gate.Stats().InFlight; len(got) != 0 {
		t.Fatalf("in flight after the TTL = %v, want none", got)
	}
	if code := rig.challenge(first, "h-after-ttl", openModule); code != "" {
		t.Fatalf("challenge after the TTL: refused %q", code)
	}
}

// Paid traffic is admitted while open traffic is saturated on BOTH open limits.
func TestPaidAdmittedWhileOpenIsSaturated(t *testing.T) {
	rig := newDeliveryRig(t, DeliveryLimits{OpenRatePerSecond: 1, OpenBurst: OpenPendingSlots})

	n := 0
	for i := 0; i < OpenPendingSlots/MaxInFlightHandshakesPerConnection; i++ {
		b := rig.client()
		for j := 0; j < MaxInFlightHandshakesPerConnection; j++ {
			if code := rig.challenge(b, fmt.Sprintf("open-%d", n), openModule); code != "" {
				t.Fatalf("open challenge %d refused %q", n, code)
			}
			n++
		}
	}
	paid := rig.client()
	if code := rig.challenge(paid, "open-saturated", openModule); code != DeliveryRefusedProviderBusy {
		t.Fatalf("open challenge with the open share full: %q", code)
	}
	if rig.prove(paid, "open-orphan-proof", openModule) {
		t.Fatal("an open proof passed an empty bucket")
	}

	for i := 0; i < MaxInFlightHandshakesPerConnection; i++ {
		if code := rig.challenge(paid, fmt.Sprintf("paid-%d", i), paidModule); code != "" {
			t.Fatalf("paid challenge %d refused %q while open was saturated", i, code)
		}
	}
	for i := 0; i < 4; i++ {
		if !rig.prove(paid, fmt.Sprintf("paid-%d", i), paidModule) {
			t.Fatalf("paid proof %d refused while open was saturated", i)
		}
	}
	stats := rig.gate.Stats()
	if stats.OpenPending != OpenPendingSlots || stats.TotalPending != OpenPendingSlots+MaxInFlightHandshakesPerConnection-4 {
		t.Fatalf("pending = %d open / %d total, want %d / %d", stats.OpenPending, stats.TotalPending,
			OpenPendingSlots, OpenPendingSlots+MaxInFlightHandshakesPerConnection-4)
	}
}

// A module nobody published, and a malformed frame, are open traffic: naming
// a module that does not exist is not a way around the open limits.
func TestUnknownModulesAndJunkAreOpenTraffic(t *testing.T) {
	rig := newDeliveryRig(t, DeliveryLimits{OpenRatePerSecond: 1, OpenBurst: 2})
	browser := rig.client()

	if code := rig.challenge(browser, "u-1", "com.nobody.module"); code != "" {
		t.Fatalf("first unknown-module challenge refused %q", code)
	}
	if code := rig.challenge(browser, "u-2", openModule); code != "" {
		t.Fatalf("open challenge refused %q", code)
	}
	if code := rig.challenge(browser, "u-3", "com.nobody.module"); code != DeliveryRefusedRateLimited {
		t.Fatalf("unknown module past the burst: %q, want %q", code, DeliveryRefusedRateLimited)
	}

	// Junk is not decoded as a licensing frame, does not panic, and reaches
	// the handler (which refuses it as the module does).
	before := rig.handled.Load()
	rig.exchange(browser, []byte("\x08\x00\x00\x00$LCH\xff\xff\xff\x7f"))
	if rig.handled.Load() != before+1 {
		t.Fatal("a malformed frame did not reach the handler")
	}
}

func TestRetiredVersionIsRefused(t *testing.T) {
	rig := newDeliveryRig(t, DefaultDeliveryLimits())
	browser := rig.client()

	rig.gate.RetireKeyID(openModule + ":1.0.0")
	if code := rig.challenge(browser, "r-1", openModule); code != DeliveryRefusedVersionRetired {
		t.Fatalf("retired version: code %q, want %q", code, DeliveryRefusedVersionRetired)
	}
	rig.gate.ReinstateKeyID(openModule + ":1.0.0")
	if code := rig.challenge(browser, "r-2", openModule); code != "" {
		t.Fatalf("reinstated version refused %q", code)
	}
}

// The gate wraps only the module-delivery protocol.
func TestWrapDeliveryHostLeavesOtherProtocolsAlone(t *testing.T) {
	rig := newDeliveryRig(t, DeliveryLimits{OpenRatePerSecond: 1, OpenBurst: 1})
	wrapped := WrapDeliveryHost(rig.provider, rig.gate)
	if WrapDeliveryHost(wrapped, rig.gate) != wrapped {
		t.Fatal("wrapping twice stacked a second gate")
	}
	const echo = "/test/echo/1.0.0"
	wrapped.SetStreamHandler(echo, func(s network.Stream) {
		defer s.Close()
		b, _ := io.ReadAll(s)
		_, _ = s.Write(b)
	})
	browser := rig.client()
	for i := 0; i < 5; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		s, err := browser.NewStream(ctx, rig.provider.ID(), echo)
		cancel()
		if err != nil {
			t.Fatalf("NewStream(echo): %v", err)
		}
		frame := buildTestProof("e", openModule, "1.0.0")
		_, _ = s.Write(frame)
		_ = s.CloseWrite()
		got, _ := io.ReadAll(s)
		_ = s.Close()
		if len(got) != len(frame) {
			t.Fatalf("echo %d: %d bytes back, want %d", i, len(got), len(frame))
		}
	}
}
