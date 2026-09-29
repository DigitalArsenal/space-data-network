package format2

// T6 acceptance measurements on the published engine (design §18 T6 #2, #3,
// #4, #5). Machine and load are printed with every MEASURED line; the
// host-02-profile numbers are ops evidence (22.3a-11), run with the
// commands in the task's report.
//
//	SDN_FORMAT2_STORE=<root>   run #2/#3 against an existing format-2 store
//	                           (a migrated fixture; its fsql2/ at <root>)
//	SDN_FORMAT2_ENGINE_ROOT    that store's engine root ("." default)
//	SDN_FORMAT2_READ_FOR=30m   #3's duration (default 15s)
//	SDN_FORMAT2_KILLS=1000     #5's kill -9 count (default 10)

import (
	"bufio"
	"context"
	"encoding/hex"
	"fmt"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

func loadAvg() float64 {
	out, err := exec.Command("sysctl", "-n", "vm.loadavg").Output()
	if err != nil {
		raw, err := os.ReadFile("/proc/loadavg")
		if err != nil {
			return -1
		}
		out = raw
	}
	var a float64
	fmt.Sscanf(strings.Trim(strings.TrimSpace(string(out)), "{} "), "%f", &a)
	return a
}

func machine() string {
	return fmt.Sprintf("%s/%s, %d CPUs, load %.1f", runtime.GOOS, runtime.GOARCH, runtime.NumCPU(), loadAvg())
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

func (l *latencies) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.d) == 0 {
		return "no samples"
	}
	s := append([]time.Duration(nil), l.d...)
	sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
	q := func(p float64) time.Duration { return s[int(float64(len(s)-1)*p)] }
	return fmt.Sprintf("n=%d p50=%s p99=%s max=%s", len(s), q(0.5).Round(time.Microsecond), q(0.99).Round(time.Microsecond),
		s[len(s)-1].Round(time.Microsecond))
}

func envDur(name string, def time.Duration) time.Duration {
	if d, err := time.ParseDuration(os.Getenv(name)); err == nil && d > 0 {
		return d
	}
	return def
}

func envInt(name string, def int) int {
	var n int
	if _, err := fmt.Sscanf(os.Getenv(name), "%d", &n); err == nil && n > 0 {
		return n
	}
	return def
}

// fillStore writes n OMM records spread over parts partitions.
func fillStore(t testing.TB, s *Store, parts, n int) {
	t.Helper()
	ctx := context.Background()
	base := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	per := n / parts
	var wg sync.WaitGroup
	errs := make(chan error, parts)
	for p := 0; p < parts; p++ {
		wg.Add(1)
		go func(p int) {
			defer wg.Done()
			for off := 0; off < per; off += 500 {
				k := 500
				if off+k > per {
					k = per - off
				}
				puts := make([]Put, k)
				for i := range puts {
					seq := p*per + off + i
					puts[i] = Put{Data: testOMM(uint32(100000+seq), base.Add(time.Duration(seq)*time.Second), fmt.Sprintf("F%d", seq))}
				}
				if _, err := s.PutBatch(ctx, "OMM.fbs", puts, fmt.Sprintf("source:fill%02d", p), nil,
					&Tags{ProviderID: "prov", SourceName: fmt.Sprintf("fill%02d", p), BatchID: "b1"}); err != nil {
					errs <- err
					return
				}
			}
		}(p)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
}

func fixtureOrFresh(t testing.TB) (root, engineRoot string, fresh bool) {
	if r := os.Getenv("SDN_FORMAT2_STORE"); r != "" {
		return r, os.Getenv("SDN_FORMAT2_ENGINE_ROOT"), false
	}
	return t.TempDir(), "", true
}

func openAt(t testing.TB, root, engineRoot string, writers uint32) *Store {
	t.Helper()
	requireEngine(t)
	// Two interactive lanes and one bulk lane: host-02's topology (§5.1);
	// SDN_FORMAT2_LANES overrides the interactive count.
	s, err := Open(StoreConfig{Root: root, EngineRoot: engineRoot, AOTCacheDir: testAOTDir(t), CompileOnMiss: true,
		AllowFresh: true, Topology: Topology{Writers: writers, InteractiveLanes: uint32(envInt("SDN_FORMAT2_LANES", 2)), BulkLanes: 1}})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	return s
}

// T6 #2: clean shutdown -> first answered counter read <= 2 s; kill -9
// during ingest -> first answered counter read <= 5 s; 0 d-* bytes read and
// 0 frames parsed at open.
func TestColdStartReadsCountersWithoutReadingData(t *testing.T) {
	requireEngine(t)
	root, engineRoot, fresh := fixtureOrFresh(t)
	if fresh {
		s := openAt(t, root, "", 1)
		fillStore(t, s, 50, envInt("SDN_FORMAT2_FILL", 50000))
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}
	}
	measure := func(label string) {
		start := time.Now()
		s := openAt(t, root, engineRoot, 1)
		lrb, err := s.LiveRecordBytes(context.Background())
		took := time.Since(start)
		if err != nil {
			t.Fatal(err)
		}
		st, err := s.Writer().Stats()
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("MEASURED cold start (%s): first counter read (%d live bytes) %s after open began (instances %s); frames parsed at open %d, d-* bytes read %d, meta bytes %d (%s)",
			label, lrb, took.Round(time.Millisecond), s.OpenedIn.Round(time.Millisecond), st.FramesParsedAtOpen, st.OpenDataBytes, st.OpenMetaBytes, machine())
		if st.FramesParsedAtOpen != 0 || st.OpenDataBytes != 0 {
			t.Fatalf("%s: open parsed %d frames and read %d data bytes", label, st.FramesParsedAtOpen, st.OpenDataBytes)
		}
		limit := 2 * time.Second
		if label != "clean shutdown" {
			limit = 5 * time.Second
		}
		if took > limit && os.Getenv("SDN_PS_ACCEPTANCE") == "1" {
			t.Fatalf("%s: %s > %s", label, took, limit)
		}
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}
	}
	measure("clean shutdown")
	if !fresh {
		return // never kill a process writing into a fixture
	}
	// kill -9 during ingest: a child ingests until it is killed.
	cmd := exec.Command(os.Args[0], "-test.run", "^TestColdStartIngestChild$", "-test.count=1")
	cmd.Env = append(os.Environ(), "SDN_FORMAT2_CHILD_ROOT="+root)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	time.Sleep(3 * time.Second)
	_ = cmd.Process.Signal(syscall.SIGKILL)
	_ = cmd.Wait()
	measure("kill -9 during ingest")
}

// TestColdStartIngestChild ingests until killed (the parent's kill -9).
func TestColdStartIngestChild(t *testing.T) {
	root := os.Getenv("SDN_FORMAT2_CHILD_ROOT")
	if root == "" {
		t.Skip("runs only as a child")
	}
	s := openAt(t, root, "", 1)
	fillStore(t, s, 50, 10_000_000)
}

// T6 #3 (and #4's ingest ratio): reads during saturating ingest. One writer
// ingests into 50 partitions while readers take counters, a window of 1,000,
// a source-filtered byte probe and window (first payload byte), GetRecord,
// and SELECT 1 (a lane's queue wait: statement cost is ~0).
func TestReadsDuringSaturatingIngest(t *testing.T) {
	requireEngine(t)
	root, engineRoot, fresh := fixtureOrFresh(t)
	s := openAt(t, root, engineRoot, 1)
	defer s.Close()
	ctx := context.Background()
	if fresh {
		fillStore(t, s, 50, envInt("SDN_FORMAT2_FILL", 100000))
	}
	sample, err := s.Window(ctx, WindowQuery{Schema: "OMM.fbs", Limit: 1000})
	if err != nil || len(sample) == 0 {
		t.Fatalf("sample window: %d rows, %v", len(sample), err)
	}
	srcSchema, srcName := "OMM.fbs", "fill07"
	if !fresh {
		srcSchema, srcName = "IQC.fbs", "IQEngine"
	}
	dur := envDur("SDN_FORMAT2_READ_FOR", 15*time.Second)

	ingest := func(d time.Duration, stop *atomic.Bool) int64 {
		var n atomic.Int64
		var wg sync.WaitGroup
		deadline := time.Now().Add(d)
		for p := 0; p < 50; p++ {
			wg.Add(1)
			go func(p int) {
				defer wg.Done()
				base := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
				for seq := 0; time.Now().Before(deadline) && !stop.Load(); seq += 100 {
					puts := make([]Put, 100)
					for i := range puts {
						k := int(n.Load()) + i
						puts[i] = Put{Data: testOMM(uint32(5_000_000+p*1_000_000+seq+i), base.Add(time.Duration(k)*time.Millisecond), fmt.Sprintf("I%d-%d", p, seq+i))}
					}
					if _, err := s.PutBatch(ctx, "OMM.fbs", puts, fmt.Sprintf("source:ingest%02d", p), nil,
						&Tags{ProviderID: "prov", SourceName: fmt.Sprintf("ingest%02d", p), BatchID: "live"}); err != nil {
						return
					}
					n.Add(100)
				}
			}(p)
		}
		wg.Wait()
		return n.Load()
	}
	// Baseline ingest with no reads.
	var noStop atomic.Bool
	baseDur := dur / 3
	if baseDur < 5*time.Second {
		baseDur = 5 * time.Second
	}
	baseline := ingest(baseDur, &noStop)
	baseRate := float64(baseline) / baseDur.Seconds()

	var counters, types, window, probe, srcFirst, getRec, queue latencies
	var readErrs atomic.Int64
	var stop atomic.Bool
	var wg sync.WaitGroup
	reader := func(l *latencies, fn func() error) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for !stop.Load() {
				start := time.Now()
				if err := fn(); err != nil {
					readErrs.Add(1)
					continue
				}
				l.add(time.Since(start))
				time.Sleep(time.Millisecond)
			}
		}()
	}
	rng := rand.New(rand.NewSource(1))
	var rngMu sync.Mutex
	reader(&counters, func() error { _, err := s.LiveRecordBytes(ctx); return err })
	reader(&types, func() error { _, err := s.Types(ctx); return err })
	reader(&window, func() error {
		_, err := s.Window(ctx, WindowQuery{Schema: "OMM.fbs", Limit: 1000})
		return err
	})
	reader(&probe, func() error {
		_, _, err := s.ByteProbe(ctx, WindowQuery{Schema: srcSchema, Source: srcName, Limit: 1000})
		return err
	})
	reader(&srcFirst, func() error {
		// First payload byte of a source-filtered window.
		// The window's own plan: SOURCE_EPOCH postings in epoch order.
		st, err := s.ri.Submit(ctx, Request{SQL: fmt.Sprintf(`SELECT _data FROM %s WHERE _source_name = ?1 ORDER BY _epoch DESC LIMIT 1000`,
			quoteIdent(typeName(srcSchema))), Params: []Cell{Text(srcName)}, Flags: ReqRawStream})
		if err != nil {
			return err
		}
		buf := make([]byte, 4096)
		_, err = st.Read(ctx, buf)
		st.Close()
		return err
	})
	reader(&getRec, func() error {
		rngMu.Lock()
		r := sample[rng.Intn(len(sample))]
		rngMu.Unlock()
		_, err := s.GetRecord(ctx, "OMM.fbs", r.CID)
		return err
	})
	reader(&queue, func() error {
		_, err := s.ri.Query(ctx, Request{SQL: "SELECT 1"})
		return err
	})
	loadStart := loadAvg()
	ingested := ingest(dur, &stop)
	stop.Store(true)
	wg.Wait()
	rate := float64(ingested) / dur.Seconds()
	ns := s.native.Stats()
	t.Logf("MEASURED reads during saturating ingest (%s, 1 writer, 50 partitions, %d interactive lanes, 7 concurrent readers, %s; load %.1f at start):",
		dur, envInt("SDN_FORMAT2_LANES", 2), machine(), loadStart)
	t.Logf("  ingest %.0f records/s with reads, %.0f records/s alone (%.0f%%)", rate, baseRate, 100*rate/baseRate)
	t.Logf("  LiveRecordBytes %s", counters.String())
	t.Logf("  flatsql_types (DataSummary) %s", types.String())
	t.Logf("  OMM window LIMIT 1000 %s", window.String())
	t.Logf("  %s source %q byte probe %s", srcSchema, srcName, probe.String())
	t.Logf("  %s source %q first payload byte %s", srcSchema, srcName, srcFirst.String())
	t.Logf("  GetRecord %s", getRec.String())
	t.Logf("  SELECT 1 (lane queue wait) %s", queue.String())
	t.Logf("  host store lock: %d acquisitions, max hold %s; read errors %d", ns.LockAcquisitions, ns.LockHoldMax, readErrs.Load())
	if readErrs.Load() != 0 {
		t.Fatalf("%d reads failed during ingest", readErrs.Load())
	}
}

const routerKillChildEnv = "SDN_FORMAT2_KILL_CHILD_ROOT"

// TestRouterKillChild ingests batches and appends each acked batch's CIDs
// to <root>/acked.log (synced), until killed.
func TestRouterKillChild(t *testing.T) {
	root := os.Getenv(routerKillChildEnv)
	if root == "" {
		t.Skip("runs only as a child")
	}
	s := openAt(t, root, "", 1)
	f, err := os.OpenFile(filepath.Join(root, "acked.log"), os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	gen := time.Now().UnixNano()
	for b := 0; ; b++ {
		puts := make([]Put, 50)
		for i := range puts {
			puts[i] = Put{Data: testOMM(uint32(b*50+i), time.Unix(0, gen).Add(time.Duration(b*50+i)*time.Millisecond), fmt.Sprintf("K%d-%d-%d", gen, b, i))}
		}
		peer := fmt.Sprintf("source:kill%d", b%4)
		res, err := s.PutBatch(ctx, "OMM.fbs", puts, peer, nil, &Tags{ProviderID: "prov", SourceName: peer, BatchID: "k"})
		if err != nil {
			t.Fatal(err)
		}
		var lines strings.Builder
		for i, r := range res {
			if r.Err == nil {
				fmt.Fprintf(&lines, "%s %s %s\n", r.CID, peer, hex.EncodeToString(puts[i].Data))
			}
		}
		if _, err := f.WriteString(lines.String()); err != nil {
			t.Fatal(err)
		}
		if err := f.Sync(); err != nil {
			t.Fatal(err)
		}
	}
}

// T6 #5: kill -9 during router ingest, many times: every acked record is
// present with its bytes, and resending every acked record adds 0 rows.
func TestRouterKill9KeepsEveryAckedRecord(t *testing.T) {
	requireEngine(t)
	kills := envInt("SDN_FORMAT2_KILLS", 10)
	root := t.TempDir()
	rng := rand.New(rand.NewSource(time.Now().UnixNano()))
	for k := 0; k < kills; k++ {
		cmd := exec.Command(os.Args[0], "-test.run", "^TestRouterKillChild$", "-test.count=1")
		cmd.Env = append(os.Environ(), routerKillChildEnv+"="+root)
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		time.Sleep(300*time.Millisecond + time.Duration(rng.Int63n(int64(1200*time.Millisecond))))
		_ = cmd.Process.Signal(syscall.SIGKILL)
		_ = cmd.Wait()
	}
	s := openAt(t, root, "", 1)
	defer s.Close()
	ctx := context.Background()
	f, err := os.Open(filepath.Join(root, "acked.log"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	byPeer := map[string][]Put{}
	var n int
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		parts := strings.Fields(sc.Text())
		if len(parts) != 3 {
			continue // a torn last line (the kill landed inside the append)
		}
		data, err := hex.DecodeString(parts[2])
		if err != nil {
			continue
		}
		rec, err := s.GetRecord(ctx, "OMM.fbs", parts[0])
		if err != nil {
			t.Fatalf("acked record %s missing after %d kills: %v", parts[0], kills, err)
		}
		if string(rec.Data) != string(data) {
			t.Fatalf("acked record %s: bytes differ", parts[0])
		}
		byPeer[parts[1]] = append(byPeer[parts[1]], Put{Data: data})
		n++
	}
	before, err := s.Partitions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for peer, puts := range byPeer {
		for len(puts) > 0 {
			k := len(puts)
			if k > 500 {
				k = 500
			}
			if _, err := s.PutBatch(ctx, "OMM.fbs", puts[:k], peer, nil, &Tags{ProviderID: "prov", SourceName: peer, BatchID: "k"}); err != nil {
				t.Fatal(err)
			}
			puts = puts[k:]
		}
	}
	after, err := s.Partitions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	sum := func(ps []PartitionCounter) (total int64) {
		for _, p := range ps {
			total += p.Total
		}
		return
	}
	if sum(after) != sum(before) {
		t.Fatalf("resending %d acked records added %d rows", n, sum(after)-sum(before))
	}
	t.Logf("MEASURED %d kill -9 during router ingest: %d acked records all present with their bytes; resend added 0 rows (%s)", kills, n, machine())
}
