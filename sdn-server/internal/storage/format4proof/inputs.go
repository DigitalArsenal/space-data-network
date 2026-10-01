package format4proof

import (
	"bufio"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/storage"
)

// Inputs taken once from a format-1 clone, so every arm writes the same
// bytes: the ingest seeds and the write operations' source batches. They
// live in <work>/inputs as frame files ([u32le size][record]...) plus
// inputs.json.

// The lanes of the fixture (benchset fixtures.t6w.lanes).
const (
	FixtureProvider = "space-data-network-02"
	OMMLatestBatch  = "OMM-celestrak-gp-b052"
	MPELatestBatch  = "MPE-celestrak-gp-b051"
)

// Input sets (frame files).
const (
	InputOMMB052   = "omm-b052"       // OMM batch b052, CID order (W01, W03, W04, W07 source; ingest seeds)
	InputMPEB051   = "mpe-b051"       // MPE batch b051, CID order (W01; ingest seeds)
	InputCATCSV    = "cat-satcat-csv" // the celestrak-satcat-csv lane (W02)
	InputCATSatcat = "cat-satcat"     // the first 8,192 celestrak-satcat records (ingest seeds)
	InputIQC       = "iqc-first"      // the first 10,000 IQEngine records (W01, W05; ingest seeds)
)

// InputsDir is where Prepare writes.
func InputsDir(work string) string { return filepath.Join(work, "inputs") }

// WriteFrames writes records as [u32le size][bytes] frames.
func WriteFrames(path string, recs [][]byte) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	w := bufio.NewWriterSize(f, 1<<20)
	var hdr [4]byte
	for _, r := range recs {
		binary.LittleEndian.PutUint32(hdr[:], uint32(len(r)))
		if _, err := w.Write(hdr[:]); err != nil {
			f.Close()
			return err
		}
		if _, err := w.Write(r); err != nil {
			f.Close()
			return err
		}
	}
	if err := w.Flush(); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// ReadFrames reads a frame file.
func ReadFrames(path string) ([][]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	r := bufio.NewReaderSize(f, 1<<20)
	var out [][]byte
	var hdr [4]byte
	for {
		if _, err := io.ReadFull(r, hdr[:]); err == io.EOF {
			return out, nil
		} else if err != nil {
			return nil, err
		}
		b := make([]byte, binary.LittleEndian.Uint32(hdr[:]))
		if _, err := io.ReadFull(r, b); err != nil {
			return nil, fmt.Errorf("%s: frame %d: %w", path, len(out), err)
		}
		out = append(out, b)
	}
}

// LoadInputs reads the input sets and inputs.json from work (a missing set
// is nil).
func LoadInputs(work string) (*Inputs, map[string][][]byte, error) {
	dir := InputsDir(work)
	in := &Inputs{}
	if b, err := os.ReadFile(filepath.Join(dir, "inputs.json")); err == nil {
		if err := json.Unmarshal(b, in); err != nil {
			return nil, nil, err
		}
	} else if !os.IsNotExist(err) {
		return nil, nil, err
	}
	sets := map[string][][]byte{}
	for _, name := range []string{InputOMMB052, InputMPEB051, InputCATCSV, InputCATSatcat, InputIQC} {
		recs, err := ReadFrames(filepath.Join(dir, name+".bin"))
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, nil, err
		}
		sets[name] = recs
	}
	return in, sets, nil
}

// Prepare extracts the inputs from a format-1 store clone (read through
// storage, the way the daemon reads; nothing is written to the store but
// its own open/close bookkeeping).
func Prepare(f1Clone, work string, logf func(string, ...any)) error {
	s, _, err := OpenArm(ArmF1, f1Clone)
	if err != nil {
		return err
	}
	defer s.Close()
	dir := InputsDir(work)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	window := func(q storage.IndexedRecordQuery) ([][]byte, []string, error) {
		q.AllowLargeResultSet = true
		st := time.Now()
		recs, err := s.QueryIndexedRecords(q)
		if err != nil {
			return nil, nil, err
		}
		out := make([][]byte, 0, len(recs))
		cids := make([]string, 0, len(recs))
		for _, r := range recs {
			out = append(out, r.Data)
			cids = append(cids, r.CID)
		}
		logf("prepare: %s %s/%s: %d records in %s", q.SchemaName, q.SourceName, q.BatchID, len(out), time.Since(st).Round(time.Millisecond))
		return out, cids, nil
	}
	in := &Inputs{}
	sets := []struct {
		name string
		q    storage.IndexedRecordQuery
	}{
		{InputOMMB052, storage.IndexedRecordQuery{SchemaName: "OMM.fbs", ProviderID: FixtureProvider, SourceName: "celestrak-gp",
			BatchID: OMMLatestBatch, Limit: 40000, OrderByCID: true}},
		{InputMPEB051, storage.IndexedRecordQuery{SchemaName: "MPE.fbs", ProviderID: FixtureProvider, SourceName: "celestrak-gp",
			BatchID: MPELatestBatch, Limit: 40000, OrderByCID: true}},
		{InputCATCSV, storage.IndexedRecordQuery{SchemaName: "CAT.fbs", SourceName: "celestrak-satcat-csv", Limit: 100000, OrderByCID: true}},
		{InputCATSatcat, storage.IndexedRecordQuery{SchemaName: "CAT.fbs", SourceName: "celestrak-satcat", Limit: 8192, OrderByCID: true}},
		{InputIQC, storage.IndexedRecordQuery{SchemaName: "IQC.fbs", SourceName: "IQEngine", Limit: 10000, OrderByCID: true}},
	}
	for _, set := range sets {
		recs, cids, err := window(set.q)
		if err != nil {
			return fmt.Errorf("prepare %s: %w", set.name, err)
		}
		if len(recs) == 0 {
			return fmt.Errorf("prepare %s: the fixture has no such records", set.name)
		}
		if set.name == InputOMMB052 {
			in.B052CIDs = cids
			if in.B052Tags, err = s.GetSourceTags("OMM.fbs", cids[0]); err != nil {
				return fmt.Errorf("prepare: the tag of batch b052: %w", err)
			}
		}
		if err := WriteFrames(filepath.Join(dir, set.name+".bin"), recs); err != nil {
			return err
		}
	}
	b, err := json.MarshalIndent(in, "", " ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "inputs.json"), b, 0o644)
}
