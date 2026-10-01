package format4proof

import (
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/datasync"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
)

// M01, reads never wait: W01 (OMM, MPE and IQC back to back) then W06, while
// R01 (OMM and MPE hits), R05 (limit 100), R11 (page 1) and R16
// (epoch.nearest, limit 200) each run at 20 calls a second. The reads are
// timed idle first, then during the writes; the WAL is sampled every second.
// With Minutes > 0 the write cycle repeats (a new OMM batch, then a
// supersede keeping it) until the time is up: the 30-minute WAL-bounded run.

// M01Spec is one M01 run.
type M01Spec struct {
	Arm, Store, Out, Work string
	IdleSeconds           int
	Minutes               int
}

type m01Reader struct {
	name string
	run  func(s *storage.FlatSQLStore, i int) error
}

func m01Readers(hits map[string][]string) []m01Reader {
	var gets []struct{ schema, cid string }
	for _, schema := range []string{"OMM.fbs", "MPE.fbs"} {
		for _, c := range hits[schema] {
			gets = append(gets, struct{ schema, cid string }{schema, c})
		}
	}
	at := atUnix(1789371001)
	return []m01Reader{
		{"R01 get OMM+MPE", func(s *storage.FlatSQLStore, i int) error {
			if len(gets) == 0 {
				return fmt.Errorf("no R01 CIDs")
			}
			g := gets[i%len(gets)]
			_, err := s.GetRecord(g.schema, g.cid)
			if isNotFound(err) {
				return nil // W09-free run: a hit; a miss after W06 is the supersede's doing
			}
			return err
		}},
		{"R05 datasync OMM limit=100", func(s *storage.FlatSQLStore, _ int) error {
			_, _, err := datasync.Scan(s, datasync.QueryRequest{Schema: "OMM.fbs", Limit: 100}, datasync.MaxSyncChunkLimit)
			return err
		}},
		{"R11 index OMM page=1", func(s *storage.FlatSQLStore, _ int) error {
			_, _, err := s.RecordIndexPage(storage.RecordIndexPageQuery{SchemaName: "OMM.fbs", Limit: 50})
			return err
		}},
		{"R16 OMM nearest limit=200", func(s *storage.FlatSQLStore, _ int) error {
			q := storage.EpochRecordQuery{SchemaName: "OMM.fbs", Profile: storage.EpochProfileNearest, At: at, Limit: 200}
			if _, err := s.CountEpochRecords(q); err != nil {
				return err
			}
			_, err := s.QueryEpochRecords(q)
			return err
		}},
	}
}

// RunM01 runs M01 on spec.Store and writes the run.
func RunM01(spec M01Spec, hits map[string][]string) (*Run, error) {
	r := &Run{Kind: KindM01, Arm: spec.Arm, Format: ArmFormat(spec.Arm), Label: LabelFixture,
		Started: time.Now().UTC().Format(time.RFC3339), Machine: ThisMachine(), LoadStart: Load(), Extra: map[string]any{}}
	in, sets, err := LoadInputs(spec.Work)
	if err != nil {
		return r, err
	}
	s, openMs, err := OpenArm(spec.Arm, spec.Store)
	r.OpenMs = openMs
	if err != nil {
		return r, err
	}
	if spec.IdleSeconds <= 0 {
		spec.IdleSeconds = 30
	}
	var mu sync.Mutex
	var busy atomic.Bool
	var walMax atomic.Int64
	t0 := time.Now()
	stop := make(chan struct{})
	var wg sync.WaitGroup
	for _, rd := range m01Readers(hits) {
		wg.Add(1)
		go func(rd m01Reader) {
			defer wg.Done()
			tick := time.NewTicker(50 * time.Millisecond) // 20 calls a second; a blocked call drops ticks
			defer tick.Stop()
			for i := 0; ; i++ {
				select {
				case <-stop:
					return
				case <-tick.C:
				}
				class := M01Idle
				if busy.Load() {
					class = M01Busy
				}
				var err error
				d, ok := guard(5*time.Minute, func() { err = rd.run(s, i) })
				sm := Sample{Class: class, Shape: rd.name, Pass: 1, Ms: float64(d.Microseconds()) / 1000, AtS: time.Since(t0).Seconds()}
				if !ok {
					sm.Err = "timeout after 5m"
				} else if err != nil {
					sm.Err = err.Error()
				}
				mu.Lock()
				r.Samples = append(r.Samples, sm)
				mu.Unlock()
			}
		}(rd)
	}
	wg.Add(1)
	go func() { // WAL and resource sampler
		defer wg.Done()
		tick := time.NewTicker(time.Second)
		defer tick.Stop()
		for n := 0; ; n++ {
			select {
			case <-stop:
				return
			case <-tick.C:
			}
			t, err := DiskTree(spec.Store)
			if err == nil && t.WAL > walMax.Load() {
				walMax.Store(t.WAL)
			}
			if n%10 == 0 {
				m := Snapshot("m01", "")
				m.WALBytes, m.StoreBytes = t.WAL, t.Apparent
				mu.Lock()
				r.Mem = append(r.Mem, m)
				mu.Unlock()
			}
		}
	}()
	time.Sleep(time.Duration(spec.IdleSeconds) * time.Second)
	busy.Store(true)
	ws := time.Now()
	e := &writeEnv{s: s, in: in, sets: sets, hits: hits, run: &Run{}, t0: t0, result: map[string]any{}}
	writeErr := e.w01("M01")
	if writeErr == nil {
		writeErr = e.supersede("M01", "OMM supersede keep b053", "OMM.fbs", "celestrak-gp", "OMM-celestrak-gp-b053")
	}
	cycles := 1
	deadline := ws.Add(time.Duration(spec.Minutes) * time.Minute)
	for writeErr == nil && time.Now().Before(deadline) {
		t := fixtureTags("celestrak-gp")
		t.ProducerPeerID = ""
		t.BatchID = fmt.Sprintf("OMM-celestrak-gp-m01-%04d", cycles)
		writeErr = e.store("M01", "OMM cycle", "OMM.fbs", "source:celestrak", clones("OMM", sets[InputOMMB052], int64(708+cycles)), t)
		if writeErr == nil {
			writeErr = e.supersede("M01", "OMM cycle supersede", "OMM.fbs", "celestrak-gp", t.BatchID)
		}
		cycles++
	}
	busy.Store(false)
	writeS := time.Since(ws).Seconds()
	close(stop)
	wg.Wait()
	mu.Lock()
	r.Samples = append(r.Samples, e.run.Samples...)
	var busyMax float64
	errs, over := 0, 0
	for _, sm := range r.Samples {
		if sm.Class != M01Busy && sm.Class != M01Idle {
			continue
		}
		if sm.Err != "" {
			errs++
			continue
		}
		if sm.Class == M01Busy {
			if sm.Ms > busyMax {
				busyMax = sm.Ms
			}
			if sm.Ms > M01BlockedMs {
				over++
			}
		}
	}
	mu.Unlock()
	r.Extra["busy_read_max_ms"] = busyMax
	r.Extra["reads_over_50ms"] = float64(over)
	r.Extra["read_errors"] = float64(errs)
	r.Extra["wal_max_bytes"] = float64(walMax.Load())
	r.Extra["write_seconds"] = writeS
	r.Extra["write_cycles"] = float64(cycles)
	r.Extra["writes"] = e.result
	if writeErr != nil {
		r.Extra["write_error"] = writeErr.Error()
	}
	cs := time.Now()
	if err := s.Close(); err != nil {
		r.Extra["close_error"] = err.Error()
	}
	r.Extra["close_s"] = time.Since(cs).Seconds()
	r.LoadEnd = Load()
	if spec.Out != "" {
		if _, err := WriteRun(spec.Out, r); err != nil {
			return r, err
		}
	}
	return r, writeErr
}
