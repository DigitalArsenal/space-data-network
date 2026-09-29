package format2

import (
	"context"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
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

// B1 (terabyte audit): past 128 partitions a type publishes nLabels 0xffff
// and keeps labeled_through in its log (A10). A write waits for its labels
// (A20), so it must read them there: on 58a742f44 the 129th producer's first
// write polled forever. Each wait here must end because the labels are read,
// not because the deadline passed, and must not need HeadReader.mu.
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
	var cids []string
	for i := 0; i < producers; i++ {
		peer := fmt.Sprintf("source:tb-glue-%03d", i)
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		res, err := s.PutBatch(ctx, "OMM.fbs", ommPuts(2, 2*i, base, "B1"), peer, nil, tags)
		if err != nil {
			cancel()
			t.Fatalf("producer %d: %v", i, err)
		}
		for _, r := range res {
			if r.Err != nil {
				cancel()
				t.Fatalf("producer %d: record rejected: %v", i, r.Err)
			}
			cids = append(cids, r.CID)
		}
		// The last producer waits with the counter lock held elsewhere: a
		// label wait must not queue behind a counter sweep.
		if i == producers-1 {
			s.heads.mu.Lock()
		}
		start := time.Now()
		err = s.WaitLabeled(ctx, "OMM.fbs", peer)
		took := time.Since(start)
		if i == producers-1 {
			s.heads.mu.Unlock()
		}
		cancel()
		if err != nil {
			t.Fatalf("producer %d of %d: WaitLabeled returned %v after %v", i+1, producers, err, took)
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
	// Every partition is labeled exactly through its pseq_hi, both in the
	// table folded batch by batch during the waits and in one folded afresh
	// from the latest checkpoint.
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
			lt, known, err := h.labeledThrough(spec.FID, uint32(p.PID))
			if err != nil || !known || lt != uint64(p.PseqHi) {
				t.Fatalf("partition %d (%s), %s fold: labeled_through %d known %v err %v, pseq_hi %d",
					p.PID, p.Producer, name, lt, known, err, p.PseqHi)
			}
		}
	}
	// A20: once labeled, a type-level read sees every record.
	ctx := context.Background()
	for _, c := range cids {
		bin, _ := CIDFromText(c)
		got, err := s.Interactive().Query(ctx, Request{SQL: `SELECT _gseq FROM "OMM" WHERE _cid_bin = ?1 LIMIT 1`, Params: []Cell{Blob(bin)}})
		if err != nil {
			t.Fatal(err)
		}
		if len(got.Rows) != 1 {
			t.Fatalf("%s is labeled but not visible at type level", c)
		}
	}
	t.Logf("%d producers of one type: slowest label wait %v over all, %v past the inline table", producers, slowest, slowestPast128)
}

// A label wait always ends: labels that never reach pseq_hi (or cannot be
// read) end it at the deadline, which is counted.
func TestWaitLabelsEndsAtTheDeadline(t *testing.T) {
	for _, tc := range []struct {
		name  string
		known bool
	}{{"labels behind", true}, {"labels unreadable", false}} {
		t.Run(tc.name, func(t *testing.T) {
			reads := 0
			start := time.Now()
			timedOut, err := waitLabels(context.Background(), nil, 7, 50*time.Millisecond, func() (uint64, bool, error) {
				reads++
				return 3, tc.known, nil
			})
			took := time.Since(start)
			if err != nil || !timedOut {
				t.Fatalf("timedOut %v err %v", timedOut, err)
			}
			if took < 50*time.Millisecond || took > 5*time.Second {
				t.Fatalf("returned after %v, want the 50ms deadline", took)
			}
			if reads < 2 {
				t.Fatalf("%d reads: the wait never polled", reads)
			}
		})
	}
	// Labels at pseq_hi end the wait at once; a cancelled context ends it
	// with the context's error.
	if timedOut, err := waitLabels(context.Background(), nil, 7, time.Hour, func() (uint64, bool, error) { return 7, true, nil }); err != nil || timedOut {
		t.Fatalf("labels at pseq_hi: timedOut %v err %v", timedOut, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := waitLabels(ctx, nil, 7, time.Hour, func() (uint64, bool, error) { return 0, true, nil }); err != context.Canceled {
		t.Fatalf("cancelled context: %v", err)
	}
}
