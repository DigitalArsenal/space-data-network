package sdnservices

import (
	"encoding/json"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/ipfs/kubo/sdn/modulert"
)

type fakeWakeupTimer struct {
	clock   *fakeWakeupClock
	id      uint64
	stopped bool
}

func TestWakeupBrokerRegistrationOwnershipAndUnregisterAreGenerationSafe(t *testing.T) {
	start := time.Unix(3_000, 0).UTC()
	clock := newFakeWakeupClock(start)
	broker := NewWakeupBroker(clock)
	defer broker.Close()
	identity := WakeupIdentity{ArtifactHash: "abababababababababababababababababababababababababababababababab", NodeID: "node-a"}

	firstDelivered := make(chan Wakeup, 1)
	unregisterFirst, err := broker.Register(identity, func(wakeup Wakeup) { firstDelivered <- wakeup })
	if err != nil {
		t.Fatal(err)
	}
	if _, err := broker.Register(identity, func(Wakeup) {}); err == nil {
		t.Fatal("Register() replaced an active handler for the same signed node identity")
	}
	if err := broker.Arm(identity, "old-token", start.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	unregisterFirst()
	clock.Advance(time.Second)
	select {
	case wakeup := <-firstDelivered:
		t.Fatalf("unregistered handler received armed wakeup: %+v", wakeup)
	default:
	}

	secondDelivered := make(chan Wakeup, 1)
	unregisterSecond, err := broker.Register(identity, func(wakeup Wakeup) { secondDelivered <- wakeup })
	if err != nil {
		t.Fatal(err)
	}
	defer unregisterSecond()
	// A stale cleanup closure from the first instance must not unregister the
	// replacement instance that now owns this identity.
	unregisterFirst()
	if err := broker.Arm(identity, "new-token", clock.Now().Add(time.Second)); err != nil {
		t.Fatalf("Arm() after stale unregister = %v", err)
	}
	clock.Advance(time.Second)
	if wakeup := <-secondDelivered; wakeup.Token != "new-token" {
		t.Fatalf("replacement delivery = %+v", wakeup)
	}
}

func (timer *fakeWakeupTimer) Stop() bool {
	timer.clock.mu.Lock()
	defer timer.clock.mu.Unlock()
	if timer.stopped {
		return false
	}
	timer.stopped = true
	delete(timer.clock.callbacks, timer.id)
	return true
}

type fakeWakeupCallback struct {
	at time.Time
	fn func()
}

type fakeWakeupClock struct {
	mu        sync.Mutex
	now       time.Time
	nextID    uint64
	callbacks map[uint64]fakeWakeupCallback
}

func newFakeWakeupClock(now time.Time) *fakeWakeupClock {
	return &fakeWakeupClock{now: now, callbacks: make(map[uint64]fakeWakeupCallback)}
}

func (clock *fakeWakeupClock) Now() time.Time {
	clock.mu.Lock()
	defer clock.mu.Unlock()
	return clock.now
}

func (clock *fakeWakeupClock) AfterFunc(delay time.Duration, fn func()) WakeupTimer {
	clock.mu.Lock()
	defer clock.mu.Unlock()
	clock.nextID++
	id := clock.nextID
	clock.callbacks[id] = fakeWakeupCallback{at: clock.now.Add(delay), fn: fn}
	return &fakeWakeupTimer{clock: clock, id: id}
}

func (clock *fakeWakeupClock) Advance(delta time.Duration) {
	clock.mu.Lock()
	clock.now = clock.now.Add(delta)
	var ids []int
	for id, callback := range clock.callbacks {
		if !callback.at.After(clock.now) {
			ids = append(ids, int(id))
		}
	}
	sort.Ints(ids)
	callbacks := make([]func(), 0, len(ids))
	for _, rawID := range ids {
		id := uint64(rawID)
		callbacks = append(callbacks, clock.callbacks[id].fn)
		delete(clock.callbacks, id)
	}
	clock.mu.Unlock()
	for _, callback := range callbacks {
		callback()
	}
}

func TestWakeupBrokerArmsCancelsAndSuppressesStaleDelivery(t *testing.T) {
	start := time.Unix(1_000, 0).UTC()
	clock := newFakeWakeupClock(start)
	broker := NewWakeupBroker(clock)
	defer broker.Close()
	identity := WakeupIdentity{ArtifactHash: "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee", NodeID: "node-a"}
	delivered := make(chan Wakeup, 4)
	unregister, err := broker.Register(identity, func(wakeup Wakeup) { delivered <- wakeup })
	if err != nil {
		t.Fatal(err)
	}
	defer unregister()

	if err := broker.Arm(identity, "token-a", start.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	clock.Advance(999 * time.Millisecond)
	select {
	case got := <-delivered:
		t.Fatalf("wakeup delivered early: %+v", got)
	default:
	}
	clock.Advance(time.Millisecond)
	if got := <-delivered; got.Token != "token-a" || !got.RequestedAt.Equal(start.Add(time.Second)) {
		t.Fatalf("delivered wakeup = %+v", got)
	}

	if err := broker.Arm(identity, "token-b", clock.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if canceled := broker.Cancel(identity, "token-b"); !canceled {
		t.Fatal("Cancel() = false, want true")
	}
	clock.Advance(time.Second)
	select {
	case got := <-delivered:
		t.Fatalf("canceled wakeup delivered: %+v", got)
	default:
	}

	// Re-arming the same opaque token invalidates the old callback. Only the
	// newest requested instant may deliver.
	if err := broker.Arm(identity, "token-c", clock.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := broker.Arm(identity, "token-c", clock.Now().Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	clock.Advance(time.Second)
	select {
	case got := <-delivered:
		t.Fatalf("stale re-armed wakeup delivered: %+v", got)
	default:
	}
	clock.Advance(time.Second)
	if got := <-delivered; got.Token != "token-c" {
		t.Fatalf("latest wakeup = %+v, want token-c", got)
	}
}

func TestWakeupCapabilityOnlyAcceptsGenericArmAndCancel(t *testing.T) {
	start := time.Unix(2_000, 0).UTC()
	clock := newFakeWakeupClock(start)
	broker := NewWakeupBroker(clock)
	defer broker.Close()
	identity := WakeupIdentity{ArtifactHash: "ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff", NodeID: "node-a"}
	delivered := make(chan Wakeup, 1)
	_, err := broker.Register(identity, func(wakeup Wakeup) { delivered <- wakeup })
	if err != nil {
		t.Fatal(err)
	}
	resolve := func(*modulert.Module) (string, string, error) { return identity.ArtifactHash, identity.NodeID, nil }
	handler := NewWakeupCapFactory(broker, resolve)(nil, modulert.NewHostBridge(nil, []string{"timers"}))

	armPayload, _ := json.Marshal(map[string]interface{}{"token": "opaque-1", "at_unix_ms": start.Add(time.Second).UnixMilli()})
	response, err := handler("timers.arm", armPayload)
	if err != nil || !jsonResponseOK(response) {
		t.Fatalf("timers.arm response/error = %s/%v", response, err)
	}
	clock.Advance(time.Second)
	if got := <-delivered; got.Token != "opaque-1" {
		t.Fatalf("delivered = %+v", got)
	}

	if response, _ := handler("timers.policy", []byte(`{"expression":"* * * * *"}`)); jsonResponseOK(response) {
		t.Fatalf("policy operation unexpectedly accepted: %s", response)
	}
	denied := NewWakeupCapFactory(broker, resolve)(nil, modulert.NewHostBridge(nil, nil))
	if response, _ := denied("timers.arm", armPayload); jsonResponseOK(response) {
		t.Fatalf("timers.arm succeeded without timers capability: %s", response)
	}
}

func jsonResponseOK(raw []byte) bool {
	var response struct {
		OK bool `json:"ok"`
	}
	return json.Unmarshal(raw, &response) == nil && response.OK
}
