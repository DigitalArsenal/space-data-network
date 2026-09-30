package format2

// Terabyte audit (B8, M7) on the engine: counter reads read only the heads
// that moved and count the type logs on disk; a GetRecord miss and CopiesOf
// ask only the partitions that can hold a copy the catalog does not show.

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// glueStore opens a store with producers of OMM, two records each, every one
// labeled. The background sweep is off: the test counts head reads.
func glueStore(t *testing.T, producers int) (*Store, []string) {
	t.Helper()
	s := openTestStore(t, t.TempDir())
	s.heads.sweepEvery = 0
	base := time.Date(2026, 9, 2, 0, 0, 0, 0, time.UTC)
	tags := &Tags{ProviderID: "space-data-network-02", SourceName: "tb-glue", BatchID: "b1"}
	var cids []string
	for i := 0; i < producers; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		peer := fmt.Sprintf("source:tb-glue-%03d", i)
		res, err := s.PutBatch(ctx, "OMM.fbs", ommPuts(2, 2*i, base, "GLUE"), peer, nil, tags)
		if err == nil {
			err = s.WaitLabeled(ctx, "OMM.fbs", peer)
		}
		cancel()
		if err != nil {
			t.Fatalf("producer %d: %v", i, err)
		}
		for _, r := range res {
			if r.Err != nil {
				t.Fatalf("producer %d: %v", i, r.Err)
			}
			cids = append(cids, r.CID)
		}
	}
	return s, cids
}

// treeBytes sums the regular files under dir.
func treeBytes(t *testing.T, dir string) int64 {
	t.Helper()
	var n int64
	err := filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		if err != nil || !d.Type().IsRegular() {
			return err
		}
		fi, err := d.Info()
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		if err == nil {
			n += fi.Size()
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return n
}

// B8: PeerStorageBytes and LiveRecordBytes read the totals, re-reading only
// the head a write acked; DiskUsageBytes counts the type directories (type
// logs, arrivals, catalog runs), the registry and the journals, which no
// partition head counts. On 58a742f44 DiskUsageBytes was Σ partition
// disk_bytes alone.
func TestCountersReadTheHeadsThatMovedAndCountTheTypeLogs(t *testing.T) {
	s, _ := glueStore(t, 6)
	s.heads.dirEvery = 0
	ctx := context.Background()
	parts, err := s.Partitions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(parts) != 6 {
		t.Fatalf("%d partitions, want 6", len(parts))
	}
	spec, err := s.spec("OMM.fbs")
	if err != nil {
		t.Fatal(err)
	}
	peer := "source:tb-glue-003"
	p := s.w.Registered([]byte(peer), spec.FID)
	pc, ok, err := s.heads.Partition(p.PID)
	if err != nil || !ok {
		t.Fatalf("partition %d: %v %v", p.PID, ok, err)
	}
	before, err := s.PeerStorageBytes(ctx, pc.Producer)
	if err != nil || before != pc.LiveBytes || before <= 0 {
		t.Fatalf("PeerStorageBytes %d (%v), want the partition's %d", before, err, pc.LiveBytes)
	}
	lrb0, err := s.LiveRecordBytes(ctx)
	if err != nil {
		t.Fatal(err)
	}
	reads := s.heads.HeadReads()
	put := ommPuts(1, 900, time.Date(2026, 9, 3, 0, 0, 0, 0, time.UTC), "MORE")
	if _, err := s.PutBatch(ctx, "OMM.fbs", put, peer, nil, &Tags{ProviderID: "space-data-network-02", SourceName: "tb-glue", BatchID: "b2"}); err != nil {
		t.Fatal(err)
	}
	after, err := s.PeerStorageBytes(ctx, pc.Producer)
	if err != nil || after <= before {
		t.Fatalf("PeerStorageBytes after an acked write %d (%v), was %d", after, err, before)
	}
	lrb, err := s.LiveRecordBytes(ctx)
	if err != nil || lrb-lrb0 != after-before {
		t.Fatalf("LiveRecordBytes moved %d (%v), the producer's bytes %d", lrb-lrb0, err, after-before)
	}
	if n := s.heads.HeadReads() - reads; n != 1 {
		t.Fatalf("the counter reads after one acked write read %d heads, want 1 (of %d)", n, len(parts))
	}

	// The type directories are summed by the sweep, never by a counter read.
	s.heads.sweepEvery, s.heads.sweepFull = 20*time.Millisecond, 20*time.Millisecond
	root := s.heads.root
	var du, sum, typeDir, other int64
	deadline := time.Now().Add(30 * time.Second)
	for {
		typeDir = treeBytes(t, filepath.Join(root, "t"))
		other = treeBytes(t, filepath.Join(root, "j"))
		for _, name := range []string{"registry.fsl", "registry.fsh"} {
			if fi, err := os.Stat(filepath.Join(root, name)); err == nil {
				other += fi.Size()
			}
		}
		parts, err := s.Partitions(ctx)
		if err != nil {
			t.Fatal(err)
		}
		sum = 0
		for _, p := range parts {
			sum += p.DiskBytes
		}
		if du, err = s.DiskUsageBytes(ctx); err != nil {
			t.Fatal(err)
		}
		if typeDir > 0 && du-sum == typeDir+other && treeBytes(t, filepath.Join(root, "t")) == typeDir {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("DiskUsageBytes %d = Σ partition disk_bytes %d + %d; the type directories hold %d and the registry and journals %d",
				du, sum, du-sum, typeDir, other)
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Logf("DiskUsageBytes %d: partitions %d, type directories %d, registry and journals %d", du, sum, typeDir, other)
}

// M7: a GetRecord miss asks no partition labeled through its pseq_hi. On
// 58a742f44 every miss asked every partition of the type, one statement
// each: 130 here.
func TestGetRecordMissAsksOnlyUnlabeledPartitions(t *testing.T) {
	const producers = 130 // past the inline label table (the labels are in the type log)
	s, cids := glueStore(t, producers)
	ctx := context.Background()
	missing := CIDText(CIDBytes([]byte("never stored")))
	seeks := s.PartitionSeeks()
	start := time.Now()
	const misses = 20
	for i := 0; i < misses; i++ {
		if _, err := s.GetRecord(ctx, "OMM.fbs", missing); !errors.Is(err, ErrNotFound) {
			t.Fatalf("GetRecord of a CID never stored: %v, want ErrNotFound", err)
		}
	}
	perMiss := time.Since(start) / misses
	if n := s.PartitionSeeks() - seeks; n != 0 {
		t.Fatalf("%d misses over %d labeled partitions asked %d partitions", misses, producers, n)
	}
	for _, c := range cids[:10] {
		if r, err := s.GetRecord(ctx, "OMM.fbs", c); err != nil || r.CID != c {
			t.Fatalf("GetRecord %s: %v", c, err)
		}
	}
	// Read-your-writes at ack (A20): a record read before its label is
	// found in its partition.
	res, err := s.PutBatch(ctx, "OMM.fbs", ommPuts(1, 5000, time.Date(2026, 9, 4, 0, 0, 0, 0, time.UTC), "RYW"), "source:tb-glue-new", nil, nil)
	if err != nil || res[0].Err != nil {
		t.Fatalf("put: %v %v", err, res)
	}
	if r, err := s.GetRecord(ctx, "OMM.fbs", res[0].CID); err != nil || r.CID != res[0].CID {
		t.Fatalf("GetRecord at ack: %v", err)
	}
	t.Logf("GetRecord miss over %d partitions: %v each, %d partition statements (%s)", producers, perMiss.Round(time.Microsecond),
		s.PartitionSeeks()-seeks, machine())
}

// M7: while the type provably holds no REPEAT copy and nothing unlabeled,
// CopiesOf asks the catalog and then only the partition it names; once it
// may hold one, every partition. On 58a742f44 every call asked every
// partition (40 here).
func TestCopiesOfFindsEveryCopy(t *testing.T) {
	const producers = 40
	s, cids := glueStore(t, producers)
	ctx := context.Background()
	seeks := s.PartitionSeeks()
	copies, err := s.CopiesOf(ctx, "OMM.fbs", cids[7])
	if err != nil || len(copies) != 1 || copies[0].CID != cids[7] {
		t.Fatalf("CopiesOf a single-copy record: %d copies (%v)", len(copies), err)
	}
	if n := s.PartitionSeeks() - seeks; n != 1 {
		t.Fatalf("CopiesOf with no REPEAT copy and every partition labeled asked %d partitions, want the catalog's one", n)
	}
	// A second producer stores the same record: a REPEAT copy.
	data := ommPuts(2, 2*3, time.Date(2026, 9, 2, 0, 0, 0, 0, time.UTC), "GLUE")[1]
	if CIDText(CIDBytes(data.Data)) != cids[7] {
		t.Fatal("the fixture's seventh record moved")
	}
	res, err := s.PutBatch(ctx, "OMM.fbs", []Put{data}, "source:tb-glue-mirror", nil, &Tags{ProviderID: "mirror", SourceName: "m", BatchID: "m1"})
	if err != nil || res[0].Err != nil {
		t.Fatalf("mirror put: %v %v", err, res)
	}
	if err := s.WaitLabeled(ctx, "OMM.fbs", "source:tb-glue-mirror"); err != nil {
		t.Fatal(err)
	}
	copies, err = s.CopiesOf(ctx, "OMM.fbs", cids[7])
	if err != nil || len(copies) != 2 || copies[0].Producer == copies[1].Producer {
		t.Fatalf("CopiesOf a record two producers hold: %+v (%v)", copies, err)
	}
}

// Review of the M7 shortcut: a kill of a FIRST copy lowers its partition's
// live count at the partition commit, and the type's first-live count only
// when the type owner labels it. While any kill of the type is acked but not
// labeled, a labeled REPEAT copy must still be found. The first cut of the
// shortcut compared Σ live with first-live alone and missed the mirror's
// copy after every acked kill (78 of 78 trials in review).
func TestCopiesOfFindsTheRepeatCopyWhileAKillIsUnlabeled(t *testing.T) {
	const producers = 40
	s, cids := glueStore(t, producers)
	ctx := context.Background()
	data := ommPuts(2, 2*3, time.Date(2026, 9, 2, 0, 0, 0, 0, time.UTC), "GLUE")[1]
	if CIDText(CIDBytes(data.Data)) != cids[7] {
		t.Fatal("the fixture's seventh record moved")
	}
	res, err := s.PutBatch(ctx, "OMM.fbs", []Put{data}, "source:tb-glue-mirror", nil, &Tags{ProviderID: "mirror", SourceName: "m", BatchID: "m1"})
	if err != nil || res[0].Err != nil {
		t.Fatalf("mirror put: %v %v", err, res)
	}
	if err := s.WaitLabeled(ctx, "OMM.fbs", "source:tb-glue-mirror"); err != nil {
		t.Fatal(err)
	}
	misses, trials := 0, 0
	for i := 0; i < producers; i++ {
		if i == 3 {
			continue
		}
		for k := 0; k < 2; k++ {
			// A FIRST-only record of another producer killed, acked only
			// (router Delete), then the copies of the mirrored record.
			if err := s.Delete(ctx, "OMM.fbs", fmt.Sprintf("source:tb-glue-%03d", i), cids[2*i+k]); err != nil {
				t.Fatal(err)
			}
			got, err := s.CopiesOf(ctx, "OMM.fbs", cids[7])
			if err != nil {
				t.Fatal(err)
			}
			if trials++; len(got) != 2 {
				misses++
			}
		}
	}
	if misses > 0 {
		t.Fatalf("CopiesOf missed the mirror's REPEAT copy in %d of %d calls made right after an acked kill", misses, trials)
	}
}

// f2Delete's steps (format2_daemon_writes.go: CopiesOf, a TOMB_CID per
// producer holding a copy, one label wait) while another delete is acked but
// not labeled: the record must be gone afterwards, the mirror's copy too.
func TestDeleteKillsEveryCopyWhileAnotherKillIsUnlabeled(t *testing.T) {
	const producers = 8
	s, cids := glueStore(t, producers)
	ctx := context.Background()
	data := ommPuts(2, 2*3, time.Date(2026, 9, 2, 0, 0, 0, 0, time.UTC), "GLUE")[1]
	res, err := s.PutBatch(ctx, "OMM.fbs", []Put{data}, "source:tb-glue-mirror", nil, &Tags{ProviderID: "mirror", SourceName: "m", BatchID: "m1"})
	if err != nil || res[0].Err != nil {
		t.Fatalf("mirror put: %v %v", err, res)
	}
	if err := s.WaitLabeled(ctx, "OMM.fbs", "source:tb-glue-mirror"); err != nil {
		t.Fatal(err)
	}
	for round := 0; round < 3; round++ {
		victim, target := cids[2*round], cids[7]
		if round > 0 {
			// Mirror the next target first.
			d := ommPuts(2, 2*(3+round), time.Date(2026, 9, 2, 0, 0, 0, 0, time.UTC), "GLUE")[1]
			target = CIDText(CIDBytes(d.Data))
			if _, err := s.PutBatch(ctx, "OMM.fbs", []Put{d}, "source:tb-glue-mirror", nil, &Tags{ProviderID: "mirror", SourceName: "m", BatchID: "m1"}); err != nil {
				t.Fatal(err)
			}
			if err := s.WaitLabeled(ctx, "OMM.fbs", "source:tb-glue-mirror"); err != nil {
				t.Fatal(err)
			}
		}
		// Another request's delete, acked; its label wait still running.
		if err := s.Delete(ctx, "OMM.fbs", fmt.Sprintf("source:tb-glue-%03d", round), victim); err != nil {
			t.Fatal(err)
		}
		copies, err := s.CopiesOf(ctx, "OMM.fbs", target)
		if err != nil {
			t.Fatal(err)
		}
		var peers []string
		for _, c := range copies {
			if err := s.Delete(ctx, "OMM.fbs", c.Producer, target); err != nil {
				t.Fatal(err)
			}
			peers = append(peers, c.Producer)
		}
		if err := s.WaitLabeledUntil(ctx, "OMM.fbs", append(peers, fmt.Sprintf("source:tb-glue-%03d", round)), time.Now().Add(LabelWaitMax)); err != nil {
			t.Fatal(err)
		}
		if r, err := s.GetRecord(ctx, "OMM.fbs", target); r != nil || !errors.Is(err, ErrNotFound) {
			t.Fatalf("round %d: the record is still readable after its %d copies were killed (%v): copy of %v", round, len(copies), err, r)
		}
	}
}

// A partition whose head moved without a mark (a write whose wait for the
// ack was never seen) and whose labels are already past the pseq_hi the
// reader published is asked by a GetRecord miss, and its head read again:
// the next miss asks nothing.
func TestGetRecordMissAsksAPartitionWhoseHeadMovedUnseen(t *testing.T) {
	s, _ := glueStore(t, 4)
	ctx := context.Background()
	missing := CIDText(CIDBytes([]byte("never stored")))
	if _, err := s.GetRecord(ctx, "OMM.fbs", missing); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	// A record written through the partition's ring directly (as PutBatch
	// does, less the router's mark): its commit moves the head unseen.
	spec, err := s.spec("OMM.fbs")
	if err != nil {
		t.Fatal(err)
	}
	const peer = "source:tb-glue-001"
	p, err := s.w.Partition([]byte(peer), spec.FID)
	if err != nil {
		t.Fatal(err)
	}
	put := ommPuts(1, 700, time.Date(2026, 9, 5, 0, 0, 0, 0, time.UTC), "UNSEEN")[0]
	rseq, err := p.Enqueue(ctx, &Entry{Kind: EntRecord, Flags: FlagCidPresent | FlagAckWanted, ArrivalMs: time.Now().UnixMilli(),
		CID: CIDBytes(put.Data), Attr: BuildRecordAttr(RecordAttr{PeerID: []byte(peer)}), Frame: frame(put.Data)})
	if err == nil {
		err = p.WaitAck(ctx, rseq)
	}
	if err != nil {
		t.Fatalf("put: %v", err)
	}
	if err := s.WaitLabeled(ctx, "OMM.fbs", peer); err != nil {
		t.Fatal(err)
	}
	seeks := s.PartitionSeeks()
	if _, err := s.GetRecord(ctx, "OMM.fbs", missing); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	if n := s.PartitionSeeks() - seeks; n != 1 {
		t.Fatalf("a miss after an unseen commit asked %d partitions, want the one whose labels passed its published pseq_hi", n)
	}
	seeks = s.PartitionSeeks()
	if _, err := s.GetRecord(ctx, "OMM.fbs", missing); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	if n := s.PartitionSeeks() - seeks; n != 0 {
		t.Fatalf("the next miss asked %d partitions, want 0 (the moved head read again)", n)
	}
}

// M8: a window's CIDs stream into one bounded set of binary keys: a REPEAT
// copy counts once across partitions, and past the store's window budget
// (shared by concurrent reads, returned on Release) the read ends in
// ErrWindowTooLarge instead of growing the heap. On 0de504531 WindowCIDs
// held the whole statement result and every text CID, without a bound.
func TestWindowCIDsStreamIntoABoundedSet(t *testing.T) {
	s, cids := glueStore(t, 3)
	ctx := context.Background()
	q := WindowQuery{Schema: "OMM.fbs", Source: "tb-glue"}
	got, err := s.WindowCIDs(ctx, q)
	if err != nil || len(got) != len(cids) {
		t.Fatalf("WindowCIDs: %d CIDs (%v), want %d", len(got), err, len(cids))
	}
	want := map[string]bool{}
	for _, c := range cids {
		want[c] = true
	}
	for _, c := range got {
		if !want[c] {
			t.Fatalf("WindowCIDs returned %s, not stored", c)
		}
	}
	// The same record stored by a second producer (a REPEAT copy).
	data := ommPuts(2, 0, time.Date(2026, 9, 2, 0, 0, 0, 0, time.UTC), "GLUE")[0]
	if _, err := s.PutBatch(ctx, "OMM.fbs", []Put{data}, "source:tb-glue-mirror", nil,
		&Tags{ProviderID: "space-data-network-02", SourceName: "tb-glue", BatchID: "m1"}); err != nil {
		t.Fatal(err)
	}
	parts, err := s.PartitionsOf("OMM.fbs")
	if err != nil || len(parts) != 4 {
		t.Fatalf("%d partitions (%v), want 4", len(parts), err)
	}
	set := s.NewCIDSet()
	for _, p := range parts {
		pq := q
		pq.Table = p.SQLName
		if err := s.AddWindowCIDs(ctx, pq, set); err != nil {
			t.Fatal(err)
		}
	}
	if set.Len() != len(cids) {
		t.Fatalf("the partitions' windows hold %d distinct CIDs, want %d (the REPEAT copy once)", set.Len(), len(cids))
	}
	if held := s.windowHeld.Load(); held != int64(len(cids)) {
		t.Fatalf("the budget holds %d CIDs for a set of %d", held, len(cids))
	}
	// The budget is the store's: while this set holds its CIDs, another read
	// of the same window does not fit in twice their number minus two.
	s.SetWindowCIDBudget(int64(2*len(cids) - 2))
	defer s.SetWindowCIDBudget(0)
	if _, err := s.WindowCIDs(ctx, q); !errors.Is(err, ErrWindowTooLarge) {
		t.Fatalf("WindowCIDs past the budget: %v, want ErrWindowTooLarge", err)
	}
	if held := s.windowHeld.Load(); held != int64(len(cids)) {
		t.Fatalf("a read that failed kept %d CIDs of the budget, want the set's %d", held-int64(len(cids)), len(cids))
	}
	set.Release()
	if got, err := s.WindowCIDs(ctx, q); err != nil || len(got) != len(cids) {
		t.Fatalf("WindowCIDs once the set was released: %d (%v)", len(got), err)
	}
	if held := s.windowHeld.Load(); held != 0 {
		t.Fatalf("%d CIDs still held after every set was released", held)
	}
}
