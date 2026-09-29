package format2

// Reader instance memory under a sustained statement mix while one writer
// ingests into 50 partitions (env-gated): which statement kind grows a
// reader's linear memory. The 30-minute soak lost the point instance to a
// guest bad_alloc after 28 minutes.
//
//	SDN_FORMAT2_MEMPROBE=20000   statements per kind (0: skip)

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestReaderMemoryUnderAStatementMix(t *testing.T) {
	n, _ := strconv.Atoi(os.Getenv("SDN_FORMAT2_MEMPROBE"))
	if n <= 0 {
		t.Skip("SDN_FORMAT2_MEMPROBE=<statements per kind>")
	}
	s := openTestStore(t, t.TempDir())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	base := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	var seq atomic.Int64
	var cids sync.Map
	var wg sync.WaitGroup
	for p := 0; p < 50; p++ {
		wg.Add(1)
		go func(p int) {
			defer wg.Done()
			for ctx.Err() == nil {
				k := int(seq.Add(100)) - 100
				puts := make([]Put, 100)
				for i := range puts {
					puts[i] = Put{Data: testOMM(uint32(100000+k+i), base.Add(time.Duration(k+i)*time.Second), fmt.Sprintf("M%d", k+i))}
				}
				res, err := s.PutBatch(ctx, "OMM.fbs", puts, fmt.Sprintf("source:m%02d", p), nil, &Tags{SourceName: fmt.Sprintf("m%02d", p), BatchID: "live"})
				if err != nil {
					return
				}
				cids.Store(p, res[0].CID)
			}
		}(p)
	}
	defer func() { cancel(); wg.Wait() }()
	time.Sleep(2 * time.Second)
	var one string
	cids.Range(func(_, v any) bool { one = v.(string); return false })
	c, _ := CIDFromText(one)
	pages := func() uint64 { return s.Point().Instance().Memory().Pages() }
	kinds := []struct {
		name string
		fn   func() error
	}{
		{"SELECT 1", func() error { _, err := s.QueryPoint(ctx, Request{SQL: "SELECT 1"}); return err }},
		{"GetRecord", func() error { _, err := s.GetRecord(ctx, "OMM.fbs", one); return err }},
		{"CID IN at type level", func() error {
			_, err := s.QueryPoint(ctx, Request{SQL: `SELECT _cid_bin FROM "OMM" WHERE _cid_bin IN (?1)`, Params: []Cell{Blob(c)}})
			return err
		}},
		{"flatsql_lanes", func() error { _, err := s.Lanes(ctx); return err }},
		{"flatsql_licences", func() error { _, err := s.Licences(ctx); return err }},
		{"PresentInPartition", func() error {
			_, err := s.PresentInPartition(ctx, "OMM.fbs", "source:m07", []string{one})
			return err
		}},
	}
	if os.Getenv("SDN_FORMAT2_MEMPROBE_LONG") == "1" {
		// GetRecord alone, sampled, with the ingest running, then stopped.
		for phase := 0; phase < 2; phase++ {
			if phase == 1 {
				cancel()
				wg.Wait()
				ctx = context.Background()
			}
			for r := 0; r < 5; r++ {
				before := pages()
				for i := 0; i < n; i++ {
					if _, err := s.GetRecord(ctx, "OMM.fbs", one); err != nil {
						t.Fatal(err)
					}
				}
				t.Logf("MEASURED GetRecord x%d (ingest %v): %d -> %d pages", n, phase == 0, before, pages())
			}
		}
		return
	}
	for _, k := range kinds {
		before := pages()
		start := time.Now()
		for i := 0; i < n; i++ {
			if err := k.fn(); err != nil {
				t.Fatalf("%s: %v", k.name, err)
			}
		}
		t.Logf("MEASURED %-22s %d statements in %s: point instance %d -> %d pages (%+d KiB per 1,000 statements)", k.name, n,
			time.Since(start).Round(time.Millisecond), before, pages(), int64(pages()-before)*64*1000/int64(n))
	}
}
