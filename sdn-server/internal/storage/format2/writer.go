package format2

// The writer instance and the producer half of its rings (design §5.2, §6,
// §7; flatsql ps/ring.h). Go is the producer: per partition it reserves
// credits, copies one entry into the partition's ring in the writer's shared
// memory, publishes the new tail and rings the owning writer's doorbell. The
// writer releases ring space and advances ackedRseq only after the commit
// holding the entry is durable (§6.4), so an ack here is a durable ack.

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/flatsqlrt"
	"github.com/spacedatanetwork/sdn-server/internal/wasmrt"
)

// Ring entry kinds and flags (flatsql ps/ring.h).
const (
	EntRecord    uint16 = 1
	EntLicence   uint16 = 2
	EntTombCid   uint16 = 3
	EntReconcile uint16 = 4
	EntTxnBegin  uint16 = 6
	EntTxnEnd    uint16 = 7
	EntCtl       uint16 = 8

	FlagSealed        uint16 = 0x0001 // frame = plaintext frame, then u32 len + sealed bytes
	FlagCidPresent    uint16 = 0x0002
	FlagAckWanted     uint16 = 0x0004
	FlagCidOverPrefix uint16 = 0x0008 // the CID hashes the size-prefixed frame
)

const (
	entryHeaderBytes = 72 // packed EntryHeader
	cidBytes         = 36 // CIDv1 raw sha2-256, binary
	rejectSlots      = 32

	ringActive      = 0
	ringDraining    = 1
	ringQuarantined = 2
	ringPaused      = 3
)

// Reject codes the engine returns for an entry (flatsql ps/extract.h).
const (
	RejBadEntry    int32 = -100
	RejFrameSize   int32 = -101
	RejFid         int32 = -102
	RejVerify      int32 = -103
	RejCid         int32 = -104
	RejSealed      int32 = -105
	RejAttr        int32 = -106
	RejQuarantined int32 = -107
	RejNoType      int32 = -108
	RejTxnTooLarge int32 = -109
)

var (
	// ErrStopped: the instance is stopping or stopped.
	ErrStopped = errors.New("format2: partition-store instance is not live")
	// ErrQuarantined: the partition is quarantined (§15); writes are refused.
	ErrQuarantined = errors.New("format2: partition is quarantined")
	// ErrEntryTooLarge: the entry exceeds the ring's maximum entry.
	ErrEntryTooLarge = errors.New("format2: entry larger than the ring's maximum entry")
)

// RejectError is an entry the engine refused (never a trap: §5.2).
type RejectError struct {
	Rseq uint64
	Code int32
}

func (e *RejectError) Error() string {
	return fmt.Sprintf("format2: entry %d rejected by the engine (code %d)", e.Rseq, e.Code)
}

// WriterConfig configures the writer instance (flatsql_ps_init TLVs 1-18).
type WriterConfig struct {
	Root            string // engine root inside the host store root ("." by default)
	Writers         uint32 // writer threads (0: engine default 1)
	SyncThreads     uint32
	PoolBytes       uint64
	SlabBytes       uint32
	RingCap         uint64
	Cooperative     bool
	Create          bool
	RequireMigrated bool
	ZeroFillStep    uint64
	SealBytes       uint64
	QuotaBytes      uint64
	BallastBytes    uint64
	ReclaimGraceMs  uint64
	NoAutoCompact   bool
	CompactThreads  uint32
	CommitJournal   bool
}

func (c WriterConfig) encode() []byte {
	root := c.Root
	if root == "" {
		root = "."
	}
	t := tlv(nil).bytes(1, []byte(root))
	if c.Writers > 0 {
		t = t.u32(2, c.Writers)
	}
	if c.SyncThreads > 0 {
		t = t.u32(3, c.SyncThreads)
	}
	if c.PoolBytes > 0 {
		t = t.u64(4, c.PoolBytes)
	}
	if c.SlabBytes > 0 {
		t = t.u32(5, c.SlabBytes)
	}
	if c.RingCap > 0 {
		t = t.u64(6, c.RingCap)
	}
	t = t.u8(7, c.Cooperative).u8(8, c.Create).u8(9, c.RequireMigrated)
	if c.ZeroFillStep > 0 {
		t = t.u64(10, c.ZeroFillStep)
	}
	if c.SealBytes > 0 {
		t = t.u64(12, c.SealBytes)
	}
	if c.QuotaBytes > 0 {
		t = t.u64(13, c.QuotaBytes)
	}
	if c.BallastBytes > 0 {
		t = t.u64(14, c.BallastBytes)
	}
	if c.ReclaimGraceMs > 0 {
		t = t.u64(15, c.ReclaimGraceMs)
	}
	t = t.u8(16, !c.NoAutoCompact)
	if c.CompactThreads > 0 {
		t = t.u32(17, c.CompactThreads)
	}
	t = t.u8(18, c.CommitJournal)
	return t
}

// InstanceOptions are the substrate settings shared by every instance.
type InstanceOptions struct {
	Store         *flatsqlrt.NativeStore // the node's host I/O store (one per store root)
	AOTCacheDir   string
	CompileOnMiss bool // tests and prewarm only: a daemon never compiles (A30)
	FDBudget      int
	OnFailure     func(*flatsqlrt.PSInstance, error)
}

// Writer is the writer instance: registration, rings, acks.
type Writer struct {
	inst *flatsqlrt.PSInstance
	mem  *wasmrt.SharedMemory
	lay  WriterLayout

	mu    sync.Mutex // registration and the partition map (never on the enqueue path)
	types map[[4]byte]bool
	parts map[partKey]*Partition
	byPid map[uint32]*Partition
}

type partKey struct {
	peer string
	fid  [4]byte
}

// OpenWriter opens (and, with Create, creates) the store's writer instance.
func OpenWriter(opt InstanceOptions, cfg WriterConfig) (*Writer, error) {
	inst, err := flatsqlrt.OpenPSInstance(flatsqlrt.PSConfig{
		Role: flatsqlrt.PSRoleWriter, ABI: flatsqlrt.PSABIEngine,
		Wasm: flatsqlrt.PSThreadsWasm(), AOTCacheDir: opt.AOTCacheDir, AOTPrefix: flatsqlrt.PSThreadsAOTPrefix,
		CompileOnMiss: opt.CompileOnMiss, Store: opt.Store, FDBudget: opt.FDBudget,
		InitConfig: cfg.encode(), HeartbeatStale: -1, StopDeadline: 10 * time.Second,
		ControlBudget: 60 * time.Second, OnFailure: opt.OnFailure,
	})
	if err != nil {
		return nil, err
	}
	lay, err := ParseWriterLayout(inst.EngineLayout())
	if err != nil {
		_ = inst.Stop()
		return nil, err
	}
	return &Writer{inst: inst, mem: inst.Memory(), lay: lay, types: map[[4]byte]bool{},
		parts: map[partKey]*Partition{}, byPid: map[uint32]*Partition{}}, nil
}

// Instance exposes the substrate instance (stats, tests).
func (w *Writer) Instance() *flatsqlrt.PSInstance { return w.inst }

// Layout returns the engine's writer layout.
func (w *Writer) Layout() WriterLayout { return w.lay }

// Stop stops the writer instance (bounded; see PSInstance.Stop).
func (w *Writer) Stop() error { return w.inst.Stop() }

// call runs a control export with byte arguments copied into the instance.
func (w *Writer) call(name string, args [][]byte, tail ...interface{}) (int32, error) {
	mod := w.inst.Module()
	var params []interface{}
	var ptrs []uint32
	defer func() {
		for _, p := range ptrs {
			mod.Deallocate(p)
		}
	}()
	for _, a := range args {
		if len(a) == 0 {
			params = append(params, int32(0), int32(0))
			continue
		}
		p, err := mod.Allocate(a)
		if err != nil {
			return 0, err
		}
		ptrs = append(ptrs, p)
		params = append(params, int32(p), int32(len(a)))
	}
	params = append(params, tail...)
	v, err := w.inst.Control(name, params...)
	if err != nil {
		return 0, err
	}
	return wasmrt.ToInt32(v[0]), nil
}

// RegisterType registers an SDS type (its BFBS and extraction rules) durably.
// Registering the same config again is a no-op.
func (w *Writer) RegisterType(t TypeSpec) error {
	cfg := t.Encode()
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.types[t.FID] {
		return nil
	}
	rc, err := w.call("flatsql_ps_register_type", [][]byte{cfg})
	if err != nil {
		return err
	}
	if rc < 0 {
		return fmt.Errorf("format2: register type %s: engine status %d", t.SchemaName, rc)
	}
	w.types[t.FID] = true
	return nil
}

// Partition returns the (peer, type) partition, registering it durably on
// first use (A3: the engine derives the producer token from the raw peer id).
func (w *Writer) Partition(peer []byte, fid [4]byte) (*Partition, error) {
	key := partKey{string(peer), fid}
	w.mu.Lock()
	defer w.mu.Unlock()
	if p := w.parts[key]; p != nil {
		return p, nil
	}
	mod := w.inst.Module()
	fidPtr, err := mod.Allocate(fid[:])
	if err != nil {
		return nil, err
	}
	defer mod.Deallocate(fidPtr)
	var peerPtr uint32
	if len(peer) > 0 {
		if peerPtr, err = mod.Allocate(peer); err != nil {
			return nil, err
		}
		defer mod.Deallocate(peerPtr)
	}
	v, err := w.inst.Control("flatsql_ps_register_partition", int32(peerPtr), int32(len(peer)), int32(fidPtr))
	if err != nil {
		return nil, err
	}
	pid := wasmrt.ToInt32(v[0])
	if pid <= 0 {
		return nil, fmt.Errorf("format2: register partition %q/%s: engine status %d", peer, fid[:], pid)
	}
	if p := w.byPid[uint32(pid)]; p != nil {
		// Two raw peer ids that sanitize to one token share a partition (A3).
		w.parts[key] = p
		return p, nil
	}
	v, err = w.inst.Control("flatsql_ps_ring", pid)
	if err != nil {
		return nil, err
	}
	ringAddr, _ := v[0].(float64)
	if ringAddr <= 0 || ringAddr > math.MaxUint32 {
		return nil, fmt.Errorf("format2: partition %d has no ring", pid)
	}
	p := &Partition{w: w, PID: uint32(pid), ring: uint32(ringAddr), rejects: map[uint64]int32{}}
	if !w.inst.Enter() {
		return nil, ErrStopped
	}
	for _, off := range []uint32{w.lay.OffTail, w.lay.OffHead, w.lay.OffAckedRseq, w.lay.OffProdBusy, w.lay.OffNextRseq,
		w.lay.OffRejectHead, w.lay.OffRejectTail, w.lay.OffOwnerWord, w.lay.OffCap, w.lay.OffMaxEntry} {
		if err := w.mem.Check(p.ring+off, 8); err != nil {
			w.inst.Exit()
			return nil, err
		}
	}
	p.capBytes = w.mem.Load64(p.ring + w.lay.OffCap)
	p.maxEntry = w.mem.Load64(p.ring + w.lay.OffMaxEntry)
	p.nSlots = w.mem.Load32(p.ring + w.lay.OffNSlots)
	p.slab = w.mem.Load32(p.ring + w.lay.OffSlabBytes)
	w.inst.Exit()
	if p.nSlots == 0 || p.slab == 0 {
		return nil, fmt.Errorf("format2: partition %d ring is not configured", pid)
	}
	w.parts[key] = p
	w.byPid[p.PID] = p
	return p, nil
}

// SetReaderGate passes the oldest running reader statement's start (the
// engine's monotonic clock; negative: none) to the reclaimer (A12).
func (w *Writer) SetReaderGate(oldestStartNs float64) error {
	_, err := w.inst.Control("flatsql_ps_reader_gate", oldestStartNs)
	return err
}

// SetQuota sets the store's cap on on-disk bytes (§13; 0: none).
func (w *Writer) SetQuota(bytes uint64) error {
	_, err := w.inst.Control("flatsql_ps_set_quota", float64(bytes))
	return err
}

// WriterStats is flatsql_ps_stats for the writer role.
type WriterStats struct {
	Commits, SyncRounds, IterationsWithCommit, RowsAppended, DedupeHits, Retags, Tombs, Rejects uint64
	Merges, Seals, TypeCommits, FirstLabels, RepeatLabels, Promotions, NoticesDropped           uint64
	FramesParsedAtOpen, OpenReadBytes, OpenDataBytes, OpenMetaBytes, AdoptedBatches             uint64
	PoolSlabsInUse, PoolSlabsPeak, PoolCommittedBytes, CommittedBytes                           uint64
}

// Stats reads the writer's counters.
func (w *Writer) Stats() (WriterStats, error) {
	const n = 24 * 8
	mod := w.inst.Module()
	out, err := mod.AllocateSize(n)
	if err != nil {
		return WriterStats{}, err
	}
	defer mod.Deallocate(out)
	v, err := w.inst.Control("flatsql_ps_stats", int32(out), int32(n))
	if err != nil {
		return WriterStats{}, err
	}
	if got := wasmrt.ToInt32(v[0]); got != n {
		return WriterStats{}, fmt.Errorf("format2: writer stats %d bytes, want %d", got, n)
	}
	if !w.inst.Enter() {
		return WriterStats{}, ErrStopped
	}
	b := w.mem.ReadBytes(out, n)
	w.inst.Exit()
	u := func(i int) uint64 { return binary.LittleEndian.Uint64(b[8*i:]) }
	return WriterStats{u(0), u(1), u(2), u(3), u(4), u(5), u(6), u(7), u(8), u(9), u(10), u(11), u(12), u(13), u(14),
		u(15), u(16), u(17), u(18), u(19), u(20), u(21), u(22), u(23)}, nil
}

// Entry is one ring entry (§6.2).
type Entry struct {
	Kind      uint16
	Flags     uint16
	ArrivalMs int64
	CID       []byte // binary CIDv1 (36 bytes) when FlagCidPresent
	Attr      []byte // RecordAttr bytes
	Frame     []byte // [u32le size][FlatBuffer]; for a sealed entry the plaintext frame
	Sealed    []byte // FlagSealed: the sealed frame
}

func (e *Entry) size() uint64 {
	raw := uint64(entryHeaderBytes + len(e.Attr) + len(e.Frame))
	if e.Flags&FlagSealed != 0 {
		raw += 4 + uint64(len(e.Sealed))
	}
	return (raw + 7) &^ 7
}

// Partition is one (producer, type) partition's ring, seen from Go.
type Partition struct {
	w        *Writer
	PID      uint32
	ring     uint32
	capBytes uint64
	maxEntry uint64
	nSlots   uint32
	slab     uint32

	mu      sync.Mutex // producers are serialized per partition (SPSC)
	rejects map[uint64]int32
	scratch []byte

	creditWaits atomic.Uint64
}

// MaxEntry is the largest entry the ring admits.
func (p *Partition) MaxEntry() uint64 { return p.maxEntry }

// CreditWaits counts entries that found no credit and waited.
func (p *Partition) CreditWaits() uint64 { return p.creditWaits.Load() }

func (p *Partition) f(off uint32) uint32 { return p.ring + off }

// ringOwner rings the owning writer and, during a HANDOFF, the target (A24).
func (p *Partition) ringOwner() {
	lay := &p.w.lay
	w := p.w.mem.Load64(p.f(lay.OffOwnerWord))
	n := int(lay.NWriters)
	_ = p.w.inst.Ring(int((w>>32)&0xff) % n)
	if h := p.w.mem.Load32(p.f(lay.OffHandoffTo)); h != 0xff && int(h) < n {
		_ = p.w.inst.Ring(int(h))
	}
}

func (p *Partition) ringWrite(pos uint64, src []byte) {
	lay := &p.w.lay
	S := uint64(p.slab)
	pages := p.ring + lay.OffPages
	for len(src) > 0 {
		page := pos / S
		off := pos % S
		n := S - off
		if n > uint64(len(src)) {
			n = uint64(len(src))
		}
		v := p.w.mem.Load64(pages + uint32((page%uint64(p.nSlots))*8))
		addr := uint64(lay.PoolBase) + uint64(uint32(v))*S + off
		p.w.mem.WriteBytes(uint32(addr), src[:n])
		src = src[n:]
		pos += n
	}
}

// Enqueue copies one entry into the ring (waiting for credits, §7) and
// returns its rseq. The entry is durable once Acked(rseq).
func (p *Partition) Enqueue(ctx context.Context, e *Entry) (uint64, error) {
	L := e.size()
	if L > p.maxEntry {
		return 0, fmt.Errorf("%w (%d > %d bytes)", ErrEntryTooLarge, L, p.maxEntry)
	}
	if e.Flags&FlagCidPresent != 0 && len(e.CID) != cidBytes {
		return 0, fmt.Errorf("format2: a CID is %d bytes, not %d", cidBytes, len(e.CID))
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	// Assemble the entry once (header rseq patched in place).
	if cap(p.scratch) < int(L) {
		p.scratch = make([]byte, L)
	}
	buf := p.scratch[:L]
	for i := range buf {
		buf[i] = 0
	}
	binary.LittleEndian.PutUint32(buf[0:], uint32(L))
	binary.LittleEndian.PutUint16(buf[4:], e.Kind)
	binary.LittleEndian.PutUint16(buf[6:], e.Flags)
	binary.LittleEndian.PutUint64(buf[16:], uint64(e.ArrivalMs))
	copy(buf[24:60], e.CID)
	binary.LittleEndian.PutUint32(buf[64:], uint32(len(e.Attr)))
	binary.LittleEndian.PutUint32(buf[68:], uint32(len(e.Frame)))
	at := entryHeaderBytes
	at += copy(buf[at:], e.Attr)
	at += copy(buf[at:], e.Frame)
	if e.Flags&FlagSealed != 0 {
		binary.LittleEndian.PutUint32(buf[at:], uint32(len(e.Sealed)))
		at += 4
		copy(buf[at:], e.Sealed)
	}
	lay := &p.w.lay
	mem := p.w.mem
	S := uint64(p.slab)
	var wait backoff
	for {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		if !p.w.inst.Enter() {
			return 0, ErrStopped
		}
		st := mem.Load32(p.f(lay.OffState))
		if st == ringQuarantined {
			p.w.inst.Exit()
			return 0, ErrQuarantined
		}
		mem.Add64(p.f(lay.OffProdBusy), 1)
		if mem.Load32(p.f(lay.OffReclaim)) != 0 {
			mem.Add64(p.f(lay.OffProdBusy), ^uint64(0))
			p.w.inst.Exit()
			runtime.Gosched()
			continue
		}
		tail := mem.Load64(p.f(lay.OffTail))
		head := mem.Load64(p.f(lay.OffHead))
		used := tail - head
		ok := st == ringActive && (used == 0 || used+L <= p.capBytes)
		mapped := true
		if ok {
			for pg := tail / S; pg <= (tail+L-1)/S; pg++ {
				v := mem.Load64(p.ring + lay.OffPages + uint32((pg%uint64(p.nSlots))*8))
				if v>>32 != pg+1 {
					mapped = false
					mem.Store32(p.f(lay.OffWantPage), uint32((tail+L-1)/S+1))
					break
				}
			}
		}
		if ok && mapped {
			rseq := mem.Add64(p.f(lay.OffNextRseq), 1) - 1
			binary.LittleEndian.PutUint64(buf[8:], rseq)
			p.ringWrite(tail, buf)
			mem.Store64(p.f(lay.OffTail), tail+L)
			mem.Add64(p.f(lay.OffProdBusy), ^uint64(0))
			p.ringOwner()
			p.w.inst.Exit()
			return rseq, nil
		}
		mem.Add64(p.f(lay.OffProdBusy), ^uint64(0))
		p.ringOwner()
		// Staging stops while the reject ring is full; this producer holds
		// p.mu, so it drains it (WaitAck cannot while we wait here).
		p.drainRejects()
		p.creditWaits.Add(1)
		mem.Store32(p.f(lay.OffProdWaiting), 1)
		genOff := lay.OffAckGen
		if !mapped {
			genOff = lay.OffMapGen
		}
		g := mem.Load32(p.f(genOff))
		p.w.inst.Exit()
		// Zero credits: wait for an ack (space) or a mapping (pages).
		wait.reset()
		for {
			if err := wait.sleep(ctx, p.w.inst.Stopping()); err != nil {
				return 0, err
			}
			if !p.w.inst.Enter() {
				return 0, ErrStopped
			}
			changed := mem.Load32(p.f(genOff)) != g || mem.Load32(p.f(lay.OffState)) != st
			p.w.inst.Exit()
			if changed || wait.elapsed() > time.Second {
				break
			}
		}
	}
}

// drainRejects moves the engine's reject entries into p.rejects (the reject
// ring has 32 slots and staging stops while it is full). Caller holds p.mu.
func (p *Partition) drainRejects() {
	lay := &p.w.lay
	mem := p.w.mem
	t := mem.Load64(p.f(lay.OffRejectTail))
	h := mem.Load64(p.f(lay.OffRejectHead))
	for ; t < h; t++ {
		e := p.ring + lay.OffRejects + uint32((t%rejectSlots)*16)
		p.rejects[mem.Load64(e)] = int32(mem.Load32(e + 8))
	}
	mem.Store64(p.f(lay.OffRejectTail), t)
}

// Acked returns the partition's durable ack HWM.
func (p *Partition) Acked() (uint64, error) {
	if !p.w.inst.Enter() {
		return 0, ErrStopped
	}
	defer p.w.inst.Exit()
	return p.w.mem.Load64(p.f(p.w.lay.OffAckedRseq)), nil
}

// WaitAck waits until rseq is durable. A rejected entry returns *RejectError.
func (p *Partition) WaitAck(ctx context.Context, rseq uint64) error {
	lay := &p.w.lay
	mem := p.w.mem
	var wait backoff
	for {
		if !p.w.inst.Enter() {
			return ErrStopped
		}
		acked := mem.Load64(p.f(lay.OffAckedRseq))
		st := mem.Load32(p.f(lay.OffState))
		if acked >= rseq {
			p.mu.Lock()
			p.drainRejects()
			code, rejected := p.rejects[rseq]
			if rejected {
				delete(p.rejects, rseq)
			}
			p.mu.Unlock()
			p.w.inst.Exit()
			if rejected {
				return &RejectError{Rseq: rseq, Code: code}
			}
			return nil
		}
		p.w.inst.Exit()
		if st == ringQuarantined {
			return ErrQuarantined
		}
		if err := wait.sleep(ctx, p.w.inst.Stopping()); err != nil {
			return err
		}
	}
}

// Rejects drains and returns every reject recorded since the last call
// (rseq -> code), for callers that wait on a batch's last rseq only.
func (p *Partition) Rejects() map[uint64]int32 {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.w.inst.Enter() {
		p.drainRejects()
		p.w.inst.Exit()
	}
	out := p.rejects
	p.rejects = map[uint64]int32{}
	return out
}

// backoff polls shared-memory words: a short spin of yields, then sleeps
// doubling from 20 us to 1 ms. The guest's futex-style waits notify waiters
// inside WasmEdge, which Go cannot join, so the host polls.
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

func (b *backoff) sleep(ctx context.Context, stop <-chan struct{}) error {
	if b.start.IsZero() {
		b.start = time.Now()
	}
	b.n++
	if b.n <= 32 {
		runtime.Gosched()
		return ctx.Err()
	}
	d := 20 * time.Microsecond << uint(min(b.n-33, 6))
	if d > time.Millisecond {
		d = time.Millisecond
	}
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
