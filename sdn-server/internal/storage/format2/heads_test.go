package format2

// The labeled_through reader on type heads and type logs written here as the
// engine lays them out (flatsql ps/format.h TypeHeadFixed, LabelEntry,
// TypeBatchHeader; type_owner.cpp, reclaim.cpp typeRotateMeta and
// typeRetireMeta). No engine: these run in the quick CI jobs.

import (
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"hash/crc32"
	"maps"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// synthLog writes one type's head and log segments.
type synthLog struct {
	t    *testing.T
	dir  string
	gen  uint64
	inc  uint32
	segs map[uint32][]byte
	// truth is labeled_through as of the last batch appended.
	truth map[uint32]uint64
}

func newSynthLog(t *testing.T) *synthLog {
	return &synthLog{t: t, dir: t.TempDir(), inc: 1, segs: map[uint32][]byte{}, truth: map[uint32]uint64{}}
}

// batch appends a type batch to seg carrying labels (full: a FULL_LABELS
// checkpoint of every label so far) and pad bytes of other payload; it
// returns the batch's offset.
func (l *synthLog) batch(seg uint32, labels map[uint32]uint64, full bool, pad int) uint64 {
	maps.Copy(l.truth, labels)
	src := labels
	if full {
		src = l.truth
	}
	b := make([]byte, typeBatchHeaderBytes+labelEntryBytes*len(src)+pad+8)
	binary.LittleEndian.PutUint32(b[0:], magicTypeBatch)
	binary.LittleEndian.PutUint16(b[4:], 1)
	if full {
		binary.LittleEndian.PutUint16(b[6:], 1)
	}
	binary.LittleEndian.PutUint32(b[44:], uint32(len(src)))
	binary.LittleEndian.PutUint32(b[56:], uint32(len(b)))
	at := typeBatchHeaderBytes
	for pid, v := range src {
		binary.LittleEndian.PutUint32(b[at:], pid)
		binary.LittleEndian.PutUint64(b[at+8:], v)
		at += labelEntryBytes
	}
	binary.LittleEndian.PutUint32(b[len(b)-8:], crc32.Checksum(b[:len(b)-8], castagnoli))
	off := uint64(len(l.segs[seg]))
	l.segs[seg] = append(l.segs[seg], b...)
	return off
}

// flush writes seg with zeroTail zero bytes past its batches (a
// preallocated tail); a sealed segment is flushed with none.
func (l *synthLog) flush(seg uint32, zeroTail int) {
	b := append(append([]byte(nil), l.segs[seg]...), make([]byte, zeroTail)...)
	if err := os.WriteFile(filepath.Join(l.dir, fmt.Sprintf("m-%06x.fsl", seg)), b, 0o644); err != nil {
		l.t.Fatal(err)
	}
}

func (l *synthLog) retire(seg uint32) {
	if err := os.Remove(filepath.Join(l.dir, fmt.Sprintf("m-%06x.fsl", seg))); err != nil {
		l.t.Fatal(err)
	}
}

// head publishes a type head naming the log through (mSeg, mEnd) and the
// label checkpoint (labels in the log, nLabels 0xffff), or an inline table
// when inline is not nil.
func (l *synthLog) head(mSeg uint32, mEnd uint64, ckptSeg uint32, ckptOff uint64, inline map[uint32]uint64) {
	l.gen++
	slot := make([]byte, headSlotBytes)
	nL0 := 2 // L0 directory entries sit between the fixed part and the labels
	used := typeHeadFixedBytes + typeL0DirBytes*nL0 + 4
	nLabels := labelsInLog
	if inline != nil {
		nLabels = len(inline)
		used += labelEntryBytes * len(inline)
		at := typeHeadFixedBytes + typeL0DirBytes*nL0
		for pid, v := range inline {
			binary.LittleEndian.PutUint32(slot[at:], pid)
			binary.LittleEndian.PutUint64(slot[at+8:], v)
			at += labelEntryBytes
		}
	}
	binary.LittleEndian.PutUint32(slot[0:], magicHead)
	binary.LittleEndian.PutUint16(slot[4:], storeFormat)
	binary.LittleEndian.PutUint16(slot[6:], headType)
	binary.LittleEndian.PutUint64(slot[8:], l.gen)
	binary.LittleEndian.PutUint32(slot[24:], uint32(used))
	binary.LittleEndian.PutUint32(slot[52:], l.inc)
	binary.LittleEndian.PutUint32(slot[64:], mSeg)
	binary.LittleEndian.PutUint64(slot[72:], mEnd)
	binary.LittleEndian.PutUint16(slot[92:], uint16(nL0))
	binary.LittleEndian.PutUint16(slot[94:], uint16(nLabels))
	binary.LittleEndian.PutUint64(slot[112:], ckptOff)
	binary.LittleEndian.PutUint32(slot[120:], ckptSeg)
	binary.LittleEndian.PutUint32(slot[used-4:], crc32.Checksum(slot[:used-4], castagnoli))
	f, err := os.OpenFile(filepath.Join(l.dir, "h.fsh"), os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		l.t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteAt(slot, int64(l.gen%2)*headSlotBytes); err != nil {
		l.t.Fatal(err)
	}
}

// publish flushes seg (with a zero tail) and publishes a head naming its end.
func (l *synthLog) publish(seg, ckptSeg uint32, ckptOff uint64) {
	l.flush(seg, 4096)
	l.head(seg, uint64(len(l.segs[seg])), ckptSeg, ckptOff, nil)
}

// expire makes the next through read the head again.
func expire(tl *typeLabels) {
	tl.vmu.Lock()
	tl.readAt = time.Time{}
	tl.vmu.Unlock()
}

// expect checks want against tl after one fresh read (known true), or, with
// retry, after at most one read that finds the log changed under it.
func expect(t *testing.T, name string, tl *typeLabels, want map[uint32]uint64, retry bool) {
	t.Helper()
	for pid, v := range want {
		expire(tl)
		got, known, err := tl.through(pid)
		if retry && err == nil && !known {
			expire(tl)
			got, known, err = tl.through(pid)
		}
		if err != nil || !known || got != v {
			t.Fatalf("%s: pid %d: labeled_through %d known %v err %v, want %d", name, pid, got, known, err, v)
		}
	}
}

func newLabels(dir string) *typeLabels {
	return &typeLabels{dir: dir, labels: map[uint32]uint64{}}
}

// Up to 128 partitions the table is inline in the type head; past that it is
// a fold of the type log from the checkpoint. One reader follows the type
// through the switch, rotations, a moved checkpoint, retired segments and a
// new incarnation, and always agrees with a reader that starts afresh.
func TestTypeLabelsFollowTheInlineTableAndTheTypeLog(t *testing.T) {
	l := newSynthLog(t)
	inc := newLabels(l.dir)
	defer inc.close()
	check := func(name string, retry bool) {
		t.Helper()
		expect(t, name, inc, l.truth, retry)
		fresh := newLabels(l.dir)
		defer fresh.close()
		expect(t, name+" (fresh)", fresh, l.truth, false)
	}

	// Inline: 128 partitions in the head.
	inline := map[uint32]uint64{}
	for pid := uint32(1); pid <= 128; pid++ {
		inline[pid] = uint64(pid) * 3
	}
	maps.Copy(l.truth, inline)
	l.head(0, 0, 0, 0, inline)
	check("inline", false)

	// The 129th partition: labels move to the log (a FULL checkpoint, then
	// deltas); a zero-filled tail past mEnd is never read.
	ck := l.batch(0, map[uint32]uint64{129: 7}, true, 64)
	for i := 0; i < 50; i++ {
		l.batch(0, map[uint32]uint64{uint32(i%129 + 1): uint64(1000 + i)}, false, 200)
	}
	l.publish(0, 0, ck)
	check("log, one segment", false)

	// More deltas: the reader continues from where it stopped.
	for i := 0; i < 30; i++ {
		l.batch(0, map[uint32]uint64{uint32(i%7 + 1): uint64(5000 + i)}, false, 100)
	}
	l.publish(0, 0, ck)
	check("log, continued", false)

	// A batch written past the published mEnd is not read.
	published := maps.Clone(l.truth)
	end := uint64(len(l.segs[0]))
	l.batch(0, map[uint32]uint64{3: 999999}, false, 0)
	l.flush(0, 4096)
	l.head(0, end, 0, ck, nil)
	l.segs[0], l.truth = l.segs[0][:end], published
	check("unpublished tail ignored", false)

	// Rotation: segment 0 sealed at its last batch, deltas in segment 1, the
	// checkpoint still in segment 0.
	l.flush(0, 0)
	l.batch(1, map[uint32]uint64{2: 6000}, false, 0)
	l.publish(1, 0, ck)
	check("rotated, checkpoint in segment 0", false)

	// The checkpoint moves to segment 1 and segment 0 is retired.
	ck = l.batch(1, map[uint32]uint64{9: 7777}, true, 0)
	for i := 0; i < 10; i++ {
		l.batch(1, map[uint32]uint64{uint32(20 + i): uint64(8000 + i)}, false, 50)
	}
	l.publish(1, 1, ck)
	l.retire(0)
	check("checkpoint moved, segment 0 retired", false)

	// Two rotations while the reader is idle, then the segments it was
	// reading are retired: its next read finds the log gone under it and
	// starts again at the checkpoint.
	l.batch(1, map[uint32]uint64{40: 9000}, false, 0)
	l.flush(1, 0)
	l.batch(2, map[uint32]uint64{41: 9100}, true, 0)
	l.flush(2, 0)
	ck = l.batch(3, map[uint32]uint64{42: 9200}, true, 0)
	l.batch(3, map[uint32]uint64{43: 9300}, false, 0)
	l.publish(3, 3, ck)
	l.retire(1)
	l.retire(2)
	check("segments retired under the reader", true)

	// A new incarnation rewrites the log past its last head: fold again.
	l.inc++
	l.segs[3] = l.segs[3][:ck]
	l.truth = map[uint32]uint64{}
	for pid := uint32(1); pid <= 129; pid++ {
		l.truth[pid] = uint64(pid)
	}
	ck = l.batch(3, nil, true, 0)
	l.batch(3, map[uint32]uint64{5: 12345}, false, 0)
	l.publish(3, 3, ck)
	check("new incarnation", false)
}

// Labels that cannot be read are unknown (the wait polls again), never 0 as
// if known, and never a value the log does not hold.
func TestTypeLabelsUnknownWhenUnreadable(t *testing.T) {
	l := newSynthLog(t)
	tl := newLabels(l.dir)
	defer tl.close()
	if _, known, err := tl.through(1); err != nil || known {
		t.Fatalf("no head: known %v err %v", known, err)
	}
	// A head naming the log before any checkpoint, its segment 0 absent.
	l.batch(3, map[uint32]uint64{1: 5}, false, 0)
	l.publish(3, 0, 0)
	expire(tl)
	if _, known, err := tl.through(1); err != nil || known {
		t.Fatalf("checkpoint (0,0) with segment 0 absent: known %v err %v", known, err)
	}
	// An inline table longer than its head slot.
	l.head(0, 0, 0, 0, map[uint32]uint64{1: 1})
	b, err := os.ReadFile(filepath.Join(l.dir, "h.fsh"))
	if err != nil {
		t.Fatal(err)
	}
	slot := b[(l.gen%2)*headSlotBytes:]
	binary.LittleEndian.PutUint16(slot[94:], 200)
	used := binary.LittleEndian.Uint32(slot[24:])
	binary.LittleEndian.PutUint32(slot[used-4:], crc32.Checksum(slot[:used-4], castagnoli))
	if err := os.WriteFile(filepath.Join(l.dir, "h.fsh"), b, 0o644); err != nil {
		t.Fatal(err)
	}
	expire(tl)
	if _, known, err := tl.through(1); err != nil || known {
		t.Fatalf("inline table past the slot: known %v err %v", known, err)
	}
	// A batch that does not parse.
	l2 := newSynthLog(t)
	ck := l2.batch(0, map[uint32]uint64{1: 5}, true, 0)
	l2.batch(0, map[uint32]uint64{1: 6}, false, 0)
	binary.LittleEndian.PutUint32(l2.segs[0][len(l2.segs[0])-typeBatchHeaderBytes-labelEntryBytes-8:], 0xdeadbeef)
	l2.publish(0, 0, ck)
	tl2 := newLabels(l2.dir)
	defer tl2.close()
	if lt, known, err := tl2.through(1); err != nil || known {
		t.Fatalf("torn batch: labeled_through %d known %v err %v", lt, known, err)
	}
}

// A waiter never queues behind another waiter's read or fold (a fold from the
// checkpoint can replay a 64 MiB segment): it takes the table as it stands,
// which only under-reports, and polls again.
func TestTypeLabelsWaiterDoesNotQueueBehindAFold(t *testing.T) {
	l := newSynthLog(t)
	ck := l.batch(0, map[uint32]uint64{1: 10, 2: 20}, true, 0)
	l.publish(0, 0, ck)
	tl := newLabels(l.dir)
	defer tl.close()
	expect(t, "before", tl, l.truth, false)
	l.batch(0, map[uint32]uint64{1: 11}, false, 0)
	l.publish(0, 0, ck)
	expire(tl)
	tl.mu.Lock() // another waiter is folding
	done := make(chan struct{})
	var lt uint64
	var known bool
	var err error
	go func() {
		lt, known, err = tl.through(1)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		tl.mu.Unlock()
		t.Fatal("a label read queued behind the fold lock")
	}
	tl.mu.Unlock()
	if err != nil || !known || lt != 10 {
		t.Fatalf("during the fold: labeled_through %d known %v err %v, want the published 10", lt, known, err)
	}
	expect(t, "after", tl, map[uint32]uint64{1: 11}, false)
}

// After Close nothing reopens a head or a log segment: reads say ErrStopped.
func TestHeadReaderReadsNothingAfterClose(t *testing.T) {
	root := t.TempDir()
	fid := [4]byte{'O', 'M', 'M', ' '}
	l := newSynthLog(t)
	l.dir = filepath.Join(root, Dir, "t", fmt.Sprintf("%x", fid[:]))
	if err := os.MkdirAll(l.dir, 0o755); err != nil {
		t.Fatal(err)
	}
	ck := l.batch(0, map[uint32]uint64{1: 10}, true, 0)
	l.publish(0, 0, ck)
	h := NewHeadReader(root)
	if lt, known, err := h.labeledThrough(fid, 1); err != nil || !known || lt != 10 {
		t.Fatalf("labeled_through %d known %v err %v", lt, known, err)
	}
	tl := h.labels[fid]
	h.Close()
	if tl.head != nil || tl.log != nil {
		t.Fatal("Close left a type head or log segment open")
	}
	if _, _, err := h.labeledThrough(fid, 1); !errors.Is(err, ErrStopped) {
		t.Fatalf("labeledThrough after Close: %v, want ErrStopped", err)
	}
	if h.labels != nil {
		t.Fatal("a read after Close rebuilt the label readers")
	}
	expire(tl)
	if _, _, err := tl.through(1); !errors.Is(err, ErrStopped) || tl.head != nil {
		t.Fatalf("a closed type reader read again: err %v, head open %v", err, tl.head != nil)
	}
	if _, err := h.Partitions(); !errors.Is(err, ErrStopped) {
		t.Fatalf("Partitions after Close: %v, want ErrStopped", err)
	}
	if _, err := h.Types(); !errors.Is(err, ErrStopped) || h.fds.len() != 0 || h.typeHeads != nil || h.regHead != nil {
		t.Fatalf("Types after Close: %v (descriptors reopened: %d heads, types %v, registry %v), want ErrStopped",
			err, h.fds.len(), h.typeHeads != nil, h.regHead != nil)
	}
	if _, err := h.DiskBytes(); !errors.Is(err, ErrStopped) {
		t.Fatalf("DiskBytes after Close: %v, want ErrStopped", err)
	}
}

// synthStore writes a store's registry and partition heads as the engine
// lays them out (flatsql ps/format.h RegistryHeadFixed, PartitionHeadFixed,
// the registry frames of open.cpp).
type synthStore struct {
	t      *testing.T
	root   string // engine root: <root>/fsql2
	gen    uint64
	inc    uint32
	frames []byte
	nFrame uint64
	maxPid uint32
	heads  map[uint32]uint64 // pid -> head generation
}

func newSynthStore(t *testing.T) *synthStore {
	s := &synthStore{t: t, root: t.TempDir(), inc: 7, heads: map[uint32]uint64{}}
	if err := os.MkdirAll(filepath.Join(s.root, Dir), 0o755); err != nil {
		t.Fatal(err)
	}
	return s
}

func (s *synthStore) frame(kind uint16, payload []byte) {
	body := binary.LittleEndian.AppendUint16(nil, kind)
	body = append(body, payload...)
	f := binary.LittleEndian.AppendUint32(nil, uint32(len(body)))
	f = binary.LittleEndian.AppendUint32(f, crc32.Checksum(body, castagnoli))
	s.frames = append(s.frames, append(f, body...)...)
	s.nFrame++
}

func str16(b []byte, v string) []byte {
	return append(binary.LittleEndian.AppendUint16(b, uint16(len(v))), v...)
}

func (s *synthStore) addType(fid, schema string) {
	s.frame(regTypeAdd, str16([]byte(fid), schema))
}

func (s *synthStore) addPartition(pid uint32, fid, token string) {
	p := binary.LittleEndian.AppendUint32(nil, pid)
	p = append(p, fid...)
	p = str16(p, token)
	p = str16(p, fmt.Sprintf("%s_%08x", fid, pid))
	s.frame(regPartitionAdd, p)
	s.maxPid = max(s.maxPid, pid)
}

func (s *synthStore) drop(pid uint32) {
	s.frame(regDrop, binary.LittleEndian.AppendUint32(nil, pid))
}

// publish writes registry.fsl and a registry head naming every frame.
func (s *synthStore) publish() {
	dir := filepath.Join(s.root, Dir)
	if err := os.WriteFile(filepath.Join(dir, "registry.fsl"), s.frames, 0o644); err != nil {
		s.t.Fatal(err)
	}
	s.gen++
	slot := make([]byte, headSlotBytes)
	const used = 64 + 4
	binary.LittleEndian.PutUint32(slot[0:], magicHead)
	binary.LittleEndian.PutUint16(slot[4:], storeFormat)
	binary.LittleEndian.PutUint16(slot[6:], headRegistry)
	binary.LittleEndian.PutUint64(slot[8:], s.gen)
	binary.LittleEndian.PutUint32(slot[24:], used)
	binary.LittleEndian.PutUint64(slot[32:], s.nFrame)
	binary.LittleEndian.PutUint64(slot[40:], uint64(len(s.frames)))
	binary.LittleEndian.PutUint32(slot[48:], s.maxPid)
	binary.LittleEndian.PutUint32(slot[52:], s.inc)
	binary.LittleEndian.PutUint32(slot[used-4:], crc32.Checksum(slot[:used-4], castagnoli))
	s.writeSlot(filepath.Join(dir, "registry.fsh"), s.gen, slot)
}

func (s *synthStore) writeSlot(path string, gen uint64, slot []byte) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		s.t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteAt(slot, int64(gen%2)*headSlotBytes); err != nil {
		s.t.Fatal(err)
	}
}

// head publishes a partition head with the counters c (commit_seq, pseq_hi,
// total, total bytes, live, live bytes, tombs, disk bytes, latest arrival).
func (s *synthStore) head(pid uint32, c [9]int64) {
	dir := filepath.Join(s.root, Dir, "p", fmt.Sprintf("%08x", pid))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		s.t.Fatal(err)
	}
	s.heads[pid]++
	gen := s.heads[pid]
	slot := make([]byte, headSlotBytes)
	const used = 288 + 4
	binary.LittleEndian.PutUint32(slot[0:], magicHead)
	binary.LittleEndian.PutUint16(slot[4:], storeFormat)
	binary.LittleEndian.PutUint16(slot[6:], headPartition)
	binary.LittleEndian.PutUint64(slot[8:], gen)
	binary.LittleEndian.PutUint32(slot[20:], pid)
	binary.LittleEndian.PutUint32(slot[24:], used)
	for i, off := range []int{32, 40, 120, 128, 136, 144, 152, 160, 184} {
		binary.LittleEndian.PutUint64(slot[off:], uint64(c[i]))
	}
	binary.LittleEndian.PutUint32(slot[used-4:], crc32.Checksum(slot[:used-4], castagnoli))
	s.writeSlot(filepath.Join(dir, "h.fsh"), gen, slot)
}

// clobber overwrites a partition head with zeros (a reader that reads it
// again sees no valid slot).
func (s *synthStore) clobber(pid uint32) {
	path := filepath.Join(s.root, Dir, "p", fmt.Sprintf("%08x", pid), "h.fsh")
	if err := os.WriteFile(path, make([]byte, 2*headSlotBytes), 0o644); err != nil {
		s.t.Fatal(err)
	}
}

func partCounters(pid uint32, seq int64) [9]int64 {
	n := int64(pid)
	return [9]int64{seq, 10 * seq, n + seq, 1000*n + seq, n, 900 * n, seq, 4096*n + seq, 1_700_000_000_000 + n}
}

// openFDs counts the process's open descriptors (-1 where it cannot). Names
// only: stat of a /dev/fd entry fails on macOS.
func openFDs() int {
	for _, dir := range []string{"/proc/self/fd", "/dev/fd"} {
		d, err := os.Open(dir)
		if err != nil {
			continue
		}
		names, err := d.Readdirnames(-1)
		d.Close()
		if err == nil {
			return len(names)
		}
	}
	return -1
}

// B8 (terabyte audit): a counter read reads only the heads that moved, keeps
// at most maxHeadFDs head descriptors open, and serves the totals per type,
// per producer and for the store from what it read. On 58a742f44 every
// counter read read every head under one mutex and kept one descriptor per
// head for good: the clobbered heads below read back as zero counters, and
// the 300 partitions held 300 descriptors.
func TestCounterReadsReadOnlyTheHeadsThatMoved(t *testing.T) {
	const parts, fdCap = 300, 64 // the descriptor cap scaled down with the store
	st := newSynthStore(t)
	types := []string{"OMM ", "CAT ", "IQC "}
	for i, fid := range types {
		st.addType(fid, []string{"OMM.fbs", "CAT.fbs", "IQC.fbs"}[i])
	}
	want := map[uint32][9]int64{}
	token := map[uint32]string{}
	for pid := uint32(1); pid <= parts; pid++ {
		token[pid] = fmt.Sprintf("peer%02d", pid%50)
		st.addPartition(pid, types[pid%3], token[pid])
		want[pid] = partCounters(pid, 1)
		st.head(pid, want[pid])
	}
	st.publish()
	sums := func() (total CounterTotals, byType map[string]CounterTotals, byProd map[string]int64) {
		byType, byProd = map[string]CounterTotals{}, map[string]int64{}
		for pid, c := range want {
			typ := typeName([]string{"OMM.fbs", "CAT.fbs", "IQC.fbs"}[pid%3])
			pc := PartitionCounter{CommitSeq: c[0], Total: c[2], TotalBytes: c[3], Live: c[4], LiveBytes: c[5], Tombs: c[6], DiskBytes: c[7]}
			total.add(&pc, 1)
			tt := byType[typ]
			tt.add(&pc, 1)
			tt.LatestArrival = max(tt.LatestArrival, c[8])
			byType[typ] = tt
			byProd[token[pid]] += c[5]
		}
		return
	}
	check := func(label string, h *HeadReader) {
		t.Helper()
		wantTotal, wantTypes, wantProd := sums()
		got, err := h.Totals()
		if err != nil || got != wantTotal {
			t.Fatalf("%s: store totals %+v (%v), want %+v", label, got, err, wantTotal)
		}
		gotTypes, err := h.TypeTotals()
		if err != nil || len(gotTypes) != len(wantTypes) {
			t.Fatalf("%s: type totals %v (%v)", label, gotTypes, err)
		}
		for typ, w := range wantTypes {
			if gotTypes[typ] != w {
				t.Fatalf("%s: %s totals %+v, want %+v", label, typ, gotTypes[typ], w)
			}
		}
		for token, w := range wantProd {
			if p, err := h.ProducerTotals(token); err != nil || p.LiveBytes != w {
				t.Fatalf("%s: producer %s live bytes %d (%v), want %d", label, token, p.LiveBytes, err, w)
			}
		}
		list, err := h.Partitions()
		if err != nil || len(list) != len(want) {
			t.Fatalf("%s: %d partitions (%v), want %d", label, len(list), err, len(want))
		}
		for _, p := range list {
			c := want[uint32(p.PID)]
			if p.CommitSeq != c[0] || p.PseqHi != c[1] || p.Live != c[4] || p.DiskBytes != c[7] || p.LatestArrival != c[8] {
				t.Fatalf("%s: partition %d counters %+v, want %v", label, p.PID, p, c)
			}
		}
	}

	fdsBefore := openFDs()
	h := NewHeadReader(st.root)
	h.sweepEvery = 0 // the sweep is driven by hand below
	h.fds.max = fdCap
	h.dirEvery = 0
	defer h.Close()
	check("first read", h)
	if n := h.HeadReads(); n != parts {
		t.Fatalf("the first counter read read %d heads, want each of the %d once", n, parts)
	}
	if n := h.OpenHeadFDs(); n > fdCap {
		t.Fatalf("%d head descriptors open, want at most %d", n, fdCap)
	}
	if fdsBefore >= 0 {
		if grew := openFDs() - fdsBefore; grew > fdCap+8 {
			t.Fatalf("the process holds %d more descriptors after reading %d heads, want at most %d", grew, parts, fdCap+8)
		}
	}

	// Nothing moved: no head is read again, whatever the files say now.
	for pid := uint32(1); pid <= parts; pid++ {
		st.clobber(pid)
	}
	reads := h.HeadReads()
	check("nothing moved", h)
	if n := h.HeadReads() - reads; n != 0 {
		t.Fatalf("counter reads with nothing moved read %d heads", n)
	}

	// Three writes acked: exactly their heads are read again.
	for _, pid := range []uint32{7, 70, 140} {
		want[pid] = partCounters(pid, 5)
		st.head(pid, want[pid])
		h.Touch(pid)
	}
	reads = h.HeadReads()
	check("three acked", h)
	if n := h.HeadReads() - reads; n != 3 {
		t.Fatalf("after three acks the counter reads read %d heads, want 3", n)
	}

	// An engine-side commit (no ack): the sweep finds it within one pass.
	for pid := uint32(1); pid <= parts; pid++ {
		st.head(pid, want[pid]) // valid heads again, same counters
	}
	want[190] = partCounters(190, 9)
	st.head(190, want[190])
	h.sweepEvery, h.sweepFull = time.Second, time.Second // one step covers every partition
	h.sweepStep()
	check("after the sweep", h)

	// The registry adds a partition and drops another.
	token[parts+1] = "peer99"
	st.addPartition(parts+1, "CAT ", token[parts+1])
	want[parts+1] = partCounters(parts+1, 2)
	st.head(parts+1, want[parts+1])
	st.drop(3)
	delete(want, 3)
	st.publish()
	check("registry moved", h)

	// Disk bytes count the type directories and the registry too.
	tdir := filepath.Join(st.root, Dir, "t", hex.EncodeToString([]byte("OMM ")))
	if err := os.MkdirAll(tdir, 0o755); err != nil {
		t.Fatal(err)
	}
	tl := newSynthLog(t) // a type head, so the directory has a commit_seq
	tl.dir = tdir
	tl.head(0, 0, 0, 0, map[uint32]uint64{})
	for name, n := range map[string]int{"m-000000.fsl": 12345, "g-000000.fsg": 777, "x-000001.fsx": 4096} {
		if err := os.WriteFile(filepath.Join(tdir, name), make([]byte, n), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	var reg int64
	for _, name := range []string{"registry.fsl", "registry.fsh"} {
		fi, err := os.Stat(filepath.Join(st.root, Dir, name))
		if err != nil {
			t.Fatal(err)
		}
		reg += fi.Size()
	}
	total, _, _ := sums()
	typeDir := int64(12345 + 777 + 4096 + 2*headSlotBytes)
	h.sweepStep() // the sweep sums the type directories; no counter read scans one
	if got, err := h.DiskBytes(); err != nil || got != total.DiskBytes+typeDir+reg {
		t.Fatalf("disk bytes %d (%v), want %d partition + %d type directory + %d registry", got, err, total.DiskBytes, typeDir, reg)
	}
}

// B8 with the engine's commit_seq words (flatsql_ps_ring_words): each sweep
// reads exactly the heads whose published commit_seq moved, and a partition
// whose word cannot be read.
func TestSweepReadsOnlyTheHeadsWhoseCommitSeqMoved(t *testing.T) {
	const parts = 60
	st := newSynthStore(t)
	st.addType("OMM ", "OMM.fbs")
	published := map[uint32]uint64{}
	for pid := uint32(1); pid <= parts; pid++ {
		st.addPartition(pid, "OMM ", fmt.Sprintf("peer%02d", pid))
		st.head(pid, partCounters(pid, 1))
		published[pid] = 1
	}
	st.publish()
	h := NewHeadReader(st.root)
	h.sweepEvery = 0
	defer h.Close()
	var unreadable uint32
	h.words = func(pid uint32) (uint64, uint64, bool, error) {
		if pid == unreadable {
			return 0, 0, false, nil
		}
		return published[pid], 0, true, nil
	}
	if _, err := h.Partitions(); err != nil {
		t.Fatal(err)
	}
	sweep := func(label string, want int, check func()) {
		t.Helper()
		reads := h.HeadReads()
		h.sweepStep()
		if n := int(h.HeadReads() - reads); n != want {
			t.Fatalf("%s: the sweep read %d heads, want %d", label, n, want)
		}
		if check != nil {
			check()
		}
	}
	sweep("nothing moved", 0, nil)
	for _, pid := range []uint32{5, 17, 42} {
		st.head(pid, partCounters(pid, 3))
		published[pid] = 3
	}
	sweep("three moved", 3, func() {
		list, err := h.Partitions()
		if err != nil {
			t.Fatal(err)
		}
		for _, p := range list {
			want := int64(1)
			if p.PID == 5 || p.PID == 17 || p.PID == 42 {
				want = 3
			}
			if p.CommitSeq != want {
				t.Fatalf("partition %d commit_seq %d, want %d", p.PID, p.CommitSeq, want)
			}
		}
	})
	sweep("settled", 0, nil)
	unreadable = 9
	sweep("one word unreadable", 1, nil)
}

// The published counters under a running sweep, concurrent counter reads,
// acked writes and Close (run under -race in CI): every read answers or says
// ErrStopped, and the totals equal the heads once the writes stop.
func TestCountersUnderAConcurrentSweep(t *testing.T) {
	const parts = 40
	st := newSynthStore(t)
	st.addType("OMM ", "OMM.fbs")
	for pid := uint32(1); pid <= parts; pid++ {
		st.addPartition(pid, "OMM ", fmt.Sprintf("peer%02d", pid%4))
		st.head(pid, partCounters(pid, 1))
	}
	st.publish()
	h := NewHeadReader(st.root)
	h.sweepEvery, h.sweepFull, h.dirEvery = 5*time.Millisecond, 5*time.Millisecond, 0
	var wg sync.WaitGroup
	stop := make(chan struct{})
	for r := 0; r < 4; r++ {
		wg.Add(1)
		go func(r int) {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				var err error
				switch r {
				case 0:
					_, err = h.Totals()
				case 1:
					_, err = h.Partitions()
				case 2:
					_, err = h.ProducerTotals("peer01")
				default:
					_, err = h.DiskBytes()
				}
				if err != nil && !errors.Is(err, ErrStopped) {
					t.Error(err)
					return
				}
			}
		}(r)
	}
	final := map[uint32]int64{}
	for seq := int64(2); seq < 30; seq++ {
		pid := uint32(seq%parts) + 1
		st.head(pid, partCounters(pid, seq))
		final[pid] = seq
		h.Touch(pid)
		time.Sleep(time.Millisecond)
	}
	close(stop)
	wg.Wait()
	list, err := h.Partitions()
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range list {
		want := final[uint32(p.PID)]
		if want == 0 {
			want = 1
		}
		if p.CommitSeq != want {
			t.Fatalf("partition %d commit_seq %d, want %d", p.PID, p.CommitSeq, want)
		}
	}
	h.Close()
	if _, err := h.Totals(); !errors.Is(err, ErrStopped) {
		t.Fatalf("Totals after Close: %v", err)
	}
}

// synthParts publishes n partitions of one type with partCounters(pid, 1)
// and returns their store.
func synthParts(t *testing.T, n int) *synthStore {
	st := newSynthStore(t)
	st.addType("OMM ", "OMM.fbs")
	for pid := uint32(1); pid <= uint32(n); pid++ {
		st.addPartition(pid, "OMM ", fmt.Sprintf("peer%03d", pid))
		st.head(pid, partCounters(pid, 1))
	}
	st.publish()
	return st
}

// wantLive is Σ live of partCounters(pid, 1) over pids 1..n.
func wantLive(n int) int64 {
	var live int64
	for pid := 1; pid <= n; pid++ {
		live += partCounters(uint32(pid), 1)[4]
	}
	return live
}

// Review of B8: a counter read never waits on the sweep or on a type
// directory sum. Both used to run under the reader's one mutex (the sweep's
// chunks of 256 heads, and a ReadDir and a stat per file of every moved type
// directory), so every counter read, TypeOf and GetRecord miss queued
// behind them. Here the sweep's and the directory sum's locks are held for
// the whole check, as a long pass would hold them.
func TestCounterReadsNeverWaitOnTheSweepOrADirectorySum(t *testing.T) {
	const parts = 50
	st := synthParts(t, parts)
	h := NewHeadReader(st.root)
	h.sweepEvery = 0
	defer h.Close()
	if _, err := h.DiskBytes(); err != nil { // the first sum
		t.Fatal(err)
	}
	h.swMu.Lock()
	h.dirMu.Lock()
	done := make(chan error, 1)
	go func() {
		st.head(9, partCounters(9, 4))
		h.Touch(9)
		tot, err := h.Totals()
		if err == nil && tot.Live != wantLive(parts) {
			err = fmt.Errorf("live %d, want %d", tot.Live, wantLive(parts))
		}
		if err == nil {
			var pc PartitionCounter
			if pc, _, err = h.Partition(9); err == nil && pc.CommitSeq != 4 {
				err = fmt.Errorf("partition 9 commit_seq %d after its ack, want 4", pc.CommitSeq)
			}
		}
		if err == nil {
			_, err = h.DiskBytes()
		}
		if err == nil {
			_, err = h.ProducerTotalsFresh("peer009")
		}
		if err == nil {
			_, _, err = h.TypeOf("OMM")
		}
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("a counter read waited on the sweep or a directory sum")
	}
	h.dirMu.Unlock()
	h.swMu.Unlock()
}

// Review of B8: the sweep reads heads without the descriptor cache, so a
// pass over more partitions than the cache holds leaves the written
// partitions' descriptors in place (it used to push every one of them out).
func TestSweepKeepsTheWrittenPartitionsDescriptors(t *testing.T) {
	const parts, fdCap = 120, 16
	st := synthParts(t, parts)
	h := NewHeadReader(st.root)
	h.sweepEvery, h.sweepFull = 0, 0
	h.fds.max = fdCap
	defer h.Close()
	if _, err := h.Totals(); err != nil {
		t.Fatal(err)
	}
	written := []uint32{3, 30, 60, 90, 119}
	for _, pid := range written {
		st.head(pid, partCounters(pid, 2))
		h.Touch(pid)
	}
	if _, err := h.Totals(); err != nil {
		t.Fatal(err)
	}
	h.sweepStep()
	h.sweepStep()
	if n := h.OpenHeadFDs(); n != len(written) {
		t.Fatalf("%d head descriptors open after two sweeps over %d partitions, want the %d written ones", n, parts, len(written))
	}
	h.mu.Lock()
	for _, pid := range written {
		if _, ok := h.fds.m[h.partHeadPath(pid)]; !ok {
			h.mu.Unlock()
			t.Fatalf("the sweep evicted written partition %d's descriptor", pid)
		}
	}
	h.mu.Unlock()
}

// Review of B8: the first counter read loads every head in chunks of
// sweepChunk per hold of the mutex, and concurrent first readers share the
// load: every head is read exactly once and every reader gets the whole
// totals. It used to read every head in one hold (441 ms at 10k partitions,
// during which TypeOf and GetRecord misses waited).
func TestFirstReadersShareTheChunkedLoad(t *testing.T) {
	const parts = 4*sweepChunk + 17
	st := synthParts(t, parts)
	h := NewHeadReader(st.root)
	h.sweepEvery = 0
	defer h.Close()
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for r := 0; r < 8; r++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			tot, err := h.Totals()
			if err == nil && (tot.Partitions != parts || tot.Live != wantLive(parts)) {
				err = fmt.Errorf("totals %+v, want %d partitions and %d live", tot, parts, wantLive(parts))
			}
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if n := h.HeadReads(); n != parts {
		t.Fatalf("8 concurrent first reads read %d heads, want each of the %d once", n, parts)
	}
}

// Review of B8: Close is idempotent once the sweep runs (a second Close
// closed the sweep's stop channel again and panicked).
func TestHeadReaderCloseTwice(t *testing.T) {
	st := synthParts(t, 3)
	h := NewHeadReader(st.root)
	h.sweepEvery = time.Millisecond
	if _, err := h.Totals(); err != nil {
		t.Fatal(err)
	}
	h.Close()
	h.Close()
	if _, err := h.Totals(); !errors.Is(err, ErrStopped) {
		t.Fatalf("Totals after Close: %v", err)
	}
}

// A read that must not lag reads its own heads now: ProducerTotalsFresh sees
// an engine-side commit (an eviction's kills: no ack, no mark) at once,
// where the published totals wait for the sweep.
func TestProducerTotalsFreshSeesAnUnmarkedCommit(t *testing.T) {
	st := synthParts(t, 20)
	h := NewHeadReader(st.root)
	h.sweepEvery = 0
	defer h.Close()
	before, err := h.ProducerTotals("peer007")
	if err != nil {
		t.Fatal(err)
	}
	c := partCounters(7, 3)
	c[5] = 11 // live bytes after the eviction
	st.head(7, c)
	if got, err := h.ProducerTotals("peer007"); err != nil || got.LiveBytes != before.LiveBytes {
		t.Fatalf("published producer totals moved without a mark or a sweep: %d (%v)", got.LiveBytes, err)
	}
	got, err := h.ProducerTotalsFresh("peer007")
	if err != nil || got.LiveBytes != 11 {
		t.Fatalf("fresh producer live bytes %d (%v), want 11", got.LiveBytes, err)
	}
	if tot, err := h.Totals(); err != nil || tot.LiveBytes != wantLiveBytes(20)-before.LiveBytes+11 {
		t.Fatalf("store live bytes %d (%v) after the fresh read, want %d", tot.LiveBytes, err, wantLiveBytes(20)-before.LiveBytes+11)
	}
}

func wantLiveBytes(n int) int64 {
	var b int64
	for pid := 1; pid <= n; pid++ {
		b += partCounters(uint32(pid), 1)[5]
	}
	return b
}
