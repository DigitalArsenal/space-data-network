// wxf-ingest-amplification-e2e measures the real embedded engine on a populated
// WXF feed. The fixture uses the sea lane's nine fields and 512 tiles per lead.
// Data is synthetic; no production store is opened or changed.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/WXF"
	flatbuffers "github.com/google/flatbuffers/go"
	"github.com/ipfs/go-cid"
	"github.com/spacedatanetwork/sdn-server/internal/flatsqlrt"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
	"github.com/spacedatanetwork/sdn-server/internal/storage/format4"
)

const tiles = 512

var fields = []string{"TMP:2m", "DPT:2m", "UGRD:10m", "VGRD:10m", "HPBL:surface", "HGT:surface", "PRATE:surface", "VIS:surface", "HGT:cloud-ceiling"}
var ctx = context.Background()

func check(err error) {
	if err != nil {
		panic(err)
	}
}

func require(ok bool, message string) {
	if !ok {
		panic(message)
	}
}

func weather(id int) format4.In {
	b := flatbuffers.NewBuilder(9000)
	field := b.CreateString(fmt.Sprintf("gfs:%08d:%s", id, fields[id%len(fields)]))
	WXF.WXFStartVALUESVector(b, 46*46)
	for j := 46*46 - 1; j >= 0; j-- {
		// Distinct float tokens exercise the numeric-array FTS expansion.
		b.PrependFloat32(float32((id*17+j*31)%100000)/1000 + 200)
	}
	values := b.EndVector(46 * 46)
	WXF.WXFGridStart(b)
	WXF.WXFGridAddNLAT(b, 46)
	WXF.WXFGridAddNLON(b, 46)
	grid := WXF.WXFGridEnd(b)
	WXF.WXFStart(b)
	WXF.WXFAddFIELD_ID(b, field)
	WXF.WXFAddGRID(b, grid)
	WXF.WXFAddVALUES(b, values)
	WXF.WXFAddVALID_TIME_MS(b, 1792238400000+uint64(id/(tiles*len(fields)))*3600000)
	WXF.FinishSizePrefixedWXFBuffer(b, WXF.WXFEnd(b))
	// The ingest connector splits the size-prefixed stream. The engine adds
	// its own frame prefix; including it here would double-frame the record.
	data := b.FinishedBytes()[4:]
	c, err := (cid.Prefix{Version: 1, Codec: cid.Raw, MhType: 0x12, MhLength: 32}).Sum(data)
	check(err)
	return format4.In{CID: c.String(), Plain: data, TS: 1792238400}
}

func batch(records []format4.In, name string) format4.Batch {
	return format4.Batch{Type: "WXF", Peer: "wxf-amplification-e2e", Records: records,
		Tags: []format4.Tag{{Provider: "noaa-ncep", Source: "gfs-0p25-sea", Batch: name, SourceURL: "https://example.test/gfs"}}}
}

func open(root string, mode format4.CreateMode) *format4.Engine {
	e, err := format4.Open(ctx, format4.Options{DataRoot: root, Create: mode, GseqFloor: 1,
		AOTCacheDir: storage.EngineAOTCacheDir()})
	check(err)
	return e
}

func spec(fullText bool) format4.TypeSpec {
	s, err := format4.TypeSpecFor("WXF.fbs")
	check(err)
	s.FullText = fullText
	return s
}

func checkSearchFixture() {
	_, _, err := flatsqlrt.PrewarmP4ThreadsAOT(storage.EngineAOTCacheDir())
	check(err)
	root, err := os.MkdirTemp("", "wxf-fts-fixture-")
	check(err)
	defer os.RemoveAll(root)
	e := open(root, format4.CreateFresh)
	defer func() { check(e.Close(ctx)) }()
	check(e.RegisterType(spec(true)))
	put(e, batch([]format4.In{weather(0)}, "fixture"))
	for deadline := time.Now().Add(30 * time.Second); time.Now().Before(deadline); {
		st, err := e.Stats()
		check(err)
		if st[39] == 1 {
			fmt.Println("fixture_search_text=PASS")
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	panic("fixture did not reach the real FTS index")
}

func put(e *format4.Engine, b format4.Batch) int64 {
	out, err := e.Put(ctx, b)
	check(err)
	require(len(out) == len(b.Records), "missing PUT outcomes")
	for _, o := range out {
		want := format4.ActNew
		if b.Mode == format4.ModeMigrate {
			want = format4.ActMigrated
		}
		require(o.Action == want, fmt.Sprintf("PUT did not insert: %+v", o))
	}
	return out[len(out)-1].Seq
}

func size(path string) int64 {
	s, err := os.Stat(path)
	if os.IsNotExist(err) {
		return 0
	}
	check(err)
	return s.Size()
}

func seed(root string, target int64) (int, int64) {
	stream := filepath.Join(root, "fsql4/P/WXF/noaa-ncep@gfs-0p25-sea.fsdata")
	mode := format4.CreateForMigration
	if size(filepath.Join(root, "fsql4/STORE")) > 0 {
		mode = format4.OpenExisting
	}
	e := open(root, mode)
	defer func() { check(e.Close(ctx)) }()
	check(e.RegisterType(spec(false)))
	n := 0
	if raw, err := os.ReadFile(filepath.Join(root, "seed-count")); err == nil {
		_, err = fmt.Sscanf(string(raw), "%d", &n)
		check(err)
	}
	start, last := time.Now(), time.Now()
	for size(stream) < target {
		// Migrate batches build the historical fixture efficiently while
		// retaining the real lane's distinct batch per nine-field tile. A
		// single seed tag would hide costs proportional to batch cardinality.
		records := make([]format4.In, 9*227)
		b := batch(records, "")
		b.Mode, b.Tags = format4.ModeMigrate, nil
		for i := range records {
			records[i] = weather(n + i)
			records[i].Seq = int64(n + i + 1)
			records[i].Tags = []format4.TagAt{{Tag: i / len(fields), At: records[i].TS}}
			if i%len(fields) == 0 {
				tag := batch(nil, fmt.Sprintf("seed-tile-%d", (n+i)/len(fields))).Tags[0]
				b.Tags = append(b.Tags, tag)
			}
		}
		put(e, b)
		n += len(records)
		check(os.WriteFile(filepath.Join(root, "seed-count"), []byte(fmt.Sprint(n)), 0600))
		if time.Since(last) > 10*time.Second {
			fmt.Printf("seed records=%d fsdata_bytes=%d elapsed=%s\n", n, size(stream), time.Since(start).Round(time.Millisecond))
			last = time.Now()
		}
	}
	if mode == format4.CreateForMigration {
		_, err := e.Rebuild(ctx, "WXF", format4.RebuildPartitionIndexes)
		check(err)
		check(e.Activate(ctx))
	}
	fmt.Printf("fixture records=%d fsdata_bytes=%d seed_time=%s\n", n, size(stream), time.Since(start))
	st, err := e.Types(ctx)
	check(err)
	for _, s := range st {
		if s.Type == "WXF" {
			return n, s.MaxSeq
		}
	}
	panic("seed type missing")
}

// Start the synthetic text index at the seed watermark. This represents an
// already-indexed partition without spending the benchmark on historical FTS
// construction. Seed payloads and all SQLite record indexes are real writes.
func seedTextWatermark(root string, seq int64) {
	p := filepath.Join(root, "fsql4/T/WXF.fts")
	query := fmt.Sprintf("CREATE TABLE IF NOT EXISTS ftsmeta(k TEXT PRIMARY KEY, v) WITHOUT ROWID; INSERT OR REPLACE INTO ftsmeta VALUES('through', %d);", seq)
	out, err := exec.Command("sqlite3", p, query).CombinedOutput()
	if err != nil {
		panic(fmt.Sprintf("seed text watermark: %v: %s", err, out))
	}
}

type measurement struct {
	Arm           string  `json:"arm"`
	Records       int     `json:"records"`
	PayloadBytes  int64   `json:"payload_bytes"`
	AppendSeconds float64 `json:"append_reference_seconds"`
	IngestSeconds float64 `json:"ingest_seconds"`
	SettleSeconds float64 `json:"fts_catchup_seconds"`
	WrittenBytes  uint64  `json:"physical_written_bytes"`
	Amplification float64 `json:"physical_write_amplification"`
	GroupCommits  uint64  `json:"group_commits"`
	StreamSyncs   uint64  `json:"stream_syncs"`
	FTSRows       uint64  `json:"fts_rows"`
	FTSBytes      int64   `json:"fts_file_bytes"`
}

func measure(root, arm string, first int, fullText bool) measurement {
	batchName := fmt.Sprintf("%s-%d", arm, first)
	records := make([]format4.In, tiles*len(fields))
	var payload int64
	for i := range records {
		records[i] = weather(first + i)
		payload += int64(len(records[i].Plain))
	}
	// Independent append-only reference, synced once per tile like PUT. This
	// is a reference cost, not a subtraction claiming CPU/index phase timing.
	f, err := os.CreateTemp(root, "append-reference-")
	check(err)
	start := time.Now()
	for i, r := range records {
		_, err = f.Write(r.Plain)
		check(err)
		if (i+1)%len(fields) == 0 {
			check(f.Sync())
		}
	}
	appendTime := time.Since(start)
	check(f.Close())
	check(os.Remove(f.Name()))

	e := open(root, format4.OpenExisting)
	check(e.RegisterType(spec(fullText)))
	before, err := e.Stats()
	check(err)
	writtenBefore := diskWritten()
	start = time.Now()
	var lastSeq int64
	for i := 0; i < len(records); i += len(fields) {
		lastSeq = put(e, batch(records[i:i+len(fields)], fmt.Sprintf("%s-tile-%d", batchName, i/len(fields))))
	}
	ingestTime := time.Since(start)
	check(os.WriteFile(filepath.Join(root, "seed-count"), []byte(fmt.Sprint(first+len(records))), 0600))
	settle := time.Now()
	if fullText {
		deadline := time.Now().Add(15 * time.Minute)
		for {
			st, err := e.FTS(ctx)
			check(err)
			ready := false
			for _, s := range st {
				if s.Type == "WXF" && s.Through >= lastSeq {
					ready = true
				}
			}
			if ready {
				break
			}
			require(time.Now().Before(deadline), "FTS catch-up exceeded 15 minutes")
			time.Sleep(100 * time.Millisecond)
		}
	}
	settleTime := time.Since(settle)
	after, err := e.Stats()
	check(err)
	if fullText {
		require(after[39]-before[39] == uint64(len(records)), "baseline did not index every grid in FTS")
	}
	// Close flushes checkpoints before physical write accounting is sampled.
	check(e.Close(ctx))
	written := diskWritten() - writtenBefore
	m := measurement{Arm: arm, Records: len(records), PayloadBytes: payload,
		AppendSeconds: appendTime.Seconds(), IngestSeconds: ingestTime.Seconds(), SettleSeconds: settleTime.Seconds(),
		WrittenBytes: written, Amplification: float64(written) / float64(payload),
		GroupCommits: after[12] - before[12], StreamSyncs: after[13] - before[13], FTSRows: after[39] - before[39],
		FTSBytes: size(filepath.Join(root, "fsql4/T/WXF.fts"))}
	encoded, err := json.Marshal(m)
	check(err)
	fmt.Println(string(encoded))
	// Reopen and prove every payload byte and source identity survived.
	e = open(root, format4.OpenExisting)
	defer func() { check(e.Close(ctx)) }()
	for i := 0; i < len(records); i += 64 {
		end := min(i+64, len(records))
		ids := make([]string, end-i)
		want := map[string][]byte{}
		for j, r := range records[i:end] {
			ids[j], want[r.CID] = r.CID, r.Plain
		}
		got, err := e.Get(ctx, "WXF", ids, false, true)
		check(err)
		require(len(got) == len(ids), "reopen missing records")
		for _, r := range got {
			require(bytes.Equal(r.Data, want[r.CID]), "record bytes changed")
			require(r.Peer == "wxf-amplification-e2e" && r.TS == 1792238400, "record provenance changed")
		}
		tags, err := e.Tags(ctx, "WXF", ids)
		check(err)
		require(len(tags) == len(ids), "reopen missing source tags")
		for _, tag := range tags {
			require(tag.Provider == "noaa-ncep" && tag.Source == "gfs-0p25-sea" && tag.SourceURL == "https://example.test/gfs", "source tags changed")
		}
	}
	got, err := e.Scan(ctx, format4.Query{Type: "WXF", Lane: format4.LaneFilter{Provider: "noaa-ncep", Source: "gfs-0p25-sea", Batch: batchName + "-tile-0"}, Limit: 10, Hydrate: true})
	check(err)
	require(len(got) == len(fields), "batch metadata lookup changed")
	for _, r := range got {
		require(r.Tag != nil && r.Tag.Batch == batchName+"-tile-0", "batch provenance changed")
	}
	if !fullText {
		states, err := e.FTS(ctx)
		check(err)
		for _, s := range states {
			if s.Type == "WXF" {
				require(s.State == "off", "WXF full text re-enabled on reopen")
			}
		}
	}
	return m
}

func main() {
	root := flag.String("root", os.Getenv("SDN_WXF_E2E_ROOT"), "owned fixture directory; retained when specified")
	seedBytes := flag.Int64("seed-bytes", 15_000_000_000, "minimum populated fsdata bytes")
	baselineOnly := flag.Bool("baseline-only", false, "measure baseline before changing registration")
	fixtureOnly := flag.Bool("fixture-only", false, "validate the synthetic grid before measuring")
	flag.Parse()
	checkSearchFixture()
	if *fixtureOnly {
		return
	}
	if *root == "" {
		dir, err := os.MkdirTemp("", "wxf-ingest-amplification-")
		check(err)
		*root = dir
		defer os.RemoveAll(dir)
	} else {
		check(os.MkdirAll(*root, 0700))
	}
	_, _, err := flatsqlrt.PrewarmP4ThreadsAOT(storage.EngineAOTCacheDir())
	check(err)
	n, seq := seed(*root, *seedBytes)
	seedTextWatermark(*root, seq)
	a := measure(*root, "before", n, true)
	n += tiles * len(fields)
	check(os.WriteFile(filepath.Join(*root, "seed-count"), []byte(fmt.Sprint(n)), 0600))
	if *baselineOnly {
		return
	}
	s, err := format4.TypeSpecFor("WXF.fbs")
	check(err)
	require(!s.FullText && len(s.BFBS) > 0 && s.Flags != 0, "WXF must retain verification and SQL schema without FTS")
	b := measure(*root, "after", n, s.FullText)
	n += tiles * len(fields)
	check(os.WriteFile(filepath.Join(*root, "seed-count"), []byte(fmt.Sprint(n)), 0600))
	require(b.FTSRows == 0, "candidate still writes FTS rows")
	require(b.GroupCommits <= tiles, "index commits exceed input batches")
	fmt.Printf("PASS records_verified=%d ingest_speedup=%.3f settled_speedup=%.3f write_amplification=%.3f->%.3f\n",
		2*tiles*len(fields), a.IngestSeconds/b.IngestSeconds, (a.IngestSeconds+a.SettleSeconds)/(b.IngestSeconds+b.SettleSeconds), a.Amplification, b.Amplification)
}
