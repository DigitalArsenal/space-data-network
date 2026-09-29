package format2

// The labeled_through reader on type heads and type logs written here as the
// engine lays them out (flatsql ps/format.h TypeHeadFixed, LabelEntry,
// TypeBatchHeader; type_owner.cpp, reclaim.cpp typeRotateMeta and
// typeRetireMeta). No engine: these run in the quick CI jobs.

import (
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"maps"
	"os"
	"path/filepath"
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
	if _, err := h.Types(); !errors.Is(err, ErrStopped) || h.files != nil {
		t.Fatalf("Types after Close: %v (files reopened %v), want ErrStopped", err, h.files != nil)
	}
}
