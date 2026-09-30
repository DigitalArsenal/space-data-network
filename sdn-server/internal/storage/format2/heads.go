package format2

// Counters without a lane (design A28, A32; flatsql T2 deviation 14): the
// registry and every partition and type head are read straight from the
// files the writer publishes (flatsql ps/format.h: HeadPrefix,
// PartitionHeadFixed, TypeHeadFixed, RegistryHeadFixed). A head is two 4 KiB
// slots the writer overwrites alternately after every commit; a reader takes
// the valid slot (magic, kind, CRC32C) with the highest generation, so it
// never sees a torn one and never waits on the writer.
//
// A counter read never reads every head (terabyte audit B8). The reader keeps
// each partition's counters as its head last gave them, and their totals per
// type, per producer and for the store, published under smu. A read brings
// them up to date by reading only the heads that can have moved by a write:
// the partitions a write reached since their head was last read (the writer
// marks them: Touch) and the partitions the registry added. Each hold of mu
// reads at most sweepChunk heads; concurrent readers share the work, and a
// read returns once every partition marked before it began was read.
//
// Heads that move by the engine's own commits (merges, compaction, quota
// eviction) are caught by a background sweep that re-reads every partition
// head within sweepPeriod, without mu and without the descriptor cache, so
// it never delays a counter read and never evicts a written partition's
// descriptor. The totals therefore lag an engine-side commit by up to one
// sweep period: they serve the dashboard and the store totals. The decisions
// that cannot lag read their own heads now: a producer's quota
// (ProducerTotalsFresh, at most one head per type) and CopiesOf
// (PartitionsOfTypeFresh). Partition head descriptors sit behind an LRU of at
// most maxHeadFDs. The type directories' bytes (the type logs, arrivals and
// catalog, which no head counts) are summed by the sweep, outside every lock
// a reader takes.

import (
	"container/list"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

const (
	magicHead     = 0x32485346 // "FSH2"
	headSlotBytes = 4096
	headPrefix    = 32
	headPartition = 1
	headType      = 2
	headRegistry  = 3

	regPartitionAdd = 1
	regTypeAdd      = 2
	regQuarantine   = 3
	regUnquarantine = 4
	regDrop         = 6
	headQuarantined = 2

	// Type heads and the type log (flatsql ps/format.h: TypeHeadFixed,
	// TypeL0DirEntry, LabelEntry, TypeBatchHeader).
	typeHeadFixedBytes   = 160
	typeL0DirBytes       = 32
	labelEntryBytes      = 16
	labelsInLog          = 0xffff     // TypeHeadFixed.nLabels: labels live in the type log (A10)
	magicTypeBatch       = 0x42545346 // "FSTB"
	typeBatchHeaderBytes = 104

	// labelFreshFor: label waits on one type share a head read this recent.
	labelFreshFor = 250 * time.Microsecond

	// maxHeadFDs bounds the partition head descriptors kept open.
	maxHeadFDs = 1024
	// The background sweep re-reads every partition head within sweepPeriod,
	// sweepTick apart. sweepChunk bounds the heads one hold of mu reads (and
	// one publish of the counters).
	sweepPeriod = 30 * time.Second
	sweepTick   = time.Second
	sweepChunk  = 256
	// loadChunk bounds the heads one hold of mu reads during the first load
	// (each opens its head: about 40 µs), so a TypeOf or a GetRecord miss
	// waits a few milliseconds at most.
	loadChunk = 64
	// typeDirEvery: a type directory whose type head moved is summed again
	// at most this often (and at most at a tenth of the time its last sum
	// took); every one is summed again within sweepPeriod regardless.
	typeDirEvery = 2 * time.Second
)

// CounterTotals sums partition head counters (§4.4).
type CounterTotals struct {
	Partitions                                int64
	Total, TotalBytes, Live, LiveBytes, Tombs int64
	DiskBytes                                 int64
	// CommitSeqs sums the partitions' commit_seq: it moves whenever one of
	// them commits (a mark for caches over the partitions' contents).
	CommitSeqs int64
	// LatestArrival is the newest arrival over the partitions (per type
	// only; 0 in the store and producer totals).
	LatestArrival int64
}

func (c *CounterTotals) add(pc *PartitionCounter, sign int64) {
	c.Partitions += sign
	c.Total += sign * pc.Total
	c.TotalBytes += sign * pc.TotalBytes
	c.Live += sign * pc.Live
	c.LiveBytes += sign * pc.LiveBytes
	c.Tombs += sign * pc.Tombs
	c.DiskBytes += sign * pc.DiskBytes
	c.CommitSeqs += sign * pc.CommitSeq
}

// HeadReader reads counters from heads. Safe for concurrent use.
//
// Lock order: swMu, then mu, then dirMu, then smu, then dmu; a type's label
// lock (typeLabels.vmu) before smu. The sweep and the type directory sums
// never take mu while they read.
type HeadReader struct {
	root string // <engine root>/fsql2

	// mu serializes the registry refresh, the descriptor cache, the type
	// head descriptors, buf, the first load and the reads of marked
	// partitions. Each hold reads at most sweepChunk heads.
	mu          sync.Mutex
	closed      bool // Close ran: nothing reopens a head (h.mu)
	incarnation uint32
	fslRead     uint64
	frames      uint64
	regHead     *os.File
	fds         fdLRU
	typeHeads   map[string]*os.File // fid -> type head
	buf         [2 * headSlotBytes]byte
	loaded      bool     // every partition head read once this incarnation
	loadQ       []uint32 // the partitions the first load has still to read (nil: not started)

	stopped atomic.Bool // Close began: the sweep and the direct reads stop

	sweepEvery, sweepFull time.Duration // sweepTick, sweepPeriod (tests shorten them)
	dirEvery              time.Duration // typeDirEvery
	sweepStop, sweepDone  chan struct{} // h.mu

	// swMu serializes sweep steps: the walk position and its buffer.
	swMu    sync.Mutex
	sweepAt uint32
	swBuf   [2 * headSlotBytes]byte

	// dirMu guards the type directory sums (typeDirs, dirBuf).
	dirMu    sync.Mutex
	typeDirs map[string]*typeDirState
	dirBuf   [2 * headSlotBytes]byte

	// dmu guards the marks: partitions whose head moved by a write since it
	// was last read, oldest first. dirty maps a marked pid to the sequence
	// of its oldest pending mark; dirtyQ holds the marks in sequence order
	// (an entry whose sequence dirty no longer holds is stale).
	dmu     sync.Mutex
	touches uint64
	dirty   map[uint32]uint64
	dirtyQ  []dirtyMark
	dirtyAt int

	// smu guards the published counters.
	smu       sync.RWMutex
	parts     map[uint32]*headPart
	types     map[string]string // fid -> schema name ("OMM.fbs")
	byType    map[string]*typeTotals
	byProd    map[string]*prodTotals
	total     CounterTotals
	maxPid    uint32
	dirSum    int64 // Σ type directories + the registry and journals, as last summed
	dirSummed bool

	headReads atomic.Uint64 // partition head reads

	// words, when the engine publishes them in each ring descriptor
	// (flatsql_ps_ring_words, proposed; unset until an embedded engine
	// exports it), reads a partition's durable commit_seq and
	// labeled_through from shared memory: the sweep then reads a head as
	// soon as its commit_seq moves, and skips the heads whose commit_seq did
	// not. An error or an unknown partition leaves the head to the
	// time-sliced walk. Set before first use.
	words func(pid uint32) (commitSeq, labeledThrough uint64, ok bool, err error)

	// Label waits (A20) never take mu: each type has its own reader.
	lmu          sync.Mutex
	labelsClosed bool
	labels       map[[4]byte]*typeLabels
	waitTimeouts atomic.Uint64
}

type dirtyMark struct {
	pid uint32
	seq uint64
}

type headPart struct {
	pid                     uint32
	fid                     string
	token, sqlName          string
	regQuarantined, dropped bool
	read                    bool             // pc holds the head's counters
	gen                     uint64           // the head slot generation pc came from
	pc                      PartitionCounter // as the head last gave them (smu)
}

// typeTotals is one type's totals and its partitions.
type typeTotals struct {
	CounterTotals
	members map[uint32]*headPart
}

// prodTotals is one producer's totals and its partitions (one per type).
type prodTotals struct {
	CounterTotals
	members map[uint32]*headPart
}

// typeDirState is a type directory's bytes as last summed.
type typeDirState struct {
	seq   uint64 // the type head's commit_seq then
	at    time.Time
	took  time.Duration
	bytes int64
}

// NewHeadReader reads the store whose engine root is root (its fsql2/ is
// inside).
func NewHeadReader(root string) *HeadReader {
	h := &HeadReader{root: filepath.Join(root, Dir), sweepEvery: sweepTick, sweepFull: sweepPeriod, dirEvery: typeDirEvery,
		typeDirs: map[string]*typeDirState{}}
	h.fds.max = maxHeadFDs
	h.reset()
	return h
}

// reset forgets every partition and type (a new incarnation; h.mu held or
// not yet shared). The type directory sums stand: they are bytes on disk.
func (h *HeadReader) reset() {
	h.fds.closeAll()
	for _, f := range h.typeHeads {
		f.Close()
	}
	h.typeHeads = map[string]*os.File{}
	h.loaded, h.loadQ = false, nil
	h.smu.Lock()
	h.parts = map[uint32]*headPart{}
	h.types = map[string]string{}
	h.byType = map[string]*typeTotals{}
	h.byProd = map[string]*prodTotals{}
	h.total = CounterTotals{}
	h.maxPid = 0
	h.smu.Unlock()
}

// Close releases the descriptors and stops the sweep. Reads after Close
// return ErrStopped and open nothing. A second Close does nothing.
func (h *HeadReader) Close() {
	h.stopped.Store(true)
	h.mu.Lock()
	h.closed = true
	h.fds.closeAll()
	h.fds.l = nil
	if h.regHead != nil {
		h.regHead.Close()
		h.regHead = nil
	}
	for _, f := range h.typeHeads {
		f.Close()
	}
	h.typeHeads = nil
	stop, done := h.sweepStop, h.sweepDone
	h.sweepStop, h.sweepDone = nil, nil
	h.mu.Unlock()
	if stop != nil {
		close(stop)
		<-done
	}
	h.lmu.Lock()
	h.labelsClosed = true
	labels := h.labels
	h.labels = nil
	h.lmu.Unlock()
	for _, t := range labels {
		t.close()
	}
}

// Touch marks a partition whose head a write moved (acked, or ended without
// the ack being seen): the next counter read reads it again (the router's
// read-your-writes).
func (h *HeadReader) Touch(pid uint32) {
	h.dmu.Lock()
	h.touches++
	if _, ok := h.dirty[pid]; !ok {
		if h.dirty == nil {
			h.dirty = map[uint32]uint64{}
		}
		h.dirty[pid] = h.touches
		h.dirtyQ = append(h.dirtyQ, dirtyMark{pid, h.touches})
	}
	h.dmu.Unlock()
}

// touchMark is the sequence of the latest mark.
func (h *HeadReader) touchMark() uint64 {
	h.dmu.Lock()
	defer h.dmu.Unlock()
	return h.touches
}

// takeDirty takes up to k marks made at or before seq, oldest first.
func (h *HeadReader) takeDirty(seq uint64, k int) []dirtyMark {
	h.dmu.Lock()
	defer h.dmu.Unlock()
	var out []dirtyMark
	for h.dirtyAt < len(h.dirtyQ) && len(out) < k {
		m := h.dirtyQ[h.dirtyAt]
		if cur, ok := h.dirty[m.pid]; !ok || cur != m.seq {
			h.dirtyAt++ // superseded by an older mark put back in front
			continue
		}
		if m.seq > seq {
			break
		}
		h.dirtyAt++
		delete(h.dirty, m.pid)
		out = append(out, m)
	}
	if h.dirtyAt == len(h.dirtyQ) {
		h.dirtyQ, h.dirtyAt = h.dirtyQ[:0], 0
	} else if h.dirtyAt >= 1024 && 2*h.dirtyAt >= len(h.dirtyQ) {
		h.dirtyQ = append(h.dirtyQ[:0], h.dirtyQ[h.dirtyAt:]...)
		h.dirtyAt = 0
	}
	return out
}

// requeue puts marks whose heads were not read back in front, with their
// sequences: a reader waiting for them still waits.
func (h *HeadReader) requeue(ms []dirtyMark) {
	if len(ms) == 0 {
		return
	}
	h.dmu.Lock()
	defer h.dmu.Unlock()
	if h.dirty == nil {
		h.dirty = map[uint32]uint64{}
	}
	front := make([]dirtyMark, 0, len(ms)+len(h.dirtyQ)-h.dirtyAt)
	for _, m := range ms {
		if cur, ok := h.dirty[m.pid]; ok && cur < m.seq {
			continue
		}
		h.dirty[m.pid] = m.seq
		front = append(front, m)
	}
	h.dirtyQ = append(front, h.dirtyQ[h.dirtyAt:]...)
	h.dirtyAt = 0
}

// clearMarks drops the marks of pids made at or before seq: their heads were
// read after them.
func (h *HeadReader) clearMarks(pids []uint32, seq uint64) {
	h.dmu.Lock()
	defer h.dmu.Unlock()
	for _, pid := range pids {
		if cur, ok := h.dirty[pid]; ok && cur <= seq {
			delete(h.dirty, pid) // its queue entry is stale now
		}
	}
}

// HeadReads counts the partition head reads so far.
func (h *HeadReader) HeadReads() uint64 { return h.headReads.Load() }

// OpenHeadFDs is the partition head descriptors open now.
func (h *HeadReader) OpenHeadFDs() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.fds.len()
}

// fdLRU keeps at most max head descriptors open, closing the least recently
// used (h.mu held).
type fdLRU struct {
	max int
	l   *list.List // front: most recent; *fdEntry
	m   map[string]*list.Element
}

type fdEntry struct {
	path string
	f    *os.File
}

// get returns the open descriptor of path (nil when the file does not exist).
func (c *fdLRU) get(path string) (*os.File, error) {
	if e, ok := c.m[path]; ok {
		c.l.MoveToFront(e)
		return e.Value.(*fdEntry).f, nil
	}
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if c.l == nil {
		c.l, c.m = list.New(), map[string]*list.Element{}
	}
	c.m[path] = c.l.PushFront(&fdEntry{path: path, f: f})
	for c.max > 0 && c.l.Len() > c.max {
		e := c.l.Back()
		c.l.Remove(e)
		delete(c.m, e.Value.(*fdEntry).path)
		e.Value.(*fdEntry).f.Close()
	}
	return f, nil
}

func (c *fdLRU) drop(path string) {
	if e, ok := c.m[path]; ok {
		c.l.Remove(e)
		delete(c.m, path)
		e.Value.(*fdEntry).f.Close()
	}
}

func (c *fdLRU) closeAll() {
	if c.l == nil {
		return
	}
	for e := c.l.Front(); e != nil; e = e.Next() {
		e.Value.(*fdEntry).f.Close()
	}
	c.l, c.m = list.New(), map[string]*list.Element{}
}

func (c *fdLRU) len() int {
	if c.l == nil {
		return 0
	}
	return c.l.Len()
}

// readSlot reads the best valid slot of a head through f (nil when neither
// is valid). It aliases h.buf: use it before the next read (h.mu held).
func (h *HeadReader) readSlot(f *os.File, kind uint16) ([]byte, error) {
	slot, _, err := readHeadSlot(f, h.buf[:], kind)
	return slot, err
}

// readHeadSlot reads the best valid slot of a head through f into buf and
// returns it with its generation (nil when neither slot is valid).
func readHeadSlot(f *os.File, buf []byte, kind uint16) ([]byte, uint64, error) {
	n, err := f.ReadAt(buf[:2*headSlotBytes], 0)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, 0, err
	}
	slot, gen := bestHeadSlot(buf, n, kind)
	return slot, gen, nil
}

// bestHeadSlot returns the valid slot (magic, kind, CRC32C) with the highest
// generation among the n bytes of a head read into buf, trimmed to its used
// length, and that generation; nil when neither slot is valid.
func bestHeadSlot(buf []byte, n int, kind uint16) ([]byte, uint64) {
	var best []byte
	var bestGen uint64
	for s := 0; s < 2; s++ {
		if n < s*headSlotBytes+headPrefix+4 {
			continue
		}
		p := buf[s*headSlotBytes:]
		avail := n - s*headSlotBytes
		if avail > headSlotBytes {
			avail = headSlotBytes
		}
		used := int(binary.LittleEndian.Uint32(p[24:]))
		if binary.LittleEndian.Uint32(p) != magicHead || binary.LittleEndian.Uint16(p[4:]) != storeFormat ||
			binary.LittleEndian.Uint16(p[6:]) != kind || used < headPrefix+4 || used > avail {
			continue
		}
		if crc32.Checksum(p[:used-4], castagnoli) != binary.LittleEndian.Uint32(p[used-4:]) {
			continue
		}
		if gen := binary.LittleEndian.Uint64(p[8:]); best == nil || gen > bestGen {
			best, bestGen = p[:used], gen
		}
	}
	return best, bestGen
}

func (h *HeadReader) partHeadPath(pid uint32) string {
	return filepath.Join(h.root, "p", fmt.Sprintf("%08x", pid), "h.fsh")
}

func (h *HeadReader) typeDir(fid string) string {
	return filepath.Join(h.root, "t", hex.EncodeToString([]byte(fid)))
}

// refresh reads the registry frames written since the last call and applies
// them (h.mu held).
func (h *HeadReader) refresh() error {
	if h.regHead == nil {
		f, err := os.Open(filepath.Join(h.root, "registry.fsh"))
		if errors.Is(err, os.ErrNotExist) {
			return nil // no partition registered yet
		}
		if err != nil {
			return err
		}
		h.regHead = f
	}
	slot, err := h.readSlot(h.regHead, headRegistry)
	if err != nil {
		return err
	}
	if slot == nil || len(slot) < 64 {
		return nil
	}
	frameCount := binary.LittleEndian.Uint64(slot[32:])
	fslEnd := binary.LittleEndian.Uint64(slot[40:])
	maxPid := binary.LittleEndian.Uint32(slot[48:])
	incarnation := binary.LittleEndian.Uint32(slot[52:])
	if incarnation != h.incarnation || fslEnd < h.fslRead {
		h.incarnation, h.fslRead, h.frames = incarnation, 0, 0
		h.reset()
	}
	h.smu.Lock()
	h.maxPid = max(h.maxPid, maxPid)
	h.smu.Unlock()
	if h.frames >= frameCount || fslEnd <= h.fslRead {
		return nil
	}
	f, err := os.Open(filepath.Join(h.root, "registry.fsl"))
	if err != nil {
		return err
	}
	defer f.Close()
	buf := make([]byte, fslEnd-h.fslRead)
	if _, err := f.ReadAt(buf, int64(h.fslRead)); err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	h.smu.Lock()
	defer h.smu.Unlock()
	off := 0
	for off+10 <= len(buf) && h.frames < frameCount {
		n := int(binary.LittleEndian.Uint32(buf[off:]))
		if n < 2 || off+8+n > len(buf) {
			break
		}
		body := buf[off+8 : off+8+n]
		if crc32.Checksum(body, castagnoli) != binary.LittleEndian.Uint32(buf[off+4:]) {
			break
		}
		h.applyFrame(binary.LittleEndian.Uint16(body), body[2:])
		off += 8 + n
		h.frames++
	}
	h.fslRead += uint64(off)
	return nil
}

// applyFrame applies one registry frame (h.mu and h.smu held).
func (h *HeadReader) applyFrame(kind uint16, p []byte) {
	str := func() (string, bool) {
		if len(p) < 2 {
			return "", false
		}
		k := int(binary.LittleEndian.Uint16(p))
		if len(p) < 2+k {
			return "", false
		}
		s := string(p[2 : 2+k])
		p = p[2+k:]
		return s, true
	}
	switch kind {
	case regPartitionAdd:
		if len(p) < 8 {
			return
		}
		hp := &headPart{pid: binary.LittleEndian.Uint32(p), fid: string(p[4:8])}
		p = p[8:]
		var ok1, ok2 bool
		hp.token, ok1 = str()
		hp.sqlName, ok2 = str()
		if !ok1 || !ok2 || h.parts[hp.pid] != nil {
			return
		}
		h.parts[hp.pid] = hp
		h.maxPid = max(h.maxPid, hp.pid)
		tt := h.byType[hp.fid]
		if tt == nil {
			tt = &typeTotals{members: map[uint32]*headPart{}}
			h.byType[hp.fid] = tt
		}
		tt.members[hp.pid] = hp
		pt := h.byProd[hp.token]
		if pt == nil {
			pt = &prodTotals{members: map[uint32]*headPart{}}
			h.byProd[hp.token] = pt
		}
		pt.members[hp.pid] = hp
		for _, c := range []*CounterTotals{&h.total, &tt.CounterTotals, &pt.CounterTotals} {
			c.add(&hp.pc, 1)
		}
		h.Touch(hp.pid) // its head is read by the next counter read
	case regTypeAdd:
		if len(p) >= 4 {
			fid := string(p[:4])
			p = p[4:]
			if name, ok := str(); ok {
				h.types[fid] = name
			}
		}
	case regQuarantine, regUnquarantine, regDrop:
		if len(p) < 4 {
			return
		}
		hp := h.parts[binary.LittleEndian.Uint32(p)]
		if hp == nil {
			return
		}
		switch kind {
		case regDrop:
			tt, pt := h.byType[hp.fid], h.byProd[hp.token]
			for _, c := range []*CounterTotals{&h.total, &tt.CounterTotals, &pt.CounterTotals} {
				c.add(&hp.pc, -1)
			}
			delete(tt.members, hp.pid)
			delete(pt.members, hp.pid)
			delete(h.parts, hp.pid)
			hp.dropped = true
			h.fds.drop(h.partHeadPath(hp.pid))
			h.recomputeLatest(tt)
		default:
			hp.regQuarantined = kind == regQuarantine
		}
	}
}

func (h *HeadReader) recomputeLatest(tt *typeTotals) {
	tt.LatestArrival = 0
	for _, m := range tt.members {
		tt.LatestArrival = max(tt.LatestArrival, m.pc.LatestArrival)
	}
}

// headUpdate is a partition's counters read from its head.
type headUpdate struct {
	hp  *headPart
	gen uint64
	pc  PartitionCounter
}

// parsePartHead decodes a partition head slot (nil when no slot is valid:
// ok false).
func parsePartHead(slot []byte) (PartitionCounter, bool) {
	if len(slot) < 192 {
		return PartitionCounter{}, false
	}
	u := func(off int) int64 { return int64(binary.LittleEndian.Uint64(slot[off:])) }
	return PartitionCounter{CommitSeq: u(32), PseqHi: u(40),
		Total: u(120), TotalBytes: u(128), Live: u(136), LiveBytes: u(144),
		Tombs: u(152), DiskBytes: u(160), MinEpoch: u(168), MaxEpoch: u(176), LatestArrival: u(184),
		Quarantined: binary.LittleEndian.Uint32(slot[28:])&headQuarantined != 0}, true
}

// readPartHeadLRU reads one partition head through the descriptor cache
// (h.mu held). ok is false when there is nothing to read: no head file yet
// (a partition that never committed keeps zero counters) or no valid slot.
func (h *HeadReader) readPartHeadLRU(hp *headPart) (headUpdate, bool, error) {
	path := h.partHeadPath(hp.pid)
	f, err := h.fds.get(path)
	if err != nil || f == nil {
		return headUpdate{}, false, err
	}
	h.headReads.Add(1)
	slot, gen, err := readHeadSlot(f, h.buf[:], headPartition)
	if err != nil {
		h.fds.drop(path)
		return headUpdate{}, false, err
	}
	pc, ok := parsePartHead(slot)
	return headUpdate{hp: hp, gen: gen, pc: pc}, ok, nil
}

// readPartHeadDirect reads one partition head without the descriptor cache
// (open, read, close) into buf; no lock is needed.
func (h *HeadReader) readPartHeadDirect(hp *headPart, buf []byte) (headUpdate, bool, error) {
	f, err := os.Open(h.partHeadPath(hp.pid))
	if errors.Is(err, os.ErrNotExist) {
		return headUpdate{}, false, nil
	}
	if err != nil {
		return headUpdate{}, false, err
	}
	defer f.Close()
	h.headReads.Add(1)
	slot, gen, err := readHeadSlot(f, buf, headPartition)
	if err != nil {
		return headUpdate{}, false, err
	}
	pc, ok := parsePartHead(slot)
	return headUpdate{hp: hp, gen: gen, pc: pc}, ok, nil
}

// publish applies what was read. A head read older than (or the same as) the
// one already published changes nothing, and so does one of a partition no
// longer registered in this incarnation: the sweep, a fresh read and a
// counter read may read the same head concurrently.
func (h *HeadReader) publish(ups []headUpdate) {
	if len(ups) == 0 {
		return
	}
	h.smu.Lock()
	defer h.smu.Unlock()
	for _, u := range ups {
		hp := u.hp
		if hp.dropped || h.parts[hp.pid] != hp || (hp.read && u.gen <= hp.gen) {
			continue
		}
		h.setCounters(hp, u.pc)
		hp.gen = u.gen
	}
}

// setCounters replaces a partition's counters and moves every total by the
// difference (h.smu held).
func (h *HeadReader) setCounters(hp *headPart, pc PartitionCounter) {
	tt, pt := h.byType[hp.fid], h.byProd[hp.token]
	old := hp.pc
	for _, c := range []*CounterTotals{&h.total, &tt.CounterTotals, &pt.CounterTotals} {
		c.add(&old, -1)
		c.add(&pc, 1)
	}
	hp.pc, hp.read = pc, true
	if pc.LatestArrival >= tt.LatestArrival {
		tt.LatestArrival = pc.LatestArrival
	} else if old.LatestArrival == tt.LatestArrival {
		h.recomputeLatest(tt)
	}
}

// part returns a registered partition (nil when it is not).
func (h *HeadReader) part(pid uint32) *headPart {
	h.smu.RLock()
	defer h.smu.RUnlock()
	return h.parts[pid]
}

// readMarked reads the heads of marked partitions through the descriptor
// cache and publishes them (h.mu held). The marks not read on an error are
// put back.
func (h *HeadReader) readMarked(marks []dirtyMark) error {
	ups := make([]headUpdate, 0, len(marks))
	for i, m := range marks {
		hp := h.part(m.pid)
		if hp == nil {
			continue // dropped, or not registered in this incarnation
		}
		u, ok, err := h.readPartHeadLRU(hp)
		if err != nil {
			h.publish(ups)
			h.requeue(marks[i:])
			return err
		}
		if ok {
			ups = append(ups, u)
		}
	}
	h.publish(ups)
	return nil
}

// loadStep reads the next chunk of the first load: every partition head
// once, without the descriptor cache (h.mu held).
func (h *HeadReader) loadStep() error {
	if h.loadQ == nil {
		h.smu.RLock()
		h.loadQ = make([]uint32, 0, len(h.parts))
		for pid := range h.parts {
			h.loadQ = append(h.loadQ, pid)
		}
		h.smu.RUnlock()
		sort.Slice(h.loadQ, func(i, j int) bool { return h.loadQ[i] < h.loadQ[j] })
	}
	n := min(loadChunk, len(h.loadQ))
	mark := h.touchMark() // the registrations' marks: read by this load
	ups := make([]headUpdate, 0, n)
	for i, pid := range h.loadQ[:n] {
		hp := h.part(pid)
		if hp == nil {
			continue
		}
		u, ok, err := h.readPartHeadDirect(hp, h.buf[:])
		if err != nil {
			h.publish(ups)
			h.loadQ = h.loadQ[i:]
			return err
		}
		if ok {
			ups = append(ups, u)
		}
	}
	h.publish(ups)
	h.clearMarks(h.loadQ[:n], mark)
	h.loadQ = h.loadQ[n:]
	if len(h.loadQ) == 0 {
		h.loaded, h.loadQ = true, nil
	}
	return nil
}

// sync brings the published counters up to date: the registry, then the
// heads of the partitions marked before the call, sweepChunk per hold of mu
// (every head the first time, loadChunk per hold). Concurrent callers share
// the work; each returns once the marks made before it began are read.
func (h *HeadReader) sync() error {
	var target uint64
	first := true
	for {
		h.mu.Lock()
		if h.closed {
			h.mu.Unlock()
			return ErrStopped
		}
		h.startSweep()
		if err := h.refresh(); err != nil {
			h.mu.Unlock()
			return err
		}
		if first {
			// After the first refresh: the partitions it registered are
			// marked, and read before this call returns.
			target, first = h.touchMark(), false
		}
		if !h.loaded {
			err := h.loadStep()
			h.mu.Unlock()
			if err != nil {
				return err
			}
			continue
		}
		marks := h.takeDirty(target, sweepChunk)
		if len(marks) == 0 {
			h.mu.Unlock()
			return nil
		}
		err := h.readMarked(marks)
		h.mu.Unlock()
		if err != nil {
			return err
		}
	}
}

// startSweep starts the background sweep once (h.mu held).
func (h *HeadReader) startSweep() {
	if h.sweepStop != nil || h.closed || h.sweepEvery <= 0 {
		return
	}
	h.sweepStop, h.sweepDone = make(chan struct{}), make(chan struct{})
	go h.sweepLoop(h.sweepStop, h.sweepDone, h.sweepEvery)
}

func (h *HeadReader) sweepLoop(stop, done chan struct{}, every time.Duration) {
	defer close(done)
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
		}
		h.sweepStep()
	}
}

// sweepStep re-reads the next share of partition heads (every head within
// sweepFull) and, with the engine's commit_seq words, every head whose
// commit_seq moved; then the type directories. It reads without mu and
// without the descriptor cache, and publishes sweepChunk heads at a time.
func (h *HeadReader) sweepStep() {
	h.swMu.Lock()
	defer h.swMu.Unlock()
	if h.stopped.Load() {
		return
	}
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return
	}
	err := h.refresh()
	h.mu.Unlock()
	if err != nil {
		return
	}
	var skip map[uint32]bool
	if h.words != nil {
		// Read the heads whose commit_seq moved now; the walk below skips
		// them and those that did not move.
		var moved []*headPart
		moved, skip = h.wordsMoved()
		if !h.sweepRead(moved) {
			return
		}
		for _, hp := range moved {
			skip[hp.pid] = true
		}
	}
	h.smu.RLock()
	n := len(h.parts)
	h.smu.RUnlock()
	want := n
	if h.sweepFull > h.sweepEvery && h.sweepEvery > 0 {
		want = int((int64(n)*int64(h.sweepEvery) + int64(h.sweepFull) - 1) / int64(h.sweepFull))
	}
	want = min(n, max(want, 64))
	if !h.sweepRead(h.nextSweep(want, skip)) {
		return
	}
	h.sumDirs()
}

// sweepRead reads hps' heads with the sweep's buffer, publishing sweepChunk
// at a time; false when the reader closed.
func (h *HeadReader) sweepRead(hps []*headPart) bool {
	ups := make([]headUpdate, 0, min(len(hps), sweepChunk))
	for _, hp := range hps {
		if h.stopped.Load() {
			return false
		}
		u, ok, err := h.readPartHeadDirect(hp, h.swBuf[:])
		if err == nil && ok {
			ups = append(ups, u)
		}
		if len(ups) >= sweepChunk {
			h.publish(ups)
			ups = ups[:0]
		}
	}
	h.publish(ups)
	return !h.stopped.Load()
}

// wordsMoved asks the engine's words which partitions' commit_seq moved
// since their head was read (moved) and which did not (unchanged). A
// partition whose words cannot be read is in neither: the time-sliced walk
// reads it in its turn.
func (h *HeadReader) wordsMoved() (moved []*headPart, unchanged map[uint32]bool) {
	type seen struct {
		hp   *headPart
		seq  int64
		read bool
	}
	h.smu.RLock()
	all := make([]seen, 0, len(h.parts))
	for _, hp := range h.parts {
		all = append(all, seen{hp, hp.pc.CommitSeq, hp.read})
	}
	h.smu.RUnlock()
	unchanged = make(map[uint32]bool, len(all))
	for _, p := range all {
		seq, _, ok, err := h.words(p.hp.pid)
		switch {
		case err != nil || !ok:
		case p.read && int64(seq) == p.seq:
			unchanged[p.hp.pid] = true
		default:
			moved = append(moved, p.hp)
		}
	}
	return moved, unchanged
}

// nextSweep returns up to k registered partitions after sweepAt, wrapping,
// leaving out those in skip (swMu held). Pids are dense (allocated in order,
// never reused), so the walk costs the chunk plus the pids it skips.
func (h *HeadReader) nextSweep(k int, skip map[uint32]bool) []*headPart {
	h.smu.RLock()
	defer h.smu.RUnlock()
	if len(h.parts) == 0 || h.maxPid == 0 {
		return nil
	}
	k = min(k, len(h.parts))
	out := make([]*headPart, 0, k)
	pid := h.sweepAt
	for steps := uint32(0); len(out) < k && steps < h.maxPid; steps++ {
		pid++
		if pid > h.maxPid {
			pid = 1
		}
		if hp := h.parts[pid]; hp != nil && !skip[pid] {
			out = append(out, hp)
		}
	}
	h.sweepAt = pid
	return out
}

// sumDirs sums the bytes of the type directories: each whose type head's
// commit_seq moved (at most once per dirEvery, and at most at a tenth of the
// time its last sum took), every one at least once per sweepFull, and the
// registry and journal files. It takes no lock a counter read takes.
func (h *HeadReader) sumDirs() {
	h.dirMu.Lock()
	defer h.dirMu.Unlock()
	if h.stopped.Load() {
		return
	}
	h.smu.RLock()
	fids := make([]string, 0, len(h.types))
	for fid := range h.types {
		fids = append(fids, fid)
	}
	h.smu.RUnlock()
	now := time.Now()
	keep := make(map[string]bool, len(fids))
	for _, fid := range fids {
		keep[fid] = true
		seq := h.typeCommitSeqDirect(fid)
		st := h.typeDirs[fid]
		if st != nil {
			every := max(h.dirEvery, 10*st.took)
			age := now.Sub(st.at)
			moved := seq == 0 || st.seq != seq // a type with no head yet counts as moved
			if !(moved && age >= every) && !(h.sweepFull > 0 && age >= max(h.sweepFull, every)) {
				continue
			}
		} else {
			st = &typeDirState{}
			h.typeDirs[fid] = st
		}
		start := time.Now()
		st.bytes = treeFileBytes(h.typeDir(fid))
		st.seq, st.at, st.took = seq, now, time.Since(start)
	}
	for fid := range h.typeDirs {
		if !keep[fid] {
			delete(h.typeDirs, fid)
		}
	}
	sum := treeFileBytes(filepath.Join(h.root, "j"))
	for _, name := range []string{"registry.fsl", "registry.fsh"} {
		if fi, err := os.Stat(filepath.Join(h.root, name)); err == nil {
			sum += fi.Size()
		}
	}
	for _, st := range h.typeDirs {
		sum += st.bytes
	}
	h.smu.Lock()
	h.dirSum, h.dirSummed = sum, true
	h.smu.Unlock()
}

// typeCommitSeqDirect reads a type head's commit_seq without a kept
// descriptor (dirMu held; 0 when it cannot be read).
func (h *HeadReader) typeCommitSeqDirect(fid string) uint64 {
	f, err := os.Open(filepath.Join(h.typeDir(fid), "h.fsh"))
	if err != nil {
		return 0
	}
	defer f.Close()
	slot, _, err := readHeadSlot(f, h.dirBuf[:], headType)
	if err != nil || len(slot) < 40 {
		return 0
	}
	return binary.LittleEndian.Uint64(slot[32:])
}

// treeFileBytes sums the sizes of the regular files under dir.
func treeFileBytes(dir string) int64 {
	var n int64
	_ = filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			if d != nil && d.IsDir() {
				return fs.SkipDir
			}
			return nil // gone while walking (a retired run)
		}
		if !d.Type().IsRegular() {
			return nil
		}
		if fi, err := d.Info(); err == nil {
			n += fi.Size()
		}
		return nil
	})
	return n
}

// typeHead reads a type's head slot through its kept descriptor (h.mu held;
// the slot aliases h.buf).
func (h *HeadReader) typeHead(fid string) ([]byte, error) {
	f := h.typeHeads[fid]
	if f == nil {
		var err error
		f, err = os.Open(filepath.Join(h.typeDir(fid), "h.fsh"))
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		h.typeHeads[fid] = f
	}
	return h.readSlot(f, headType)
}

// counter is a partition's published counters with its registry identity
// (h.smu held).
func (h *HeadReader) counter(hp *headPart) PartitionCounter {
	pc := hp.pc
	pc.PID, pc.Producer, pc.SQLName, pc.Type = int64(hp.pid), hp.token, hp.sqlName, typeName(h.types[hp.fid])
	pc.Quarantined = pc.Quarantined || hp.regQuarantined
	return pc
}

// Partitions returns every partition's counters.
func (h *HeadReader) Partitions() ([]PartitionCounter, error) {
	if err := h.sync(); err != nil {
		return nil, err
	}
	h.smu.RLock()
	defer h.smu.RUnlock()
	out := make([]PartitionCounter, 0, len(h.parts))
	for _, hp := range h.parts {
		out = append(out, h.counter(hp))
	}
	return out, nil
}

// Partition returns one partition's counters (ok false when it is not
// registered).
func (h *HeadReader) Partition(pid uint32) (PartitionCounter, bool, error) {
	if err := h.sync(); err != nil {
		return PartitionCounter{}, false, err
	}
	h.smu.RLock()
	defer h.smu.RUnlock()
	hp := h.parts[pid]
	if hp == nil {
		return PartitionCounter{}, false, nil
	}
	return h.counter(hp), true, nil
}

// fidsOf returns the fids registered under a type name (h.smu held).
func (h *HeadReader) fidsOf(typ string) []string {
	var out []string
	for fid, schema := range h.types {
		if typeName(schema) == typ {
			out = append(out, fid)
		}
	}
	return out
}

// membersOf returns one type's partitions (h.smu held).
func (h *HeadReader) membersOf(typ string) []*headPart {
	var out []*headPart
	for _, fid := range h.fidsOf(typ) {
		if tt := h.byType[fid]; tt != nil {
			for _, hp := range tt.members {
				out = append(out, hp)
			}
		}
	}
	return out
}

// PartitionsOfType returns the counters of one type's partitions ("OMM").
func (h *HeadReader) PartitionsOfType(typ string) ([]PartitionCounter, error) {
	if err := h.sync(); err != nil {
		return nil, err
	}
	h.smu.RLock()
	defer h.smu.RUnlock()
	members := h.membersOf(typ)
	out := make([]PartitionCounter, 0, len(members))
	for _, hp := range members {
		out = append(out, h.counter(hp))
	}
	return out, nil
}

// PartitionsOfTypeFresh reads every head of one type now (no descriptor
// kept, no lock held while reading) and returns the counters those reads
// gave: each read after the call began, whatever the sweep last saw. A
// partition with no head file yet has zero counters (it committed nothing).
func (h *HeadReader) PartitionsOfTypeFresh(typ string) ([]PartitionCounter, error) {
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return nil, ErrStopped
	}
	err := h.refresh()
	h.mu.Unlock()
	if err != nil {
		return nil, err
	}
	h.smu.RLock()
	members := h.membersOf(typ)
	out := make([]PartitionCounter, len(members))
	for i, hp := range members {
		out[i] = PartitionCounter{PID: int64(hp.pid), Producer: hp.token, SQLName: hp.sqlName, Type: typeName(h.types[hp.fid]),
			Quarantined: hp.regQuarantined}
	}
	h.smu.RUnlock()
	mark := h.touchMark()
	buf := make([]byte, 2*headSlotBytes)
	ups := make([]headUpdate, 0, min(len(members), sweepChunk))
	for i, hp := range members {
		if h.stopped.Load() {
			return nil, ErrStopped
		}
		u, ok, err := h.readPartHeadDirect(hp, buf)
		if err != nil {
			h.publish(ups)
			return nil, err
		}
		if !ok {
			continue
		}
		id := out[i]
		out[i] = u.pc
		out[i].PID, out[i].Producer, out[i].SQLName, out[i].Type = id.PID, id.Producer, id.SQLName, id.Type
		out[i].Quarantined = u.pc.Quarantined || id.Quarantined
		if ups = append(ups, u); len(ups) >= sweepChunk {
			h.publish(ups)
			ups = ups[:0]
		}
	}
	h.publish(ups)
	pids := make([]uint32, len(members))
	for i, hp := range members {
		pids[i] = hp.pid
	}
	h.clearMarks(pids, mark)
	return out, nil
}

// Totals returns the store's totals.
func (h *HeadReader) Totals() (CounterTotals, error) {
	if err := h.sync(); err != nil {
		return CounterTotals{}, err
	}
	h.smu.RLock()
	defer h.smu.RUnlock()
	return h.total, nil
}

// TypeTotals returns every type's totals, by type name ("OMM").
func (h *HeadReader) TypeTotals() (map[string]CounterTotals, error) {
	if err := h.sync(); err != nil {
		return nil, err
	}
	h.smu.RLock()
	defer h.smu.RUnlock()
	out := make(map[string]CounterTotals, len(h.byType))
	for fid, tt := range h.byType {
		typ := typeName(h.types[fid])
		c := out[typ]
		c.Partitions += tt.Partitions
		c.Total += tt.Total
		c.TotalBytes += tt.TotalBytes
		c.Live += tt.Live
		c.LiveBytes += tt.LiveBytes
		c.Tombs += tt.Tombs
		c.DiskBytes += tt.DiskBytes
		c.CommitSeqs += tt.CommitSeqs
		c.LatestArrival = max(c.LatestArrival, tt.LatestArrival)
		out[typ] = c
	}
	return out, nil
}

// ProducerTotals returns the totals of one producer's partitions (its token)
// as published: an engine-side commit (a quota eviction) shows within one
// sweep period.
func (h *HeadReader) ProducerTotals(token string) (CounterTotals, error) {
	if err := h.sync(); err != nil {
		return CounterTotals{}, err
	}
	h.smu.RLock()
	defer h.smu.RUnlock()
	if pt := h.byProd[token]; pt != nil {
		return pt.CounterTotals, nil
	}
	return CounterTotals{}, nil
}

// ProducerTotalsFresh is ProducerTotals with that producer's heads read now
// (at most one per type, through the descriptor cache): the input of a quota
// decision, which an eviction the sweep has not seen yet must not skew.
func (h *HeadReader) ProducerTotalsFresh(token string) (CounterTotals, error) {
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return CounterTotals{}, ErrStopped
	}
	if err := h.refresh(); err != nil {
		h.mu.Unlock()
		return CounterTotals{}, err
	}
	h.smu.RLock()
	var members []*headPart
	var pids []uint32
	if pt := h.byProd[token]; pt != nil {
		for _, hp := range pt.members {
			members = append(members, hp)
			pids = append(pids, hp.pid)
		}
	}
	h.smu.RUnlock()
	mark := h.touchMark()
	ups := make([]headUpdate, 0, len(members))
	for _, hp := range members {
		u, ok, err := h.readPartHeadLRU(hp)
		if err != nil {
			h.publish(ups)
			h.mu.Unlock()
			return CounterTotals{}, err
		}
		if ok {
			ups = append(ups, u)
		}
	}
	h.publish(ups)
	h.clearMarks(pids, mark)
	h.mu.Unlock()
	h.smu.RLock()
	defer h.smu.RUnlock()
	if pt := h.byProd[token]; pt != nil {
		return pt.CounterTotals, nil
	}
	return CounterTotals{}, nil
}

// DiskBytes is the store's bytes on disk: Σ partition disk_bytes, the type
// directories (logs, arrivals, catalog runs), the registry and the commit
// journals. The directories are summed by the sweep (once here, the first
// time), so they lag by up to one sweep tick; no read scans a directory.
func (h *HeadReader) DiskBytes() (int64, error) {
	if err := h.sync(); err != nil {
		return 0, err
	}
	h.smu.RLock()
	summed := h.dirSummed
	h.smu.RUnlock()
	if !summed {
		h.sumDirs()
	}
	if h.stopped.Load() {
		return 0, ErrStopped
	}
	h.smu.RLock()
	defer h.smu.RUnlock()
	return h.total.DiskBytes + h.dirSum, nil
}

// Types reads every type's head (gseq_hi, arrivals, first-live counters).
func (h *HeadReader) Types() ([]TypeCounter, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return nil, ErrStopped
	}
	if err := h.refresh(); err != nil {
		return nil, err
	}
	h.smu.RLock()
	fids := make(map[string]string, len(h.types))
	np := make(map[string]int64, len(h.types))
	for fid, schema := range h.types {
		fids[fid] = schema
		if tt := h.byType[fid]; tt != nil {
			np[fid] = int64(len(tt.members))
		}
	}
	h.smu.RUnlock()
	out := make([]TypeCounter, 0, len(fids))
	for fid, schema := range fids {
		tc, err := h.typeCounter(fid, schema, np[fid])
		if err != nil {
			return nil, err
		}
		out = append(out, tc)
	}
	return out, nil
}

// TypeOf reads one type's head ("OMM"); ok is false when no partition
// registered the type yet.
func (h *HeadReader) TypeOf(typ string) (tc TypeCounter, ok bool, err error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return TypeCounter{}, false, ErrStopped
	}
	if err := h.refresh(); err != nil {
		return TypeCounter{}, false, err
	}
	h.smu.RLock()
	fids := h.fidsOf(typ)
	schemas := make([]string, len(fids))
	np := make([]int64, len(fids))
	for i, fid := range fids {
		schemas[i] = h.types[fid]
		if tt := h.byType[fid]; tt != nil {
			np[i] = int64(len(tt.members))
		}
	}
	h.smu.RUnlock()
	if len(fids) == 0 {
		return TypeCounter{}, false, nil
	}
	tc, err = h.typeCounter(fids[0], schemas[0], np[0])
	return tc, err == nil, err
}

// typeCounter reads one type head (h.mu held).
func (h *HeadReader) typeCounter(fid, schema string, np int64) (TypeCounter, error) {
	tc := TypeCounter{Type: typeName(schema), Schema: schema, NP: np}
	slot, err := h.typeHead(fid)
	if err != nil {
		return tc, err
	}
	if slot != nil && len(slot) >= 112 {
		u := func(off int) int64 { return int64(binary.LittleEndian.Uint64(slot[off:])) }
		tc.CommitSeq, tc.GseqHi, tc.Arrivals = u(32), u(40), u(80)
		tc.FirstLive, tc.FirstLiveBytes = u(96), u(104)
	}
	return tc, nil
}

// fidOf returns the fid a type name is registered under (4 bytes; ok false
// when none is).
func (h *HeadReader) fidOf(typ string) ([4]byte, bool) {
	h.mu.Lock()
	if !h.closed {
		_ = h.refresh()
	}
	h.mu.Unlock()
	return h.fidOfPublished(typ)
}

// fidOfPublished is fidOf from the registry as last read.
func (h *HeadReader) fidOfPublished(typ string) ([4]byte, bool) {
	h.smu.RLock()
	defer h.smu.RUnlock()
	var fid [4]byte
	fids := h.fidsOf(typ)
	if len(fids) == 0 {
		return fid, false
	}
	copy(fid[:], fids[0])
	return fid, true
}

// behindParts returns the partitions of one type whose published counters
// can hide a live copy the catalog does not show: those whose labeled_through
// is below their pseq_hi, those not read yet, all of them (with a pseq_hi)
// when the labels are unknown, and those whose labels are past their
// published pseq_hi (a head that moved without a mark: read again by the
// next counter read). labels(pid) is the type's labeled_through (have false:
// no entry, 0), known false when the table is unknown. h.smu is taken; the
// caller may hold the label table's lock.
func (h *HeadReader) behindParts(typ string, known bool, labels func(pid uint32) (uint64, bool)) (out []PartitionCounter, stale []uint32) {
	h.smu.RLock()
	defer h.smu.RUnlock()
	for _, hp := range h.membersOf(typ) {
		hi := uint64(max(hp.pc.PseqHi, 0))
		if !hp.read {
			out = append(out, h.counter(hp))
			continue
		}
		if !known {
			if hi > 0 {
				out = append(out, h.counter(hp))
			}
			continue
		}
		lt, _ := labels(hp.pid)
		switch {
		case lt > hi:
			out = append(out, h.counter(hp))
			stale = append(stale, hp.pid)
		case lt < hi:
			out = append(out, h.counter(hp))
		}
	}
	return out, stale
}

// partitionPseqHi reads one partition head's pseq_hi (0 when unwritten). It
// opens the head for this one read and takes no lock: a write waiting for
// its labels never queues behind a counter read holding mu.
func (h *HeadReader) partitionPseqHi(pid uint32) (uint64, error) {
	f, err := os.Open(h.partHeadPath(pid))
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	defer f.Close()
	var buf [2 * headSlotBytes]byte
	slot, _, err := readHeadSlot(f, buf[:], headPartition)
	if err != nil {
		return 0, err
	}
	if len(slot) < 48 {
		return 0, nil
	}
	return binary.LittleEndian.Uint64(slot[40:]), nil
}

// labeledThrough returns labeled_through[pid] of a type (0 for a partition
// not labeled yet). known is false when the labels could not be read this
// time (no head yet, a torn or retired segment, another waiter folding a
// table not yet known): the caller polls again. After Close it returns
// ErrStopped. The labels are always read from the type head (or its log),
// which is what type-level statements read: never from a word the engine
// may publish before its head holds it.
func (h *HeadReader) labeledThrough(fid [4]byte, pid uint32) (through uint64, known bool, err error) {
	t, err := h.typeLabelsOf(fid)
	if err != nil {
		return 0, false, err
	}
	return t.through(pid)
}

// typeLabelsOf returns a type's label reader, creating it on first use.
func (h *HeadReader) typeLabelsOf(fid [4]byte) (*typeLabels, error) {
	h.lmu.Lock()
	defer h.lmu.Unlock()
	if h.labelsClosed {
		return nil, ErrStopped
	}
	t := h.labels[fid]
	if t == nil {
		if h.labels == nil {
			h.labels = map[[4]byte]*typeLabels{}
		}
		t = &typeLabels{dir: filepath.Join(h.root, "t", hex.EncodeToString(fid[:])), labels: map[uint32]uint64{}}
		h.labels[fid] = t
	}
	return t, nil
}

// withLabels runs fn over a type's labeled_through table as read now (or by
// a read another caller finished within labelFreshFor), under the table's
// read lock: nothing is copied. known is false when the labels cannot be
// read this time; a table read while another caller folds only
// under-reports. A read error other than ErrStopped counts as unknown.
func (h *HeadReader) withLabels(fid [4]byte, fn func(labels map[uint32]uint64, known bool)) error {
	t, err := h.typeLabelsOf(fid)
	if err != nil {
		return err
	}
	if _, _, err := t.through(0); err != nil {
		if errors.Is(err, ErrStopped) {
			return err
		}
		fn(nil, false)
		return nil
	}
	t.vmu.RLock()
	defer t.vmu.RUnlock()
	fn(t.labels, t.known)
	return nil
}

// typeLabels follows one type's labeled_through table, the way the engine's
// readers do (flatsql ps/snapshot.cpp: LaneStore::loadType, loadTypeLabels).
// Up to 128 partitions the table is inline in the type head. Past that the
// head says nLabels 0xffff and names a full checkpoint batch in the type log
// (labelCkptSeg, labelCkptOff); every later batch carries the labels it
// changed, through the head's (mSeg, mEnd). The log is append-only below a
// published mEnd, so the fold continues from where the last read stopped; a
// batch whose FULL_LABELS flag starts a new checkpoint simply overwrites.
//
// Two locks: mu serializes the reads of the head and the log (a fold from
// the checkpoint can replay a whole 64 MiB segment), and vmu guards the
// table they publish. A waiter that finds mu held does not queue behind the
// fold: it takes the table as it stands and polls again, so its own
// deadline and context still bound it. A table read mid-fold only
// under-reports (labeled_through rises in log order), never over-reports.
type typeLabels struct {
	dir string // <root>/fsql2/t/<fid hex>

	vmu    sync.RWMutex
	labels map[uint32]uint64
	known  bool
	readAt time.Time

	mu     sync.Mutex
	closed bool
	head   *os.File
	buf    [2 * headSlotBytes]byte

	// The type-log fold (nLabels 0xffff): labels applied through (seg, off).
	folding     bool
	incarnation uint32
	seg         uint32
	off         uint64
	log         *os.File
	logSeg      uint32
	hdr         [typeBatchHeaderBytes]byte
	entries     []byte
}

func (t *typeLabels) close() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.closed = true
	if t.head != nil {
		t.head.Close()
		t.head = nil
	}
	if t.log != nil {
		t.log.Close()
		t.log = nil
	}
}

// published returns the table's value for pid and whether the table is
// known and was read within labelFreshFor.
func (t *typeLabels) published(pid uint32) (through uint64, known, fresh bool) {
	t.vmu.RLock()
	defer t.vmu.RUnlock()
	return t.labels[pid], t.known, t.known && time.Since(t.readAt) < labelFreshFor
}

func (t *typeLabels) through(pid uint32) (uint64, bool, error) {
	lt, known, fresh := t.published(pid)
	if fresh {
		return lt, true, nil
	}
	if !t.mu.TryLock() {
		return lt, known, nil // another waiter is reading: poll again
	}
	defer t.mu.Unlock()
	if t.closed {
		return 0, false, ErrStopped
	}
	if lt, _, fresh := t.published(pid); fresh {
		return lt, true, nil // read by the waiter that held mu
	}
	err := t.read()
	lt, known, _ = t.published(pid)
	if err != nil {
		return 0, false, err
	}
	return lt, known, nil
}

// setKnown publishes whether the table is known, as of now (t.mu held).
func (t *typeLabels) setKnown(known bool) {
	t.vmu.Lock()
	t.known, t.readAt = known, time.Now()
	t.vmu.Unlock()
}

// read reads the type head and brings the table up to it (t.mu held).
func (t *typeLabels) read() error {
	if t.head == nil {
		f, err := os.Open(filepath.Join(t.dir, "h.fsh"))
		if errors.Is(err, os.ErrNotExist) {
			t.setKnown(false)
			return nil // the type owner has not written its head yet
		}
		if err != nil {
			t.setKnown(false)
			return err
		}
		t.head = f
	}
	n, err := t.head.ReadAt(t.buf[:], 0)
	if err != nil && !errors.Is(err, io.EOF) {
		t.setKnown(false)
		return err
	}
	slot, _ := bestHeadSlot(t.buf[:], n, headType)
	if len(slot) < typeHeadFixedBytes+4 {
		t.setKnown(false)
		return nil
	}
	nL0 := int(binary.LittleEndian.Uint16(slot[92:]))
	nLabels := int(binary.LittleEndian.Uint16(slot[94:]))
	at := typeHeadFixedBytes + typeL0DirBytes*nL0
	if nLabels != labelsInLog {
		if at+labelEntryBytes*nLabels+4 > len(slot) {
			t.setKnown(false)
			return nil
		}
		t.folding = false
		t.vmu.Lock()
		clear(t.labels)
		for i := 0; i < nLabels; i, at = i+1, at+labelEntryBytes {
			t.labels[binary.LittleEndian.Uint32(slot[at:])] = binary.LittleEndian.Uint64(slot[at+8:])
		}
		t.known, t.readAt = true, time.Now()
		t.vmu.Unlock()
		return nil
	}
	known, err := t.fold(slot)
	if err != nil || !known {
		t.folding = false // the next read starts again at the checkpoint
	}
	t.setKnown(known && err == nil)
	return err
}

// fold applies the type-log batches after the last fold, through the head's
// (mSeg, mEnd). A missing segment (retired under a moved checkpoint) or a
// batch that does not parse leaves the table unknown for this read.
func (t *typeLabels) fold(slot []byte) (bool, error) {
	incarnation := binary.LittleEndian.Uint32(slot[52:])
	mSeg := binary.LittleEndian.Uint32(slot[64:])
	mEnd := binary.LittleEndian.Uint64(slot[72:])
	if !t.folding || t.incarnation != incarnation || t.seg > mSeg || (t.seg == mSeg && t.off > mEnd) {
		t.folding, t.incarnation = true, incarnation
		t.seg = binary.LittleEndian.Uint32(slot[120:])
		t.off = binary.LittleEndian.Uint64(slot[112:])
		t.vmu.Lock()
		clear(t.labels)
		t.known = false // rebuilt from the checkpoint: unknown until the fold ends
		t.vmu.Unlock()
		if t.seg > mSeg {
			return false, nil
		}
	}
	for {
		f, err := t.logFile(t.seg)
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		end := mEnd
		if t.seg < mSeg {
			fi, err := f.Stat()
			if err != nil {
				return false, err
			}
			end = uint64(fi.Size()) // a sealed segment is cut at its last batch
		}
		for t.off+typeBatchHeaderBytes <= end {
			if _, err := f.ReadAt(t.hdr[:], int64(t.off)); err != nil {
				return false, err
			}
			nLabel := uint64(binary.LittleEndian.Uint32(t.hdr[44:]))
			batchLen := uint64(binary.LittleEndian.Uint32(t.hdr[56:]))
			if binary.LittleEndian.Uint32(t.hdr[:]) != magicTypeBatch || batchLen < typeBatchHeaderBytes+8 ||
				t.off+batchLen > end || typeBatchHeaderBytes+nLabel*labelEntryBytes+8 > batchLen {
				return false, nil
			}
			if nLabel > 0 {
				n := int(nLabel) * labelEntryBytes
				if cap(t.entries) < n {
					t.entries = make([]byte, n)
				}
				e := t.entries[:n]
				if _, err := f.ReadAt(e, int64(t.off+typeBatchHeaderBytes)); err != nil {
					return false, err
				}
				t.vmu.Lock()
				for i := 0; i < n; i += labelEntryBytes {
					t.labels[binary.LittleEndian.Uint32(e[i:])] = binary.LittleEndian.Uint64(e[i+8:])
				}
				t.vmu.Unlock()
			}
			t.off += batchLen
		}
		if t.seg >= mSeg {
			return true, nil
		}
		t.seg, t.off = t.seg+1, 0
	}
}

// logFile returns the type log segment seg, keeping one descriptor: the
// segment the fold is in.
func (t *typeLabels) logFile(seg uint32) (*os.File, error) {
	if t.log != nil && t.logSeg == seg {
		return t.log, nil
	}
	if t.log != nil {
		t.log.Close()
		t.log = nil
	}
	f, err := os.Open(filepath.Join(t.dir, fmt.Sprintf("m-%06x.fsl", seg)))
	if err != nil {
		return nil, err
	}
	t.log, t.logSeg = f, seg
	return f, nil
}
