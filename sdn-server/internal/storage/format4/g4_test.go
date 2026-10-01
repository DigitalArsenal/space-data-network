package format4_test

// Gate G4, SDN side (stack design flatsql-sqlite-partitions.md §16; build-out
// contract §6 "host"): the threaded engine in SDN's wasmrt.
//
//   - One writer, 4,096-record OMM calls, back to back with format 2 in the
//     same process and session: call p99 under 494 ms (format 2's baseline)
//     and at most format 2's p99 measured here.
//   - M01 (benchset "reads never wait"): W01's OMM shape (32,015 clones,
//     one new batch per cycle) and W06 (the current-set supersede that keeps
//     it) in cycles for SDN_P4_G4_MINUTES (30), while
//     R01 (get hits), R05 (datasync first page), R11 (index page 1) and R16
//     (epoch nearest, limit 200) each run at 20 calls/s: no read over 50 ms,
//     0 errors, and the WAL bounded (sampled every 5 s through SUMMARY 4).
//
// It runs only with SDN_P4_G4=1 (it takes the better part of an hour) and an
// engine (realWasm). Every number goes with the load average at its start
// and end. SDN_P4_G4_REPORT names a JSON report file.

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/storage/format2"
	"github.com/spacedatanetwork/sdn-server/internal/storage/format4"
	"github.com/spacedatanetwork/sdn-server/internal/storage/format4/format4test"
)

const (
	g4CallRecords   = 4096
	g4F2Baseline    = 494 * time.Millisecond
	g4ReadBudget    = 50 * time.Millisecond
	g4W01Records    = 32015
	g4ReadRate      = 20 // calls/s per read shape
	g4WALCapBytes   = 1 << 30
	g4WALSampleTick = 5 * time.Second
)

func envInt(name string, def int) int {
	if v, err := strconv.Atoi(os.Getenv(name)); err == nil && v > 0 {
		return v
	}
	return def
}

func loadAvg() string {
	out, err := exec.Command("sysctl", "-n", "vm.loadavg").Output()
	if err != nil {
		if b, err := os.ReadFile("/proc/loadavg"); err == nil {
			return strings.TrimSpace(string(b))
		}
		return "unknown"
	}
	return strings.Trim(strings.TrimSpace(string(out)), "{} ")
}

type latencies struct {
	mu sync.Mutex
	d  []time.Duration
}

func (l *latencies) add(d time.Duration) {
	l.mu.Lock()
	l.d = append(l.d, d)
	l.mu.Unlock()
}

type pct struct {
	N             int
	P50, P99, Max time.Duration
}

func (l *latencies) summary() pct {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.d) == 0 {
		return pct{}
	}
	s := append([]time.Duration(nil), l.d...)
	sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
	at := func(p float64) time.Duration { return s[int(math.Ceil(p*float64(len(s))))-1] }
	return pct{N: len(s), P50: at(0.50), P99: at(0.99), Max: s[len(s)-1]}
}

// g4OMM is clone k of object i: one of 32,015 objects at a later epoch per
// clone (the benchset's clone shape, epoch + 61 s per clone).
func g4OMM(i, k int) []byte {
	epoch := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC).Add(time.Duration(i%86400)*time.Second + time.Duration(k)*61*time.Second)
	return format4test.OMMRecord(uint32(10000+i), fmt.Sprintf("2026-%03dA", i%1000), epoch.Format("2006-01-02T15:04:05.000000"))
}

type g4Report struct {
	LoadStart, LoadAfterIngest, LoadEnd string
	F2OneWriter, F4OneWriter            pct
	F2Rate, F4Rate                      float64 // records/s
	Idle, DuringWrites                  map[string]pct
	ReadsOverBudget, ReadErrors         int64
	WriteErrors                         int64
	Cycles                              int
	WALMaxBytes                         int64
	WALSamples                          []int64
	Minutes                             int
}

func TestG4ReadsNeverWaitAndOneWriterCalls(t *testing.T) {
	if os.Getenv("SDN_P4_G4") != "1" {
		t.Skip("gate G4 runs with SDN_P4_G4=1 (about an hour)")
	}
	realWasm(t)
	calls := envInt("SDN_P4_G4_CALLS", 100)
	minutes := envInt("SDN_P4_G4_MINUTES", 30)
	rep := g4Report{LoadStart: loadAvg(), Minutes: minutes, Idle: map[string]pct{}, DuringWrites: map[string]pct{}}
	ctx := context.Background()

	// The same records for both formats: calls x 4,096 OMM clones.
	batch := func(c int) [][]byte {
		out := make([][]byte, g4CallRecords)
		for j := range out {
			n := c*g4CallRecords + j
			out[j] = g4OMM(n%g4W01Records, n/g4W01Records)
		}
		return out
	}

	// Format 2, one writer.
	var f2 latencies
	{
		s, err := format2.Open(format2.StoreConfig{Root: t.TempDir(), AOTCacheDir: realAOT(t), CompileOnMiss: true, AllowFresh: true})
		if err != nil {
			t.Fatalf("format 2: %v", err)
		}
		start := time.Now()
		for c := 0; c < calls; c++ {
			recs := batch(c)
			puts := make([]format2.Put, len(recs))
			for i, r := range recs {
				puts[i] = format2.Put{Data: r}
			}
			t0 := time.Now()
			if _, err := s.PutBatch(ctx, "OMM.fbs", puts, "source:celestrak", nil,
				&format2.Tags{ProviderID: "space-data-network-02", SourceName: "celestrak-gp", BatchID: fmt.Sprintf("b%d", c/8)}); err != nil {
				t.Fatalf("format 2 call %d: %v", c, err)
			}
			f2.add(time.Since(t0))
		}
		rep.F2Rate = float64(calls*g4CallRecords) / time.Since(start).Seconds()
		_ = s.Close()
	}
	rep.F2OneWriter = f2.summary()

	// Format 4, one writer, on a fresh store.
	e := openReal(t, t.TempDir(), format4.CreateFresh, 0, nil)
	for _, schema := range []string{"OMM.fbs"} {
		spec, err := format4.TypeSpecFor(schema)
		if err != nil {
			t.Fatal(err)
		}
		if err := e.RegisterType(spec); err != nil {
			t.Fatal(err)
		}
	}
	var f4 latencies
	var held atomic.Pointer[[]string] // CIDs of the live set, for R01
	{
		start := time.Now()
		for c := 0; c < calls; c++ {
			recs := batch(c)
			ins := make([]format4.In, len(recs))
			for i, r := range recs {
				ins[i] = format4test.In(r, time.Now().Unix())
			}
			t0 := time.Now()
			out, err := e.Put(ctx, format4.Batch{Type: "OMM", Peer: "source:celestrak",
				Tags:    []format4.Tag{{Provider: "space-data-network-02", Source: "celestrak-gp", Batch: fmt.Sprintf("b%d", c/8)}},
				Records: ins})
			f4.add(time.Since(t0))
			if err != nil {
				t.Fatalf("format 4 call %d: %v", c, err)
			}
			for i, o := range out {
				if o.Action == format4.ActRejected {
					t.Fatalf("format 4 call %d record %d rejected %d (%s)", c, i, o.Reject, format4.RejectReason(o.Reject))
				}
			}
			if c == calls-1 {
				h := make([]string, 0, 256)
				for _, in := range ins[:256] {
					h = append(h, in.CID)
				}
				held.Store(&h)
			}
		}
		rep.F4Rate = float64(calls*g4CallRecords) / time.Since(start).Seconds()
	}
	rep.F4OneWriter = f4.summary()
	rep.LoadAfterIngest = loadAvg()
	t.Logf("one writer, %d x %d OMM: F2 %.0f rec/s p50 %s p99 %s; F4 %.0f rec/s p50 %s p99 %s (load %s -> %s)",
		calls, g4CallRecords, rep.F2Rate, rep.F2OneWriter.P50, rep.F2OneWriter.P99, rep.F4Rate, rep.F4OneWriter.P50,
		rep.F4OneWriter.P99, rep.LoadStart, rep.LoadAfterIngest)

	// M01: readers at 20 calls/s per shape, idle first, then during writes.
	var overBudget, readErrs, writeErrs atomic.Int64
	var during atomic.Bool
	idle := map[string]*latencies{}
	busy := map[string]*latencies{}
	shapes := map[string]func(context.Context, int) error{
		"R01 get": func(ctx context.Context, i int) error {
			h := *held.Load()
			recs, err := e.Get(ctx, "OMM", []string{h[i%len(h)]}, false, true)
			if err == nil && len(recs) == 0 && !during.Load() {
				err = fmt.Errorf("R01 missed a held record")
			}
			return err
		},
		"R05 datasync page": func(ctx context.Context, _ int) error {
			if _, err := e.Head(ctx, format4.Query{Type: "OMM"}); err != nil {
				return err
			}
			_, err := e.Scan(ctx, format4.Query{Type: "OMM", Limit: 100})
			return err
		},
		"R11 index page 1": func(ctx context.Context, _ int) error {
			if _, err := e.Head(ctx, format4.Query{Type: "OMM"}); err != nil {
				return err
			}
			_, err := e.IndexPage(ctx, format4.Query{Type: "OMM", Limit: 50})
			return err
		},
		"R16 epoch nearest": func(ctx context.Context, _ int) error {
			_, err := e.Epoch(ctx, format4.EpochQuery{Query: format4.Query{Type: "OMM", Limit: 200}, Profile: format4.EpochNearest,
				At: time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC).Unix()})
			return err
		},
	}
	stop := make(chan struct{})
	var wg sync.WaitGroup
	for name, fn := range shapes {
		idle[name], busy[name] = &latencies{}, &latencies{}
		wg.Add(1)
		go func(name string, fn func(context.Context, int) error) {
			defer wg.Done()
			tick := time.NewTicker(time.Second / g4ReadRate)
			defer tick.Stop()
			for i := 0; ; i++ {
				select {
				case <-stop:
					return
				case <-tick.C:
				}
				rctx, cancel := context.WithTimeout(ctx, 10*time.Second)
				t0 := time.Now()
				err := fn(rctx, i)
				d := time.Since(t0)
				cancel()
				if err != nil {
					readErrs.Add(1)
					t.Errorf("%s: %v", name, err)
					continue
				}
				if during.Load() {
					busy[name].add(d)
					if d > g4ReadBudget {
						overBudget.Add(1)
					}
				} else {
					idle[name].add(d)
				}
			}
		}(name, fn)
	}
	time.Sleep(10 * time.Second) // the idle baseline
	during.Store(true)

	// WAL sampler.
	var walMax atomic.Int64
	var samples []int64
	var smu sync.Mutex
	wg.Add(1)
	go func() {
		defer wg.Done()
		tick := time.NewTicker(g4WALSampleTick)
		defer tick.Stop()
		for {
			select {
			case <-stop:
				return
			case <-tick.C:
			}
			disk, err := e.Disk(ctx)
			if err != nil {
				readErrs.Add(1)
				t.Errorf("disk: %v", err)
				continue
			}
			var wal int64
			for _, d := range disk {
				wal += d.WALBytes
			}
			if wal > walMax.Load() {
				walMax.Store(wal)
			}
			smu.Lock()
			samples = append(samples, wal)
			smu.Unlock()
		}
	}()

	// W01 (OMM 32,015) + W06 (supersede keeping the new batch), in cycles
	// until the time is up.
	deadline := time.Now().Add(time.Duration(minutes) * time.Minute)
	cycle := 0
	for time.Now().Before(deadline) {
		cycle++
		batchID := fmt.Sprintf("OMM-celestrak-gp-c%d", cycle)
		for off := 0; off < g4W01Records; off += g4CallRecords {
			var ins []format4.In
			for i := off; i < min(off+g4CallRecords, g4W01Records); i++ {
				ins = append(ins, format4test.In(g4OMM(i, 1000+cycle), time.Now().Unix()))
			}
			if _, err := e.Put(ctx, format4.Batch{Type: "OMM", Peer: "source:celestrak",
				Tags: []format4.Tag{{Provider: "space-data-network-02", Source: "celestrak-gp", Batch: batchID}}, Records: ins}); err != nil {
				writeErrs.Add(1)
				t.Errorf("W01 cycle %d: %v", cycle, err)
			}
			if off == 0 {
				h := make([]string, 0, 256)
				for _, in := range ins[:256] {
					h = append(h, in.CID)
				}
				held.Store(&h)
			}
		}
		if _, err := e.Supersede(ctx, "OMM", "space-data-network-02", "celestrak-gp", batchID, true); err != nil {
			writeErrs.Add(1)
			t.Errorf("W06 cycle %d: %v", cycle, err)
		}
	}
	close(stop)
	wg.Wait()

	for name := range shapes {
		rep.Idle[name], rep.DuringWrites[name] = idle[name].summary(), busy[name].summary()
	}
	rep.ReadsOverBudget, rep.ReadErrors, rep.WriteErrors = overBudget.Load(), readErrs.Load(), writeErrs.Load()
	rep.Cycles, rep.WALMaxBytes, rep.WALSamples = cycle, walMax.Load(), samples
	rep.LoadEnd = loadAvg()
	if path := os.Getenv("SDN_P4_G4_REPORT"); path != "" {
		b, _ := json.MarshalIndent(rep, "", " ")
		if err := os.WriteFile(path, b, 0o644); err != nil {
			t.Error(err)
		}
	}
	for name, p := range rep.DuringWrites {
		t.Logf("%s: idle p50 %s p99 %s | during writes p50 %s p99 %s max %s (n %d)", name, rep.Idle[name].P50,
			rep.Idle[name].P99, p.P50, p.P99, p.Max, p.N)
	}
	t.Logf("M01: %d cycles in %d min, %d reads over %s, %d read errors, %d write errors, WAL max %d MB (load %s -> %s)",
		rep.Cycles, minutes, rep.ReadsOverBudget, g4ReadBudget, rep.ReadErrors, rep.WriteErrors, rep.WALMaxBytes>>20,
		rep.LoadStart, rep.LoadEnd)

	// The gate.
	if rep.F4OneWriter.P99 >= g4F2Baseline || rep.F4OneWriter.P99 > rep.F2OneWriter.P99 {
		t.Errorf("G4: one-writer call p99 %s, want < %s and <= format 2's %s", rep.F4OneWriter.P99, g4F2Baseline, rep.F2OneWriter.P99)
	}
	if rep.ReadsOverBudget > 0 || rep.ReadErrors > 0 || rep.WriteErrors > 0 {
		t.Errorf("G4: %d reads over %s, %d read errors, %d write errors", rep.ReadsOverBudget, g4ReadBudget, rep.ReadErrors, rep.WriteErrors)
	}
	if rep.WALMaxBytes > g4WALCapBytes {
		t.Errorf("G4: WAL reached %d MB, above the %d MB instance budget", rep.WALMaxBytes>>20, g4WALCapBytes>>20)
	}
	if n := len(samples); n >= 12 {
		// Bounded: the last quarter's peak is not above the first quarter's
		// peak plus one RESTART threshold (256 MiB).
		peak := func(s []int64) (m int64) {
			for _, v := range s {
				m = max(m, v)
			}
			return m
		}
		if first, last := peak(samples[:n/4]), peak(samples[3*n/4:]); last > first+256<<20 {
			t.Errorf("G4: WAL grows over the run: first-quarter peak %d MB, last-quarter peak %d MB", first>>20, last>>20)
		}
	}
}
