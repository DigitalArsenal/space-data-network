package format2

// Counters without a lane (design A28, A32; flatsql T2 deviation 14): the
// registry and every partition and type head are read straight from the
// files the writer publishes (flatsql ps/format.h: HeadPrefix,
// PartitionHeadFixed, TypeHeadFixed, RegistryHeadFixed). A head is two 4 KiB
// slots the writer overwrites alternately after every commit; a reader takes
// the valid slot (magic, kind, CRC32C) with the highest generation, so it
// never sees a torn one and never waits on the writer. The cost is one pread
// of the registry head, the registry frames not read before, and one pread
// per head: no lane, no statement, no lock.

import (
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"os"
	"path/filepath"
	"strings"
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
)

// HeadReader reads counters from heads. Safe for concurrent use.
type HeadReader struct {
	root string // <engine root>/fsql2

	mu          sync.Mutex
	incarnation uint32
	fslRead     uint64
	frames      uint64
	parts       map[uint32]*headPart
	types       map[string]string // fid -> schema name ("OMM.fbs")
	files       map[string]*os.File
	buf         [2 * headSlotBytes]byte

	// Label waits (A20) never take mu: each type has its own reader.
	lmu          sync.Mutex
	labels       map[[4]byte]*typeLabels
	waitTimeouts atomic.Uint64
	lastTimeout  atomic.Int64 // unix ns of the last logged timeout
}

// Close releases the cached head descriptors.
func (h *HeadReader) Close() {
	h.mu.Lock()
	for _, f := range h.files {
		f.Close()
	}
	h.files = nil
	h.mu.Unlock()
	h.lmu.Lock()
	for _, t := range h.labels {
		t.close()
	}
	h.labels = nil
	h.lmu.Unlock()
}

type headPart struct {
	pid                  uint32
	fid                  string
	token, sqlName       string
	quarantined, dropped bool
}

// NewHeadReader reads the store whose engine root is root (its fsql2/ is
// inside).
func NewHeadReader(root string) *HeadReader {
	return &HeadReader{root: filepath.Join(root, Dir), parts: map[uint32]*headPart{}, types: map[string]string{}}
}

// readHeadSlot returns the best valid slot of a head file (nil when the head
// was never written). Head files are rewritten in place (two slots), never
// replaced, so their descriptors are kept open: a counter read is one pread
// per head.
func (h *HeadReader) readHeadSlot(path string, kind uint16) ([]byte, error) {
	f := h.files[path]
	if f == nil {
		var err error
		f, err = os.Open(path)
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		if h.files == nil {
			h.files = map[string]*os.File{}
		}
		h.files[path] = f
	}
	n, err := f.ReadAt(h.buf[:], 0)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	return bestHeadSlot(h.buf[:], n, kind), nil // aliases h.buf: read before the next call (h.mu held)
}

// bestHeadSlot returns the valid slot (magic, kind, CRC32C) with the highest
// generation among the n bytes of a head read into buf, trimmed to its used
// length; nil when neither slot is valid.
func bestHeadSlot(buf []byte, n int, kind uint16) []byte {
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
	return best
}

// refresh reads registry frames written since the last call.
func (h *HeadReader) refresh() error {
	slot, err := h.readHeadSlot(filepath.Join(h.root, "registry.fsh"), headRegistry)
	if err != nil {
		return err
	}
	if slot == nil || len(slot) < 64 {
		return nil // no partition registered yet
	}
	frameCount := binary.LittleEndian.Uint64(slot[32:])
	fslEnd := binary.LittleEndian.Uint64(slot[40:])
	incarnation := binary.LittleEndian.Uint32(slot[52:])
	if incarnation != h.incarnation || fslEnd < h.fslRead {
		h.incarnation, h.fslRead, h.frames = incarnation, 0, 0
		for path, f := range h.files {
			if !strings.HasSuffix(path, "registry.fsh") {
				f.Close()
				delete(h.files, path)
			}
		}
		h.parts = map[uint32]*headPart{}
		h.types = map[string]string{}
	}
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
		kind := binary.LittleEndian.Uint16(body)
		p := body[2:]
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
			if len(p) >= 8 {
				hp := &headPart{pid: binary.LittleEndian.Uint32(p), fid: string(p[4:8])}
				p = p[8:]
				var ok1, ok2 bool
				hp.token, ok1 = str()
				hp.sqlName, ok2 = str()
				if ok1 && ok2 {
					h.parts[hp.pid] = hp
				}
			}
		case regTypeAdd:
			if len(p) >= 4 {
				fid := string(p[:4])
				p = p[4:]
				if name, ok := str(); ok {
					h.types[fid] = name
				}
			}
		case regQuarantine, regUnquarantine, regDrop:
			if len(p) >= 4 {
				if hp := h.parts[binary.LittleEndian.Uint32(p)]; hp != nil {
					switch kind {
					case regDrop:
						hp.dropped = true
					default:
						hp.quarantined = kind == regQuarantine
					}
				}
			}
		}
		off += 8 + n
		h.frames++
	}
	h.fslRead += uint64(off)
	return nil
}

// Partitions reads every partition's counters from its head.
func (h *HeadReader) Partitions() ([]PartitionCounter, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if err := h.refresh(); err != nil {
		return nil, err
	}
	out := make([]PartitionCounter, 0, len(h.parts))
	for _, hp := range h.parts {
		if hp.dropped {
			continue
		}
		pc := PartitionCounter{PID: int64(hp.pid), Producer: hp.token, SQLName: hp.sqlName, Type: typeName(h.types[hp.fid]),
			Quarantined: hp.quarantined}
		slot, err := h.readHeadSlot(filepath.Join(h.root, "p", fmt.Sprintf("%08x", hp.pid), "h.fsh"), headPartition)
		if err != nil {
			return nil, err
		}
		if slot != nil && len(slot) >= 192 {
			u := func(off int) int64 { return int64(binary.LittleEndian.Uint64(slot[off:])) }
			pc.CommitSeq, pc.PseqHi = u(32), u(40)
			pc.Total, pc.TotalBytes, pc.Live, pc.LiveBytes = u(120), u(128), u(136), u(144)
			pc.Tombs, pc.DiskBytes = u(152), u(160)
			pc.MinEpoch, pc.MaxEpoch, pc.LatestArrival = u(168), u(176), u(184)
			if binary.LittleEndian.Uint32(slot[28:])&headQuarantined != 0 {
				pc.Quarantined = true
			}
		}
		out = append(out, pc)
	}
	return out, nil
}

// Types reads every type's head (gseq_hi, arrivals, first-live counters).
func (h *HeadReader) Types() ([]TypeCounter, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if err := h.refresh(); err != nil {
		return nil, err
	}
	np := map[string]int64{}
	for _, hp := range h.parts {
		if !hp.dropped {
			np[hp.fid]++
		}
	}
	out := make([]TypeCounter, 0, len(h.types))
	for fid, schema := range h.types {
		tc := TypeCounter{Type: typeName(schema), Schema: schema, NP: np[fid]}
		slot, err := h.readHeadSlot(filepath.Join(h.root, "t", hex.EncodeToString([]byte(fid)), "h.fsh"), headType)
		if err != nil {
			return nil, err
		}
		if slot != nil && len(slot) >= 112 {
			u := func(off int) int64 { return int64(binary.LittleEndian.Uint64(slot[off:])) }
			tc.CommitSeq, tc.GseqHi, tc.Arrivals = u(32), u(40), u(80)
			tc.FirstLive, tc.FirstLiveBytes = u(96), u(104)
		}
		out = append(out, tc)
	}
	return out, nil
}

// partitionPseqHi reads one partition head's pseq_hi (0 when unwritten). It
// opens the head for this one read and takes no lock: a write waiting for
// its labels never queues behind a counter sweep holding mu.
func (h *HeadReader) partitionPseqHi(pid uint32) (uint64, error) {
	f, err := os.Open(filepath.Join(h.root, "p", fmt.Sprintf("%08x", pid), "h.fsh"))
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	defer f.Close()
	var buf [2 * headSlotBytes]byte
	n, err := f.ReadAt(buf[:], 0)
	if err != nil && !errors.Is(err, io.EOF) {
		return 0, err
	}
	slot := bestHeadSlot(buf[:], n, headPartition)
	if len(slot) < 48 {
		return 0, nil
	}
	return binary.LittleEndian.Uint64(slot[40:]), nil
}

// labeledThrough returns labeled_through[pid] of a type (0 for a partition
// not labeled yet). known is false when the labels could not be read this
// time (no head yet, a torn or retired segment): the caller polls again.
func (h *HeadReader) labeledThrough(fid [4]byte, pid uint32) (through uint64, known bool, err error) {
	h.lmu.Lock()
	t := h.labels[fid]
	if t == nil {
		if h.labels == nil {
			h.labels = map[[4]byte]*typeLabels{}
		}
		t = &typeLabels{dir: filepath.Join(h.root, "t", hex.EncodeToString(fid[:])), labels: map[uint32]uint64{}}
		h.labels[fid] = t
	}
	h.lmu.Unlock()
	return t.through(pid)
}

// typeLabels follows one type's labeled_through table, the way the engine's
// readers do (flatsql ps/snapshot.cpp: LaneStore::loadType, loadTypeLabels).
// Up to 128 partitions the table is inline in the type head. Past that the
// head says nLabels 0xffff and names a full checkpoint batch in the type log
// (labelCkptSeg, labelCkptOff); every later batch carries the labels it
// changed, through the head's (mSeg, mEnd). The log is append-only below a
// published mEnd, so the fold continues from where the last read stopped; a
// batch whose FULL_LABELS flag starts a new checkpoint simply overwrites.
type typeLabels struct {
	dir string // <root>/fsql2/t/<fid hex>

	mu     sync.Mutex
	head   *os.File
	buf    [2 * headSlotBytes]byte
	readAt time.Time
	known  bool
	labels map[uint32]uint64

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
	if t.head != nil {
		t.head.Close()
		t.head = nil
	}
	if t.log != nil {
		t.log.Close()
		t.log = nil
	}
}

func (t *typeLabels) through(pid uint32) (uint64, bool, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.known || time.Since(t.readAt) >= labelFreshFor {
		if err := t.read(); err != nil {
			return 0, false, err
		}
	}
	if !t.known {
		return 0, false, nil
	}
	return t.labels[pid], true, nil
}

// read reads the type head and brings the table up to it.
func (t *typeLabels) read() error {
	t.known, t.readAt = false, time.Now()
	if t.head == nil {
		f, err := os.Open(filepath.Join(t.dir, "h.fsh"))
		if errors.Is(err, os.ErrNotExist) {
			return nil // the type owner has not written its head yet
		}
		if err != nil {
			return err
		}
		t.head = f
	}
	n, err := t.head.ReadAt(t.buf[:], 0)
	if err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	slot := bestHeadSlot(t.buf[:], n, headType)
	if len(slot) < typeHeadFixedBytes+4 {
		return nil
	}
	nL0 := int(binary.LittleEndian.Uint16(slot[92:]))
	nLabels := int(binary.LittleEndian.Uint16(slot[94:]))
	at := typeHeadFixedBytes + typeL0DirBytes*nL0
	if nLabels != labelsInLog {
		if at+labelEntryBytes*nLabels+4 > len(slot) {
			return nil
		}
		t.folding = false
		clear(t.labels)
		for i := 0; i < nLabels; i, at = i+1, at+labelEntryBytes {
			t.labels[binary.LittleEndian.Uint32(slot[at:])] = binary.LittleEndian.Uint64(slot[at+8:])
		}
		t.known = true
		return nil
	}
	known, err := t.fold(slot)
	if err != nil || !known {
		t.folding = false // the next read starts again at the checkpoint
	}
	t.known = known
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
		clear(t.labels)
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
				for i := 0; i < n; i += labelEntryBytes {
					t.labels[binary.LittleEndian.Uint32(e[i:])] = binary.LittleEndian.Uint64(e[i+8:])
				}
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
