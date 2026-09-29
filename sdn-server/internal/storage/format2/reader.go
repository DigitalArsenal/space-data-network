package format2

// Reader instances and the client half of their mailbox (design §5.3, §8,
// §9, A28; flatsql ps/lane.h). A reader instance runs L lane threads over
// committed files only: it shares no memory and no lock with the writer, so
// a read never waits on ingest (reads-never-wait law). Go claims a request
// slot in the instance's memory, writes the SQL and RB1 parameters, queues
// the slot on the MPMC queue and wakes an idle lane; the lane streams RB1 (or
// raw frames) through the slot's ring. Nothing here has a deadline: a client
// that leaves cancels its statement (the lane polls the cancel word).

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync/atomic"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/flatsqlrt"
	"github.com/spacedatanetwork/sdn-server/internal/wasmrt"
)

// Slot states (flatsql SlotState).
const (
	slotFree    = 0
	slotClaimed = 1
	slotQueued  = 2
	slotRunning = 3
	slotParked  = 4
	slotDone    = 5
)

// Request flags (flatsql ReqFlag).
const (
	ReqRawStream   uint32 = 1 // [u32le size][bytes] per BLOB cell instead of RB1
	ReqSandbox     uint32 = 2 // untrusted SQL: authorizer, one SELECT, work budget
	ReqNoAdmission uint32 = 4 // trusted maintenance reads on bulk lanes only
)

// Reader status codes (flatsql ReaderStatus).
const (
	StatusOK            int32 = 0
	StatusNeedsBulk     int32 = -20
	StatusSnapshotGone  int32 = -21
	StatusTimeout       int32 = -22
	StatusCancelled     int32 = -23
	StatusNoMem         int32 = -24
	StatusSQLError      int32 = -25
	StatusNotAuthorized int32 = -26
	StatusStopped       int32 = -27
	StatusRetryable     int32 = -28
	StatusCorrupt       int32 = -29
	StatusNotMigrated   int32 = -30
	StatusBusy          int32 = -31
	StatusTooLarge      int32 = -32
)

// StatusError is a statement that ended with a non-zero status.
type StatusError struct {
	Status int32
	Msg    string
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("format2: statement status %d: %s", e.Status, e.Msg)
}

// IsStatus reports whether err is a StatusError with the given status.
func IsStatus(err error, status int32) bool {
	var se *StatusError
	return errors.As(err, &se) && se.Status == status
}

// ReaderConfig configures a reader instance (flatsql_ps_init TLVs 1, 20-30).
type ReaderConfig struct {
	Root           string // engine root ("." by default)
	Lanes          uint32
	ArenaBytes     uint64 // per lane
	CacheBytes     uint64
	MaxParked      uint32
	Slots          uint32
	ReqBytes       uint32
	RingBytes      uint32
	LaneCacheBytes uint64
	StackBytes     uint64
	SandboxRows    uint64
	SandboxBytes   uint64
}

func (c ReaderConfig) encode() []byte {
	root := c.Root
	if root == "" {
		root = "."
	}
	t := tlv(nil).bytes(1, []byte(root))
	add32 := func(tag uint16, v uint32) {
		if v > 0 {
			t = t.u32(tag, v)
		}
	}
	add64 := func(tag uint16, v uint64) {
		if v > 0 {
			t = t.u64(tag, v)
		}
	}
	add32(20, c.Lanes)
	add64(21, c.ArenaBytes)
	add64(22, c.CacheBytes)
	add32(23, c.MaxParked)
	add32(24, c.Slots)
	add32(25, c.ReqBytes)
	add32(26, c.RingBytes)
	add64(27, c.LaneCacheBytes)
	add64(28, c.StackBytes)
	add64(29, c.SandboxRows)
	add64(30, c.SandboxBytes)
	return t
}

// Reader is one reader instance (interactive, bulk or sandbox lanes).
type Reader struct {
	inst      *flatsqlrt.PSInstance
	mem       *wasmrt.SharedMemory
	lay       ReaderLayout
	role      flatsqlrt.PSRole
	claimHint atomic.Uint32
}

// OpenReader opens a reader instance of the given role.
func OpenReader(opt InstanceOptions, role flatsqlrt.PSRole, cfg ReaderConfig) (*Reader, error) {
	if role == flatsqlrt.PSRoleWriter {
		return nil, errors.New("format2: a reader needs a reader role")
	}
	inst, err := flatsqlrt.OpenPSInstance(flatsqlrt.PSConfig{
		Role: role, ABI: flatsqlrt.PSABIEngine,
		Wasm: flatsqlrt.PSThreadsWasm(), AOTCacheDir: opt.AOTCacheDir, AOTPrefix: flatsqlrt.PSThreadsAOTPrefix,
		CompileOnMiss: opt.CompileOnMiss, Store: opt.Store, FDBudget: opt.FDBudget,
		InitConfig: cfg.encode(), HeartbeatStale: -1, StopDeadline: 10 * time.Second,
		ControlBudget: 60 * time.Second, OnFailure: opt.OnFailure,
	})
	if err != nil {
		return nil, err
	}
	lay, err := ParseReaderLayout(inst.EngineLayout())
	if err != nil {
		_ = inst.Stop()
		return nil, err
	}
	return &Reader{inst: inst, mem: inst.Memory(), lay: lay, role: role}, nil
}

// Instance exposes the substrate instance.
func (r *Reader) Instance() *flatsqlrt.PSInstance { return r.inst }

// Layout returns the mailbox layout.
func (r *Reader) Layout() ReaderLayout { return r.lay }

// Stop stops the instance.
func (r *Reader) Stop() error { return r.inst.Stop() }

// OldestActiveStart is the start (engine monotonic ns) of the oldest running
// or parked statement across the lanes, or 0 when none runs (A12).
func (r *Reader) OldestActiveStart() uint64 {
	if !r.inst.Enter() {
		return 0
	}
	defer r.inst.Exit()
	var oldest uint64
	for _, a := range r.lay.LaneAnnounce {
		if v := r.mem.Load64(a); v != 0 && (oldest == 0 || v < oldest) {
			oldest = v
		}
	}
	return oldest
}

// Request is one statement.
type Request struct {
	SQL             string
	Params          []Cell
	Flags           uint32
	MaxRowsExamined uint64
	MaxBytesRead    uint64
	MaxResultRows   uint64
	MaxResultBytes  uint64
}

// Outcome is a finished statement's status and work counters.
type Outcome struct {
	Status       int32
	Err          string
	RowsOut      uint64
	RowsExamined uint64
	BytesRead    uint64
	IndexEntries uint64
	FenceReads   uint64
	RunNs        uint64
}

// Stmt is a submitted statement: read its output, then Finish.
type Stmt struct {
	r       *Reader
	slot    uint32
	hdr     uint32
	ring    uint32
	ringCap uint64
	done    bool
}

func (r *Reader) h(slot uint32) uint32 { return r.lay.SlotBase + slot*r.lay.SlotStride }

// Submit claims a slot, writes the request and queues it. It waits for a
// free slot while every slot is in use (bounded by ctx).
func (r *Reader) Submit(ctx context.Context, req Request) (*Stmt, error) {
	params := EncodeParams(req.Params)
	if uint32(len(req.SQL)+len(params)) > r.lay.ReqBytes {
		return nil, &StatusError{Status: StatusTooLarge, Msg: "request larger than a slot"}
	}
	lay := &r.lay
	mem := r.mem
	var wait backoff
	for {
		if !r.inst.Enter() {
			return nil, ErrStopped
		}
		start := r.claimHint.Add(1)
		for k := uint32(0); k < lay.NSlots; k++ {
			i := (start + k) % lay.NSlots
			h := r.h(i)
			if !mem.CAS32(h+lay.OffState, slotFree, slotClaimed) {
				continue
			}
			rq := h + lay.HeaderSize
			mem.WriteBytes(rq, []byte(req.SQL))
			mem.WriteBytes(rq+uint32(len(req.SQL)), params)
			mem.Store32(h+lay.OffFlags, req.Flags)
			mem.Store32(h+lay.OffSQLLen, uint32(len(req.SQL)))
			mem.Store32(h+lay.OffParamsLen, uint32(len(params)))
			mem.Store64(h+lay.OffMaxRowsExamined, req.MaxRowsExamined)
			mem.Store64(h+lay.OffMaxBytesRead, req.MaxBytesRead)
			mem.Store64(h+lay.OffMaxResultRows, req.MaxResultRows)
			mem.Store64(h+lay.OffMaxResultBytes, req.MaxResultBytes)
			mem.Store64(h+lay.OffRingHead, 0)
			mem.Store64(h+lay.OffRingTail, 0)
			mem.Store32(h+lay.OffCancel, 0)
			mem.Store32(h+lay.OffStatus, 0)
			mem.Store32(h+lay.OffErrLen, 0)
			for _, off := range []uint32{lay.OffRowsOut, lay.OffRowsExamined, lay.OffBytesRead, lay.OffIndexEntries,
				lay.OffFenceReads, lay.OffSubmitNs, lay.OffStartNs, lay.OffEndNs} {
				mem.Store64(h+off, 0)
			}
			mem.Store32(h+lay.OffState, slotQueued)
			if !r.enqueue(i) {
				mem.Store32(h+lay.OffState, slotFree)
				r.inst.Exit()
				return nil, &StatusError{Status: StatusBusy, Msg: "submission queue full"}
			}
			r.wakeIdleLane()
			st := &Stmt{r: r, slot: i, hdr: h, ring: rq + lay.ReqBytes, ringCap: uint64(mem.Load32(h + lay.OffRingCap))}
			r.inst.Exit()
			if st.ringCap == 0 {
				st.ringCap = uint64(lay.RingBytes)
			}
			return st, nil
		}
		r.inst.Exit()
		if err := wait.sleep(ctx, r.inst.Stopping()); err != nil {
			return nil, err
		}
	}
}

// enqueue pushes a slot index on the Vyukov MPMC queue (cells of 16 bytes:
// {seq u64, value u32}). Caller is inside Enter.
func (r *Reader) enqueue(slot uint32) bool {
	lay := &r.lay
	mem := r.mem
	pos := mem.Load64(lay.QueueEnq)
	for {
		cell := lay.QueueCells + uint32((pos&uint64(lay.QueueMask))*16)
		seq := mem.Load64(cell)
		switch dif := int64(seq) - int64(pos); {
		case dif == 0:
			if mem.CAS64(lay.QueueEnq, pos, pos+1) {
				mem.Store32(cell+8, slot)
				mem.Store64(cell, pos+1)
				return true
			}
		case dif < 0:
			return false
		default:
			pos = mem.Load64(lay.QueueEnq)
		}
	}
}

// wakeIdleLane wakes the first idle lane (state 0), else one waiting on a
// parked statement (state 3). A busy lane re-checks the queue before it
// sleeps. Caller is inside Enter.
func (r *Reader) wakeIdleLane() {
	pick := -1
	for i, a := range r.lay.LaneState {
		s := r.mem.Load32(a)
		if s == 0 {
			pick = i
			break
		}
		if s == 3 && pick < 0 {
			pick = i
		}
	}
	if pick >= 0 {
		_ = r.inst.Wake(pick)
	}
}

// Read copies result bytes into dst. It returns io.EOF once the statement
// is done and every byte has been read.
func (s *Stmt) Read(ctx context.Context, dst []byte) (int, error) {
	r := s.r
	lay := &r.lay
	mem := r.mem
	var wait backoff
	for {
		if !r.inst.Enter() {
			return 0, ErrStopped
		}
		seq := mem.Load32(s.hdr + lay.OffOutSeq)
		state := mem.Load32(s.hdr + lay.OffState)
		tail := mem.Load64(s.hdr + lay.OffRingTail)
		head := mem.Load64(s.hdr + lay.OffRingHead)
		if tail > head {
			n := tail - head
			if n > uint64(len(dst)) {
				n = uint64(len(dst))
			}
			at := head % s.ringCap
			first := s.ringCap - at
			if first > n {
				first = n
			}
			mem.ReadInto(s.ring+uint32(at), dst[:first])
			if n > first {
				mem.ReadInto(s.ring, dst[first:n])
			}
			mem.Store64(s.hdr+lay.OffRingHead, head+n)
			mem.Add32(s.hdr+lay.OffSpaceSeq, 1)
			if mem.Load32(s.hdr+lay.OffState) == slotParked {
				_ = r.inst.Wake(int(mem.Load32(s.hdr + lay.OffLane)))
			}
			r.inst.Exit()
			return int(n), nil
		}
		r.inst.Exit()
		if state == slotDone {
			return 0, io.EOF
		}
		for {
			if err := wait.sleep(ctx, r.inst.Stopping()); err != nil {
				return 0, err
			}
			if !r.inst.Enter() {
				return 0, ErrStopped
			}
			moved := mem.Load32(s.hdr+lay.OffOutSeq) != seq
			r.inst.Exit()
			if moved || wait.elapsed() > 20*time.Millisecond {
				wait.reset()
				break
			}
		}
	}
}

// Cancel asks the lane to end the statement (it ends with StatusCancelled).
func (s *Stmt) Cancel() {
	r := s.r
	if !r.inst.Enter() {
		return
	}
	defer r.inst.Exit()
	r.mem.Store32(s.hdr+r.lay.OffCancel, 1)
	if r.mem.Load32(s.hdr+r.lay.OffState) == slotParked {
		_ = r.inst.Wake(int(r.mem.Load32(s.hdr + r.lay.OffLane)))
	}
}

// Finish returns the outcome and frees the slot. Call it after Read returned
// io.EOF (Close does the draining for a caller that stops early).
func (s *Stmt) Finish() Outcome {
	r := s.r
	lay := &r.lay
	if s.done || !r.inst.Enter() {
		return Outcome{Status: StatusStopped, Err: "instance stopped"}
	}
	defer r.inst.Exit()
	mem := r.mem
	h := s.hdr
	o := Outcome{
		Status:       int32(mem.Load32(h + lay.OffStatus)),
		RowsOut:      mem.Load64(h + lay.OffRowsOut),
		RowsExamined: mem.Load64(h + lay.OffRowsExamined),
		BytesRead:    mem.Load64(h + lay.OffBytesRead),
		IndexEntries: mem.Load64(h + lay.OffIndexEntries),
		FenceReads:   mem.Load64(h + lay.OffFenceReads),
	}
	if start, end := mem.Load64(h+lay.OffStartNs), mem.Load64(h+lay.OffEndNs); end > start {
		o.RunNs = end - start
	}
	if n := mem.Load32(h + lay.OffErrLen); n > 0 {
		if n > 256 {
			n = 256
		}
		o.Err = string(mem.ReadBytes(h+lay.OffErr, int(n)))
	}
	mem.Store32(h+lay.OffState, slotFree)
	s.done = true
	return o
}

// Close ends a statement whatever its state: cancel, drain to DONE, free.
func (s *Stmt) Close() Outcome {
	if s.done {
		return Outcome{}
	}
	s.Cancel()
	buf := make([]byte, 64<<10)
	for {
		if _, err := s.Read(context.Background(), buf); err != nil {
			if errors.Is(err, ErrStopped) {
				s.done = true
				return Outcome{Status: StatusStopped}
			}
			break
		}
	}
	return s.Finish()
}

// Result is a fully read RB1 statement.
type Result struct {
	Names []string
	Rows  [][]Cell
	Outcome
}

// Query runs a statement to completion and decodes its RB1 rows. A non-zero
// status returns a *StatusError (with the rows decoded so far in Result).
func (r *Reader) Query(ctx context.Context, req Request) (*Result, error) {
	req.Flags &^= ReqRawStream
	st, err := r.Submit(ctx, req)
	if err != nil {
		return nil, err
	}
	var dec RB1Decoder
	buf := make([]byte, 256<<10)
	for {
		n, err := st.Read(ctx, buf)
		if n > 0 {
			if ferr := dec.Feed(buf[:n]); ferr != nil {
				st.Close()
				return nil, ferr
			}
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			st.Close()
			return nil, err
		}
	}
	o := st.Finish()
	res := &Result{Names: dec.Names, Rows: dec.Rows, Outcome: o}
	if o.Status != 0 {
		return res, &StatusError{Status: o.Status, Msg: o.Err}
	}
	return res, nil
}

// Stream runs a statement and hands each chunk of output to fn as it
// arrives (RB1 bytes, or raw frames with ReqRawStream). Nothing is
// materialized: a large window is bounded by the slot's ring.
func (r *Reader) Stream(ctx context.Context, req Request, fn func([]byte) error) (Outcome, error) {
	st, err := r.Submit(ctx, req)
	if err != nil {
		return Outcome{}, err
	}
	buf := make([]byte, 256<<10)
	for {
		n, err := st.Read(ctx, buf)
		if n > 0 {
			if ferr := fn(buf[:n]); ferr != nil {
				o := st.Close()
				return o, ferr
			}
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			o := st.Close()
			return o, err
		}
	}
	o := st.Finish()
	if o.Status != 0 {
		return o, &StatusError{Status: o.Status, Msg: o.Err}
	}
	return o, nil
}
