package format4proof

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/storage"
)

// The writes W01–W10 (benchset "writes"), each on a fresh clone of the arm's
// fixture unless it names a predecessor. A write run times the operation,
// keeps its result, closes the store, and digests the record set of every
// type the operation touched (format 1 through its own engine; format 4
// through the engine's API), so the arms' record and tag sets compare.

// WriteSpec is one write run.
type WriteSpec struct {
	Arm, Op, Store, Out, Work string
	Store2                    string // W07: the import target (a second fresh clone)
}

// touched is what each write changes, and so what its digest covers.
var touched = map[string][]string{
	"W01": {"OMM.fbs", "MPE.fbs", "IQC.fbs"},
	"W02": {"CAT.fbs"},
	"W03": {"OMM.fbs"},
	"W04": {"OMM.fbs"},
	"W05": {"IQC.fbs"},
	"W06": {"OMM.fbs"},
	"W07": {"OMM.fbs"},
	"W08": {"OMM.fbs"},
	"W09": {"OMM.fbs", "MPE.fbs"},
	"W10": {"OMM.fbs", "MPE.fbs", "CAT.fbs", "IQC.fbs"},
}

// WriteOps lists the writes in benchset order.
var WriteOps = []string{"W01", "W02", "W03", "W04", "W05", "W06", "W07", "W08", "W09", "W10"}

// writeEnv is what an operation runs against.
type writeEnv struct {
	s      *storage.FlatSQLStore
	in     *Inputs
	sets   map[string][][]byte
	hits   map[string][]string // R01's CIDs (W09)
	run    *Run
	spec   WriteSpec
	t0     time.Time
	result map[string]any
}

func (e *writeEnv) timed(class, shape string, fn func() (int64, error)) error {
	var n int64
	var err error
	d, ok := guard(3*time.Hour, func() { n, err = fn() })
	sm := Sample{Class: class, Shape: shape, Pass: 1, Ms: float64(d.Microseconds()) / 1000, Rows: n, AtS: time.Since(e.t0).Seconds()}
	if !ok {
		err = fmt.Errorf("timeout after 3h")
	}
	if err != nil {
		sm.Err = err.Error()
	}
	e.run.Samples = append(e.run.Samples, sm)
	return err
}

func clones(typ string, seeds [][]byte, c int64) [][]byte {
	out := make([][]byte, len(seeds))
	for i, s := range seeds {
		out[i] = CloneOf(typ, s, c, nil)
	}
	return out
}

func (e *writeEnv) store(class, shape, schema, peer string, recs [][]byte, tags storage.SourceTags) error {
	return e.timed(class, shape, func() (int64, error) {
		n, err := e.s.StoreBatchWithSourceTags(schema, recs, peer, nil, tags)
		e.result[shape+"_inserted"] = n
		return int64(n), err
	})
}

func b052Tags(in *Inputs) storage.SourceTags {
	t := in.B052Tags
	if t.SourceName == "" {
		t = fixtureTags("celestrak-gp")
		t.ProducerPeerID = ""
		t.BatchID = OMMLatestBatch
	}
	return t
}

// w01 is the flow batch ingest: OMM b052 cloned c=708 as batch b053, MPE b051
// cloned c=708 as b052, the first 10,000 IQC cloned c=1.
func (e *writeEnv) w01(class string) error {
	omm := fixtureTags("celestrak-gp")
	omm.ProducerPeerID = ""
	omm.BatchID = "OMM-celestrak-gp-b053"
	if err := e.store(class, "OMM", "OMM.fbs", "source:celestrak", clones("OMM", e.sets[InputOMMB052], 708), omm); err != nil {
		return err
	}
	if class == "W06" {
		return nil // W06 needs only W01's OMM part
	}
	mpe := omm
	mpe.BatchID = "MPE-celestrak-gp-b052"
	if err := e.store(class, "MPE", "MPE.fbs", "source:celestrak", clones("MPE", e.sets[InputMPEB051], 708), mpe); err != nil {
		return err
	}
	iqc := fixtureTags("IQEngine")
	iqc.ProducerPeerID = ""
	iqc.BatchID = "iqc-bench-1"
	return e.store(class, "IQC", "IQC.fbs", "source:sigmf", clones("IQC", e.sets[InputIQC], 1), iqc)
}

func (e *writeEnv) supersede(class, shape, schema, source, keep string) error {
	return e.timed(class, shape, func() (int64, error) {
		res, err := e.s.SupersedeSourceBatches(schema, FixtureProvider, source, keep)
		e.result[shape+"_tags_deleted"] = res.TagsDeleted
		e.result[shape+"_records_deleted"] = res.RecordsDeleted
		e.result[shape+"_files_deleted"] = res.FilesDeleted
		return res.RecordsDeleted, err
	})
}

func (e *writeEnv) op(op string) error {
	switch op {
	case "W01":
		return e.w01(op)
	case "W02":
		var recs [][]byte
		for i, s := range e.sets[InputCATCSV] {
			recs = append(recs, CloneKeepIdentity(s, int64(i%7)+1))
		}
		t := fixtureTags("celestrak-satcat-csv")
		t.ProducerPeerID = ""
		t.BatchID = "CAT-bench-2"
		return e.store(op, "CAT", "CAT.fbs", "source:celestrak", recs, t)
	case "W03":
		return e.store(op, "OMM", "OMM.fbs", "source:celestrak", e.sets[InputOMMB052], b052Tags(e.in))
	case "W04":
		t := b052Tags(e.in)
		t.BatchID = OMMLatestBatch + "r"
		if err := e.store(op, "OMM retag", "OMM.fbs", "source:celestrak", e.sets[InputOMMB052], t); err != nil {
			return err
		}
		return e.supersede(op, "OMM supersede keep b052r", "OMM.fbs", "celestrak-gp", t.BatchID)
	case "W05":
		var recs [][]byte
		changed := 0
		for i, s := range e.sets[InputIQC] {
			r, ok := RestampIQC(s, int64(i))
			if ok {
				changed++
			}
			recs = append(recs, r)
		}
		e.result["restamped"] = changed
		t := fixtureTags("IQEngine")
		t.ProducerPeerID = ""
		t.BatchID = "iqc-bench-restamp"
		return e.store(op, "IQC", "IQC.fbs", "source:sigmf", recs, t)
	case "W06":
		if err := e.w01(op); err != nil {
			return err
		}
		return e.supersede(op, "OMM supersede keep b053", "OMM.fbs", "celestrak-gp", "OMM-celestrak-gp-b053")
	case "W07":
		return e.w07()
	case "W08":
		seeds := e.sets[InputOMMB052]
		if len(seeds) > 1000 {
			seeds = seeds[:1000]
		}
		t := fixtureTags("celestrak-gp")
		t.ProducerPeerID = ""
		t.BatchID = "OMM-single-w08"
		for i, s := range seeds {
			rec := CloneOf("OMM", s, 709, nil)
			if err := e.timed(op, "StoreWithSourceTags", func() (int64, error) {
				_, err := e.s.StoreWithSourceTags("OMM.fbs", rec, "source:celestrak", nil, t)
				return 1, err
			}); err != nil {
				return fmt.Errorf("write %d: %w", i, err)
			}
		}
		return nil
	case "W09":
		for _, schema := range []string{"OMM.fbs", "MPE.fbs"} {
			for _, cid := range e.hits[schema] {
				schema, cid := schema, cid
				if err := e.timed(op, "Delete "+schema, func() (int64, error) { return 1, e.s.Delete(schema, cid) }); err != nil {
					return err
				}
			}
		}
		return nil
	case "W10":
		live, err := e.s.LiveRecordBytes()
		if err != nil {
			return err
		}
		max := live * 9 / 10
		e.result["live_bytes_before"], e.result["quota_bytes"] = live, max
		return e.timed(op, "GarbageCollectToQuota", func() (int64, error) {
			n, err := e.s.GarbageCollectToQuota(max)
			e.result["evicted"] = n
			return n, err
		})
	}
	return fmt.Errorf("unknown write %s", op)
}

// w07: export W01's OMM batch from this store (arm A), import it into a
// fresh clone (arm B), then import the same shard again (all repeats).
func (e *writeEnv) w07() error {
	if err := e.w01("W07-W01"); err != nil {
		return err
	}
	dir := filepath.Join(e.spec.Work, "w07-shard-"+e.spec.Arm)
	_ = os.RemoveAll(dir)
	var exp *storage.DatasetExport
	if err := e.timed("W07", "ExportDatasetWindow", func() (int64, error) {
		var err error
		exp, err = e.s.ExportDatasetWindow(dir, storage.IndexedRecordQuery{SchemaName: "OMM.fbs", ProviderID: FixtureProvider,
			SourceName: "celestrak-gp", BatchID: "OMM-celestrak-gp-b053", Limit: 40000, AllowLargeResultSet: true, OrderByCID: true})
		if exp != nil {
			return int64(exp.RecordCount), err
		}
		return 0, err
	}); err != nil {
		return err
	}
	if err := e.s.Close(); err != nil {
		return err
	}
	b, _, err := OpenArm(e.spec.Arm, e.spec.Store2)
	if err != nil {
		return err
	}
	e.s = b
	for _, shape := range []string{"import", "import again"} {
		shape := shape
		if err := e.timed("W07", shape, func() (int64, error) {
			n, _, err := b.ImportDatasetShardFromFilesContext(context.Background(), exp.ShardPath, exp.IndexPath, "source:celestrak")
			e.result[shape+"_inserted"] = n
			return int64(n), err
		}); err != nil {
			return err
		}
	}
	return nil
}

// RunWrite runs one write on spec.Store (spec.Store2 for W07's import) and
// digests what it touched.
func RunWrite(spec WriteSpec, hits map[string][]string) (*Run, error) {
	r := &Run{Kind: KindWrites, Arm: spec.Arm, Format: ArmFormat(spec.Arm), Label: LabelFixture, Class: spec.Op,
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
	e := &writeEnv{s: s, in: in, sets: sets, hits: hits, run: r, spec: spec, t0: time.Now(), result: map[string]any{}}
	r.Mem = append(r.Mem, Snapshot("open", spec.Store))
	opErr := e.op(spec.Op)
	r.Mem = append(r.Mem, Snapshot("after "+spec.Op, ""))
	if opErr != nil {
		r.Extra["error"] = opErr.Error()
	}
	r.Extra["result"] = e.result
	cs := time.Now()
	if err := e.s.Close(); err != nil {
		r.Extra["close_error"] = err.Error()
	}
	r.Extra["close_s"] = time.Since(cs).Seconds()
	digestStore := spec.Store
	if spec.Op == "W07" {
		digestStore = spec.Store2
	}
	if opErr == nil {
		st := time.Now()
		d, err := DigestStore(spec.Arm, digestStore, touched[spec.Op])
		r.Extra["digest_s"] = time.Since(st).Seconds()
		if err != nil {
			r.Extra["digest_error"] = err.Error()
		} else if d != nil {
			r.Extra["digest"] = d
		}
	}
	r.LoadEnd = Load()
	if spec.Out != "" {
		if _, err := WriteRun(spec.Out, r); err != nil {
			return r, err
		}
	}
	return r, opErr
}
