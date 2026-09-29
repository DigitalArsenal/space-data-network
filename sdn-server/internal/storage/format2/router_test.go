package format2

import (
	"context"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// typeHeadLabelForm reads a type head's nLabels word straight from the file:
// 0xffff once the type keeps its labels in the type log (more than 128
// partitions, A10).
func typeHeadLabelForm(t *testing.T, s *Store, schema string) uint16 {
	t.Helper()
	spec, err := s.spec(schema)
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(s.heads.root, "t", hex.EncodeToString(spec.FID[:]), "h.fsh"))
	if err != nil {
		t.Fatal(err)
	}
	slot := bestHeadSlot(b, len(b), headType)
	if len(slot) < typeHeadFixedBytes {
		t.Fatalf("%s: no valid type head", schema)
	}
	return binary.LittleEndian.Uint16(slot[94:])
}

// visibleAtTypeLevel reports the CIDs a type-level read of OMM does not see.
func visibleAtTypeLevel(ctx context.Context, s *Store, cids []string) ([]string, error) {
	var missing []string
	for _, c := range cids {
		bin, err := CIDFromText(c)
		if err != nil {
			return nil, err
		}
		got, err := s.Interactive().Query(ctx, Request{SQL: `SELECT _gseq FROM "OMM" WHERE _cid_bin = ?1 LIMIT 1`, Params: []Cell{Blob(bin)}})
		if err != nil {
			return nil, err
		}
		if len(got.Rows) != 1 {
			missing = append(missing, c)
		}
	}
	return missing, nil
}

// labelsMatchHeads checks that every partition is labeled exactly through
// its pseq_hi, in the store's reader and in one folded afresh from the latest
// checkpoint.
func labelsMatchHeads(t *testing.T, s *Store, fid [4]byte, producers int) {
	t.Helper()
	parts, err := s.heads.Partitions()
	if err != nil {
		t.Fatal(err)
	}
	if len(parts) != producers {
		t.Fatalf("%d partitions, want %d", len(parts), producers)
	}
	fresh := NewHeadReader(filepath.Dir(s.heads.root))
	defer fresh.Close()
	for _, p := range parts {
		for name, h := range map[string]*HeadReader{"incremental": s.heads, "fresh": fresh} {
			var lt uint64
			var known bool
			// A reader that finds another folding polls again (known false).
			for tries := 0; tries < 1000; tries++ {
				if lt, known, err = h.labeledThrough(fid, uint32(p.PID)); err != nil || known {
					break
				}
				time.Sleep(time.Millisecond)
			}
			if err != nil || !known || lt != uint64(p.PseqHi) {
				t.Fatalf("partition %d (%s), %s fold: labeled_through %d known %v err %v, pseq_hi %d",
					p.PID, p.Producer, name, lt, known, err, p.PseqHi)
			}
		}
	}
}

// B1 (terabyte audit): past 128 partitions a type publishes nLabels 0xffff
// and keeps labeled_through in its log (A10). A write waits for its labels
// (A20), so it must read them there: on 58a742f44 the 129th producer's first
// write polled forever. Each wait here must end because the labels are read,
// not because the deadline passed; the records must be visible at type level
// the moment it returns; and it must not need HeadReader.mu (a counter
// sweep) or Writer.mu (another producer's registration).
func TestWaitLabeledPastTheInlineLabelTable(t *testing.T) {
	s := openTestStore(t, t.TempDir())
	base := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	const producers = 130 // two past kMaxInlineLabels
	tags := &Tags{ProviderID: "space-data-network-02", SourceName: "tb-glue", BatchID: "b1"}
	spec, err := s.spec("OMM.fbs")
	if err != nil {
		t.Fatal(err)
	}
	var slowest, slowestPast128 time.Duration
	for i := 0; i < producers; i++ {
		peer := fmt.Sprintf("source:tb-glue-%03d", i)
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		res, err := s.PutBatch(ctx, "OMM.fbs", ommPuts(2, 2*i, base, "B1"), peer, nil, tags)
		if err != nil {
			cancel()
			t.Fatalf("producer %d: %v", i, err)
		}
		var cids []string
		for _, r := range res {
			if r.Err != nil {
				cancel()
				t.Fatalf("producer %d: record rejected: %v", i, r.Err)
			}
			cids = append(cids, r.CID)
		}
		// The last producer waits with the counter lock and the
		// registration lock held elsewhere.
		if i == producers-1 {
			s.heads.mu.Lock()
			s.w.mu.Lock()
		}
		start := time.Now()
		err = s.WaitLabeled(ctx, "OMM.fbs", peer)
		took := time.Since(start)
		if i == producers-1 {
			s.w.mu.Unlock()
			s.heads.mu.Unlock()
		}
		if err != nil {
			cancel()
			t.Fatalf("producer %d of %d: WaitLabeled returned %v after %v", i+1, producers, err, took)
		}
		// A20: labeled means a type-level read sees the records now.
		missing, err := visibleAtTypeLevel(ctx, s, cids)
		cancel()
		if err != nil {
			t.Fatal(err)
		}
		if len(missing) > 0 {
			t.Fatalf("producer %d of %d: WaitLabeled returned but %d of its records are not visible at type level (%v)",
				i+1, producers, len(missing), missing)
		}
		slowest = max(slowest, took)
		if i >= 128 {
			slowestPast128 = max(slowestPast128, took)
		}
	}
	if form := typeHeadLabelForm(t, s, "OMM.fbs"); form != labelsInLog {
		t.Fatalf("type head nLabels = %d with %d partitions, want 0x%x (labels in the type log)", form, producers, labelsInLog)
	}
	if n := s.LabelWaitTimeouts(); n != 0 {
		t.Fatalf("%d label waits ended on the deadline, not on the labels", n)
	}
	labelsMatchHeads(t, s, spec.FID, producers)
	t.Logf("%d producers of one type: slowest label wait %v over all, %v past the inline table", producers, slowest, slowestPast128)
}

// The same across the boundary with producers registering concurrently while
// counter sweeps and fresh readers fold: every wait that returns nil must
// leave its records visible at type level at once (an over-reported
// labeled_through would not), and a wait may only end in ErrNotLabeled, never
// in another error. Run under -race in CI.
func TestWaitLabeledConcurrentAcrossTheInlineLabelTable(t *testing.T) {
	s := openTestStore(t, t.TempDir())
	base := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	const producers, rounds = 160, 2
	tags := &Tags{ProviderID: "space-data-network-02", SourceName: "tb-glue", BatchID: "b1c"}
	spec, err := s.spec("OMM.fbs")
	if err != nil {
		t.Fatal(err)
	}
	stop := make(chan struct{})
	var sweepers sync.WaitGroup
	var sweeps, folds atomic.Int64
	sweepers.Add(2)
	go func() {
		defer sweepers.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			if _, err := s.heads.Partitions(); err != nil {
				t.Error(err)
				return
			}
			sweeps.Add(1)
		}
	}()
	go func() {
		defer sweepers.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			fr := NewHeadReader(filepath.Dir(s.heads.root))
			for pid := uint32(1); pid <= 4; pid++ {
				if _, _, err := fr.labeledThrough(spec.FID, pid); err != nil {
					t.Errorf("fresh reader: %v", err)
				}
			}
			fr.Close()
			folds.Add(1)
		}
	}()
	var mu sync.Mutex
	var waits []time.Duration
	var notLabeled int
	var wg sync.WaitGroup
	for g := 0; g < producers; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			peer := fmt.Sprintf("source:tb-glue-c%03d", g)
			for k := 0; k < rounds; k++ {
				ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
				res, err := s.PutBatch(ctx, "OMM.fbs", ommPuts(2, 1000+g*10+k*2, base, "B1C"), peer, nil, tags)
				if err != nil {
					cancel()
					t.Errorf("put %s: %v", peer, err)
					return
				}
				cids := []string{res[0].CID, res[1].CID}
				start := time.Now()
				err = s.WaitLabeled(ctx, "OMM.fbs", peer)
				took := time.Since(start)
				switch {
				case errors.Is(err, ErrNotLabeled):
					mu.Lock()
					notLabeled++
					mu.Unlock()
				case err != nil:
					t.Errorf("wait %s: %v", peer, err)
				default:
					if missing, err := visibleAtTypeLevel(ctx, s, cids); err != nil {
						t.Error(err)
					} else if len(missing) > 0 {
						t.Errorf("%s round %d: WaitLabeled returned nil but %v are not visible at type level", peer, k, missing)
					}
				}
				cancel()
				if took > LabelWaitMax+time.Second {
					t.Errorf("%s: WaitLabeled took %v, over its %v budget", peer, took, LabelWaitMax)
				}
				mu.Lock()
				waits = append(waits, took)
				mu.Unlock()
			}
		}(g)
	}
	wg.Wait()
	close(stop)
	sweepers.Wait()
	if t.Failed() {
		return
	}
	if form := typeHeadLabelForm(t, s, "OMM.fbs"); form != labelsInLog {
		t.Fatalf("type head nLabels = %d with %d partitions, want 0x%x", form, producers, labelsInLog)
	}
	// Once the owner has caught up, every label equals pseq_hi.
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	for g := 0; g < producers; g++ {
		for {
			err := s.WaitLabeled(ctx, "OMM.fbs", fmt.Sprintf("source:tb-glue-c%03d", g))
			if err == nil {
				break
			}
			if !errors.Is(err, ErrNotLabeled) {
				t.Fatal(err)
			}
		}
	}
	labelsMatchHeads(t, s, spec.FID, producers)
	sort.Slice(waits, func(i, j int) bool { return waits[i] < waits[j] })
	t.Logf("%d producers x %d rounds, concurrent: label wait p50 %v p99 %v max %v; %d ended not labeled; %d sweeps, %d fresh folds",
		producers, rounds, waits[len(waits)/2], waits[len(waits)*99/100], waits[len(waits)-1], notLabeled, sweeps.Load(), folds.Load())
}

// A wait whose labels do not arrive ends at its deadline with a
// *LabelWaitError (ErrNotLabeled) naming the partition, not with nil: here
// another waiter holds the type's fold lock and the table was never read, so
// this one can neither read nor queue.
func TestWaitLabeledEndsNotLabeledAtTheDeadline(t *testing.T) {
	s := openTestStore(t, t.TempDir())
	spec, err := s.spec("OMM.fbs")
	if err != nil {
		t.Fatal(err)
	}
	peer := "source:tb-glue-late"
	if _, err := s.PutBatch(context.Background(), "OMM.fbs", ommPuts(1, 0, time.Now().UTC(), "late"), peer, nil, nil); err != nil {
		t.Fatal(err)
	}
	s.heads.lmu.Lock()
	s.heads.labels = map[[4]byte]*typeLabels{spec.FID: {dir: filepath.Join(s.heads.root, "t", hex.EncodeToString(spec.FID[:])),
		labels: map[uint32]uint64{}}}
	tl := s.heads.labels[spec.FID]
	s.heads.lmu.Unlock()
	tl.mu.Lock()
	start := time.Now()
	err = s.WaitLabeledUntil(context.Background(), "OMM.fbs", []string{peer, "source:never-registered"}, time.Now().Add(100*time.Millisecond))
	took := time.Since(start)
	tl.mu.Unlock()
	var lw *LabelWaitError
	if !errors.Is(err, ErrNotLabeled) || !errors.As(err, &lw) {
		t.Fatalf("WaitLabeledUntil = %v, want a *LabelWaitError", err)
	}
	if lw.Schema != "OMM.fbs" || lw.PID == 0 || lw.PseqHi == 0 || lw.Known || lw.Partitions != 1 {
		t.Fatalf("LabelWaitError %+v", *lw)
	}
	if took < 100*time.Millisecond || took > 5*time.Second {
		t.Fatalf("returned after %v, want the 100ms deadline", took)
	}
	if n := s.LabelWaitTimeouts(); n != 1 {
		t.Fatalf("LabelWaitTimeouts = %d, want 1", n)
	}
	// With the lock free the same wait ends on the labels.
	if err := s.WaitLabeled(context.Background(), "OMM.fbs", peer); err != nil {
		t.Fatal(err)
	}
	// After Close a wait ends with ErrStopped and opens nothing.
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if err := s.WaitLabeled(context.Background(), "OMM.fbs", peer); !errors.Is(err, ErrStopped) {
		t.Fatalf("WaitLabeled after Close = %v, want ErrStopped", err)
	}
	if s.heads.labels != nil {
		t.Fatal("a wait after Close rebuilt the label readers")
	}
}

// A label wait always ends: labels that never reach pseq_hi, or cannot be
// read, end it at the deadline; a read error is polled through, not
// returned (the records are durable already).
func TestWaitLabelsEndsAtTheDeadline(t *testing.T) {
	errRead := errors.New("too many open files")
	for _, tc := range []struct {
		name    string
		known   bool
		err     error
		hiErr   bool
		wantErr error
	}{
		{name: "labels behind", known: true},
		{name: "labels unreadable", known: false},
		{name: "labels read error", err: errRead, wantErr: errRead},
		{name: "pseq_hi read error", hiErr: true, wantErr: errRead},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reads := 0
			start := time.Now()
			st, err := waitLabels(context.Background(), nil, time.Now().Add(50*time.Millisecond), func() (uint64, error) {
				if tc.hiErr {
					return 0, errRead
				}
				return 7, nil
			}, func() (uint64, bool, error) {
				reads++
				return 3, tc.known, tc.err
			})
			took := time.Since(start)
			if err != nil || !st.timedOut {
				t.Fatalf("timedOut %v err %v", st.timedOut, err)
			}
			if st.readErr != tc.wantErr {
				t.Fatalf("readErr %v, want %v", st.readErr, tc.wantErr)
			}
			if took < 50*time.Millisecond || took > 5*time.Second {
				t.Fatalf("returned after %v, want the 50ms deadline", took)
			}
			if !tc.hiErr && reads < 2 {
				t.Fatalf("%d reads: the wait never polled", reads)
			}
		})
	}
	hi7 := func() (uint64, error) { return 7, nil }
	// Labels at pseq_hi end the wait at once, after read errors too.
	fails := 3
	st, err := waitLabels(context.Background(), nil, time.Now().Add(time.Hour), hi7, func() (uint64, bool, error) {
		if fails > 0 {
			fails--
			return 0, false, errors.New("transient")
		}
		return 7, true, nil
	})
	if err != nil || st.timedOut {
		t.Fatalf("labels at pseq_hi: timedOut %v err %v", st.timedOut, err)
	}
	// Nothing committed: nothing to wait for.
	if st, err := waitLabels(context.Background(), nil, time.Now().Add(time.Hour), func() (uint64, error) { return 0, nil },
		func() (uint64, bool, error) { t.Fatal("labels read for an empty partition"); return 0, false, nil }); err != nil || st.timedOut {
		t.Fatalf("empty partition: timedOut %v err %v", st.timedOut, err)
	}
	// A cancelled context ends it with the context's error; a closed store
	// (stop, or a reader that says ErrStopped) with ErrStopped.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := waitLabels(ctx, nil, time.Now().Add(time.Hour), hi7, func() (uint64, bool, error) { return 0, true, nil }); err != context.Canceled {
		t.Fatalf("cancelled context: %v", err)
	}
	stop := make(chan struct{})
	close(stop)
	if _, err := waitLabels(context.Background(), stop, time.Now().Add(time.Hour), hi7, func() (uint64, bool, error) { return 7, true, nil }); err != ErrStopped {
		t.Fatalf("stopped store: %v", err)
	}
	if _, err := waitLabels(context.Background(), nil, time.Now().Add(time.Hour), hi7, func() (uint64, bool, error) { return 0, false, ErrStopped }); err != ErrStopped {
		t.Fatalf("closed reader: %v", err)
	}
}
