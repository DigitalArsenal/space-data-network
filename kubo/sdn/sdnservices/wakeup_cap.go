package sdnservices

import (
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/ipfs/kubo/sdn/modulert"
)

// WakeupTimer is the only timer behavior the broker needs from a host clock.
type WakeupTimer interface {
	Stop() bool
}

// WakeupClock makes generic wakeup delivery deterministic in tests and keeps
// all scheduling policy outside the host.
type WakeupClock interface {
	Now() time.Time
	AfterFunc(time.Duration, func()) WakeupTimer
}

type systemWakeupClock struct{}

func (systemWakeupClock) Now() time.Time { return time.Now() }
func (systemWakeupClock) AfterFunc(delay time.Duration, callback func()) WakeupTimer {
	return time.AfterFunc(delay, callback)
}

// WakeupIdentity addresses one independently instantiated signed node.
type WakeupIdentity struct {
	ArtifactHash string
	NodeID       string
}

// Wakeup is the opaque notification delivered to a registered node.
type Wakeup struct {
	Identity    WakeupIdentity
	Token       string
	RequestedAt time.Time
	DeliveredAt time.Time
}

type wakeupKey struct {
	identity WakeupIdentity
	token    string
}

type armedWakeup struct {
	sequence            uint64
	handlerRegistration uint64
	requestedAt         time.Time
	timer               WakeupTimer
}

type wakeupRegistration struct {
	sequence uint64
	handler  func(Wakeup)
}

// WakeupBroker implements only registration, absolute arm, cancellation, and
// delivery. It does not parse or retain scheduling policy.
type WakeupBroker struct {
	clock    WakeupClock
	mu       sync.Mutex
	next     uint64
	closed   bool
	handlers map[WakeupIdentity]wakeupRegistration
	armed    map[wakeupKey]armedWakeup
}

func NewWakeupBroker(clock WakeupClock) *WakeupBroker {
	if clock == nil {
		clock = systemWakeupClock{}
	}
	return &WakeupBroker{
		clock:    clock,
		handlers: make(map[WakeupIdentity]wakeupRegistration),
		armed:    make(map[wakeupKey]armedWakeup),
	}
}

func (broker *WakeupBroker) Register(identity WakeupIdentity, handler func(Wakeup)) (func(), error) {
	if err := validateWakeupIdentity(identity); err != nil {
		return nil, err
	}
	if handler == nil {
		return nil, errors.New("wakeup handler is required")
	}
	broker.mu.Lock()
	defer broker.mu.Unlock()
	if broker.closed {
		return nil, errors.New("wakeup broker is closed")
	}
	if _, exists := broker.handlers[identity]; exists {
		return nil, errors.New("wakeup identity already has a registered delivery handler")
	}
	broker.next++
	registrationSequence := broker.next
	broker.handlers[identity] = wakeupRegistration{sequence: registrationSequence, handler: handler}
	return func() {
		broker.mu.Lock()
		current, exists := broker.handlers[identity]
		if exists && current.sequence == registrationSequence {
			delete(broker.handlers, identity)
			for key, armed := range broker.armed {
				if key.identity != identity {
					continue
				}
				if armed.timer != nil {
					armed.timer.Stop()
				}
				delete(broker.armed, key)
			}
		}
		broker.mu.Unlock()
	}, nil
}

func (broker *WakeupBroker) Arm(identity WakeupIdentity, token string, requestedAt time.Time) error {
	if err := validateWakeupIdentity(identity); err != nil {
		return err
	}
	if token == "" || len(token) > 1024 {
		return errors.New("wakeup token must contain 1 to 1024 bytes")
	}
	key := wakeupKey{identity: identity, token: token}
	broker.mu.Lock()
	defer broker.mu.Unlock()
	if broker.closed {
		return errors.New("wakeup broker is closed")
	}
	registration, registered := broker.handlers[identity]
	if !registered || registration.handler == nil {
		return errors.New("wakeup identity has no registered delivery handler")
	}
	if current, ok := broker.armed[key]; ok && current.timer != nil {
		current.timer.Stop()
	}
	broker.next++
	sequence := broker.next
	delay := requestedAt.Sub(broker.clock.Now())
	if delay < 0 {
		delay = 0
	}
	timer := broker.clock.AfterFunc(delay, func() { broker.deliver(key, sequence) })
	broker.armed[key] = armedWakeup{
		sequence:            sequence,
		handlerRegistration: registration.sequence,
		requestedAt:         requestedAt,
		timer:               timer,
	}
	return nil
}

func (broker *WakeupBroker) Cancel(identity WakeupIdentity, token string) bool {
	key := wakeupKey{identity: identity, token: token}
	broker.mu.Lock()
	defer broker.mu.Unlock()
	current, ok := broker.armed[key]
	if !ok {
		return false
	}
	delete(broker.armed, key)
	if current.timer != nil {
		current.timer.Stop()
	}
	return true
}

func (broker *WakeupBroker) deliver(key wakeupKey, sequence uint64) {
	broker.mu.Lock()
	current, ok := broker.armed[key]
	if !ok || current.sequence != sequence || broker.closed {
		broker.mu.Unlock()
		return
	}
	delete(broker.armed, key)
	registration, registered := broker.handlers[key.identity]
	if !registered || registration.sequence != current.handlerRegistration {
		broker.mu.Unlock()
		return
	}
	handler := registration.handler
	wakeup := Wakeup{
		Identity:    key.identity,
		Token:       key.token,
		RequestedAt: current.requestedAt,
		DeliveredAt: broker.clock.Now(),
	}
	broker.mu.Unlock()
	if handler != nil {
		handler(wakeup)
	}
}

func (broker *WakeupBroker) Close() {
	broker.mu.Lock()
	defer broker.mu.Unlock()
	if broker.closed {
		return
	}
	broker.closed = true
	for key, current := range broker.armed {
		if current.timer != nil {
			current.timer.Stop()
		}
		delete(broker.armed, key)
	}
	broker.handlers = nil
}

func validateWakeupIdentity(identity WakeupIdentity) error {
	if len(identity.ArtifactHash) != 64 || !isHex(identity.ArtifactHash) {
		return errors.New("wakeup artifact hash must be 64 hexadecimal characters")
	}
	return validateOpaqueSegment("wakeup node id", identity.NodeID)
}

// NewWakeupCapFactory exposes generic absolute arm/cancel operations to a
// capability-approved node. The identity is host-bound, never guest-supplied.
func NewWakeupCapFactory(broker *WakeupBroker, resolveIdentity OpaqueStateIdentityResolver) modulert.BridgeCapFactory {
	return func(mod *modulert.Module, bridge *modulert.HostBridge) modulert.CapHandler {
		return func(operation string, payload []byte) ([]byte, error) {
			if bridge == nil || !bridge.HasCapability("timers") {
				return errCapJSON("wakeup operations require the timers capability grant"), nil
			}
			if broker == nil {
				return errCapJSON("wakeup broker is not configured"), nil
			}
			if resolveIdentity == nil {
				return errCapJSON("wakeup identity resolver is not configured"), nil
			}
			// Resolve at call time, after Module.Load has published its manifest.
			// Resolving during capability provisioning would bind every module to
			// the temporary "unknown-module" ID.
			artifactHash, nodeID, identityErr := resolveIdentity(mod)
			if identityErr != nil {
				return errCapJSON("wakeup identity is unavailable: " + identityErr.Error()), nil
			}
			identity := WakeupIdentity{ArtifactHash: artifactHash, NodeID: nodeID}
			var request struct {
				Token    string `json:"token"`
				AtUnixMS int64  `json:"at_unix_ms"`
			}
			if len(payload) > 0 && json.Unmarshal(payload, &request) != nil {
				return errCapJSON("wakeup request is not valid JSON"), nil
			}
			switch operation {
			case "timers.arm":
				if request.AtUnixMS <= 0 {
					return errCapJSON("timers.arm requires at_unix_ms"), nil
				}
				if err := broker.Arm(identity, request.Token, time.UnixMilli(request.AtUnixMS)); err != nil {
					return errCapJSON("timers.arm failed: " + err.Error()), nil
				}
				return okCapJSON(map[string]interface{}{"armed": true, "token": request.Token}), nil
			case "timers.cancel":
				return okCapJSON(map[string]interface{}{"canceled": broker.Cancel(identity, request.Token), "token": request.Token}), nil
			default:
				return errCapJSON(fmt.Sprintf("unknown wakeup operation: %s", operation)), nil
			}
		}
	}
}
