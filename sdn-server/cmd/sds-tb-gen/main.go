// Command sds-tb-gen is the terabyte harness's SDN end-to-end tier (flatsql
// TB audit §4 "Generating TB quickly" 3): the node's own write path, driven
// the way ingest drives it, on store format 4 (the default; -format 1 or 2
// for the others).
//
//	sds-tb-gen -store <dir> -corpus <corpus.tbc2> [-producers 64] [-peers-per-type 200]
//	           [-batch 4096] [-duration 20m] [-csv <prefix>]
//
// About -producers goroutines each call StoreBatchWithSourceTags with -batch
// records of one standard from one producer peer (peers in turn, so every
// partition is written once before any twice), a new BatchID for every
// fetch of every (peer, standard) partition, so the lane tables grow the way
// they do in production (a fetch is a batch). Records are clones of real
// host-02 records (the TBC2 seed corpus: flatsql `flatsql_ps_test
// --test=tb_corpus_export`) with identity, epoch and numeric fields mutated
// per clone, so every clone has its own CID; -supersede-pct of the records of
// a standard with a supersede rule reuse the previous clone's identity.
// -peers-per-type producer peers per standard: above 128 a standard's type
// head no longer carries labeled_through (audit B1).
//
// Every -sample it writes a CSV row (<prefix>.samples.csv) and a line: records
// and calls a second, call latency p50/p99/max over the window, calls in
// flight and STALLED (in flight longer than -stall: a write that never
// returns, audit B1), partitions written, Go heap, RSS, file descriptors, and
// the latency of the node's reads on the same store: GetRecord hit and miss
// (M7), DiskUsageBytes and PeerStorageBytes (B8), DataSummary (B7). It stops
// at -duration or -max-records, then waits two -stall periods for calls in
// flight and reports the ones still stuck. When no call returns for -hang
// while calls are in flight (a growth step's pause does not count), it stops
// at once (exit 3) with the stacks.
//
// The engines run AOT (prewarmed into the daemon's cache first, as the
// `prewarm-aot` command does); SDN_STORE_FORMAT is set to -format for the
// store. Fixture-derived seeds and the count-scaled growth steps of the
// format-4 evidence harness: growth.go.
package main

import (
	"crypto/sha256"
	"encoding/base32"
	"flag"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"runtime"
	"runtime/pprof"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/flatsqlrt"
	"github.com/spacedatanetwork/sdn-server/internal/sds"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
	"github.com/spacedatanetwork/sdn-server/internal/storage/format2"
	"github.com/spacedatanetwork/sdn-server/internal/storage/format4proof"
)

type config struct {
	store, corpus, csv, schemas, mix string
	producers, peersPerType, batch   int
	duration, sample, stall, readEvy time.Duration
	hang                             time.Duration
	maxRecords                       int64
	supersedePct                     float64
	prewarm                          bool
	cpuProfile                       string
	format, seeds, steps, results    string // growth.go
	zipf, minFreeGiB                 float64
	// feedPerPeer gives every producer peer its own source feed (provider
	// and source), so a format-4 store grows one feed file per peer per
	// standard (C-37) instead of the 64 x 16 feeds the peers share.
	feedPerPeer bool
}

type stats struct {
	records, calls, errors, bytes atomic.Int64
	lastDone                      atomic.Int64 // unix ns of the last write call that returned
	mu                            sync.Mutex
	lat                           []float64 // this window, ms
	inflight                      map[int]time.Time
	firstErr                      []string
	stalledReported               map[int]bool
	partitions                    sync.Map // "schema/peer" -> true
	nPartitions                   atomic.Int64
	cidMu                         sync.Mutex
	cids                          map[string][]string // schema -> recent CIDs
	stepLat                       []float64           // since the last growth step, ms (under mu)
	stepInserted, inserted        int64               // records stored (under mu)
}

// std is one standard the writers write: its share of the calls, its
// counters, and gen, record k of the standard (keep: reuse the previous
// clone's identity, a supersede).
type std struct {
	name       string
	share      float64
	counter    atomic.Uint64
	turn       atomic.Uint64
	supersedes bool
	fetch      []atomic.Uint64
	seedSource string
	gen        func(k uint64, keep bool) []byte
}

type readStat struct {
	name string
	ms   float64
	err  string
}

func main() {
	var c config
	flag.StringVar(&c.store, "store", "", "store directory (created as a fresh store of -format when empty)")
	flag.StringVar(&c.corpus, "corpus", "", "TBC2 seed corpus (flatsql_ps_test --test=tb_corpus_export)")
	flag.StringVar(&c.csv, "csv", "", "CSV prefix (default <store>/../sds-tb-gen)")
	flag.StringVar(&c.schemas, "schemas", "OMM.fbs,CAT.fbs,IQC.fbs", "standards written")
	flag.StringVar(&c.mix, "mix", "OMM=0.6,CAT=0.2,IQC=0.2", "share of calls per standard")
	flag.IntVar(&c.producers, "producers", 64, "concurrent writers")
	flag.IntVar(&c.peersPerType, "peers-per-type", 200, "producer peers per standard (> 128: audit B1)")
	flag.IntVar(&c.batch, "batch", 4096, "records per StoreBatchWithSourceTags call")
	flag.DurationVar(&c.duration, "duration", 20*time.Minute, "run time")
	flag.Int64Var(&c.maxRecords, "max-records", 0, "stop after this many records (0: none)")
	flag.DurationVar(&c.sample, "sample", 10*time.Second, "sample interval")
	flag.DurationVar(&c.stall, "stall", 60*time.Second, "a call in flight longer than this is stalled")
	flag.DurationVar(&c.hang, "hang", 10*time.Minute, "no write call returned for this long with calls in flight: the run stops (exit 3) with the goroutine stacks")
	flag.DurationVar(&c.readEvy, "read-every", 10*time.Second, "read probe interval")
	flag.Float64Var(&c.supersedePct, "supersede-pct", 15, "share of a supersede standard's records that supersede the previous clone")
	flag.BoolVar(&c.prewarm, "prewarm", true, "AOT-compile the engines into the daemon's cache first")
	flag.StringVar(&c.cpuProfile, "cpuprofile", "", "write a CPU profile of the run here")
	flag.StringVar(&c.format, "format", "4", "store format: 1, 2 or 4 (\"sqlite\", the default)")
	flag.StringVar(&c.seeds, "seeds", "", "format4proof work directory whose prepared inputs seed the records (instead of -corpus)")
	flag.Float64Var(&c.zipf, "zipf", 0, "draw producer peers Zipf(s), s > 1 (0: in turn)")
	flag.StringVar(&c.steps, "steps", "", "growth steps ID=records,… (records in the store, its start included): measure at each")
	flag.StringVar(&c.results, "results", "", "format4proof results directory for the growth steps")
	flag.Float64Var(&c.minFreeGiB, "min-free-gib", 0, "stop when the store's volume has less free space, GiB (0: no floor; the Mac runs keep 120)")
	flag.BoolVar(&c.feedPerPeer, "feed-per-peer", false, "one source feed (provider, source) per producer peer: format 4 grows a feed file per peer (C-37)")
	flag.Parse()
	if err := run(c); err != nil {
		fmt.Fprintln(os.Stderr, "sds-tb-gen:", err)
		os.Exit(1)
	}
}

func cidOf(fb []byte) string {
	sum := sha256.Sum256(fb)
	raw := append([]byte{0x01, 0x55, 0x12, 0x20}, sum[:]...)
	return "b" + strings.ToLower(strings.TrimRight(base32.StdEncoding.EncodeToString(raw), "="))
}

func peerID(schema string, i int) string {
	// Shaped like a libp2p peer id (53 characters), distinct per (standard, index).
	id := fmt.Sprintf("16Uiu2HAmTbGen%s%06d", strings.TrimSuffix(schema, ".fbs"), i)
	return id + strings.Repeat("A", max(0, 53-len(id)))
}

func pct(v []float64, q float64) float64 {
	if len(v) == 0 {
		return 0
	}
	s := append([]float64(nil), v...)
	sort.Float64s(s)
	i := int(q*float64(len(s))+0.999999) - 1
	if i < 0 {
		i = 0
	}
	if i >= len(s) {
		i = len(s) - 1
	}
	return s[i]
}

func run(c config) error {
	if c.store == "" || (c.corpus == "" && c.seeds == "") {
		return fmt.Errorf("-store and -corpus (or -seeds) are required")
	}
	arm, err := armOf(c.format)
	if err != nil {
		return err
	}
	steps, err := parseSteps(c.steps)
	if err != nil {
		return err
	}
	if len(steps) > 0 && c.results == "" {
		return fmt.Errorf("-steps needs -results")
	}
	if c.csv == "" {
		c.csv = filepath.Join(filepath.Dir(c.store), "sds-tb-gen")
	}
	fmt.Printf("# %s/%s, %d CPUs, %s\n", runtime.GOOS, runtime.GOARCH, runtime.NumCPU(), loadLine())
	var stds []*std
	if c.seeds != "" {
		stds, err = loadSeedStandards(c)
	} else {
		stds, err = loadStandards(c)
	}
	if err != nil {
		return err
	}
	var total float64
	for _, s := range stds {
		total += s.share
	}

	s, err := openStore(c, arm)
	if err != nil {
		return err
	}
	ref := &storeRef{s: s}
	var startRecords int64
	if len(steps) > 0 {
		if startRecords, err = storeRecordCount(s, stds); err != nil {
			return err
		}
		fmt.Printf("# growth: %d records at start; steps %v\n", startRecords, steps)
	}
	zipfs := map[string]*zipfPeer{}
	for i, sd := range stds {
		if z := newZipfPeer(c.zipf, c.peersPerType, int64(0x21F+i)); z != nil {
			zipfs[sd.name] = z
		}
	}

	if c.cpuProfile != "" {
		if f, err := os.Create(c.cpuProfile); err == nil {
			_ = pprof.StartCPUProfile(f)
			defer func() { pprof.StopCPUProfile(); f.Close() }()
		}
	}
	st := &stats{inflight: map[int]time.Time{}, stalledReported: map[int]bool{}, cids: map[string][]string{}}
	stepSince := time.Now()
	storeTotal := func() int64 {
		st.mu.Lock()
		defer st.mu.Unlock()
		return startRecords + st.inserted
	}
	stepped := len(steps) > 0                                                 // the sampler stops the run after the last step
	steps = runDueSteps(c, ref, arm, steps, storeTotal, stds, st, &stepSince) // a step at the starting size
	var stop atomic.Bool
	var wg sync.WaitGroup
	start := time.Now()
	st.lastDone.Store(start.UnixNano())
	for w := 0; w < c.producers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			rng := rand.New(rand.NewSource(int64(0x7B00 + w)))
			for !stop.Load() {
				// A standard by share, a peer uniformly.
				x := rng.Float64() * total
				var sd *std
				for _, s := range stds {
					if x < s.share {
						sd = s
						break
					}
					x -= s.share
				}
				if sd == nil {
					sd = stds[len(stds)-1]
				}
				// Peers in turn: every partition is written once before any
				// twice; or Zipf-drawn (-zipf).
				peer := int(sd.turn.Add(1)-1) % c.peersPerType
				if z := zipfs[sd.name]; z != nil {
					peer = z.next()
				}
				fetch := sd.fetch[peer].Add(1)
				recs := make([][]byte, 0, c.batch)
				var bytes int64
				var sample string
				for i := 0; i < c.batch; i++ {
					k := sd.counter.Add(1) - 1
					keep := sd.supersedes && rng.Float64()*100 < c.supersedePct
					fb := sd.gen(k, keep)
					recs = append(recs, fb)
					bytes += int64(len(fb))
					if i == c.batch/2 {
						sample = cidOf(fb)
					}
				}
				pid := peerID(sd.name, peer)
				prov, src := fmt.Sprintf("provider-%d", peer%64), fmt.Sprintf("%s-%d", sd.seedSource, peer%16)
				if c.feedPerPeer {
					prov, src = fmt.Sprintf("provider-%d", peer), fmt.Sprintf("%s-%d", sd.seedSource, peer)
				}
				tags := storage.SourceTags{
					ProviderID: prov,
					SourceName: src,
					BatchID:    fmt.Sprintf("%s-p%d-f%d", strings.TrimSuffix(sd.name, ".fbs"), peer, fetch),
				}
				key := sd.name + "/" + pid
				if _, loaded := st.partitions.LoadOrStore(key, true); !loaded {
					st.nPartitions.Add(1)
				}
				var n int
				var err error
				var ms float64
				ref.use(func(s *storage.FlatSQLStore) {
					st.mu.Lock()
					st.inflight[w] = time.Now()
					st.mu.Unlock()
					t := time.Now()
					n, err = s.StoreBatchWithSourceTags(sd.name, recs, pid, nil, tags)
					ms = float64(time.Since(t).Microseconds()) / 1000
					st.lastDone.Store(time.Now().UnixNano())
					st.mu.Lock()
					delete(st.inflight, w)
					st.mu.Unlock()
				})
				st.mu.Lock()
				st.lat = append(st.lat, ms)
				st.stepLat = append(st.stepLat, ms)
				st.stepInserted += int64(n)
				st.inserted += int64(n)
				if err != nil && len(st.firstErr) < 10 {
					st.firstErr = append(st.firstErr, fmt.Sprintf("%s %s: %v", sd.name, pid, err))
				}
				st.mu.Unlock()
				st.calls.Add(1)
				if err != nil {
					st.errors.Add(1)
					continue
				}
				st.records.Add(int64(len(recs)))
				st.bytes.Add(bytes)
				st.cidMu.Lock()
				l := st.cids[sd.name]
				if len(l) < 1024 {
					l = append(l, sample)
				} else {
					l[rng.Intn(len(l))] = sample
				}
				st.cids[sd.name] = l
				st.cidMu.Unlock()
				if c.maxRecords > 0 && st.records.Load() >= c.maxRecords && !stepped {
					stop.Store(true)
				}
			}
		}(w)
	}

	// The hang watchdog reads only in-memory counters, so it fires even when
	// the sampler waits on the store (a growth step's write lock waits for
	// the calls in flight). A store that never returns a write is a FAIL
	// with evidence, not a run that sits for hours. The idle time runs from
	// the later of the last returned call and the oldest call in flight: a
	// growth step holds the writers before their calls start (they wait on
	// the store's lock, not in the store), so the calls that resume after a
	// long step are not hung for the step's length.
	go func() {
		for range time.Tick(c.sample) {
			st.mu.Lock()
			inflight := len(st.inflight)
			since := time.Unix(0, st.lastDone.Load())
			var oldest time.Time
			for _, t0 := range st.inflight {
				if oldest.IsZero() || t0.Before(oldest) {
					oldest = t0
				}
			}
			st.mu.Unlock()
			if oldest.After(since) {
				since = oldest
			}
			idle := time.Since(since)
			if inflight == 0 || idle < c.hang {
				continue
			}
			fmt.Printf("# HUNG: no write call returned for %s with %d in flight (records %d, partitions %d, run %.0f s)\n",
				idle.Round(time.Second), inflight, st.records.Load(), st.nPartitions.Load(), time.Since(start).Seconds())
			dumpStacks(c.csv + ".hung-stacks.txt")
			pprof.StopCPUProfile()
			os.Exit(3)
		}
	}()

	// Read probes, on their own goroutine (a slow read never delays a sample).
	var readMu sync.Mutex
	var reads []readStat
	readDone := make(chan struct{})
	go func() {
		defer close(readDone)
		rng := rand.New(rand.NewSource(0xBEAD))
		probe := func(name string, f func() error) readStat {
			t := time.Now()
			err := f()
			r := readStat{name: name, ms: float64(time.Since(t).Microseconds()) / 1000}
			if err != nil {
				r.err = err.Error()
			}
			return r
		}
		for !stop.Load() {
			time.Sleep(c.readEvy)
			var round []readStat
			ref.use(func(s *storage.FlatSQLStore) {
				sd := stds[0]
				st.cidMu.Lock()
				l := st.cids[sd.name]
				hit := ""
				if len(l) > 0 {
					hit = l[rng.Intn(len(l))]
				}
				st.cidMu.Unlock()
				if hit != "" {
					round = append(round, probe("get_hit", func() error { _, err := s.GetRecord(sd.name, hit); return err }))
				}
				round = append(round, probe("get_miss", func() error {
					_, err := s.GetRecord(sd.name, cidOf([]byte(fmt.Sprintf("miss-%d", rng.Int63()))))
					if err != nil && strings.Contains(strings.ToLower(err.Error()), "not found") {
						return nil
					}
					return err
				}))
				round = append(round, probe("disk_usage", func() error { _, err := s.DiskUsageBytes(); return err }))
				round = append(round, probe("peer_bytes", func() error {
					_, err := s.PeerStorageBytes(peerID(sd.name, rng.Intn(c.peersPerType)))
					return err
				}))
				round = append(round, probe("data_summary", func() error { _, err := s.DataSummary(); return err }))
			})
			readMu.Lock()
			reads = round
			readMu.Unlock()
		}
	}()

	csv, err := os.Create(c.csv + ".samples.csv")
	if err != nil {
		return err
	}
	defer csv.Close()
	fmt.Fprintln(csv, "t_s,records,rec_s,mb_s,calls,call_p50_ms,call_p99_ms,call_max_ms,inflight,stalled,oldest_inflight_s,partitions,errors,go_heap_bytes,go_sys_bytes,rss_bytes,fds,get_hit_ms,get_miss_ms,disk_usage_ms,peer_bytes_ms,data_summary_ms,read_errors")
	var lastRecs, lastBytes int64
	last := time.Now()
	for {
		time.Sleep(c.sample)
		now := time.Now()
		st.mu.Lock()
		lat := st.lat
		st.lat = nil
		inflight := len(st.inflight)
		var oldest time.Duration
		stalled := 0
		for w, t0 := range st.inflight {
			age := now.Sub(t0)
			if age > oldest {
				oldest = age
			}
			if age > c.stall {
				stalled++
				if !st.stalledReported[w] {
					st.stalledReported[w] = true
					fmt.Printf("STALL writer %d: a StoreBatchWithSourceTags call in flight for %.0f s (%d partitions written so far)\n",
						w, age.Seconds(), st.nPartitions.Load())
					if len(st.stalledReported) == 1 {
						dumpStacks(c.csv + ".stall-stacks.txt") // where the first stalled call waits
					}
				}
			}
		}
		errs := st.firstErr
		st.mu.Unlock()
		var ms runtime.MemStats
		runtime.ReadMemStats(&ms)
		readMu.Lock()
		rd := reads
		readMu.Unlock()
		rv := map[string]float64{}
		readErrs := 0
		for _, r := range rd {
			rv[r.name] = r.ms
			if r.err != "" {
				readErrs++
			}
		}
		recs, bytes := st.records.Load(), st.bytes.Load()
		dt := now.Sub(last).Seconds()
		maxLat := 0.0
		for _, x := range lat {
			if x > maxLat {
				maxLat = x
			}
		}
		fmt.Fprintf(csv, "%.1f,%d,%.1f,%.3f,%d,%.2f,%.2f,%.2f,%d,%d,%.1f,%d,%d,%d,%d,%d,%d,%.2f,%.2f,%.2f,%.2f,%.2f,%d\n",
			now.Sub(start).Seconds(), recs, float64(recs-lastRecs)/dt, float64(bytes-lastBytes)/dt/1e6, st.calls.Load(),
			pct(lat, 0.5), pct(lat, 0.99), maxLat, inflight, stalled, oldest.Seconds(), st.nPartitions.Load(),
			st.errors.Load(), ms.HeapAlloc, ms.Sys, rssBytes(), openFDs(), rv["get_hit"], rv["get_miss"],
			rv["disk_usage"], rv["peer_bytes"], rv["data_summary"], readErrs)
		fmt.Printf("t=%.0fs records=%d (%.0f/s, %.1f MB/s) calls=%d p50=%.0fms p99=%.0fms max=%.0fms inflight=%d stalled=%d "+
			"partitions=%d errors=%d heap=%.0fMB rss=%.0fMB fds=%d get_hit=%.1fms disk_usage=%.1fms peer_bytes=%.1fms summary=%.1fms\n",
			now.Sub(start).Seconds(), recs, float64(recs-lastRecs)/dt, float64(bytes-lastBytes)/dt/1e6, st.calls.Load(),
			pct(lat, 0.5), pct(lat, 0.99), maxLat, inflight, stalled, st.nPartitions.Load(), st.errors.Load(),
			float64(ms.HeapAlloc)/1e6, float64(rssBytes())/1e6, openFDs(), rv["get_hit"], rv["disk_usage"],
			rv["peer_bytes"], rv["data_summary"])
		for _, e := range errs {
			fmt.Println("ERROR", e)
		}
		st.mu.Lock()
		st.firstErr = nil
		st.mu.Unlock()
		lastRecs, lastBytes, last = recs, bytes, now
		if len(steps) > 0 {
			steps = runDueSteps(c, ref, arm, steps, storeTotal, stds, st, &stepSince)
			if len(steps) == 0 {
				stop.Store(true)
			}
		}
		if free, err := format4proof.FreeBytes(c.store); err == nil && float64(free) < c.minFreeGiB*(1<<30) {
			fmt.Printf("# stopping: %.1f GiB free on the store's volume, below -min-free-gib %.0f\n", float64(free)/(1<<30), c.minFreeGiB)
			stop.Store(true)
		}
		if stop.Load() || now.Sub(start) >= c.duration {
			break
		}
	}
	stop.Store(true)
	fmt.Printf("# stopping: waiting up to %s for %d calls in flight\n", 2*c.stall, func() int {
		st.mu.Lock()
		defer st.mu.Unlock()
		return len(st.inflight)
	}())
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
		fmt.Println("# every writer returned")
	case <-time.After(2 * c.stall):
		st.mu.Lock()
		fmt.Printf("# STUCK: %d StoreBatchWithSourceTags calls never returned (oldest in flight since the run's %.0f s)\n",
			len(st.inflight), func() float64 {
				o := time.Now()
				for _, t := range st.inflight {
					if t.Before(o) {
						o = t
					}
				}
				return o.Sub(start).Seconds()
			}())
		st.mu.Unlock()
		dumpStacks(c.csv + ".stuck-stacks.txt")
		pprof.StopCPUProfile()
		os.Exit(3)
	}
	<-readDone
	closed := make(chan error, 1)
	go func() { closed <- ref.s.Close() }()
	select {
	case err := <-closed:
		fmt.Printf("# store closed (%v); %d records, %d calls, %d errors, %d partitions in %.0f s\n", err,
			st.records.Load(), st.calls.Load(), st.errors.Load(), st.nPartitions.Load(), time.Since(start).Seconds())
	case <-time.After(2 * time.Minute):
		fmt.Println("# STUCK: store close did not return in 2 minutes")
		dumpStacks(c.csv + ".stuck-stacks.txt")
		os.Exit(3)
	}
	return nil
}

// loadStandards reads the TBC2 corpus and builds the standards the writers
// write, with their shares of the calls.
func loadStandards(c config) ([]*std, error) {
	cor, err := loadCorpus(c.corpus)
	if err != nil {
		return nil, fmt.Errorf("corpus: %w", err)
	}
	shares := parseMix(c.mix)
	var stds []*std
	for _, s := range strings.Split(c.schemas, ",") {
		s = strings.TrimSpace(s)
		name := strings.TrimSuffix(s, ".fbs")
		seeds := cor.byFID["$"+name]
		if len(seeds) == 0 {
			return nil, fmt.Errorf("corpus has no %s record", name)
		}
		st := &std{name: s, share: shares[name], supersedes: name == "CAT",
			fetch: make([]atomic.Uint64, c.peersPerType), seedSource: seeds[0].source,
			gen: func(k uint64, keep bool) []byte { _, fb := clone(seeds, k, keep); return fb }}
		if st.seedSource == "" {
			st.seedSource = strings.ToLower(name) + "-source"
		}
		if st.share <= 0 {
			st.share = 1
		}
		stds = append(stds, st)
		fmt.Printf("# %s: %d seed records (source %q)\n", s, len(seeds), st.seedSource)
	}
	return stds, nil
}

// parseMix reads "OMM=0.6,CAT=0.2" into shares by standard.
func parseMix(mix string) map[string]float64 {
	shares := map[string]float64{}
	for _, kv := range strings.Split(mix, ",") {
		if i := strings.IndexByte(kv, '='); i > 0 {
			var w float64
			fmt.Sscanf(kv[i+1:], "%g", &w)
			shares[kv[:i]] = w
		}
	}
	return shares
}

// openStore prewarms the engines (as `prewarm-aot` does) and opens the store
// in the arm's format (SDN_STORE_FORMAT = -format), as the daemon opens it.
func openStore(c config, arm string) (*storage.FlatSQLStore, error) {
	os.Setenv(format2.FormatEnv, format4proof.ArmFormat(arm))
	if c.prewarm {
		cache := storage.EngineAOTCacheDir()
		if p, present, err := flatsqlrt.PrewarmEngineAOT(cache); err != nil {
			return nil, fmt.Errorf("prewarm the FlatSQL engine: %w", err)
		} else {
			fmt.Printf("# engine AOT %s (present before: %v)\n", p, present)
		}
		if p, present, err := flatsqlrt.PrewarmPSThreadsAOT(cache); err != nil {
			return nil, fmt.Errorf("prewarm the partition-store engine: %w", err)
		} else {
			fmt.Printf("# partition-store engine AOT %s (%s, present before: %v)\n", p, flatsqlrt.PSThreadsPackage, present)
		}
		if p, present, err := flatsqlrt.PrewarmP4ThreadsAOT(cache); err != nil {
			return nil, fmt.Errorf("prewarm the format-4 engine: %w", err)
		} else {
			fmt.Printf("# format-4 engine AOT %s (%s, present before: %v)\n", p, flatsqlrt.P4ThreadsPackage, present)
		}
	}
	v, err := sds.NewValidator(nil)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(c.store, 0o700); err != nil {
		return nil, err
	}
	t0 := time.Now()
	s, err := storage.NewFlatSQLStore(c.store, v)
	if err != nil {
		return nil, fmt.Errorf("open store: %w", err)
	}
	f4 := format4proof.IsFormat4(s)
	ok := map[string]bool{format4proof.ArmF1: !s.Format2() && !f4, format4proof.ArmF2: s.Format2(), format4proof.ArmS: f4}[arm]
	if !ok {
		_ = s.Close()
		return nil, fmt.Errorf("the store did not open in format %s (format2=%v format4=%v)", c.format, s.Format2(), f4)
	}
	fmt.Printf("# store open %.1f s (format %s)\n", time.Since(t0).Seconds(), map[string]string{format4proof.ArmF1: "1",
		format4proof.ArmF2: "2", format4proof.ArmS: "4"}[arm])
	return s, nil
}

// dumpStacks writes every goroutine's stack (where the stuck calls wait).
func dumpStacks(path string) {
	f, err := os.Create(path)
	if err != nil {
		return
	}
	defer f.Close()
	_ = pprof.Lookup("goroutine").WriteTo(f, 2)
	fmt.Printf("# goroutine stacks: %s\n", path)
}
