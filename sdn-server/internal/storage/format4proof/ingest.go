package format4proof

import (
	"fmt"
	"math"
	"math/rand"
	"sync"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/storage"
)

// Ingest through SDN's batch write path (StoreBatchWithSourceTags, what a
// flow's ingest_with_source calls), 4,096-record calls, into the fixture's
// own lanes:
//   A: one writer, OMM (celestrak-gp);
//   B: four writers at once, one per type, each into its lane;
//   C: same-type producers: several writers of OMM at once, each its own
//      producer peer (so its own partition).
// A and B on one clone are the earlier baselines' phases; the store they
// leave is the +28% ("grown") store. C runs on its own clone.

// IngestSpec is one ingest run.
type IngestSpec struct {
	Arm, Label, Store, Out string
	Work                   string // holds the inputs
	A, B                   int    // calls in phase A; calls per writer in phase B
	C, CWriters            int    // calls per writer and writers in phase C
	Batch                  int    // records per call (4,096)
}

// phases names the phases a spec runs ("AB", "C"): the run's class.
func (spec IngestSpec) phases() string {
	p := ""
	if spec.A > 0 {
		p += "A"
	}
	if spec.B > 0 {
		p += "B"
	}
	if spec.C > 0 && spec.CWriters > 0 {
		p += "C"
	}
	return p
}

type ingestLane struct {
	typ, schema, peer string
	tags              storage.SourceTags
	seeds             [][]byte
	next              int64
}

func fixtureTags(source string) storage.SourceTags {
	return storage.SourceTags{ProviderID: FixtureProvider, SourceName: source, ProducerPeerID: FixtureProvider}
}

// seedSets maps a type to its seed input set.
var seedSets = map[string]string{"OMM": InputOMMB052, "MPE": InputMPEB051, "CAT": InputCATSatcat, "IQC": InputIQC}

func ingestLanes(sets map[string][][]byte) (map[string]*ingestLane, error) {
	lanes := map[string]*ingestLane{
		"OMM": {typ: "OMM", schema: "OMM.fbs", peer: "source:celestrak", tags: fixtureTags("celestrak-gp")},
		"MPE": {typ: "MPE", schema: "MPE.fbs", peer: "source:celestrak", tags: fixtureTags("celestrak-gp")},
		"CAT": {typ: "CAT", schema: "CAT.fbs", peer: "source:celestrak", tags: fixtureTags("celestrak-satcat")},
		"IQC": {typ: "IQC", schema: "IQC.fbs", peer: "source:sigmf", tags: fixtureTags("IQEngine")},
	}
	for typ, l := range lanes {
		l.seeds = sets[seedSets[typ]]
		if len(l.seeds) == 0 {
			return nil, fmt.Errorf("ingest: no %s seeds (run the prepare step)", typ)
		}
	}
	return lanes, nil
}

// RunIngest runs the phases on spec.Store and writes the run.
func RunIngest(spec IngestSpec) (*Run, error) {
	if spec.Batch <= 0 {
		spec.Batch = 4096
	}
	r := &Run{Kind: KindIngest, Arm: spec.Arm, Format: ArmFormat(spec.Arm), Label: spec.Label, Class: spec.phases(),
		Started: time.Now().UTC().Format(time.RFC3339), Machine: ThisMachine(), LoadStart: Load(), Extra: map[string]any{}}
	_, sets, err := LoadInputs(spec.Work)
	if err != nil {
		return r, err
	}
	lanes, err := ingestLanes(sets)
	if err != nil {
		return r, err
	}
	r.Mem = append(r.Mem, Snapshot("start", spec.Store))
	s, openMs, err := OpenArm(spec.Arm, spec.Store)
	r.OpenMs = openMs
	if err != nil {
		return r, err
	}
	r.Mem = append(r.Mem, Snapshot("open", spec.Store))
	var mu sync.Mutex
	var totalRecs, totalBytes int64
	t0 := time.Now()
	runCall := func(phase string, l *ingestLane, callNo int, rng *rand.Rand) {
		recs := make([][]byte, 0, spec.Batch)
		var bytes int64
		for i := 0; i < spec.Batch; i++ {
			k := l.next
			l.next++
			fb := CloneOf(l.typ, l.seeds[k%int64(len(l.seeds))], k/int64(len(l.seeds))+1, rng)
			recs = append(recs, fb)
			bytes += int64(len(fb))
		}
		tg := l.tags
		tg.BatchID = fmt.Sprintf("%s-%s-zz%s%04d", l.typ, tg.SourceName, phase, callNo/8)
		var n int
		var err error
		d, ok := guard(10*time.Minute, func() { n, err = s.StoreBatchWithSourceTags(l.schema, recs, l.peer, nil, tg) })
		sm := Sample{Class: "ingest_" + phase, Shape: l.typ, Pass: 1, Ms: float64(d.Microseconds()) / 1000, Rows: int64(n),
			Bytes: bytes, AtS: time.Since(t0).Seconds()}
		if !ok {
			sm.Err = "timeout after 10m"
		} else if err != nil {
			sm.Err = err.Error()
		}
		mu.Lock()
		r.Samples = append(r.Samples, sm)
		totalRecs += int64(n)
		totalBytes += bytes
		mu.Unlock()
	}
	phase := func(name string, run func()) {
		mu.Lock()
		recs0, bytes0 := totalRecs, totalBytes
		mu.Unlock()
		m0 := Snapshot("before "+name, "")
		st := time.Now()
		done := make(chan struct{})
		go func() {
			tick := time.NewTicker(20 * time.Second)
			defer tick.Stop()
			for {
				select {
				case <-done:
					return
				case <-tick.C:
					m := Snapshot(name+" tick", spec.Store)
					mu.Lock()
					m.Records = totalRecs
					r.Mem = append(r.Mem, m)
					mu.Unlock()
				}
			}
		}()
		run()
		close(done)
		el := time.Since(st).Seconds()
		m1 := Snapshot("after "+name, spec.Store)
		mu.Lock()
		dr, db := totalRecs-recs0, totalBytes-bytes0
		r.Mem = append(r.Mem, m1)
		mu.Unlock()
		wmb := m1.DiskWMB - m0.DiskWMB
		r.Extra["ingest_"+name] = map[string]any{"records": float64(dr), "frame_bytes": float64(db), "seconds": el,
			"rec_per_s": float64(dr) / el, "frame_mb_per_s": float64(db) / el / 1e6, "disk_written_mb": wmb,
			"disk_written_per_record_b": wmb * (1 << 20) / math.Max(1, float64(dr)),
			"write_amplification":       wmb * (1 << 20) / math.Max(1, float64(db)), "load_end": Load()}
	}
	if spec.A > 0 {
		phase("A", func() {
			rng := rand.New(rand.NewSource(0xA))
			for i := 0; i < spec.A; i++ {
				runCall("A", lanes["OMM"], i, rng)
			}
		})
	}
	if spec.B > 0 {
		phase("B", func() {
			var wg sync.WaitGroup
			for i, typ := range []string{"OMM", "MPE", "CAT", "IQC"} {
				wg.Add(1)
				go func(i int, l *ingestLane) {
					defer wg.Done()
					rng := rand.New(rand.NewSource(int64(0xB0 + i)))
					for c := 0; c < spec.B; c++ {
						runCall("B", l, c, rng)
					}
				}(i, lanes[typ])
			}
			wg.Wait()
		})
	}
	if spec.C > 0 && spec.CWriters > 0 {
		phase("C", func() {
			base := lanes["OMM"]
			var wg sync.WaitGroup
			for w := 0; w < spec.CWriters; w++ {
				// Each writer its own producer peer and clone range (no two
				// writers write the same CID, so every record is new).
				l := &ingestLane{typ: "OMM", schema: "OMM.fbs", peer: fmt.Sprintf("source:proof-producer-%02d", w),
					tags: fixtureTags(fmt.Sprintf("proof-producer-%02d", w)), seeds: base.seeds,
					next: int64(1+w) * int64(spec.C*spec.Batch+len(base.seeds)) * 64}
				wg.Add(1)
				go func(w int, l *ingestLane) {
					defer wg.Done()
					rng := rand.New(rand.NewSource(int64(0xC0 + w)))
					for c := 0; c < spec.C; c++ {
						runCall("C", l, c, rng)
					}
				}(w, l)
			}
			wg.Wait()
		})
	}
	cs := time.Now()
	if err := s.Close(); err != nil {
		r.Extra["close_error"] = err.Error()
	}
	r.Extra["close_s"] = time.Since(cs).Seconds()
	r.Mem = append(r.Mem, Snapshot("closed", spec.Store))
	r.Extra["total_records"], r.Extra["total_frame_bytes"] = float64(totalRecs), float64(totalBytes)
	r.LoadEnd = Load()
	if spec.Out != "" {
		if _, err := WriteRun(spec.Out, r); err != nil {
			return r, err
		}
	}
	return r, nil
}
