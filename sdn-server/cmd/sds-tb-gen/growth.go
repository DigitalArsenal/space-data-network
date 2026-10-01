package main

// growth.go: the count-scaled growth tier of the format-4 evidence harness
// (internal/storage/format4proof; benchset growth G0–G4). With -steps the
// writers pause at each record target; the store is closed, measured on
// disk, reopened (the M02 open time) and probed with a fixed read set, cold
// then warm, and the step is written as a format4proof growth run, so the
// gate report fits each metric's per-doubling slope per store format.
//
//   -format 1|2|4        store format (4: SDN_STORE_FORMAT=4, the backend's
//                        format-4 engine; "sqlite" is accepted)
//   -seeds <work dir>    format4proof's prepared inputs (fixture records)
//                        instead of a TBC2 corpus
//   -zipf 1.1            producer peers drawn Zipf(s) instead of in turn, so
//                        partitions grow with records unevenly (tb_meta_tier)
//   -steps G1=17459492,… store record targets (the store's records at start
//                        count toward them)
//   -results <dir>       where the growth runs go
//   -min-free-gib 120    stop when the store's volume has less free space (default 0: off)
//
// Count-scaled steps (benchset G3, G4) write MPE-sized records only:
// -schemas MPE.fbs -mix MPE=1.

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"math"
	mrand "math/rand"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/datasync"
	"github.com/spacedatanetwork/sdn-server/internal/flatsqlrt"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
	"github.com/spacedatanetwork/sdn-server/internal/storage/format4proof"
)

// storeRef holds the open store. Writers and read probes use it under the
// read lock; a growth step takes the write lock (every call in flight
// finishes first), closes the store, measures and reopens it.
type storeRef struct {
	mu sync.RWMutex
	s  *storage.FlatSQLStore
}

func (r *storeRef) use(fn func(s *storage.FlatSQLStore)) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	fn(r.s)
}

// armOf maps -format to the harness arm.
func armOf(format string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(format)) {
	case "1":
		return format4proof.ArmF1, nil
	case "", "2":
		return format4proof.ArmF2, nil
	case "4", "sqlite":
		return format4proof.ArmS, nil
	}
	return "", fmt.Errorf("-format %q: want 1, 2 or 4", format)
}

// seedSets are the prepared inputs a standard clones.
var seedSets = map[string]string{"OMM": format4proof.InputOMMB052, "MPE": format4proof.InputMPEB051,
	"CAT": format4proof.InputCATSatcat, "IQC": format4proof.InputIQC}

// seedLanes are the fixture lanes the seeds come from.
var seedLanes = map[string]string{"OMM": "celestrak-gp", "MPE": "celestrak-gp", "CAT": "celestrak-satcat", "IQC": "IQEngine"}

// growthCloneBase keeps growth clones apart from every clone the harness's
// other runs write (ingest phases: c < 100; W01/W08: 708/709; M01: 709+).
const growthCloneBase = 10000

// loadSeedStandards builds the standards from format4proof's inputs: record
// k is clone growthCloneBase + k/n of seed k mod n (the harness's mutation:
// the same object later for types with an epoch, a new object otherwise; a
// CAT supersede keeps the object).
func loadSeedStandards(c config) ([]*std, error) {
	_, sets, err := format4proof.LoadInputs(c.seeds)
	if err != nil {
		return nil, err
	}
	shares := parseMix(c.mix)
	var stds []*std
	for _, s := range strings.Split(c.schemas, ",") {
		s = strings.TrimSpace(s)
		name := strings.TrimSuffix(s, ".fbs")
		seeds := sets[seedSets[name]]
		if len(seeds) == 0 {
			return nil, fmt.Errorf("-seeds has no %s inputs (run format4proof's prepare step)", name)
		}
		typ, n := name, uint64(len(seeds))
		st := &std{name: s, share: shares[name], supersedes: name == "CAT", fetch: make([]atomic.Uint64, c.peersPerType),
			seedSource: seedLanes[name],
			gen: func(k uint64, keep bool) []byte {
				cl := int64(growthCloneBase + k/n)
				if keep {
					return format4proof.CloneKeepIdentity(seeds[k%n], cl)
				}
				return format4proof.CloneOf(typ, seeds[k%n], cl, nil)
			}}
		if st.share <= 0 {
			st.share = 1
		}
		stds = append(stds, st)
		fmt.Printf("# %s: %d seed records from %s (source %q)\n", s, n, c.seeds, st.seedSource)
	}
	return stds, nil
}

// step is one growth target.
type step struct {
	id      string
	records int64
}

func parseSteps(s string) ([]step, error) {
	var out []step
	for _, kv := range strings.Split(s, ",") {
		kv = strings.TrimSpace(kv)
		if kv == "" {
			continue
		}
		id, v, ok := strings.Cut(kv, "=")
		n, err := strconv.ParseInt(v, 10, 64)
		if !ok || err != nil || n <= 0 {
			return nil, fmt.Errorf("-steps %q: want ID=records,…", kv)
		}
		out = append(out, step{id: id, records: n})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].records < out[j].records })
	return out, nil
}

// zipfPeer draws producer peers Zipf(s) over n peers (s > 1).
type zipfPeer struct {
	mu sync.Mutex
	z  *mrand.Zipf
}

func newZipfPeer(s float64, n int, seed int64) *zipfPeer {
	if s <= 1 || n < 2 {
		return nil
	}
	return &zipfPeer{z: mrand.NewZipf(mrand.New(mrand.NewSource(seed)), s, 1, uint64(n-1))}
}

func (z *zipfPeer) next() int {
	z.mu.Lock()
	defer z.mu.Unlock()
	return int(z.z.Uint64())
}

// storeRecordCount is the store's records over the standards written and the
// fixture's four types (the growth axis).
func storeRecordCount(s *storage.FlatSQLStore, stds []*std) (int64, error) {
	seen := map[string]bool{}
	var n int64
	for _, schema := range append([]string{"OMM.fbs", "MPE.fbs", "CAT.fbs", "IQC.fbs"}, stdNames(stds)...) {
		if seen[schema] {
			continue
		}
		seen[schema] = true
		c, err := s.Count(schema)
		if err != nil {
			return 0, fmt.Errorf("count %s: %w", schema, err)
		}
		n += c
	}
	return n, nil
}

func stdNames(stds []*std) []string {
	out := make([]string, 0, len(stds))
	for _, s := range stds {
		out = append(out, s.name)
	}
	return out
}

// probe is one read of the step's fixed set.
type probe struct {
	name string
	run  func(s *storage.FlatSQLStore) (int64, error)
}

func randomCID() string {
	var b [32]byte
	_, _ = rand.Read(b[:])
	return cidOf([]byte(hex.EncodeToString(b[:])))
}

// stepProbes is the benchset's per-step read set over what the run wrote:
// R01 hit and miss, R05, R11 page 1 and 400, R12 type windows, R14 source
// page, R16 nearest, R17 A18, R20 DataSummary.
func stepProbes(stds []*std, hits map[string][]string) []probe {
	var out []probe
	for _, sd := range stds {
		schema, typ := sd.name, strings.TrimSuffix(sd.name, ".fbs")
		list := hits[schema]
		if len(list) > 64 {
			list = list[len(list)-64:]
		}
		for i, cid := range list {
			cid := cid
			out = append(out, probe{fmt.Sprintf("R01 get hit %s #%02d", schema, i), func(s *storage.FlatSQLStore) (int64, error) {
				r, err := s.GetRecord(schema, cid)
				if err != nil {
					return 0, err
				}
				return int64(len(r.Data)), nil
			}})
		}
		for i := 0; i < 16; i++ {
			cid := randomCID()
			out = append(out, probe{fmt.Sprintf("R02 get miss %s #%02d", schema, i), func(s *storage.FlatSQLStore) (int64, error) {
				_, err := s.GetRecord(schema, cid)
				if err != nil && strings.Contains(strings.ToLower(err.Error()), "not found") {
					return 0, nil
				}
				if err == nil {
					return 0, fmt.Errorf("a random CID was found")
				}
				return 0, err
			}})
		}
		out = append(out,
			probe{"R05 datasync " + schema + " limit=100", func(s *storage.FlatSQLStore) (int64, error) {
				resp, _, err := datasync.Scan(s, datasync.QueryRequest{Schema: schema, Limit: 100}, datasync.MaxSyncChunkLimit)
				if err != nil {
					return 0, err
				}
				return int64(resp.Count), nil
			}},
			probe{"R11 index " + schema + " page=1", func(s *storage.FlatSQLStore) (int64, error) {
				rows, _, err := s.RecordIndexPage(storage.RecordIndexPageQuery{SchemaName: schema, Limit: 50})
				return int64(len(rows)), err
			}},
			probe{"R11 index " + schema + " page=400", func(s *storage.FlatSQLStore) (int64, error) {
				rows, _, err := s.RecordIndexPage(storage.RecordIndexPageQuery{SchemaName: schema, Limit: 50, Offset: 399 * 50})
				return int64(len(rows)), err
			}},
			probe{"R12 type window " + schema + " off=0", func(s *storage.FlatSQLStore) (int64, error) {
				r, err := s.QueryIndexedRecords(storage.IndexedRecordQuery{SchemaName: schema, Limit: 100})
				return int64(len(r)), err
			}},
			probe{"R12 type window " + schema + " off=1000", func(s *storage.FlatSQLStore) (int64, error) {
				r, err := s.QueryIndexedRecords(storage.IndexedRecordQuery{SchemaName: schema, Limit: 100, Offset: 1000})
				return int64(len(r)), err
			}},
			probe{"R14 source page " + schema + " off=0", func(s *storage.FlatSQLStore) (int64, error) {
				r, err := s.QueryIndexedRecords(storage.IndexedRecordQuery{SchemaName: schema, SourceName: sd.seedSource, Limit: 100})
				return int64(len(r)), err
			}},
			probe{"R17 A18 " + typ + "@" + sd.seedSource, func(s *storage.FlatSQLStore) (int64, error) {
				st, err := s.QuerySandboxedStream(fmt.Sprintf(`SELECT _data FROM "%s@%s"`, typ, sd.seedSource),
					flatsqlrt.SandboxCaps{Timeout: 5 * time.Minute})
				if err != nil {
					return 0, err
				}
				return int64(st.FrameCount), nil
			}})
		if schema == "OMM.fbs" || schema == "MPE.fbs" {
			at := time.Unix(1789371001, 0).UTC()
			out = append(out, probe{"R16 nearest " + schema + " limit=200", func(s *storage.FlatSQLStore) (int64, error) {
				q := storage.EpochRecordQuery{SchemaName: schema, Profile: storage.EpochProfileNearest, At: at, Limit: 200}
				if _, err := s.CountEpochRecords(q); err != nil {
					return 0, err
				}
				m, err := s.QueryEpochRecords(q)
				return int64(len(m)), err
			}})
		}
	}
	out = append(out, probe{"R20 DataSummary", func(s *storage.FlatSQLStore) (int64, error) {
		d, err := s.DataSummary()
		if err != nil || d == nil {
			return 0, err
		}
		return d.TotalRecords, nil
	}})
	return out
}

// probeWarm is the warm passes per probe at a step.
const probeWarm = 5

// growthStep measures a step with the writers paused (the caller holds the
// store's write lock): ingest numbers since the last step, bytes on disk per record,
// the reopen time, the probe set cold and warm. It writes the run.
func growthStep(c config, ref *storeRef, arm string, sp step, records int64, stds []*std, st *stats, since time.Time,
	stepRecs int64, lat []float64) error {
	r := &format4proof.Run{Kind: format4proof.KindGrowth, Arm: arm, Format: format4proof.ArmFormat(arm), Label: sp.id,
		Records: records, Started: time.Now().UTC().Format(time.RFC3339), Machine: format4proof.ThisMachine(),
		LoadStart: format4proof.Load(), Extra: map[string]any{}}
	el := time.Since(since).Seconds()
	r.Extra["ingest_records"] = float64(stepRecs)
	r.Extra["ingest_rec_per_s"] = float64(stepRecs) / math.Max(el, 1e-9)
	for _, ms := range lat {
		r.Samples = append(r.Samples, format4proof.Sample{Class: "ingest", Shape: "call", Pass: 1, Ms: ms})
	}
	r.Mem = append(r.Mem, format4proof.Snapshot("step "+sp.id, ""))
	cs := time.Now()
	if err := ref.s.Close(); err != nil {
		return fmt.Errorf("step %s: close: %w", sp.id, err)
	}
	r.Extra["close_s"] = time.Since(cs).Seconds()
	tree, err := format4proof.DiskTree(c.store, "pre-format2", "pre-format4")
	if err != nil {
		return err
	}
	r.Extra["store_bytes"], r.Extra["allocated_bytes"], r.Extra["files"] = float64(tree.Apparent), float64(tree.Allocated), float64(tree.Files)
	r.Extra["bytes_per_record"] = float64(tree.Apparent) / math.Max(float64(records), 1)
	s, openMs, err := format4proof.OpenArm(arm, c.store)
	if err != nil {
		return fmt.Errorf("step %s: reopen: %w", sp.id, err)
	}
	ref.s = s
	r.OpenMs = openMs
	r.Samples = append(r.Samples, format4proof.Sample{Class: "M02", Shape: "open", Pass: 1, Ms: openMs})
	st.cidMu.Lock()
	hits := map[string][]string{}
	for k, v := range st.cids {
		hits[k] = append([]string(nil), v...)
	}
	st.cidMu.Unlock()
	probes := stepProbes(stds, hits)
	for pass := 0; pass <= probeWarm; pass++ {
		for _, p := range probes {
			t := time.Now()
			n, err := p.run(s)
			smp := format4proof.Sample{Class: "probe", Shape: probeShape(p.name), Pass: pass,
				Ms: float64(time.Since(t).Microseconds()) / 1000, Rows: n}
			if err != nil {
				smp.Err = err.Error()
			}
			r.Samples = append(r.Samples, smp)
		}
	}
	r.Mem = append(r.Mem, format4proof.Snapshot("probed "+sp.id, ""))
	r.Extra["fds"] = float64(openFDs())
	r.Extra["partitions_written"] = float64(st.nPartitions.Load())
	r.LoadEnd = format4proof.Load()
	p, err := format4proof.WriteRun(c.results, r)
	if err == nil {
		fmt.Printf("# step %s: %d records, %.0f B/record, reopen %.0f ms, %d probes, run %s\n", sp.id, records,
			r.Extra["bytes_per_record"], openMs, len(probes), filepath.Base(p))
	}
	return err
}

// probeShape groups a probe's samples: the 64 point gets of a type are one
// shape ("R01 get hit OMM.fbs"), as in the read harness.
func probeShape(name string) string {
	if i := strings.Index(name, " #"); i > 0 {
		return name[:i]
	}
	return name
}

// runDueSteps pauses the writers (every call in flight finishes), reads the
// store's record total, and when it has reached the next step's target
// measures the largest step reached (smaller ones passed at once are
// reported skipped: two runs at one size carry no slope). It returns the
// steps left.
func runDueSteps(c config, ref *storeRef, arm string, steps []step, total func() int64, stds []*std, st *stats, since *time.Time) []step {
	ref.mu.Lock()
	defer ref.mu.Unlock()
	n := total()
	due := 0
	for due < len(steps) && n >= steps[due].records {
		due++
	}
	if due == 0 {
		return steps
	}
	for _, sk := range steps[:due-1] {
		fmt.Printf("# step %s skipped: the store passed it (%d records) before %s\n", sk.id, n, steps[due-1].id)
	}
	st.mu.Lock()
	lat, ins := st.stepLat, st.stepInserted
	st.stepLat, st.stepInserted = nil, 0
	st.mu.Unlock()
	if err := growthStep(c, ref, arm, steps[due-1], n, stds, st, *since, ins, lat); err != nil {
		fmt.Println("ERROR growth step", steps[due-1].id, err)
		return nil
	}
	*since = time.Now()
	return steps[due:]
}
