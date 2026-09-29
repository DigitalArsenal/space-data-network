package storage

// Every record the node serves verifies with the stock FlatBuffers verifier
// once placed at its alignment origin (recordalign), and the served bytes
// are the stored bytes: CIDs recomputed from them match. Stored records whose
// size prefix was stripped at publish (len 4 mod 8) and bare builds (len
// 0 mod 8) run through every serving read path of both store formats.

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/flatsqlrt"
	"github.com/spacedatanetwork/sdn-server/internal/sds"
	"github.com/spacedatanetwork/sdn-server/internal/storage/recordalign"
	"github.com/spacedatanetwork/sdn-server/internal/wasm"
)

// servedVerifier checks served bytes: CID identity, then the stock verifier
// through recordalign. It counts what the placement cost.
type servedVerifier struct {
	t      testing.TB
	v      *sds.Validator
	want   map[string]string // cid -> schema
	seen   map[string]map[string]bool
	copies map[string]int
	bytes  map[string]int64
	count  map[string]int
}

func newServedVerifier(t testing.TB) *servedVerifier {
	t.Helper()
	fm, err := wasm.NewEmbeddedFlatcModule(context.Background())
	if err != nil {
		t.Fatalf("NewEmbeddedFlatcModule: %v", err)
	}
	t.Cleanup(func() { fm.Close(context.Background()) })
	v, err := sds.NewValidator(fm)
	if err != nil {
		t.Fatal(err)
	}
	return &servedVerifier{t: t, v: v, want: map[string]string{}, seen: map[string]map[string]bool{},
		copies: map[string]int{}, bytes: map[string]int64{}, count: map[string]int{}}
}

// record checks one bare served record (a single-record response body).
func (sv *servedVerifier) record(path, schema string, served []byte) {
	sv.t.Helper()
	sv.identity(path, schema, served)
	view := recordalign.Record(served)
	if view.Copied {
		sv.copies[path]++
		sv.bytes[path] += int64(len(view.Buf))
	}
	sv.verify(path, schema, served, view)
}

// frame checks one served frame [u32 len][record]: its view never copies.
func (sv *servedVerifier) frame(path, schema string, frame []byte) {
	sv.t.Helper()
	view, ok := recordalign.Frame(frame)
	if !ok {
		sv.t.Fatalf("%s: malformed frame (%d bytes)", path, len(frame))
	}
	if view.Copied {
		sv.t.Fatalf("%s: a frame's view copied", path)
	}
	record := frame[recordalign.PrefixLen:]
	sv.identity(path, schema, record)
	sv.verify(path, schema, record, view)
}

// stream checks every frame of a served [u32 len][record]... stream.
func (sv *servedVerifier) stream(path, schema string, stream []byte) int {
	sv.t.Helper()
	n := 0
	for len(stream) > 0 {
		if len(stream) < 4 {
			sv.t.Fatalf("%s: truncated frame header", path)
		}
		size := int(binary.LittleEndian.Uint32(stream))
		if size == 0 {
			stream = stream[4:]
			continue
		}
		if 4+size > len(stream) {
			sv.t.Fatalf("%s: truncated frame", path)
		}
		sv.frame(path, schema, stream[:4+size])
		stream = stream[4+size:]
		n++
	}
	return n
}

func (sv *servedVerifier) identity(path, schema string, served []byte) {
	sv.t.Helper()
	cid := ComputeCID(served)
	if got, ok := sv.want[cid]; !ok || got != schema {
		sv.t.Fatalf("%s: served %s bytes hash to %s, which was never stored as %s (stored as %q)", path, schema, cid, schema, got)
	}
	if sv.seen[path] == nil {
		sv.seen[path] = map[string]bool{}
	}
	sv.seen[path][cid] = true
	sv.count[path]++
}

func (sv *servedVerifier) verify(path, schema string, record []byte, view recordalign.View) {
	sv.t.Helper()
	// The stock verifier is handed the view as is: a frame verifies in its
	// size-prefixed form, a bare record at its first byte.
	if err := sv.v.VerifyRecord(context.Background(), schema, view.Buf); err != nil {
		sv.t.Fatalf("%s: %s record (len %d, origin %d) fails the stock verifier: %v", path, schema, len(record), recordalign.OriginOffset(len(record)), err)
	}
	if !view.SizePrefixed && recordalign.Record(view.Buf).Copied {
		sv.t.Fatalf("%s: a bare view would be copied again", path)
	}
}

// alignmentRecords stores records of both alignment origins for CAT, OMM and
// MPE plus other standards, and returns cid -> schema.
func alignmentRecords(t *testing.T, sv *servedVerifier) map[string][][]byte {
	t.Helper()
	ctx := context.Background()
	out := map[string][][]byte{}
	add := func(schema string, data []byte) {
		if _, ok := sv.want[ComputeCID(data)]; ok {
			return
		}
		out[schema] = append(out[schema], data)
		sv.want[ComputeCID(data)] = schema
	}
	base := time.Date(2026, 9, 29, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 12; i++ {
		norad := uint32(40000 + i)
		name := fmt.Sprintf("SAT-%d%s", i, strings.Repeat("X", i))
		epoch := base.Add(time.Duration(i) * 11 * time.Minute)
		// The ingest lanes' form: Go builders finish size-prefixed, the
		// publish boundary strips it.
		add("OMM.fbs", f2TestOMM(norad, epoch, name))
		add("CAT.fbs", sds.NewCATBuilder().WithNoradCatID(norad).WithObjectName(name).
			WithObjectID(fmt.Sprintf("2026-%03dA", i)).WithOrbitalParams(95+float64(i), 53.2, 560, 540).Build()[4:])
		// flatc builds, both finishes; each variant is its own object, since
		// a CAT supersedes a stored CAT of the same identity.
		for k, opts := range []wasm.FlatcOption{sds.StoredRecordOptions | wasm.FlatcSizePrefixed, sds.StoredRecordOptions} {
			id := norad + 100 + uint32(1000*k)
			jsons := map[string]string{
				"OMM.fbs": fmt.Sprintf(`{"OBJECT_NAME":%q,"OBJECT_ID":"2026-%03dB%d","EPOCH":%q,"MEAN_MOTION":15.1,"ECCENTRICITY":0.0001,"NORAD_CAT_ID":%d}`,
					name, i, k, epoch.Format("2006-01-02T15:04:05.000000"), id),
				"MPE.fbs": fmt.Sprintf(`{"ENTITY_ID":"%d","EPOCH":%d.5,"MEAN_MOTION":15.1,"BSTAR":0.0001,"MEAN_ELEMENT_THEORY":"SGP4"}`, id, epoch.Unix()),
				"CAT.fbs": fmt.Sprintf(`{"OBJECT_NAME":%q,"OBJECT_ID":"2026-%03dC%d","NORAD_CAT_ID":%d,"PERIOD":%d.25}`, name, i, k, id, 90+i),
				// Other standards: SPW has no 8-byte field, TBS has doubles.
				"SPW.fbs": fmt.Sprintf(`{"DATE":%q,"BSRN":%d,"KP1":%d,"F107_OBS":%d.5}`, epoch.Format("2006-01-02"), 2600+i, 10+i, 70+i),
				"TBS.fbs": fmt.Sprintf(`{"ID":"310-410-%d-%d","MCC":310,"MNC":410,"LATITUDE":%d.125,"LONGITUDE":-97.5,"RANGE_M":%d.0,`+
					`"SOURCES":[{"PROVIDER_ID":"alignment","RETRIEVED_AT":"2026-09-29T00:00:00Z","LICENSE":"CC0-1.0","REPORTED_LATITUDE":%d.125}],`+
					`"CONSENSUS":{"PROVIDERS_CONSULTED":1,"CONFIDENCE":0.5}}`, 7000+i, id, 30+i, 500+i, 30+i),
			}
			for schema, js := range jsons {
				data, err := sv.v.JSONToFlatBuffer(ctx, schema, []byte(js), opts)
				if err != nil {
					t.Fatalf("%s: %v", schema, err)
				}
				if opts&wasm.FlatcSizePrefixed != 0 {
					data = data[4:]
				}
				add(schema, data)
			}
		}
	}
	for schema, recs := range out {
		origins := map[int]int{}
		for _, r := range recs {
			origins[recordalign.OriginOffset(len(r))]++
		}
		t.Logf("%s: %d records, origin 4: %d, origin 0: %d", schema, len(recs), origins[4], origins[0])
		// SPW has no 8-byte field: its bare and stripped builds are the same
		// bytes (one CID), so it has one origin.
		if schema != "SPW.fbs" && (origins[4] == 0 || origins[0] == 0) {
			t.Fatalf("%s fixture lacks an origin: %v", schema, origins)
		}
	}
	return out
}

func TestServedRecordsVerifyAtTheirOrigin(t *testing.T) {
	formats := []struct {
		name string
		open func(*testing.T) *FlatSQLStore
	}{
		{"format1", func(t *testing.T) *FlatSQLStore { return reopenDeferred(t, t.TempDir()) }},
		{"format2", func(t *testing.T) *FlatSQLStore { return openFormat2ForTest(t, t.TempDir()) }},
	}
	for _, format := range formats {
		t.Run(format.name, func(t *testing.T) {
			s := format.open(t)
			defer s.Close()
			sv := newServedVerifier(t)
			records := alignmentRecords(t, sv)
			schemas := make([]string, 0, len(records))
			for schema, recs := range records {
				schemas = append(schemas, schema)
				tags := SourceTags{ProviderID: "alignment-provider", SourceName: "alignment-" + strings.ToLower(strings.TrimSuffix(schema, ".fbs")), BatchID: "b-1"}
				if n, err := s.StoreBatchWithSourceTags(schema, recs, "source:alignment", nil, tags); err != nil || n != len(recs) {
					t.Fatalf("store %s: %d of %d, %v", schema, n, len(recs), err)
				}
			}
			sort.Strings(schemas)
			if !s.Format2() {
				if _, err := s.HydrateEngineHotWindow(); err != nil {
					t.Fatalf("hydrate the engine: %v", err)
				}
			}
			served := checkServingPaths(t, s, sv, schemas, records)
			for _, path := range served {
				t.Logf("%-34s %4d records, %3d copies, %6d bytes copied", path, sv.count[path], sv.copies[path], sv.bytes[path])
			}
		})
	}
}

// checkServingPaths reads every record through each serving path and checks
// what it served. It returns the path names.
func checkServingPaths(t *testing.T, s *FlatSQLStore, sv *servedVerifier, schemas []string, records map[string][][]byte) []string {
	t.Helper()
	var paths []string
	mark := func(path string) string { paths = append(paths, path); return path }

	// GET /api/v1/data/records/{schema}/{cid} (api/data.go handleRawRecord):
	// the body is record.Data.
	single := mark("single record (GetRawRecord)")
	for _, schema := range schemas {
		for _, d := range records[schema] {
			rec, err := s.GetRawRecord(schema, ComputeCID(d))
			if err != nil {
				t.Fatalf("GetRawRecord %s: %v", schema, err)
			}
			sv.record(single, schema, rec.Data)
		}
	}

	// /api/v1/data/query and /stream, datasync v1 pages, the admin export and
	// the libp2p FlatSQL sync: QueryRawRecords -> WriteRawRecordFrames.
	raw := mark("raw pages (WriteRawRecordFrames)")
	window := mark("indexed window (WriteRawRecordFrames)")
	export := mark("datasync export shard")
	for _, schema := range schemas {
		recs, err := s.QueryRawRecords(RawRecordQuery{SchemaName: schema, Limit: 1000})
		if err != nil {
			t.Fatalf("QueryRawRecords %s: %v", schema, err)
		}
		var buf bytes.Buffer
		if err := s.WriteRawRecordFrames(&buf, recs); err != nil {
			t.Fatal(err)
		}
		sv.stream(raw, schema, buf.Bytes())

		win, err := s.QueryIndexedRecords(IndexedRecordQuery{SchemaName: schema, Limit: 1000})
		if err != nil {
			t.Fatalf("QueryIndexedRecords %s: %v", schema, err)
		}
		buf.Reset()
		if err := s.WriteRawRecordFrames(&buf, win); err != nil {
			t.Fatal(err)
		}
		sv.stream(window, schema, buf.Bytes())

		exp, err := s.ExportDatasetWindow(t.TempDir(), IndexedRecordQuery{SchemaName: schema, Limit: 1000})
		if err != nil {
			t.Fatalf("ExportDatasetWindow %s: %v", schema, err)
		}
		shard, err := os.ReadFile(exp.ShardPath)
		if err != nil {
			t.Fatal(err)
		}
		if n := sv.stream(export, schema, shard); n != exp.RecordCount {
			t.Fatalf("export %s: %d frames, index says %d", schema, n, exp.RecordCount)
		}
	}

	// The engine's own streams: /api/v1/query and the module storage caps
	// (QueryRawStream, QuerySandboxedStream; format 2 frames in f2Frames).
	engine := mark("engine stream (QueryRawStream)")
	sandbox := mark("engine stream (QuerySandboxedStream)")
	for _, schema := range []string{"OMM.fbs", "CAT.fbs", "MPE.fbs"} {
		table := strings.TrimSuffix(schema, ".fbs")
		stream, err := s.QueryRawStream("SELECT _data FROM " + table)
		if err != nil {
			t.Fatalf("QueryRawStream %s: %v", table, err)
		}
		if n := sv.stream(engine, schema, stream.Bytes); n != len(records[schema]) {
			t.Fatalf("QueryRawStream %s: %d frames, stored %d", table, n, len(records[schema]))
		}
		sandboxed, err := s.QuerySandboxedStream("SELECT _data FROM "+table, flatsqlrt.SandboxCaps{MaxRows: 1000})
		if err != nil {
			t.Fatalf("QuerySandboxedStream %s: %v", table, err)
		}
		sv.stream(sandbox, schema, sandboxed.Bytes)
	}

	// Every path served every record.
	for _, path := range paths {
		want := 0
		for _, schema := range schemas {
			if strings.HasPrefix(path, "engine") && schema != "OMM.fbs" && schema != "CAT.fbs" && schema != "MPE.fbs" {
				continue
			}
			want += len(records[schema])
		}
		if got := len(sv.seen[path]); got != want {
			t.Fatalf("%s served %d distinct records, want %d", path, got, want)
		}
	}
	return paths
}

// TestServedRecordsVerifyOnStoreCopy serves a real store: it opens a COPY of
// a store directory (SDN_RECORDALIGN_STORE_DIR; SDN_RECORDALIGN_STORE_FORMAT=2
// for a migrated format-2 store) and pages every CAT, OMM and MPE record,
// and up to SDN_RECORDALIGN_SAMPLE (default 5000) records of every other
// standard, through the datasync page path (QueryRawRecords with the row-id
// cursor, then WriteRawRecordFrames). Each served frame must carry bytes
// whose CID is the record's CID and verify with the stock verifier through
// recordalign.Frame; every 100th record is also read as a single-record body
// (GetRawRecord) and verified through recordalign.Record.
func TestServedRecordsVerifyOnStoreCopy(t *testing.T) {
	dir := os.Getenv("SDN_RECORDALIGN_STORE_DIR")
	if dir == "" {
		t.Skip("SDN_RECORDALIGN_STORE_DIR not set (names a COPY of a store directory)")
	}
	sample := 5000
	if raw := os.Getenv("SDN_RECORDALIGN_SAMPLE"); raw != "" {
		fmt.Sscan(raw, &sample)
	}
	started := time.Now()
	var s *FlatSQLStore
	if os.Getenv("SDN_RECORDALIGN_STORE_FORMAT") == "2" {
		s = openFormat2ForTest(t, dir)
	} else {
		s = reopenDeferred(t, dir)
	}
	defer s.Close()
	t.Logf("opened %s (format 2: %v) in %s", dir, s.Format2(), time.Since(started).Round(time.Millisecond))

	type job struct {
		schema, cid, path string
		frame             []byte
		single            bool
	}
	type tally struct {
		records, singles, cidBad, verifyBad, frameCopies, singleCopies int
		singleCopied                                                   int64
		notes                                                          []string
	}
	var mu sync.Mutex
	stats := map[string]*tally{}
	jobs := make(chan job, 4096)
	var wg sync.WaitGroup
	for w := 0; w < 8; w++ {
		sv := newServedVerifier(t)
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx := context.Background()
			for j := range jobs {
				var record []byte
				var view recordalign.View
				ok := true
				if j.single {
					record = j.frame
					view = recordalign.Record(record)
				} else {
					view, ok = recordalign.Frame(j.frame)
					record = j.frame[recordalign.PrefixLen:]
				}
				cidBad := !ok || ComputeCID(record) != j.cid
				verr := sv.v.VerifyRecord(ctx, j.schema, view.Buf)
				mu.Lock()
				st := stats[j.schema]
				if st == nil {
					st = &tally{}
					stats[j.schema] = st
				}
				if j.single {
					st.singles++
					if view.Copied {
						st.singleCopies++
						st.singleCopied += int64(len(view.Buf))
					}
				} else {
					st.records++
					if view.Copied {
						st.frameCopies++
					}
				}
				if cidBad {
					st.cidBad++
				}
				if verr != nil {
					st.verifyBad++
					if len(st.notes) < 3 {
						st.notes = append(st.notes, fmt.Sprintf("%s %s len %d: %v", j.path, j.cid, len(record), verr))
					}
				}
				mu.Unlock()
			}
		}()
	}

	counts, err := s.Stats()
	if err != nil {
		t.Fatal(err)
	}
	var schemas []string
	for schema, n := range counts {
		if strings.HasSuffix(schema, ".fbs") && n > 0 {
			schemas = append(schemas, schema)
		}
	}
	sort.Strings(schemas)
	for _, schema := range schemas {
		limit := sample
		if schema == "CAT.fbs" || schema == "OMM.fbs" || schema == "MPE.fbs" {
			limit = -1
		}
		seen := map[string]bool{}
		var after int64
		for limit < 0 || len(seen) < limit {
			page, err := s.QueryRawRecords(RawRecordQuery{SchemaName: schema, Limit: 5000, UseRowIDCursor: true, AfterRowID: after})
			if err != nil {
				t.Fatalf("QueryRawRecords %s after %d: %v", schema, after, err)
			}
			if len(page) == 0 {
				break
			}
			var buf bytes.Buffer
			if err := s.WriteRawRecordFrames(&buf, page); err != nil {
				t.Fatal(err)
			}
			stream := buf.Bytes()
			for _, rec := range page {
				size := 4 + len(rec.Data)
				frame := stream[:size]
				stream = stream[size:]
				after = max(after, rec.RowID)
				if seen[rec.CID] {
					continue
				}
				seen[rec.CID] = true
				jobs <- job{schema: schema, cid: rec.CID, path: "page", frame: frame}
				if len(seen)%100 == 1 {
					single, err := s.GetRawRecord(schema, rec.CID)
					if err != nil {
						t.Fatalf("GetRawRecord %s %s: %v", schema, rec.CID, err)
					}
					jobs <- job{schema: schema, cid: rec.CID, path: "single", frame: single.Data, single: true}
				}
				if limit >= 0 && len(seen) >= limit {
					break
				}
			}
		}
	}
	close(jobs)
	wg.Wait()

	t.Logf("%-6s %8s %8s %8s %8s %8s %8s %10s", "STD", "served", "copies", "singles", "copies", "cid-bad", "verify-bad", "copied B")
	var bad int
	for _, schema := range schemas {
		st := stats[schema]
		if st == nil {
			continue
		}
		t.Logf("%-6s %8d %8d %8d %8d %8d %8d %10d", strings.TrimSuffix(schema, ".fbs"), st.records, st.frameCopies, st.singles, st.singleCopies, st.cidBad, st.verifyBad, st.singleCopied)
		for _, note := range st.notes {
			t.Logf("       %s", note)
		}
		bad += st.cidBad + st.verifyBad
	}
	t.Logf("served and verified in %s", time.Since(started).Round(time.Second))
	if bad != 0 {
		t.Fatalf("%d served record(s) failed CID identity or the stock verifier", bad)
	}
}
