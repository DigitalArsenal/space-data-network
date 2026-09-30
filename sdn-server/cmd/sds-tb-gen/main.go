// Command sds-tb-gen is the terabyte harness's SDN end-to-end tier (flatsql
// TB audit §4 "Generating TB quickly" 3): the node's own write path, driven
// the way ingest drives it, on store format 2.
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
// flight and reports the ones still stuck.
//
// The engine runs AOT (prewarmed into the daemon's cache first, as the
// `prewarm-aot` command does); SDN_STORE_FORMAT=2 is set for the store.
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
)

type config struct {
	store, corpus, csv, schemas, mix string
	producers, peersPerType, batch   int
	duration, sample, stall, readEvy time.Duration
	maxRecords                       int64
	supersedePct                     float64
	prewarm                          bool
	cpuProfile                       string
}

type stats struct {
	records, calls, errors, bytes atomic.Int64
	mu                            sync.Mutex
	lat                           []float64 // this window, ms
	inflight                      map[int]time.Time
	firstErr                      []string
	stalledReported               map[int]bool
	partitions                    sync.Map // "schema/peer" -> true
	nPartitions                   atomic.Int64
	cidMu                         sync.Mutex
	cids                          map[string][]string // schema -> recent CIDs
}

type readStat struct {
	name string
	ms   float64
	err  string
}

func main() {
	var c config
	flag.StringVar(&c.store, "store", "", "store directory (created as a fresh format-2 store when empty)")
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
	flag.DurationVar(&c.readEvy, "read-every", 10*time.Second, "read probe interval")
	flag.Float64Var(&c.supersedePct, "supersede-pct", 15, "share of a supersede standard's records that supersede the previous clone")
	flag.BoolVar(&c.prewarm, "prewarm", true, "AOT-compile the engines into the daemon's cache first")
	flag.StringVar(&c.cpuProfile, "cpuprofile", "", "write a CPU profile of the run here")
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
	if c.store == "" || c.corpus == "" {
		return fmt.Errorf("-store and -corpus are required")
	}
	if c.csv == "" {
		c.csv = filepath.Join(filepath.Dir(c.store), "sds-tb-gen")
	}
	fmt.Printf("# %s/%s, %d CPUs, %s\n", runtime.GOOS, runtime.GOARCH, runtime.NumCPU(), loadLine())
	cor, err := loadCorpus(c.corpus)
	if err != nil {
		return fmt.Errorf("corpus: %w", err)
	}
	type std struct {
		name        string
		seeds       []*seed
		share       float64
		counter     atomic.Uint64
		turn        atomic.Uint64
		supersedes  bool
		fetch       []atomic.Uint64
		seedSource  string
		seedProvide string
	}
	var stds []*std
	shares := map[string]float64{}
	for _, kv := range strings.Split(c.mix, ",") {
		var name string
		var w float64
		if i := strings.IndexByte(kv, '='); i > 0 {
			name = kv[:i]
			fmt.Sscanf(kv[i+1:], "%g", &w)
			shares[name] = w
		}
	}
	for _, s := range strings.Split(c.schemas, ",") {
		s = strings.TrimSpace(s)
		name := strings.TrimSuffix(s, ".fbs")
		seeds := cor.byFID["$"+name]
		if len(seeds) == 0 {
			return fmt.Errorf("corpus has no %s record", name)
		}
		st := &std{name: s, seeds: seeds, share: shares[name], supersedes: name == "CAT",
			fetch: make([]atomic.Uint64, c.peersPerType), seedSource: seeds[0].source, seedProvide: seeds[0].provider}
		if st.seedSource == "" {
			st.seedSource = strings.ToLower(name) + "-source"
		}
		if st.share <= 0 {
			st.share = 1
		}
		stds = append(stds, st)
		fmt.Printf("# %s: %d seed records (source %q)\n", s, len(seeds), st.seedSource)
	}
	var total float64
	for _, s := range stds {
		total += s.share
	}

	if os.Getenv(format2.FormatEnv) == "" {
		os.Setenv(format2.FormatEnv, "2")
	}
	if c.prewarm {
		cache := storage.EngineAOTCacheDir()
		if p, present, err := flatsqlrt.PrewarmEngineAOT(cache); err != nil {
			return fmt.Errorf("prewarm the FlatSQL engine: %w", err)
		} else {
			fmt.Printf("# engine AOT %s (present before: %v)\n", p, present)
		}
		if p, present, err := flatsqlrt.PrewarmPSThreadsAOT(cache); err != nil {
			return fmt.Errorf("prewarm the partition-store engine: %w", err)
		} else {
			fmt.Printf("# partition-store engine AOT %s (%s, present before: %v)\n", p, flatsqlrt.PSThreadsPackage, present)
		}
	}
	v, err := sds.NewValidator(nil)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(c.store, 0o700); err != nil {
		return err
	}
	t0 := time.Now()
	s, err := storage.NewFlatSQLStore(c.store, v)
	if err != nil {
		return fmt.Errorf("open store: %w", err)
	}
	if !s.Format2() {
		return fmt.Errorf("the store opened as format 1")
	}
	fmt.Printf("# store open %.1f s (format 2)\n", time.Since(t0).Seconds())

	if c.cpuProfile != "" {
		if f, err := os.Create(c.cpuProfile); err == nil {
			_ = pprof.StartCPUProfile(f)
			defer func() { pprof.StopCPUProfile(); f.Close() }()
		}
	}
	st := &stats{inflight: map[int]time.Time{}, stalledReported: map[int]bool{}, cids: map[string][]string{}}
	var stop atomic.Bool
	var wg sync.WaitGroup
	start := time.Now()
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
				// Peers in turn: every partition is written once before any twice.
				peer := int(sd.turn.Add(1)-1) % c.peersPerType
				fetch := sd.fetch[peer].Add(1)
				recs := make([][]byte, 0, c.batch)
				var bytes int64
				var sample string
				for i := 0; i < c.batch; i++ {
					k := sd.counter.Add(1) - 1
					keep := sd.supersedes && rng.Float64()*100 < c.supersedePct
					_, fb := clone(sd.seeds, k, keep)
					recs = append(recs, fb)
					bytes += int64(len(fb))
					if i == c.batch/2 {
						sample = cidOf(fb)
					}
				}
				pid := peerID(sd.name, peer)
				tags := storage.SourceTags{
					ProviderID: fmt.Sprintf("provider-%d", peer%64),
					SourceName: fmt.Sprintf("%s-%d", sd.seedSource, peer%16),
					BatchID:    fmt.Sprintf("%s-p%d-f%d", strings.TrimSuffix(sd.name, ".fbs"), peer, fetch),
				}
				key := sd.name + "/" + pid
				if _, loaded := st.partitions.LoadOrStore(key, true); !loaded {
					st.nPartitions.Add(1)
				}
				st.mu.Lock()
				st.inflight[w] = time.Now()
				st.mu.Unlock()
				t := time.Now()
				_, err := s.StoreBatchWithSourceTags(sd.name, recs, pid, nil, tags)
				ms := float64(time.Since(t).Microseconds()) / 1000
				st.mu.Lock()
				delete(st.inflight, w)
				st.lat = append(st.lat, ms)
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
				if c.maxRecords > 0 && st.records.Load() >= c.maxRecords {
					stop.Store(true)
				}
			}
		}(w)
	}

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
	go func() { closed <- s.Close() }()
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
