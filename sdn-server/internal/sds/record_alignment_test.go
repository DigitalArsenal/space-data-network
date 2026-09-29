package sds

// Stored-record alignment at the read boundary (owner decision 2026-09-29).
// A record whose size prefix was stripped at publish fails the stock
// FlatBuffers verifier at its own first byte and verifies behind its u32
// length; VerifyRecord places every bare stored record at its alignment
// origin (recordalign) without changing its bytes.

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/binary"
	"fmt"
	"os"
	"runtime"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ipfs/go-cid"
	mh "github.com/multiformats/go-multihash"
	_ "modernc.org/sqlite"

	"github.com/spacedatanetwork/sdn-server/internal/storage/recordalign"
	"github.com/spacedatanetwork/sdn-server/internal/wasm"
)

// alignmentFixtures returns records of the three standards the defect hits,
// each built size-prefixed and stripped the way the publish boundary stores
// it, next to the same record finished bare.
func alignmentFixtures(t *testing.T, v *Validator) map[string][2][]byte {
	t.Helper()
	ctx := context.Background()
	jsonRecords := map[string]string{
		"OMM.fbs": `{"OBJECT_NAME":"ISS (ZARYA)","OBJECT_ID":"1998-067A","EPOCH":"2026-09-27T12:00:00.000000","MEAN_MOTION":15.50103472,"ECCENTRICITY":0.0006703,"NORAD_CAT_ID":25544,"BSTAR":0.0001027}`,
		"MPE.fbs": `{"ENTITY_ID":"25544","EPOCH":1790000000.25,"MEAN_MOTION":15.50103472,"BSTAR":0.0001027,"MEAN_ELEMENT_THEORY":"SGP4"}`,
		"CAT.fbs": `{"OBJECT_NAME":"ISS (ZARYA)","OBJECT_ID":"1998-067A","NORAD_CAT_ID":25544,"PERIOD":92.9,"INCLINATION":51.64}`,
	}
	out := make(map[string][2][]byte, len(jsonRecords))
	for schema, record := range jsonRecords {
		prefixed, err := v.JSONToFlatBuffer(ctx, schema, []byte(record), StoredRecordOptions|wasm.FlatcSizePrefixed)
		if err != nil {
			t.Fatalf("%s size-prefixed build: %v", schema, err)
		}
		bare, err := v.JSONToFlatBuffer(ctx, schema, []byte(record), StoredRecordOptions)
		if err != nil {
			t.Fatalf("%s bare build: %v", schema, err)
		}
		out[schema] = [2][]byte{prefixed[4:], bare}
	}
	// The in-repo Go builders finish size-prefixed too; their stored form is
	// what every ingest lane publishes.
	out["OMM.fbs (Go builder)"] = [2][]byte{NewOMMBuilder().WithNoradCatID(25544).WithMeanMotion(15.5).WithEpochTimestamp(1790000000).Build()[4:], nil}
	out["CAT.fbs (Go builder)"] = [2][]byte{NewCATBuilder().WithNoradCatID(25544).WithOrbitalParams(92.9, 51.64, 420, 415).Build()[4:], nil}
	return out
}

func TestVerifyRecordPlacesStrippedRecordsAtTheirOrigin(t *testing.T) {
	v := newConverterValidator(t)
	ctx := context.Background()
	v.SetFieldLevelValidation(true)

	for name, pair := range alignmentFixtures(t, v) {
		schema := strings.Fields(name)[0]
		id, err := v.converterID(ctx, schema)
		if err != nil {
			t.Fatal(err)
		}
		stripped, bare := pair[0], pair[1]
		kept := bytes.Clone(stripped)

		// The defect: 8-byte fields at 4 mod 8 from the record's first byte.
		if got := len(stripped) % 8; got != 4 {
			t.Fatalf("%s: stripped record length %d is %d mod 8, want 4", name, len(stripped), got)
		}
		err = v.flatc.VerifyBinary(ctx, id, stripped, 0)
		if status, _ := wasm.FlatcStatusOf(err); status != wasm.FlatcInvalidBinary {
			t.Fatalf("%s: the stock verifier at the record's first byte: err = %v, want INVALID_BINARY (the alignment defect)", name, err)
		}
		framed := binary.LittleEndian.AppendUint32(nil, uint32(len(stripped)))
		if err := v.flatc.VerifyBinary(ctx, id, append(framed, stripped...), wasm.FlatcSizePrefixed); err != nil {
			t.Fatalf("%s: behind its u32 length: %v", name, err)
		}

		// The fix, on every in-process verification path.
		if err := v.VerifyRecord(ctx, schema, stripped); err != nil {
			t.Fatalf("%s: VerifyRecord: %v", name, err)
		}
		if err := v.Validate(ctx, schema, stripped); err != nil {
			t.Fatalf("%s: field-level Validate: %v", name, err)
		}
		if _, err := v.FlatBufferToJSON(ctx, schema, stripped, wasm.FlatcCompactJSON); err != nil {
			t.Fatalf("%s: FlatBufferToJSON: %v", name, err)
		}
		var ndjson bytes.Buffer
		if n, errs := wasm.NewStreamConverter(v.flatc, id, 0).FlatBuffersToJSONStream(ctx, [][]byte{stripped}, &ndjson); n != 1 || len(errs) != 0 {
			t.Fatalf("%s: FlatBuffersToJSONStream wrote %d, errs %v", name, n, errs)
		}
		if !bytes.Equal(stripped, kept) {
			t.Fatalf("%s: verification changed the stored bytes", name)
		}

		// A bare build is at its origin already: verified in place.
		if bare != nil {
			if len(bare)%8 != 0 {
				t.Fatalf("%s: bare build length %d is not 0 mod 8", name, len(bare))
			}
			if err := v.flatc.VerifyBinary(ctx, id, bare, 0); err != nil {
				t.Fatalf("%s: bare build at its first byte: %v", name, err)
			}
			if view := recordalign.Record(bare); view.Copied || &view.Buf[0] != &bare[0] {
				t.Fatalf("%s: a bare build was copied", name)
			}
			if err := v.VerifyRecord(ctx, schema, bare); err != nil {
				t.Fatalf("%s: VerifyRecord(bare build): %v", name, err)
			}
		}
	}
}

// TestStoredRecordAlignmentOnStore is the proof on real records. It reads
// every record of every producer table of a COPY of a node's control
// database (SDN_RECORDALIGN_CONTROL_DB; an APFS clone, never the original)
// and verifies each bare one three ways with the stock verifier (flatc's
// reflection verifier): at its own first byte, behind a fresh u32 length, and
// through VerifyRecord. It asserts the rule (origin = -len mod 8; a record
// stored with its own size prefix is its own origin), that VerifyRecord
// accepts every record and that every stored CID is the CID of the stored
// bytes, and reports the copies it costs. SDN_RECORDALIGN_WORKERS sets the
// converter instances (default 8).
func TestStoredRecordAlignmentOnStore(t *testing.T) {
	path := os.Getenv("SDN_RECORDALIGN_CONTROL_DB")
	if path == "" {
		t.Skip("SDN_RECORDALIGN_CONTROL_DB not set (names a COPY of a control.flatsqldb)")
	}
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	rows, err := db.Query(`SELECT name FROM sqlite_master WHERE type = 'table' AND name LIKE 'sds_p_%' ORDER BY name`)
	if err != nil {
		t.Fatal(err)
	}
	var tables []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		tables = append(tables, name)
	}
	rows.Close()

	workers := 8
	if raw := os.Getenv("SDN_RECORDALIGN_WORKERS"); raw != "" {
		fmt.Sscan(raw, &workers)
	}
	type job struct {
		schema, cid string
		data        []byte
	}
	type tally struct {
		records, envelopeFail, noConverter, cidMismatch int
		storedPrefixed                                  int
		origin4, bareFail, bareOKAtOrigin4              int
		ruleViolations, viewFail, copies                int
		copiedBytes, bytes                              int64
		violations                                      []string
	}
	var mu sync.Mutex
	stats := map[string]*tally{}
	get := func(schema string) *tally {
		if stats[schema] == nil {
			stats[schema] = &tally{}
		}
		return stats[schema]
	}
	jobs := make(chan job, 4096)
	var wg sync.WaitGroup
	ctx := context.Background()
	for w := 0; w < workers; w++ {
		v := newConverterValidator(t)
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range jobs {
				r := struct {
					env, noconv, cid, prefixed, o4, bareFail, bareOK4, rule, viewFail, copied bool
					note                                                                      string
				}{}
				r.cid = recordCID(j.data) != j.cid
				if err := v.VerifyEnvelope(j.schema, j.data); err != nil {
					r.env = true
				} else if id, err := v.converterID(ctx, j.schema); err != nil {
					r.noconv = true
				} else {
					if form, _ := v.DetectEnvelopeForm(j.schema, j.data); form == EnvelopeSizePrefixed {
						// Stored with its builder's own size prefix (internal
						// writers, not the publish boundary): it is its own
						// origin and verifies size-prefixed as stored.
						r.prefixed = true
						if err := v.flatc.VerifyBinary(ctx, id, j.data, wasm.FlatcSizePrefixed); err != nil {
							r.rule = true
							r.note = fmt.Sprintf("%s len %d stored size-prefixed: %v", j.cid, len(j.data), err)
						}
					} else {
						origin := recordalign.OriginOffset(len(j.data))
						r.o4 = origin == 4
						bareErr := v.flatc.VerifyBinary(ctx, id, j.data, 0)
						framed := binary.LittleEndian.AppendUint32(make([]byte, 0, 4+len(j.data)), uint32(len(j.data)))
						framedErr := v.flatc.VerifyBinary(ctx, id, append(framed, j.data...), wasm.FlatcSizePrefixed)
						r.bareFail = bareErr != nil
						r.bareOK4 = r.o4 && bareErr == nil
						// The rule: a record verifies at its origin.
						if (origin == 0 && bareErr != nil) || (origin == 4 && framedErr != nil) {
							r.rule = true
							r.note = fmt.Sprintf("%s len %d origin %d: bare %v, framed %v", j.cid, len(j.data), origin, bareErr, framedErr)
						}
					}
					if err := v.VerifyRecord(ctx, j.schema, j.data); err != nil {
						r.viewFail = true
						if r.note == "" {
							r.note = fmt.Sprintf("%s len %d: VerifyRecord %v", j.cid, len(j.data), err)
						}
					}
					buf, _ := v.placeForVerifier(j.schema, j.data, 0)
					r.copied = &buf[0] != &j.data[0]
				}
				mu.Lock()
				s := get(j.schema)
				s.records++
				s.bytes += int64(len(j.data))
				if r.env {
					s.envelopeFail++
				}
				if r.noconv {
					s.noConverter++
				}
				if r.cid {
					s.cidMismatch++
				}
				if r.prefixed {
					s.storedPrefixed++
				}
				if r.o4 {
					s.origin4++
				}
				if r.bareFail {
					s.bareFail++
				}
				if r.bareOK4 {
					s.bareOKAtOrigin4++
				}
				if r.rule {
					s.ruleViolations++
				}
				if r.viewFail {
					s.viewFail++
				}
				if r.copied {
					s.copies++
					s.copiedBytes += int64(len(j.data) + recordalign.PrefixLen)
				}
				if r.note != "" && len(s.violations) < 3 {
					s.violations = append(s.violations, r.note)
				}
				mu.Unlock()
			}
		}()
	}
	started := time.Now()
	for _, table := range tables {
		schema := table[strings.LastIndex(table, "__")+2:] + ".fbs"
		rows, err := db.Query(fmt.Sprintf(`SELECT cid, data FROM %q`, table))
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			var j job
			if err := rows.Scan(&j.cid, &j.data); err != nil {
				t.Fatal(err)
			}
			j.schema = schema
			jobs <- j
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		rows.Close()
	}
	close(jobs)
	wg.Wait()
	elapsed := time.Since(started)

	names := make([]string, 0, len(stats))
	for name := range stats {
		names = append(names, name)
	}
	sort.Strings(names)
	t.Logf("%s: %d producer tables, %d workers, %s, %s/%s", path, len(tables), workers, elapsed.Round(time.Second), runtime.GOOS, runtime.GOARCH)
	t.Logf("%-8s %8s %8s %9s %8s %8s %8s %9s %8s %8s %8s %11s", "STD", "records", "env-fail", "stored-sp", "len%8=4", "bare-bad", "4-bareok", "rule-viol", "view-bad", "cid-bad", "copies", "copied B")
	var total tally
	for _, name := range names {
		s := stats[name]
		t.Logf("%-8s %8d %8d %9d %8d %8d %8d %9d %8d %8d %8d %11d", strings.TrimSuffix(name, ".fbs"), s.records, s.envelopeFail, s.storedPrefixed, s.origin4, s.bareFail, s.bareOKAtOrigin4, s.ruleViolations, s.viewFail, s.cidMismatch, s.copies, s.copiedBytes)
		for _, note := range s.violations {
			t.Logf("         %s", note)
		}
		if s.noConverter > 0 {
			t.Logf("         %d record(s) skipped: the converter cannot load %s", s.noConverter, name)
		}
		total.records += s.records
		total.envelopeFail += s.envelopeFail
		total.storedPrefixed += s.storedPrefixed
		total.origin4 += s.origin4
		total.bareFail += s.bareFail
		total.bareOKAtOrigin4 += s.bareOKAtOrigin4
		total.ruleViolations += s.ruleViolations
		total.viewFail += s.viewFail
		total.cidMismatch += s.cidMismatch
		total.copies += s.copies
		total.copiedBytes += s.copiedBytes
		total.bytes += s.bytes
	}
	t.Logf("%-8s %8d %8d %9d %8d %8d %8d %9d %8d %8d %8d %11d", "ALL", total.records, total.envelopeFail, total.storedPrefixed, total.origin4, total.bareFail, total.bareOKAtOrigin4, total.ruleViolations, total.viewFail, total.cidMismatch, total.copies, total.copiedBytes)
	t.Logf("record bytes %d; copies are %.1f%% of records, copied bytes %.1f%% of record bytes", total.bytes,
		100*float64(total.copies)/float64(max(total.records, 1)), 100*float64(total.copiedBytes)/float64(max(total.bytes, 1)))
	if total.ruleViolations != 0 || total.viewFail != 0 || total.cidMismatch != 0 {
		t.Fatalf("%d rule violation(s), %d record(s) VerifyRecord rejects, %d CID mismatch(es)", total.ruleViolations, total.viewFail, total.cidMismatch)
	}
}

// recordCID is the store's record CID (storage.ComputeCID: CIDv1, raw,
// sha2-256 of the stored bytes).
func recordCID(data []byte) string {
	hash, err := mh.Sum(data, mh.SHA2_256, -1)
	if err != nil {
		return ""
	}
	return cid.NewCidV1(cid.Raw, hash).String()
}
