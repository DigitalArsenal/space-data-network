package wasmrt

// Service threads (design §5.4, A21, A22, A24): the host side of a threaded
// partition-store instance talks to its guest threads through words in the
// shared linear memory, never through a guest call on a hot path.
//
//   - SharedMemory: atomic loads and stores at guest addresses. The base
//     pointer is taken once. WasmEdge reserves the whole address range of a
//     memory up front on 64-bit mmap platforms and grows it in place
//     (lib/system/allocator.cpp), so the base does not move on memory.grow;
//     BaseStable re-checks that, and the layout's generation word is the
//     documented fallback.
//   - Watchdog: a service thread increments its heartbeat word every loop
//     iteration and stores HeartbeatExited when it leaves. A heartbeat that
//     has not moved for longer than the stale limit is a hung thread, which the
//     supervisor treats as a trap (A21).

import (
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"

	"github.com/second-state/WasmEdge-go/wasmedge"
)

// HeartbeatExited is the value a service thread stores in its heartbeat slot
// when it leaves; the watchdog then stops watching it.
const HeartbeatExited = ^uint32(0)

// SharedMemory is the host's view of a module's shared linear memory.
type SharedMemory struct {
	mem  *wasmedge.Memory
	base unsafe.Pointer
	// size is the byte length the accessor admits without re-checking. Guest
	// layout words live in the data segment, below the initial size.
	size uint64
}

var errNotShared = errors.New("wasmrt: module has no shared memory")

// SharedMemory returns an accessor for the module's imported shared memory.
func (m *Module) SharedMemory() (*SharedMemory, error) {
	if m == nil || m.importedMemory == nil || m.threads == nil {
		return nil, errNotShared
	}
	pages := uint64(m.importedMemory.GetPageSize())
	if pages == 0 {
		return nil, errors.New("wasmrt: shared memory has no pages")
	}
	first, err := m.importedMemory.GetData(0, 1)
	if err != nil {
		return nil, err
	}
	return &SharedMemory{mem: m.importedMemory, base: unsafe.Pointer(&first[0]), size: pages * 65536}, nil
}

// BaseStable reports whether the memory still starts where it did when the
// accessor was made (it must, across memory.grow; see the package note).
func (s *SharedMemory) BaseStable() bool {
	first, err := s.mem.GetData(0, 1)
	return err == nil && unsafe.Pointer(&first[0]) == s.base
}

// Base returns the host address of guest address 0 (diagnostics and tests).
func (s *SharedMemory) Base() uintptr { return uintptr(s.base) }

// Pages returns the memory's current size in pages (a C call; not hot).
func (s *SharedMemory) Pages() uint64 { return uint64(s.mem.GetPageSize()) }

func (s *SharedMemory) word(off uint32, width uint64) unsafe.Pointer {
	if uint64(off)+width > s.size || uint64(off)%width != 0 {
		panic(fmt.Sprintf("wasmrt: shared-memory word %#x (width %d) outside %d bytes or unaligned", off, width, s.size))
	}
	return unsafe.Add(s.base, uintptr(off))
}

// Check reports whether a word of the given width at off is addressable.
func (s *SharedMemory) Check(off uint32, width uint64) error {
	if uint64(off)+width > s.size || uint64(off)%width != 0 {
		return fmt.Errorf("wasmrt: shared-memory word %#x (width %d) outside %d bytes or unaligned", off, width, s.size)
	}
	return nil
}

func (s *SharedMemory) Load32(off uint32) uint32 {
	return atomic.LoadUint32((*uint32)(s.word(off, 4)))
}
func (s *SharedMemory) Store32(off uint32, v uint32) {
	atomic.StoreUint32((*uint32)(s.word(off, 4)), v)
}
func (s *SharedMemory) Add32(off uint32, d uint32) uint32 {
	return atomic.AddUint32((*uint32)(s.word(off, 4)), d)
}
func (s *SharedMemory) Load64(off uint32) uint64 {
	return atomic.LoadUint64((*uint64)(s.word(off, 8)))
}

// ReadBytes copies n bytes at off out of shared memory.
func (s *SharedMemory) ReadBytes(off uint32, n int) []byte {
	if n == 0 {
		return nil
	}
	if n < 0 || uint64(off)+uint64(n) > s.size {
		panic("wasmrt: shared-memory read out of range")
	}
	src := unsafe.Slice((*byte)(s.word(off, 1)), n)
	out := make([]byte, n)
	copy(out, src)
	return out
}

// Watchdog watches service-thread heartbeats (design A21).
type Watchdog struct {
	mem   *SharedMemory
	base  uint32
	count int
	stale time.Duration
	tick  time.Duration
	hung  func(thread int, idle time.Duration)

	stopOnce sync.Once
	stop     chan struct{}
	done     chan struct{}
	fired    atomic.Bool
}

// StartWatchdog watches count u32 heartbeat words from base. hung runs once,
// on the watchdog's goroutine, for the first thread whose heartbeat has been
// still for longer than stale; the watchdog then stops.
func StartWatchdog(mem *SharedMemory, base uint32, count int, stale time.Duration, hung func(thread int, idle time.Duration)) (*Watchdog, error) {
	if count < 0 || stale <= 0 {
		return nil, errors.New("wasmrt: invalid watchdog configuration")
	}
	for i := 0; i < count; i++ {
		if err := mem.Check(base+uint32(4*i), 4); err != nil {
			return nil, err
		}
	}
	tick := stale / 10
	if tick > time.Second {
		tick = time.Second
	}
	if tick < time.Millisecond {
		tick = time.Millisecond
	}
	w := &Watchdog{mem: mem, base: base, count: count, stale: stale, tick: tick, hung: hung,
		stop: make(chan struct{}), done: make(chan struct{})}
	go w.run()
	return w, nil
}

func (w *Watchdog) run() {
	defer close(w.done)
	last := make([]uint32, w.count)
	changed := make([]time.Time, w.count)
	now := time.Now()
	for i := range last {
		last[i] = w.mem.Load32(w.base + uint32(4*i))
		changed[i] = now
	}
	t := time.NewTicker(w.tick)
	defer t.Stop()
	for {
		select {
		case <-w.stop:
			return
		case now = <-t.C:
		}
		for i := 0; i < w.count; i++ {
			v := w.mem.Load32(w.base + uint32(4*i))
			if v != last[i] {
				last[i], changed[i] = v, now
				continue
			}
			if v == 0 || v == HeartbeatExited {
				continue // not started yet, or left
			}
			if idle := now.Sub(changed[i]); idle > w.stale {
				w.fired.Store(true)
				if w.hung != nil {
					w.hung(i, idle)
				}
				return
			}
		}
	}
}

// Fired reports whether the watchdog declared a thread hung.
func (w *Watchdog) Fired() bool { return w != nil && w.fired.Load() }

// Stop ends the watchdog and waits for its goroutine (unless called from the
// hung callback itself, which runs on that goroutine).
func (w *Watchdog) Stop() {
	if w == nil {
		return
	}
	w.stopOnce.Do(func() { close(w.stop) })
	select {
	case <-w.done:
	case <-time.After(time.Second):
	}
}
