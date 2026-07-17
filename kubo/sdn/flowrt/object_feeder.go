package flowrt

// object_feeder.go — the OD-flow multi-object SOURCE as a HOST-MODEL handler. It
// hands out one $OEM record per drain from an in-memory queue, so a FlowPool of N
// resident instances fits a whole provider's object set IN PARALLEL while no $OEM
// ever touches the store (in-memory-only ephemeris, by construction — the feeder
// holds the transient set only for the run, and the flow wires $OEM to od.fit, not
// to any store node).
//
// This is the production driver that replaces the store-backed StoreEphemerisSource
// ("only ISS" bug): the host fetches + splits a provider's ephemeris into a
// transient []$OEM set, seeds an ObjectFeeder, and each pool.Run() pulls the next
// distinct object here. It mirrors the store node (store_node.go) on the ingress
// side — a host capability primitive, not flow business logic.

import (
	"context"
	"sync/atomic"
)

// ObjectFeeder hands out queued $OEM records one per Next(), safe for concurrent
// use. A single feeder shared across a FlowPool lets M concurrent drains each pull
// a DISTINCT object (atomic cursor), which is how one baked per-object flow scales
// to a whole constellation.
type ObjectFeeder struct {
	records [][]byte
	cursor  int64
}

// NewObjectFeeder seeds a feeder over the transient in-memory $OEM set (each entry
// a size-prefixed $OEM FlatBuffer). The feeder does not copy; callers must not
// mutate the slices after handing them over.
func NewObjectFeeder(records [][]byte) *ObjectFeeder {
	return &ObjectFeeder{records: records}
}

// Next returns the next queued record and true, or (nil, false) once drained.
func (f *ObjectFeeder) Next() ([]byte, bool) {
	i := atomic.AddInt64(&f.cursor, 1) - 1
	if i >= int64(len(f.records)) {
		return nil, false
	}
	return f.records[i], true
}

// Remaining reports how many records have not yet been handed out.
func (f *ObjectFeeder) Remaining() int {
	handed := atomic.LoadInt64(&f.cursor)
	if handed >= int64(len(f.records)) {
		return 0
	}
	return len(f.records) - int(handed)
}

// Len reports the total number of queued records.
func (f *ObjectFeeder) Len() int { return len(f.records) }

// NewObjectFeederHandler returns a host-model SOURCE handler that emits the next
// queued $OEM record on outputPort as a single output frame (the runtime shim
// re-stamps every inter-node frame ALIGNED_BINARY/align-8, so the typed record
// flows to od.fit zero-copy). When the feeder is drained it emits nothing
// (StatusCode 0, no Outputs) — a surplus drain is a harmless no-op, so a pool may
// be driven more times than there are objects without error.
func NewObjectFeederHandler(feeder *ObjectFeeder, outputPort string) func(context.Context, *InvocationArgs) (*InvocationResult, error) {
	return func(_ context.Context, _ *InvocationArgs) (*InvocationResult, error) {
		rec, ok := feeder.Next()
		if !ok {
			return &InvocationResult{StatusCode: 0}, nil
		}
		return &InvocationResult{
			StatusCode: 0,
			Outputs:    []FrameOutput{{PortID: outputPort, Bytes: rec}},
		}, nil
	}
}
