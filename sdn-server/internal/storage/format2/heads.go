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
}

// Close releases the cached head descriptors.
func (h *HeadReader) Close() {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, f := range h.files {
		f.Close()
	}
	h.files = nil
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
	buf := h.buf[:]
	n, err := f.ReadAt(buf, 0)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
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
	return best, nil // aliases h.buf: read before the next call (h.mu held)
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
