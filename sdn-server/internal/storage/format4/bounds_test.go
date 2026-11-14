package format4

import (
	"context"
	"encoding/binary"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

// The request bounds (Options.RequestDeadline, Options.WriteHang) against an
// engine that takes requests and never answers them: what the node's writes
// met on 2026-11-14 (the engine's writer thread looping, nothing trapping).

// silentMem is a shared memory nobody on the engine side ever writes.
type silentMem struct {
	mu sync.Mutex
	b  []byte
}

func (m *silentMem) Load32(off uint32) uint32 {
	m.mu.Lock()
	defer m.mu.Unlock()
	return binary.LittleEndian.Uint32(m.b[off:])
}

func (m *silentMem) Store32(off uint32, v uint32) {
	m.mu.Lock()
	defer m.mu.Unlock()
	binary.LittleEndian.PutUint32(m.b[off:], v)
}

func (m *silentMem) Add32(off uint32, d uint32) uint32 {
	m.mu.Lock()
	defer m.mu.Unlock()
	v := binary.LittleEndian.Uint32(m.b[off:]) + d
	binary.LittleEndian.PutUint32(m.b[off:], v)
	return v
}

func (m *silentMem) CAS32(off uint32, old, new uint32) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if binary.LittleEndian.Uint32(m.b[off:]) != old {
		return false
	}
	binary.LittleEndian.PutUint32(m.b[off:], new)
	return true
}

func (m *silentMem) Load64(off uint32) uint64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	return binary.LittleEndian.Uint64(m.b[off:])
}

func (m *silentMem) Store64(off uint32, v uint64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	binary.LittleEndian.PutUint64(m.b[off:], v)
}

func (m *silentMem) CAS64(off uint32, old, new uint64) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if binary.LittleEndian.Uint64(m.b[off:]) != old {
		return false
	}
	binary.LittleEndian.PutUint64(m.b[off:], new)
	return true
}

func (m *silentMem) ReadInto(off uint32, dst []byte) {
	m.mu.Lock()
	defer m.mu.Unlock()
	copy(dst, m.b[off:])
}

func (m *silentMem) WriteBytes(off uint32, src []byte) {
	m.mu.Lock()
	defer m.mu.Unlock()
	copy(m.b[off:], src)
}

// silentInstance is the host and control side: it stops when told to, and
// its Fence does what a trap's fence does to the Engine (P4Instance.Fence:
// the instance stops, then OnFailure fences the Engine and tells the store).
type silentInstance struct {
	e         *Engine
	stopping  chan struct{}
	stopOnce  sync.Once
	mu        sync.Mutex
	fences    []error
	onFailure func(error)
}

func (s *silentInstance) stop() { s.stopOnce.Do(func() { close(s.stopping) }) }

func (s *silentInstance) Enter() bool {
	select {
	case <-s.stopping:
		return false
	default:
		return true
	}
}
func (s *silentInstance) Exit()                     {}
func (s *silentInstance) Wake(int) error            { return nil }
func (s *silentInstance) Stopping() <-chan struct{} { return s.stopping }

func (s *silentInstance) ControlBytes(string, []byte) (int32, error)        { return 0, nil }
func (s *silentInstance) ControlCall(string, ...interface{}) (int32, error) { return 0, nil }
func (s *silentInstance) ControlOut(string, int) ([]byte, int32, error)     { return nil, 0, nil }
func (s *silentInstance) StopWithin(time.Duration) (int32, error)           { s.stop(); return 0, nil }

func (s *silentInstance) Fence(cause error) {
	s.mu.Lock()
	s.fences = append(s.fences, cause)
	s.mu.Unlock()
	s.stop()
	s.e.fence(cause)
	if s.onFailure != nil {
		s.onFailure(cause)
	}
}

func (s *silentInstance) fenced() []error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]error(nil), s.fences...)
}

// silentLayout is a flatsql_p4_layout over silentMem: two slots per pool,
// one queue of 8 cells per class, one service thread per class.
func silentLayout() (raw []byte, memBytes int) {
	raw = make([]byte, layoutBytes)
	put := func(off int, v uint32) { binary.LittleEndian.PutUint32(raw[off:], v) }
	put(0, layoutVersion)
	put(4, layoutBytes)
	put(8, slotHeader)
	put(12, 32) // stop word
	base := uint32(4096)
	for p, req := range []uint32{128 << 10, 64 << 10} {
		ring := uint32(64 << 10)
		stride := uint32(slotHeader) + req + ring
		put(16+4*p, 2)
		put(24+4*p, base)
		put(32+4*p, stride)
		put(40+4*p, req)
		put(48+4*p, ring)
		base += 2 * stride
	}
	for c := 0; c < 4; c++ {
		put(56+4*c, uint32(1024+128*c)) // cells
		put(72+4*c, 7)                  // mask
		put(88+4*c, uint32(2048+16*c))  // enqueue position
		put(104+4*c, uint32(2056+16*c)) // dequeue position
	}
	put(120, 4)
	for i := 0; i < 4; i++ {
		put(128+4*i, uint32(i+1))    // thread i serves class i+1
		put(384+4*i, uint32(64+8*i)) // its doorbell pair
	}
	return raw, int(base)
}

// silentEngine is an Engine over an engine that never answers.
func silentEngine(t *testing.T, opt Options) (*Engine, *silentInstance) {
	t.Helper()
	raw, n := silentLayout()
	mem := &silentMem{b: make([]byte, n)}
	for c := 0; c < 4; c++ { // Vyukov cells start with seq = index
		for i := 0; i < 8; i++ {
			mem.Store64(uint32(1024+128*c+16*i), uint64(i))
		}
	}
	e := &Engine{}
	e.configure(opt)
	inst := &silentInstance{e: e, stopping: make(chan struct{}), onFailure: opt.OnFailure}
	if err := e.attach(mem, inst, inst, raw); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(inst.stop) // ends the background drains of abandoned requests
	return e, inst
}

func TestBoundsDefaults(t *testing.T) {
	var e Engine
	e.configure(Options{})
	if e.deadline != DefaultRequestDeadline || e.writeHang != DefaultWriteHang {
		t.Fatalf("defaults: deadline %s, write hang %s", e.deadline, e.writeHang)
	}
	e.configure(Options{RequestDeadline: -1, WriteHang: -1})
	if e.deadline != 0 || e.writeHang != 0 {
		t.Fatalf("negative must turn the bounds off: deadline %s, write hang %s", e.deadline, e.writeHang)
	}
}

// An unanswered write fences the engine through the instance's fence, and
// the write returns (stopped) instead of waiting forever.
func TestUnansweredWriteFencesEngine(t *testing.T) {
	var failures []error
	var mu sync.Mutex
	e, inst := silentEngine(t, Options{RequestDeadline: -1, WriteHang: 150 * time.Millisecond,
		OnFailure: func(err error) { mu.Lock(); failures = append(failures, err); mu.Unlock() }})
	start := time.Now()
	_, err := e.Supersede(context.Background(), "OMM", "provider", "source", "batch", true)
	took := time.Since(start)
	if err == nil || !errors.Is(err, ErrStopped) {
		t.Fatalf("write on a hung engine: err = %v, want a stopped error", err)
	}
	if took < 150*time.Millisecond || took > 5*time.Second {
		t.Fatalf("the write returned after %s (bound 150ms)", took)
	}
	fences := inst.fenced()
	if len(fences) != 1 || !errors.Is(fences[0], ErrWriteHang) || !strings.Contains(fences[0].Error(), "SUPERSEDE") {
		t.Fatalf("fences = %v, want one ErrWriteHang naming SUPERSEDE", fences)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(failures) != 1 || !errors.Is(failures[0], ErrWriteHang) {
		t.Fatalf("OnFailure got %v, want the hang (the store reopens on it)", failures)
	}
	if _, err := e.Supersede(context.Background(), "OMM", "provider", "source", "batch", true); !errors.Is(err, ErrStopped) {
		t.Fatalf("a call after the fence: %v, want stopped", err)
	}
}

// A request that is answered in time is not fenced; a read is never fenced.
func TestReadIsNotFencedAsHung(t *testing.T) {
	e, inst := silentEngine(t, Options{RequestDeadline: 300 * time.Millisecond, WriteHang: 100 * time.Millisecond})
	_, err := e.Scan(context.Background(), Query{Type: "OMM", Limit: 1})
	if !errors.Is(err, ErrUnanswered) {
		t.Fatalf("read on a silent engine: %v, want ErrUnanswered", err)
	}
	if f := inst.fenced(); len(f) != 0 {
		t.Fatalf("a read fenced the engine: %v", f)
	}
}

// Every request is bounded by the request deadline even when the caller's
// context never ends (the node passes its store's lifetime context).
func TestRequestDeadlineBoundsCallsWithoutOne(t *testing.T) {
	e, _ := silentEngine(t, Options{RequestDeadline: 200 * time.Millisecond, WriteHang: -1})
	for _, tc := range []struct {
		name string
		call func(context.Context) error
	}{
		{"SUPERSEDE", func(ctx context.Context) error {
			_, err := e.Supersede(ctx, "OMM", "provider", "source", "batch", true)
			return err
		}},
		{"SCAN", func(ctx context.Context) error { _, err := e.Scan(ctx, Query{Type: "OMM", Limit: 1}); return err }},
		{"SQL", func(ctx context.Context) error {
			_, err := e.SQL(ctx, SQLRequest{SQL: "SELECT 1"}, nil)
			return err
		}},
	} {
		start := time.Now()
		err := tc.call(context.Background())
		took := time.Since(start)
		if !errors.Is(err, ErrUnanswered) || !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("%s: err = %v, want ErrUnanswered wrapping context.DeadlineExceeded", tc.name, err)
		}
		// The deadline, then at most abandonGrace for the cancel to land.
		if took < 200*time.Millisecond || took > 200*time.Millisecond+abandonGrace+2*time.Second {
			t.Fatalf("%s returned after %s", tc.name, took)
		}
	}
}

// The caller's own context still ends a request early, with its own error.
func TestCallerContextEndsRequest(t *testing.T) {
	e, inst := silentEngine(t, Options{}) // the default bounds, far away
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_, err := e.Supersede(ctx, "OMM", "provider", "source", "batch", true)
	if !errors.Is(err, context.DeadlineExceeded) || errors.Is(err, ErrUnanswered) {
		t.Fatalf("err = %v, want the caller's deadline (not ErrUnanswered)", err)
	}
	ctx2, cancel2 := context.WithCancel(context.Background())
	time.AfterFunc(50*time.Millisecond, cancel2)
	if _, err := e.Scan(ctx2, Query{Type: "OMM", Limit: 1}); !errors.Is(err, context.Canceled) {
		t.Fatalf("read: err = %v, want context.Canceled", err)
	}
	if f := inst.fenced(); len(f) != 0 {
		t.Fatalf("fenced: %v", f)
	}
}

// BenchmarkRequestBounds is what the bounds add to one request on the Go
// side: the deadline's context and, for a write, the hang timer.
func BenchmarkRequestBounds(b *testing.B) {
	for _, bc := range []struct {
		name  string
		opt   Options
		op    uint32
		class Class
	}{
		{"write/off", Options{RequestDeadline: -1, WriteHang: -1}, opPut, ClassWrite},
		{"write/on", Options{}, opPut, ClassWrite},
		{"read/off", Options{RequestDeadline: -1, WriteHang: -1}, opScan, ClassInteractive},
		{"read/on", Options{}, opScan, ClassInteractive},
	} {
		b.Run(bc.name, func(b *testing.B) {
			var e Engine
			e.configure(bc.opt)
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				ctx, cancel := e.bound(context.Background(), bc.op)
				stop := e.watch(bc.op, bc.class)
				_ = ctx
				stop()
				cancel()
			}
		})
	}
}

// A write queued while a QUOTA_GC or REBUILD runs is not fenced as hung.
func TestLongMaintenanceDefersHang(t *testing.T) {
	e, inst := silentEngine(t, Options{RequestDeadline: -1, WriteHang: 100 * time.Millisecond})
	stopLong := e.watch(opRebuild, ClassWrite)
	ctx, cancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
	defer cancel()
	_, err := e.Supersede(ctx, "OMM", "provider", "source", "batch", true)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want the caller's deadline", err)
	}
	if f := inst.fenced(); len(f) != 0 {
		t.Fatalf("fenced while a REBUILD ran: %v", f)
	}
	stopLong()
}
