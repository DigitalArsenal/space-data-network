package main

import (
	"archive/zip"
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"math/rand/v2"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	CATFB "github.com/DigitalArsenal/spacedatastandards.org/lib/go/CAT"
)

// The builder module the importer tests run against: the signed artifact of
// space-data-network-modules data-source/gp-archive-records, recorded in
// testdata/gp_archive/gp-archive-records.provenance.json.
var (
	gpArchiveTestBuilderOnce sync.Once
	gpArchiveTestBuilder     *gpWASMRecordBuilder
	gpArchiveTestBuilderErr  error
)

func gpArchiveTestRecordBuilderPath() string {
	if p := os.Getenv("SDN_GP_ARCHIVE_RECORD_BUILDER_WASM"); p != "" {
		return p
	}
	return filepath.Join("testdata", "gp_archive", gpArchiveRecordBuilderFile)
}

func testGPArchiveRecordBuilder(t testing.TB) gpArchiveRecordBuilder {
	t.Helper()
	gpArchiveTestBuilderOnce.Do(func() {
		gpArchiveTestBuilder, _, gpArchiveTestBuilderErr = loadGPArchiveRecordBuilder(gpArchiveTestRecordBuilderPath())
	})
	if gpArchiveTestBuilderErr != nil {
		t.Fatal(gpArchiveTestBuilderErr)
	}
	return gpArchiveTestBuilder
}

func testBuildMPE(t testing.TB, records ...gpArchiveRecord) [][]byte {
	t.Helper()
	out, err := testGPArchiveRecordBuilder(t).BuildMPE(context.Background(), records)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func testBuildCAT(t testing.TB, rows ...gpArchiveCATInput) [][]byte {
	t.Helper()
	out, err := testGPArchiveRecordBuilder(t).BuildCAT(context.Background(), rows)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestGPArchiveRecordBuilderTestdataMatchesProvenance(t *testing.T) {
	var provenance struct {
		SHA256 string `json:"sha256"`
	}
	b, err := os.ReadFile(filepath.Join("testdata", "gp_archive", "gp-archive-records.provenance.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(b, &provenance); err != nil {
		t.Fatal(err)
	}
	wasm, err := os.ReadFile(filepath.Join("testdata", "gp_archive", gpArchiveRecordBuilderFile))
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(wasm)
	if got := hex.EncodeToString(sum[:]); got != provenance.SHA256 {
		t.Fatalf("testdata builder sha256 %s, provenance records %s", got, provenance.SHA256)
	}
}

// Archive encoding v1 vectors shared with the module's own suite
// (data-source/gp-archive-records/tests/archive-v1-vectors.json). Their
// expected bytes came from the original Go builders. Each vector's field values
// must match its expected bytes, and the importer's field batch for those
// values must come back from the module as exactly the expected bytes.
func TestGPArchiveRecordBuilderReproducesArchiveV1Vectors(t *testing.T) {
	var vectors struct {
		MPE []map[string]any `json:"mpe"`
		CAT []map[string]any `json:"cat"`
	}
	b, err := os.ReadFile(filepath.Join("testdata", "gp_archive", "archive-v1-vectors.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(b, &vectors); err != nil {
		t.Fatal(err)
	}
	if len(vectors.MPE) < 100 || len(vectors.CAT) < 50 {
		t.Fatalf("vectors: %d MPE, %d CAT", len(vectors.MPE), len(vectors.CAT))
	}
	str := func(v map[string]any, key string) string {
		if s, ok := v[key+"_BASE64"].(string); ok {
			raw, err := base64.StdEncoding.DecodeString(s)
			if err != nil {
				t.Fatal(err)
			}
			return string(raw)
		}
		return v[key].(string)
	}
	bits := func(v map[string]any, key string) float64 {
		u, err := strconv.ParseUint(v[key].(string), 16, 64)
		if err != nil {
			t.Fatalf("%s %s: %v", v["id"], key, err)
		}
		return math.Float64frombits(u)
	}
	records := make([]gpArchiveRecord, len(vectors.MPE))
	for i, v := range vectors.MPE {
		r := gpArchiveRecord{EntityID: str(v, gpKeyEntityID), Epoch: bits(v, gpKeyEpoch), MeanMotion: bits(v, gpKeyMeanMotion), Eccentricity: bits(v, gpKeyEccentricity), Inclination: bits(v, gpKeyInclination), RAOfAscNode: bits(v, gpKeyRAOfAscNode), ArgOfPericenter: bits(v, gpKeyArgOfPericenter), MeanAnomaly: bits(v, gpKeyMeanAnomaly), BSTAR: bits(v, gpKeyBSTAR)}
		if hex.EncodeToString(legacyArchiveV1MPE(r.EntityID, r)) != v["expected"] {
			t.Fatalf("%s: vector values do not match its expected bytes", v["id"])
		}
		records[i] = r
	}
	for i, got := range testBuildMPE(t, records...) {
		if hex.EncodeToString(got) != vectors.MPE[i]["expected"] {
			t.Fatalf("%s: module bytes differ from archive v1", vectors.MPE[i]["id"])
		}
	}
	rows := make([]gpArchiveCATInput, len(vectors.CAT))
	for i, v := range vectors.CAT {
		rows[i] = gpArchiveCATInput{EntityID: str(v, gpKeyObjectID), NORAD: uint32(v[gpKeyNORADCatID].(float64)), Name: str(v, gpKeyObjectName)}
		if hex.EncodeToString(legacyArchiveV1CAT(rows[i].EntityID, rows[i].NORAD, rows[i].Name)) != v["expected"] {
			t.Fatalf("%s: vector values do not match its expected bytes", v["id"])
		}
	}
	for i, got := range testBuildCAT(t, rows...) {
		if hex.EncodeToString(got) != vectors.CAT[i]["expected"] {
			t.Fatalf("%s: module bytes differ from archive v1", vectors.CAT[i]["id"])
		}
	}
}

// Randomized parity against the frozen archive v1 reference: arbitrary finite
// doubles by bit pattern (subnormals, negative zero, integers), and strings of
// arbitrary bytes, including quotes, backslashes, control bytes and bytes that
// are not UTF-8.
func TestGPArchiveRecordBuilderMatchesArchiveV1OnRandomRecords(t *testing.T) {
	rng := rand.New(rand.NewPCG(20260927, 1))
	anyDouble := func() float64 {
		switch rng.IntN(8) {
		case 0:
			return 0
		case 1:
			return math.Copysign(0, -1)
		case 2:
			return float64(rng.Int64N(1<<40) - 1<<39)
		case 3:
			return math.Float64frombits(rng.Uint64() & 0x800fffffffffffff) // subnormal
		default:
			for {
				if f := math.Float64frombits(rng.Uint64()); !math.IsNaN(f) && !math.IsInf(f, 0) {
					return f
				}
			}
		}
	}
	anyString := func() string {
		alphabet := []byte("0123456789-ABCDEFGHIJKLMNOPQRSTUVWXYZ :/()\"\\\x00\x01\n\t\x7f\x80\xbf\xc3\xa9\xe2\x98\x83\xf0\x9f\x98\x80\xff")
		b := make([]byte, rng.IntN(40))
		for i := range b {
			b[i] = alphabet[rng.IntN(len(alphabet))]
		}
		return string(b)
	}
	records := make([]gpArchiveRecord, 10000)
	for i := range records {
		records[i] = gpArchiveRecord{EntityID: anyString(), Epoch: anyDouble(), MeanMotion: anyDouble(), Eccentricity: anyDouble(), Inclination: anyDouble(), RAOfAscNode: anyDouble(), ArgOfPericenter: anyDouble(), MeanAnomaly: anyDouble(), BSTAR: anyDouble()}
	}
	for i, got := range testBuildMPE(t, records...) {
		if want := legacyArchiveV1MPE(records[i].EntityID, records[i]); !bytes.Equal(got, want) {
			t.Fatalf("record %d %+v:\nmodule %x\nv1     %x", i, records[i], got, want)
		}
	}
	rows := make([]gpArchiveCATInput, 5000)
	for i := range rows {
		norad := rng.Uint32()
		switch rng.IntN(4) {
		case 0:
			norad = 0
		case 1:
			norad = math.MaxUint32
		}
		rows[i] = gpArchiveCATInput{EntityID: anyString(), NORAD: norad, Name: anyString()}
	}
	for i, got := range testBuildCAT(t, rows...) {
		if want := legacyArchiveV1CAT(rows[i].EntityID, rows[i].NORAD, rows[i].Name); !bytes.Equal(got, want) {
			t.Fatalf("row %d %+v:\nmodule %x\nv1     %x", i, rows[i], got, want)
		}
	}
}

func TestGPArchiveRecordBuilderRefusesWhatTheImporterNeverSends(t *testing.T) {
	b := testGPArchiveRecordBuilder(t).(*gpWASMRecordBuilder)
	b.buf = []byte(`[{"ENTITY_ID":"A","EPOCH":1}]`)
	if _, err := b.invoke(context.Background(), "build_mpe", 1); err == nil || !strings.Contains(err.Error(), "not a GPAF field batch") {
		t.Fatalf("err = %v", err)
	}
	b.buf = appendGPArchiveFieldBatchHeader(nil, gpArchiveMPEFields[:len(gpArchiveMPEFields)-1], 0)
	if _, err := b.invoke(context.Background(), "build_mpe", 0); err == nil || !strings.Contains(err.Error(), "missing field BSTAR") {
		t.Fatalf("err = %v", err)
	}
	b.buf = appendGPArchiveMPEInput(nil, []gpArchiveRecord{{EntityID: "A"}})
	b.buf = b.buf[:len(b.buf)-1]
	if _, err := b.invoke(context.Background(), "build_mpe", 1); err == nil || !strings.Contains(err.Error(), "record 0 is truncated") {
		t.Fatalf("err = %v", err)
	}
}

// Every key the importer reads or writes is taken from one table (the gpKey
// constants); this checks the table against the SDS schemas SDN embeds and the
// JSON struct tags against the table.
func TestGPArchiveKeysMatchSDSSchemas(t *testing.T) {
	fields := func(schema, table string) map[string]bool {
		b, err := os.ReadFile(filepath.Join("..", "..", "internal", "sds", "schemas", schema))
		if err != nil {
			t.Fatal(err)
		}
		block := regexp.MustCompile(`(?s)\ntable ` + table + ` \{(.*?)\n\}`).FindSubmatch(b)
		if block == nil {
			t.Fatalf("table %s not found in %s", table, schema)
		}
		out := map[string]bool{}
		for _, m := range regexp.MustCompile(`(?m)^\s*([A-Za-z_][A-Za-z0-9_]*)\s*:`).FindAllSubmatch(block[1], -1) {
			out[string(m[1])] = true
		}
		return out
	}
	omm, mpe, cat := fields("OMM.fbs", "OMM"), fields("MPE.fbs", "MPE"), fields("CAT.fbs", "CAT")
	spaceTrackOnly := map[string]bool{gpKeyGPID: true, gpKeyINTLDES: true, gpKeySATNAME: true}
	gpKeys := []string{gpKeyGPID, gpKeyCreationDate, gpKeyObjectID, gpKeyObjectName, gpKeyNORADCatID, gpKeyEpoch, gpKeyMeanMotion, gpKeyEccentricity, gpKeyInclination, gpKeyRAOfAscNode, gpKeyArgOfPericenter, gpKeyMeanAnomaly, gpKeyBSTAR}
	for _, k := range gpKeys {
		if !omm[k] && !spaceTrackOnly[k] {
			t.Errorf("Space-Track GP key %s is not an OMM field", k)
		}
	}
	for _, k := range []string{gpKeyNORADCatID, gpKeyObjectName} {
		if !cat[k] {
			t.Errorf("SATCAT key %s is not a CAT field", k)
		}
	}
	for k := range spaceTrackOnly {
		if omm[k] || mpe[k] || cat[k] {
			t.Errorf("%s is listed as Space-Track only but is an SDS field", k)
		}
	}
	// build_mpe keys: ENTITY_ID, EPOCH and the elements, in MPE schema order.
	mpeKeys := append([]string{gpKeyEntityID, gpKeyEpoch}, gpArchiveElementKeys[:]...)
	order := regexp.MustCompile(`(?s)\ntable MPE \{.*?\n\}`)
	b, err := os.ReadFile(filepath.Join("..", "..", "internal", "sds", "schemas", "MPE.fbs"))
	if err != nil {
		t.Fatal(err)
	}
	schemaOrder := []string{}
	for _, m := range regexp.MustCompile(`(?m)^\s*([A-Z_]+)\s*:`).FindAllSubmatch(order.Find(b), -1) {
		schemaOrder = append(schemaOrder, string(m[1]))
	}
	if !reflect.DeepEqual(schemaOrder[:len(mpeKeys)], mpeKeys) {
		t.Errorf("build_mpe keys %v, MPE schema order %v", mpeKeys, schemaOrder)
	}
	for _, k := range []string{gpKeyObjectID, gpKeyNORADCatID, gpKeyObjectName} {
		if !cat[k] {
			t.Errorf("build_cat key %s is not a CAT field", k)
		}
	}
	// JSON struct tags equal the table.
	tags := func(v any) []string {
		var out []string
		rt := reflect.TypeOf(v)
		for i := 0; i < rt.NumField(); i++ {
			out = append(out, rt.Field(i).Tag.Get("json"))
		}
		return out
	}
	if got := tags(gpJSONRecord{}); !reflect.DeepEqual(got, gpKeys) {
		t.Errorf("gpJSONRecord tags %v, table %v", got, gpKeys)
	}
	if got, want := tags(gpSATCATRecord{}), []string{gpKeyNORADCatID, gpKeyINTLDES, gpKeyObjectName, gpKeySATNAME}; !reflect.DeepEqual(got, want) {
		t.Errorf("gpSATCATRecord tags %v, table %v", got, want)
	}
}

// ---------------------------------------------------------------------------
// Full-corpus gate. Builds every record of every archive source twice, with the
// archive v1 reference and with the module, and compares bytes. Read-only on
// the archive. Opt-in:
//
//	SDN_GP_ARCHIVE_CORPUS=/opt/data/sdn-archive \
//	SDN_GP_ARCHIVE_CORPUS_REPORT=<report.json> SDN_GP_ARCHIVE_CORPUS_JOBS=8 \
//	scripts/go-with-wasmedge.sh test -run TestGPArchiveRecordBuilderCorpusParity \
//	  -timeout 0 -v ./cmd/spacedatanetwork
// ---------------------------------------------------------------------------

type gpCorpusCounts struct {
	Files      int64    `json:"files"`
	Records    int64    `json:"records"`
	Identical  int64    `json:"identical"`
	Mismatched int64    `json:"mismatched"`
	Rejected   int64    `json:"rejected_by_parser"`
	Examples   []string `json:"mismatch_examples,omitempty"`
}

type gpCorpusReport struct {
	Root          string                     `json:"root"`
	StartedAt     string                     `json:"started_at"`
	UpdatedAt     string                     `json:"updated_at"`
	Complete      bool                       `json:"complete"`
	Jobs          int                        `json:"jobs"`
	BuilderPath   string                     `json:"builder_path"`
	BuilderSHA256 string                     `json:"builder_sha256"`
	SATCAT        string                     `json:"identity_satcat"`
	Sources       map[string]*gpCorpusCounts `json:"sources"`
	mu            sync.Mutex
}

func (r *gpCorpusReport) add(source string, c gpCorpusCounts) {
	r.mu.Lock()
	defer r.mu.Unlock()
	s := r.Sources[source]
	if s == nil {
		s = &gpCorpusCounts{}
		r.Sources[source] = s
	}
	s.Files += c.Files
	s.Records += c.Records
	s.Identical += c.Identical
	s.Mismatched += c.Mismatched
	s.Rejected += c.Rejected
	for _, e := range c.Examples {
		if len(s.Examples) < 20 {
			s.Examples = append(s.Examples, e)
		}
	}
}

func (r *gpCorpusReport) write(path string) error {
	r.mu.Lock()
	r.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	b, err := json.MarshalIndent(r, "", "  ")
	r.mu.Unlock()
	if err != nil || path == "" {
		return err
	}
	return writeGPArchiveJSON(path, json.RawMessage(b))
}

type gpCorpusItem struct {
	kind  string // zip, window, gp, satcat
	path  string
	entry int
}

func TestGPArchiveRecordBuilderCorpusParity(t *testing.T) {
	root := os.Getenv("SDN_GP_ARCHIVE_CORPUS")
	if root == "" {
		t.Skip("SDN_GP_ARCHIVE_CORPUS is unset")
	}
	jobs := 8
	if v, err := strconv.Atoi(os.Getenv("SDN_GP_ARCHIVE_CORPUS_JOBS")); err == nil && v > 0 {
		jobs = min(v, 8)
	}
	reportPath := os.Getenv("SDN_GP_ARCHIVE_CORPUS_REPORT")
	builderPath := gpArchiveTestRecordBuilderPath()
	wasm, err := os.ReadFile(builderPath)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(wasm)
	report := &gpCorpusReport{Root: root, StartedAt: time.Now().UTC().Format(time.RFC3339), Jobs: jobs, BuilderPath: builderPath, BuilderSHA256: hex.EncodeToString(sum[:]), Sources: map[string]*gpCorpusCounts{}}

	ledgerPath := filepath.Join(root, "state", "spacetrack-ledger.json")
	var ledger gpArchiveLedger
	b, err := os.ReadFile(ledgerPath)
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(b, &ledger); err != nil {
		t.Fatal(err)
	}
	satcatFor := func(path string) map[uint32]gpSATCATEntry {
		m, err := loadGPArchiveSATCAT(gpArchiveOptions{SATCATPath: path, LedgerPath: ledgerPath}, &ledger, gpArchiveTestCheckpoint(), newGPArchiveRunReport(), map[string]gpArchiveSource{})
		if err != nil {
			t.Fatal(err)
		}
		return m
	}
	identity := satcatFor("") // newest snapshot, as the importer uses
	report.SATCAT = "newest under " + filepath.Join(root, "spacetrack", "satcat")

	var items []gpCorpusItem
	zips, _ := filepath.Glob(filepath.Join(root, "historical", "*", "Archives2.zip"))
	for _, z := range zips {
		zr, err := zip.OpenReader(z)
		if err != nil {
			t.Fatal(err)
		}
		for i, f := range zr.File {
			if strings.HasSuffix(strings.ToLower(f.Name), ".csv") {
				items = append(items, gpCorpusItem{kind: "zip", path: z, entry: i})
			}
		}
		zr.Close()
	}
	for _, g := range []struct{ kind, glob string }{
		{"window", "spacetrack/gp_history/by-creation/*/*.json.gz"},
		{"gp", "spacetrack/gp/*/*.json.gz"},
		{"satcat", "spacetrack/satcat/*/*.json.gz"},
	} {
		paths, _ := filepath.Glob(filepath.Join(root, g.glob))
		sort.Strings(paths)
		for _, p := range paths {
			items = append(items, gpCorpusItem{kind: g.kind, path: p})
		}
	}
	if limit, err := strconv.Atoi(os.Getenv("SDN_GP_ARCHIVE_CORPUS_LIMIT")); err == nil && limit > 0 {
		// Sampling for a quick look: every n-th item.
		step := max(1, len(items)/limit)
		var sampled []gpCorpusItem
		for i := 0; i < len(items); i += step {
			sampled = append(sampled, items[i])
		}
		items = sampled
	}
	t.Logf("corpus: %d items (%d jobs), builder %s sha256 %s", len(items), jobs, builderPath, report.BuilderSHA256)

	work := make(chan gpCorpusItem)
	var done atomic.Int64
	var failed atomic.Value
	var wg sync.WaitGroup
	for w := 0; w < jobs; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			builder, _, err := loadGPArchiveRecordBuilder(builderPath)
			if err != nil {
				failed.Store(err.Error())
				for range work {
				}
				return
			}
			defer builder.Close()
			zipReaders := map[string]*zip.ReadCloser{}
			defer func() {
				for _, zr := range zipReaders {
					zr.Close()
				}
			}()
			for item := range work {
				if err := gpCorpusCompareItem(item, builder, identity, satcatFor, zipReaders, report); err != nil {
					failed.Store(fmt.Sprintf("%s %s#%d: %v", item.kind, item.path, item.entry, err))
				}
				done.Add(1)
			}
		}()
	}
	stop := make(chan struct{})
	go func() {
		tick := time.NewTicker(time.Minute)
		defer tick.Stop()
		for {
			select {
			case <-stop:
				return
			case <-tick.C:
				_ = report.write(reportPath)
				t.Logf("progress %d/%d items", done.Load(), len(items))
			}
		}
	}()
	for _, item := range items {
		work <- item
	}
	close(work)
	wg.Wait()
	close(stop)
	report.Complete = failed.Load() == nil
	if err := report.write(reportPath); err != nil {
		t.Error(err)
	}
	keys := make([]string, 0, len(report.Sources))
	for k := range report.Sources {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		c := report.Sources[k]
		t.Logf("%-12s files=%d records=%d identical=%d mismatched=%d rejected_by_parser=%d", k, c.Files, c.Records, c.Identical, c.Mismatched, c.Rejected)
		if c.Mismatched != 0 {
			t.Errorf("%s: %d mismatched records; first: %v", k, c.Mismatched, c.Examples)
		}
	}
	if msg := failed.Load(); msg != nil {
		t.Fatalf("corpus run failed: %v", msg)
	}
}

func gpCorpusCompareItem(item gpCorpusItem, builder gpArchiveRecordBuilder, identity map[uint32]gpSATCATEntry, satcatFor func(string) map[uint32]gpSATCATEntry, zipReaders map[string]*zip.ReadCloser, report *gpCorpusReport) error {
	var records []gpArchiveRecord
	var rejected int64
	entity := func(r *gpArchiveRecord) {
		if s, ok := identity[r.NORAD]; ok && s.EntityID != "" {
			r.EntityID = s.EntityID
		} else {
			r.EntityID = fmt.Sprintf("NORAD:%d", r.NORAD)
		}
	}
	name := item.path
	switch item.kind {
	case "zip":
		zr := zipReaders[item.path]
		if zr == nil {
			var err error
			if zr, err = zip.OpenReader(item.path); err != nil {
				return err
			}
			zipReaders[item.path] = zr
		}
		f := zr.File[item.entry]
		name = f.Name
		rc, err := f.Open()
		if err != nil {
			return err
		}
		r := csv.NewReader(bufio.NewReaderSize(rc, 256*1024))
		header, err := r.Read()
		if err != nil {
			rc.Close()
			return err
		}
		columns := map[string]int{}
		for i, n := range header {
			columns[strings.TrimSpace(n)] = i
		}
		for {
			row, err := r.Read()
			if err == io.EOF {
				break
			}
			if err != nil {
				rejected++
				continue
			}
			rec, reason := gpArchiveRecordFromCSV(row, columns)
			if reason != "" {
				rejected++
				continue
			}
			entity(&rec)
			records = append(records, rec)
		}
		rc.Close()
	case "window", "gp":
		raw, _, err := readGzipAndHash(item.path)
		if err != nil {
			return err
		}
		var rows []gpJSONRecord
		if err = json.Unmarshal(raw, &rows); err != nil {
			return err
		}
		for _, row := range rows {
			rec, reason := gpArchiveRecordFromJSON(row)
			if reason != "" {
				rejected++
				continue
			}
			entity(&rec)
			records = append(records, rec)
		}
	case "satcat":
		raw, _, err := readGzipAndHash(item.path)
		if err != nil {
			return err
		}
		var rows []gpSATCATRecord
		if err = json.Unmarshal(raw, &rows); err != nil {
			return err
		}
		mapping := satcatFor(item.path)
		cats := make([]gpArchiveCATInput, 0, len(rows))
		for _, row := range rows {
			n, err := parseUint32(string(row.NORAD))
			if err != nil {
				rejected++
				continue
			}
			id := mapping[n].EntityID
			if id == "" {
				id = fmt.Sprintf("NORAD:%d", n)
			}
			nm := strings.TrimSpace(string(row.ObjectName))
			if nm == "" {
				nm = strings.TrimSpace(string(row.SATNAME))
			}
			cats = append(cats, gpArchiveCATInput{EntityID: id, NORAD: n, Name: nm})
		}
		c, err := gpCorpusCompareCAT(builder, cats, name)
		if err != nil {
			return err
		}
		c.Files, c.Rejected = 1, rejected
		report.add("satcat:CAT", c)
		return nil
	}
	built, err := builder.BuildMPE(context.Background(), records)
	if err != nil {
		return err
	}
	c := gpCorpusCounts{Files: 1, Records: int64(len(records)), Rejected: rejected}
	seen := map[gpArchiveCATInput]bool{}
	var cats []gpArchiveCATInput
	for i := range records {
		r := &records[i]
		if bytes.Equal(built[i], legacyArchiveV1MPE(r.EntityID, *r)) {
			c.Identical++
		} else {
			c.Mismatched++
			if len(c.Examples) < 3 {
				c.Examples = append(c.Examples, fmt.Sprintf("%s row %d %+v", name, i, *r))
			}
		}
		// The catalog tier names an object from its GP rows when SATCAT has
		// no name, so every distinct GP name is built as a CAT too.
		k := gpArchiveCATInput{EntityID: r.EntityID, NORAD: r.NORAD, Name: r.ObjectName}
		if !seen[k] {
			seen[k] = true
			cats = append(cats, k)
		}
	}
	report.add(item.kind+":MPE", c)
	cc, err := gpCorpusCompareCAT(builder, cats, name)
	if err != nil {
		return err
	}
	report.add("gp-names:CAT", cc)
	return nil
}

func gpCorpusCompareCAT(builder gpArchiveRecordBuilder, rows []gpArchiveCATInput, name string) (gpCorpusCounts, error) {
	built, err := builder.BuildCAT(context.Background(), rows)
	if err != nil {
		return gpCorpusCounts{}, err
	}
	c := gpCorpusCounts{Records: int64(len(rows))}
	for i, row := range rows {
		if bytes.Equal(built[i], legacyArchiveV1CAT(row.EntityID, row.NORAD, row.Name)) {
			c.Identical++
		} else {
			c.Mismatched++
			if len(c.Examples) < 3 {
				c.Examples = append(c.Examples, fmt.Sprintf("%s row %d %+v", name, i, row))
			}
		}
	}
	return c, nil
}

func catFromBytes(b []byte) gpArchiveCATInput {
	c := CATFB.GetSizePrefixedRootAsCAT(b, 0)
	return gpArchiveCATInput{EntityID: string(c.OBJECT_ID()), NORAD: c.NORAD_CAT_ID(), Name: string(c.OBJECT_NAME())}
}

// Archive encoding v1 reference for the parity tests: the importer's original
// Go builders.
func legacyArchiveV1MPE(entity string, r gpArchiveRecord) []byte { return buildGPArchiveMPE(entity, r) }
func legacyArchiveV1CAT(entity string, norad uint32, name string) []byte {
	return buildGPArchiveCAT(entity, norad, name)
}

// gpArchiveV1RecordBuilder builds with the frozen archive v1 reference, so an
// import can be run twice, once per builder, and compared.
type gpArchiveV1RecordBuilder struct{}

func (gpArchiveV1RecordBuilder) BuildMPE(_ context.Context, records []gpArchiveRecord) ([][]byte, error) {
	out := make([][]byte, len(records))
	for i, r := range records {
		out[i] = legacyArchiveV1MPE(r.EntityID, r)
	}
	return out, nil
}

func (gpArchiveV1RecordBuilder) BuildCAT(_ context.Context, rows []gpArchiveCATInput) ([][]byte, error) {
	out := make([][]byte, len(rows))
	for i, r := range rows {
		out[i] = legacyArchiveV1CAT(r.EntityID, r.NORAD, r.Name)
	}
	return out, nil
}

func (gpArchiveV1RecordBuilder) Close() error { return nil }

// The whole import, run once with the module and once with the archive v1
// reference, must write the same records in the same order and leave the same
// latest state. Module batches of two rows put batch boundaries everywhere.
func TestGPArchiveImportThroughModuleMatchesArchiveV1Import(t *testing.T) {
	previous := gpArchiveRecordBuildBatch
	gpArchiveRecordBuildBatch = 2
	t.Cleanup(func() { gpArchiveRecordBuildBatch = previous })

	root := t.TempDir()
	zipPath := filepath.Join(root, "Archives2.zip")
	row := func(name, id, epoch, mm, ecc, norad, bstar string) string {
		return fmt.Sprintf("%s,%s,%s,%s,%s,51.6,247.4,130.5,325.0,0,U,%s,999,1,%s,0,0,SPACE-TRACK\n", name, id, epoch, mm, ecc, norad, bstar)
	}
	writeGPArchiveTestZipEntries(t, zipPath, []string{
		gpArchiveTestHeader +
			row("VANGUARD 1", "1958-002B", "1959-05-23T00:19:59.047968", "10.8", "0.19", "5", "0") +
			row("VANGUARD 1", "1958-002B", "1959-05-23T00:19:59.047968", "10.8", "0.19", "5", "0") +
			row("VANGUARD 1", "1958-002B", "1960-01-01T00:00:00", "10.8", "0", "5", "-0.0") +
			row("", "", "1961-03-04T05:06:07.5", "14.1", "0.001", "7", "-0.000012345"),
		gpArchiveTestHeader +
			row("TEST 6", "1958-002C", "2001-02-03T04:05:06.123456", "1.00271749", "0.000263", "6", "0.00001") +
			row("TEST 99", "2000-001A", "2020-12-31T23:59:59.999999", "15.5", "0.0006703", "99", "1e-5"),
	})
	windowDir := filepath.Join(root, "spacetrack", "gp_history", "by-creation", "2026")
	gpDir := filepath.Join(root, "spacetrack", "gp", "2026")
	for _, dir := range []string{windowDir, gpDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	r5 := gpArchiveTestJSONRecord("501", "1958-002B", "5")
	r6 := gpArchiveTestJSONRecord("601", "1958-002C", "6")
	r7 := strings.Replace(gpArchiveTestJSONRecord("701", "", "7"), `"BSTAR":"0"`, `"BSTAR":"-0.00000000000000"`, 1)
	r99 := gpArchiveTestJSONRecord("9901", "2000-001A", "99")
	r6New := strings.Replace(gpArchiveTestJSONRecord("602", "1958-002C", "6"), "1.00271749", "1.00281749", 1)
	w1Hash := writeGPArchiveTestGzip(t, filepath.Join(windowDir, "window-1.json.gz"), []byte("["+r5+","+r6+","+r7+"]"))
	w2Hash := writeGPArchiveTestGzip(t, filepath.Join(windowDir, "window-2.json.gz"), []byte("["+r6+","+r99+"]"))
	gpHash := writeGPArchiveTestGzip(t, filepath.Join(gpDir, "2026-09-26.json.gz"), []byte("["+r5+","+r6New+"]"))
	ledgerPath := filepath.Join(root, "state", "spacetrack-ledger.json")
	writeGPArchiveTestLedger(t, ledgerPath, []gpArchiveLedgerWindow{
		{File: "spacetrack/gp_history/by-creation/2026/window-1.json.gz", SHA256: w1Hash, RetrievedAt: "2026-09-26T15:00:00Z"},
		{File: "spacetrack/gp_history/by-creation/2026/window-2.json.gz", SHA256: w2Hash, RetrievedAt: "2026-09-26T15:15:00Z"},
	})
	setGPArchiveTestLedgerSnapshots(t, ledgerPath, []gpArchiveLedgerSATCAT{{File: "spacetrack/gp/2026/2026-09-26.json.gz", SHA256: gpHash, RetrievedAt: "2026-09-26T15:22:00Z"}})
	base := gpArchiveTestOptions(t, root, zipPath, ledgerPath)

	run := func(name string, builder gpArchiveRecordBuilder) (*gpArchiveMemorySink, *gpArchiveCheckpoint) {
		opts := gpArchiveOptionsForTestRun(base, filepath.Join(root, name))
		opts.recordBuilder = builder
		sink := &gpArchiveMemorySink{}
		if _, err := importGPArchive(context.Background(), opts, sink); err != nil {
			t.Fatal(err)
		}
		cp, err := loadGPArchiveCheckpoint(opts.CheckpointPath)
		if err != nil {
			t.Fatal(err)
		}
		return sink, cp
	}
	moduleSink, moduleCP := run("module", testGPArchiveRecordBuilder(t))
	v1Sink, v1CP := run("archive-v1", gpArchiveV1RecordBuilder{})
	if len(moduleSink.mpe) < 8 || len(moduleSink.catalogMPE) < 4 || len(moduleSink.cat) < 4 {
		t.Fatalf("fixture too small: history=%d catalog=%d cat=%d", len(moduleSink.mpe), len(moduleSink.catalogMPE), len(moduleSink.cat))
	}
	for name, pair := range map[string][2][][]byte{
		"history MPE": {moduleSink.mpe, v1Sink.mpe},
		"catalog MPE": {moduleSink.catalogMPE, v1Sink.catalogMPE},
		"catalog CAT": {moduleSink.cat, v1Sink.cat},
	} {
		if !reflect.DeepEqual(pair[0], pair[1]) {
			t.Errorf("%s differs: module %d records, archive v1 %d", name, len(pair[0]), len(pair[1]))
		}
	}
	if len(moduleCP.Latest) != len(v1CP.Latest) {
		t.Fatalf("latest entities: module %d, archive v1 %d", len(moduleCP.Latest), len(v1CP.Latest))
	}
	for id, st := range v1CP.Latest {
		got := moduleCP.Latest[id]
		if got == nil || got.HistoryCID != st.HistoryCID || got.CatalogCID != st.CatalogCID || !bytes.Equal(got.MPE, st.MPE) {
			t.Errorf("latest %s: module %+v, archive v1 %+v", id, got, st)
		}
	}
}
