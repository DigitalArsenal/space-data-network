package format4

// The client half of the engine's mailbox (contract §3.4), copy-adapted from
// format 2's reader mailbox (format2/reader.go) and generalized to the two
// slot pools and four request classes. Go claims a FREE slot of the pool,
// writes the request TLV and the header, marks it QUEUED, pushes its index on
// the class's MPMC queue and wakes a thread of the class; the thread streams
// the response through the slot's ring; Go drains the ring, and once the slot
// is DONE and drained it reads the status and frees the slot. Go never calls
// a guest export on this path: everything is words in shared memory and a
// doorbell.

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"runtime"
	"sync/atomic"
	"time"
)

// memory is the instance's shared memory (*wasmrt.SharedMemory). Every
// access happens between host.Enter and host.Exit.
type memory interface {
	Load32(off uint32) uint32
	Store32(off uint32, v uint32)
	Add32(off uint32, d uint32) uint32
	CAS32(off uint32, old, new uint32) bool
	Load64(off uint32) uint64
	Store64(off uint32, v uint64)
	CAS64(off uint32, old, new uint64) bool
	ReadInto(off uint32, dst []byte)
	WriteBytes(off uint32, src []byte)
}

// host is the instance around the memory (*flatsqlrt.P4Instance).
type host interface {
	Enter() bool // false once the instance is stopping
	Exit()
	Wake(thread int) error // bump doorbell[thread] and notify it
	Stopping() <-chan struct{}
}

// Layout constants (flatsql_p4.h, FLATSQL_P4_LAYOUT_VERSION 1).
const (
	layoutVersion = 1
	layoutBytes   = 640
	slotHeader    = 448
	maxThreads    = 64
)

// Slot header offsets (§3.4).
const (
	offState        = 0
	offCancel       = 4
	offOutSeq       = 8
	offSpaceSeq     = 12
	offOp           = 16
	offClass        = 20
	offFlags        = 24
	offReqLen       = 28
	offReqID        = 32
	offMaxRows      = 40
	offMaxBytes     = 48
	offMaxResRows   = 56
	offMaxResBytes  = 64
	offRingHead     = 72
	offRingTail     = 80
	offStatus       = 88
	offErrLen       = 92
	offRowsOut      = 96
	offRowsExamined = 104
	offBytesRead    = 112
	offSubmitNs     = 120
	offStartNs      = 128
	offEndNs        = 136
	offErr          = 144
	errCap          = 256
	offThread       = 400
)

// Slot states and request flags.
const (
	slotFree    = 0
	slotClaimed = 1
	slotQueued  = 2
	slotRunning = 3
	slotDone    = 5

	flagRaw     uint32 = 1
	flagSandbox uint32 = 2
)

// Thread states (the u32 after each doorbell).
const threadIdle = 0

// layout is FlatsqlP4Layout.
type layout struct {
	stopWord    uint32
	nSlots      [2]uint32
	slotBase    [2]uint32
	slotStride  [2]uint32
	reqBytes    [2]uint32
	ringBytes   [2]uint32
	queueCells  [4]uint32
	queueMask   [4]uint32
	queueEnq    [4]uint32
	queueDeq    [4]uint32
	nThreads    uint32
	threadClass []uint32
	doorbell    []uint32
}

// parseLayout decodes and checks a flatsql_p4_layout block.
func parseLayout(b []byte) (layout, error) {
	var l layout
	if len(b) < layoutBytes {
		return l, fmt.Errorf("format4: layout is %d bytes, want %d", len(b), layoutBytes)
	}
	u := func(off int) uint32 { return binary.LittleEndian.Uint32(b[off:]) }
	if u(0) != layoutVersion || u(4) != layoutBytes || u(8) != slotHeader {
		return l, fmt.Errorf("format4: layout version %d size %d header %d, want %d/%d/%d (engine ABI changed)",
			u(0), u(4), u(8), layoutVersion, layoutBytes, slotHeader)
	}
	l.stopWord = u(12)
	for p := 0; p < 2; p++ {
		l.nSlots[p], l.slotBase[p], l.slotStride[p] = u(16+4*p), u(24+4*p), u(32+4*p)
		l.reqBytes[p], l.ringBytes[p] = u(40+4*p), u(48+4*p)
		if l.nSlots[p] == 0 || l.ringBytes[p] == 0 || l.ringBytes[p]&(l.ringBytes[p]-1) != 0 ||
			uint64(l.slotStride[p]) < uint64(slotHeader)+uint64(l.reqBytes[p])+uint64(l.ringBytes[p]) || l.slotStride[p]%64 != 0 {
			return l, fmt.Errorf("format4: layout pool %d out of range (slots %d, stride %d, request %d, ring %d)",
				p, l.nSlots[p], l.slotStride[p], l.reqBytes[p], l.ringBytes[p])
		}
	}
	for c := 0; c < 4; c++ {
		l.queueCells[c], l.queueMask[c] = u(56+4*c), u(72+4*c)
		l.queueEnq[c], l.queueDeq[c] = u(88+4*c), u(104+4*c)
	}
	l.nThreads = u(120)
	if l.nThreads < 1 || l.nThreads > maxThreads {
		return l, fmt.Errorf("format4: layout reports %d service threads", l.nThreads)
	}
	for i := 0; i < int(l.nThreads); i++ {
		l.threadClass = append(l.threadClass, u(128+4*i))
		l.doorbell = append(l.doorbell, u(384+4*i))
	}
	return l, nil
}

// mailbox is the client.
type mailbox struct {
	mem   memory
	h     host
	lay   layout
	hint  [2]atomic.Uint32 // where the next claim starts, per pool
	rr    [5]atomic.Uint32 // round robin per class
	reqID atomic.Uint64
	byCls [5][]int // service threads per class
	start time.Time
}

func newMailbox(mem memory, h host, lay layout) (*mailbox, error) {
	m := &mailbox{mem: mem, h: h, lay: lay, start: time.Now()}
	for i, c := range lay.threadClass {
		if c >= 1 && c <= 4 {
			m.byCls[c] = append(m.byCls[c], i)
		}
	}
	for c := ClassWrite; c <= ClassSandbox; c++ {
		if len(m.byCls[c]) == 0 {
			return nil, fmt.Errorf("format4: the engine runs no thread of class %d", c)
		}
	}
	return m, nil
}

// call is one request.
type call struct {
	op    uint32
	class Class
	flags uint32
	req   []byte
	caps  Caps
}

func poolOf(c Class) int {
	if c == ClassWrite {
		return 0
	}
	return 1
}

// slot is a submitted request: read its output, then finish.
type slot struct {
	m       *mailbox
	pool    int
	hdr     uint32
	ring    uint32
	ringCap uint64
	done    bool
	submit  time.Time
}

// errNoSlot: every slot of the pool is in use, or the queue is full.
var errNoSlot = errors.New("format4: no free slot")

// trySubmit claims a slot and queues the request once.
func (m *mailbox) trySubmit(c call) (*slot, error) {
	p := poolOf(c.class)
	if uint64(len(c.req)) > uint64(m.lay.reqBytes[p]) {
		return nil, &StatusError{Op: opNames[c.op], Status: StatusArg,
			Msg: fmt.Sprintf("request of %d bytes is larger than a slot's %d", len(c.req), m.lay.reqBytes[p])}
	}
	if !m.h.Enter() {
		return nil, ErrStopped
	}
	defer m.h.Exit()
	n := m.lay.nSlots[p]
	start := m.hint[p].Add(1)
	for k := uint32(0); k < n; k++ {
		i := (start + k) % n
		h := m.lay.slotBase[p] + i*m.lay.slotStride[p]
		if !m.mem.CAS32(h+offState, slotFree, slotClaimed) {
			continue
		}
		rq := h + slotHeader
		m.mem.WriteBytes(rq, c.req)
		for _, off := range []uint32{offCancel, offErrLen, offThread} {
			m.mem.Store32(h+off, 0)
		}
		m.mem.Store32(h+offStatus, 0)
		for _, off := range []uint32{offRingHead, offRingTail, offRowsOut, offRowsExamined, offBytesRead, offStartNs, offEndNs} {
			m.mem.Store64(h+off, 0)
		}
		m.mem.Store32(h+offOp, c.op)
		m.mem.Store32(h+offClass, uint32(c.class))
		m.mem.Store32(h+offFlags, c.flags)
		m.mem.Store32(h+offReqLen, uint32(len(c.req)))
		m.mem.Store64(h+offReqID, m.reqID.Add(1))
		m.mem.Store64(h+offMaxRows, c.caps.MaxRowsExamined)
		m.mem.Store64(h+offMaxBytes, c.caps.MaxBytesRead)
		m.mem.Store64(h+offMaxResRows, c.caps.MaxResultRows)
		m.mem.Store64(h+offMaxResBytes, c.caps.MaxResultBytes)
		m.mem.Store64(h+offSubmitNs, uint64(time.Since(m.start)))
		m.mem.Store32(h+offState, slotQueued)
		if !m.enqueue(int(c.class)-1, i+m.first(p)) {
			m.mem.Store32(h+offState, slotFree)
			return nil, errNoSlot
		}
		m.wake(c.class)
		return &slot{m: m, pool: p, hdr: h, ring: rq + m.lay.reqBytes[p], ringCap: uint64(m.lay.ringBytes[p]), submit: time.Now()}, nil
	}
	return nil, errNoSlot
}

// first is the global index of a pool's first slot.
func (m *mailbox) first(p int) uint32 {
	if p == 0 {
		return 0
	}
	return m.lay.nSlots[0]
}

// enqueue pushes a global slot index on queue q (Vyukov MPMC: 16-byte cells
// {u64 seq, u32 value}). Caller is inside Enter.
func (m *mailbox) enqueue(q int, slot uint32) bool {
	enq := m.lay.queueEnq[q]
	pos := m.mem.Load64(enq)
	for {
		cell := m.lay.queueCells[q] + uint32((pos&uint64(m.lay.queueMask[q]))*16)
		seq := m.mem.Load64(cell)
		switch dif := int64(seq) - int64(pos); {
		case dif == 0:
			if m.mem.CAS64(enq, pos, pos+1) {
				m.mem.Store32(cell+8, slot)
				m.mem.Store64(cell, pos+1)
				return true
			}
			pos = m.mem.Load64(enq)
		case dif < 0:
			return false
		default:
			pos = m.mem.Load64(enq)
		}
	}
}

// wake wakes an idle thread of the class, else the next one round robin.
// Caller is inside Enter.
func (m *mailbox) wake(c Class) {
	threads := m.byCls[c]
	pick := -1
	for _, i := range threads {
		if m.mem.Load32(m.lay.doorbell[i]+4) == threadIdle {
			pick = i
			break
		}
	}
	if pick < 0 {
		pick = threads[int(m.rr[c].Add(1))%len(threads)]
	}
	_ = m.h.Wake(pick)
}

// submit queues the request, waiting (bounded by ctx) while no slot is free.
func (m *mailbox) submit(ctx context.Context, c call) (*slot, error) {
	var wait backoff
	for {
		s, err := m.trySubmit(c)
		if err != errNoSlot {
			return s, err
		}
		if err := wait.sleep(ctx, m.h.Stopping(), time.Millisecond); err != nil {
			return nil, err
		}
	}
}

// read copies response bytes into dst; io.EOF once the slot is DONE and
// every byte has been read.
func (s *slot) read(ctx context.Context, dst []byte) (int, error) {
	m := s.m
	var wait backoff
	for {
		if !m.h.Enter() {
			return 0, ErrStopped
		}
		seq := m.mem.Load32(s.hdr + offOutSeq)
		state := m.mem.Load32(s.hdr + offState)
		tail := m.mem.Load64(s.hdr + offRingTail)
		head := m.mem.Load64(s.hdr + offRingHead)
		if tail > head {
			n := min(tail-head, uint64(len(dst)))
			at := head & (s.ringCap - 1)
			first := min(s.ringCap-at, n)
			m.mem.ReadInto(s.ring+uint32(at), dst[:first])
			if n > first {
				m.mem.ReadInto(s.ring, dst[first:n])
			}
			m.mem.Store64(s.hdr+offRingHead, head+n)
			m.mem.Add32(s.hdr+offSpaceSeq, 1)
			if state == slotRunning {
				_ = m.h.Wake(int(m.mem.Load32(s.hdr + offThread)))
			}
			m.h.Exit()
			return int(n), nil
		}
		m.h.Exit()
		if state == slotDone {
			return 0, io.EOF
		}
		// Wait for the engine to move outSeq (output or DONE).
		for {
			if err := wait.sleep(ctx, m.h.Stopping(), time.Millisecond); err != nil {
				return 0, err
			}
			if !m.h.Enter() {
				return 0, ErrStopped
			}
			moved := m.mem.Load32(s.hdr+offOutSeq) != seq
			m.h.Exit()
			if moved || wait.elapsed() > 20*time.Millisecond {
				wait.reset()
				break
			}
		}
	}
}

// cancel asks the engine to end the request (it ends with P4_E_CANCELLED; a
// running write completes).
func (s *slot) cancel() {
	m := s.m
	if !m.h.Enter() {
		return
	}
	defer m.h.Exit()
	m.mem.Store32(s.hdr+offCancel, 1)
	if m.mem.Load32(s.hdr+offState) == slotRunning {
		_ = m.h.Wake(int(m.mem.Load32(s.hdr + offThread)))
	}
}

// outcome is a finished request's status and counters.
type outcome struct {
	status                        int32
	err                           string
	rows, rowsExamined, bytesRead uint64
	run, queue                    time.Duration
}

// finish reads the outcome and frees the slot; call it after read returned
// io.EOF.
func (s *slot) finish() outcome {
	m := s.m
	if s.done || !m.h.Enter() {
		s.done = true
		return outcome{status: StatusStopped, err: "engine stopped"}
	}
	defer m.h.Exit()
	h := s.hdr
	o := outcome{
		status:       int32(m.mem.Load32(h + offStatus)),
		rows:         m.mem.Load64(h + offRowsOut),
		rowsExamined: m.mem.Load64(h + offRowsExamined),
		bytesRead:    m.mem.Load64(h + offBytesRead),
	}
	if start, end := m.mem.Load64(h+offStartNs), m.mem.Load64(h+offEndNs); end > start {
		o.run = time.Duration(end - start)
	}
	if total := time.Since(s.submit); total > o.run {
		o.queue = total - o.run
	}
	if n := m.mem.Load32(h + offErrLen); n > 0 {
		b := make([]byte, min(n, errCap))
		m.mem.ReadInto(h+offErr, b)
		o.err = string(b)
	}
	m.mem.Store32(h+offState, slotFree)
	s.done = true
	return o
}

// drain reads and discards the rest of the response until the slot is DONE.
func (s *slot) drain(ctx context.Context) error {
	buf := make([]byte, 64<<10)
	for {
		if _, err := s.read(ctx, buf); err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}
	}
}

// abandonGrace is how long a cancelled request may take to reach DONE
// before its caller returns (§5.2 rules: one second).
var abandonGrace = time.Second

// abandon ends a request whose caller is leaving: cancel, wait up to
// abandonGrace for DONE (draining the ring), free the slot. A request that
// is not done by then is drained and freed in the background, so the slot
// comes back once the engine finishes it.
func (s *slot) abandon() {
	if s.done {
		return
	}
	s.cancel()
	ctx, cancel := context.WithTimeout(context.Background(), abandonGrace)
	err := s.drain(ctx)
	cancel()
	if err == nil {
		s.finish()
		return
	}
	if errors.Is(err, ErrStopped) {
		s.done = true
		return
	}
	go func() {
		if s.drain(context.Background()) == nil {
			s.finish()
		}
	}()
}

// run executes a request to completion, handing each output chunk to sink.
// A ctx that ends cancels the request (abandon) and returns ctx.Err().
func (m *mailbox) run(ctx context.Context, c call, sink func([]byte) error) (outcome, error) {
	s, err := m.submit(ctx, c)
	if err != nil {
		return outcome{}, err
	}
	buf := make([]byte, 256<<10)
	for {
		n, err := s.read(ctx, buf)
		if n > 0 && sink != nil {
			if serr := sink(buf[:n]); serr != nil {
				s.abandon()
				return outcome{}, serr
			}
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			if errors.Is(err, ErrStopped) {
				s.done = true
				return outcome{}, err
			}
			s.abandon()
			return outcome{}, err
		}
	}
	return s.finish(), nil
}

// backoff polls shared-memory words: a short spin of yields, then sleeps
// doubling from 20 us to limit (format 2's writer.go backoff). The guest's
// waits notify inside WasmEdge, which Go cannot join, so the host polls.
type backoff struct {
	n     int
	start time.Time
}

func (b *backoff) reset() { b.n = 0; b.start = time.Time{} }

func (b *backoff) elapsed() time.Duration {
	if b.start.IsZero() {
		return 0
	}
	return time.Since(b.start)
}

func (b *backoff) sleep(ctx context.Context, stop <-chan struct{}, limit time.Duration) error {
	if b.start.IsZero() {
		b.start = time.Now()
	}
	b.n++
	if b.n <= 32 {
		runtime.Gosched()
		return ctx.Err()
	}
	d := min(20*time.Microsecond<<uint(min(b.n-33, 13)), limit)
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-stop:
		return ErrStopped
	case <-t.C:
		return nil
	}
}
