package storage

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	flatbuffers "github.com/google/flatbuffers/go"

	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/LDM"

	"github.com/spacedatanetwork/sdn-server/internal/flatsqlrt"
	"github.com/spacedatanetwork/sdn-server/internal/storage/enginecatalog"
)

// ==========================================================================
// THE >=1-COLUMN FALLBACK IS A PUBLIC SURFACE
// ==========================================================================
//
// 22 embedded standards declare a table, a struct, a vector or a union as
// their root table's FIRST field, so the leading-run projection has nothing
// representable to emit and the >=1 COLUMN INVARIANT (a zero-column vtab
// emits malformed DDL and traps the no-exceptions engine) forces a column
// anyway.
//
// That column reads slot 0 whatever slot 0 holds. Declaring it `string` made
// the vtab hand the bytes it landed on — the interior of a sub-table, i.e.
// doubles and offsets — to sqlite3_result_text, and the sandbox JSON writer
// escapes only `"`, `\` and bytes < 0x20. The result was a PUBLIC
// /api/v1/query response body that was not valid UTF-8: json.Valid tolerates
// it, every strict JSON reader (RFC 8259 §8.1, JSON.parse over the raw
// bytes) does not.
//
// The fallback is therefore a FIXED-WIDTH one-byte read (`ubyte`). Three
// properties, measured on the pinned engine, not assumed:
//
//   - it is JSON-safe: an integer cell can never carry raw record bytes into
//     the response body;
//   - it is fixed width: unlike a string or a blob, no length read from
//     inside the record decides how many bytes the cell carries, so the
//     column cannot amplify a junk read into a per-row payload;
//   - it is obviously not the field: `SELECT SITE FROM LDM` returning a small
//     integer is a loud "this is not the site", where base64 or text is a
//     quiet lie.
//
// The record still travels WHOLE in `_data`; consumers of these standards
// decode that.

// unprojectableRecord builds one record whose slot 0 holds a REAL nested
// object of the kind flatc says the standard declares there — the shape a
// genuine $LDM or $CSM has, and the shape that put non-UTF-8 bytes in front
// of the junk column.
func unprojectableRecord(t *testing.T, root *flatcRoot) []byte {
	t.Helper()
	if len(root.Fields) == 0 {
		t.Fatalf("%s: flatc reports no root fields", root.Code)
	}
	if root.Fields[0].Kind.representable() {
		t.Fatalf("%s: slot 0 (%s) is representable — this standard does not use the fallback",
			root.Code, root.Fields[0].Name)
	}
	if root.Fields[0].Kind == flatcStruct {
		t.Fatalf("%s: slot 0 is an inline struct, which this builder cannot write", root.Code)
	}
	_, buf := flatcSyntheticRecord(root, 0)
	return buf
}

// TestUnprojectableFirstFieldColumnsAreJSONSafe stores a real record for
// EVERY standard that falls back and reads every column of its table back
// through the PUBLIC sandboxed JSON surface.
func TestUnprojectableFirstFieldColumnsAreJSONSafe(t *testing.T) {
	catalog, err := enginecatalog.Build(embeddedSchemaDir, enginecatalog.PinnedSchemas)
	if err != nil {
		t.Fatalf("build catalog: %v", err)
	}
	roots := loadFlatcOracle(t, catalog.Bindings)

	store := newEngineRecordsStore(t, filepath.Join(t.TempDir(), "store"))
	defer store.Close()

	caps := flatsqlrt.SandboxCaps{MaxRows: 8, MaxBytes: 1 << 20, Timeout: 30 * time.Second}
	tags := SourceTags{ProviderID: "junk", SourceName: "fallback-fixture", BatchID: "junk@1"}

	checked := 0
	for _, binding := range catalog.Bindings {
		if !binding.Columns[0].Junk {
			continue
		}
		root, ok := roots[binding.Schema]
		if !ok {
			continue
		}
		record := unprojectableRecord(t, root)
		if _, err := store.StoreWithSourceTags(binding.Schema, record, "peer-junk", nil, tags); err != nil {
			t.Fatalf("%s: store record: %v", binding.Schema, err)
		}

		quoted := make([]string, 0, len(binding.Columns))
		for _, col := range binding.Columns {
			quoted = append(quoted, quoteEngineRelation(col.Name))
		}
		sql := "SELECT " + strings.Join(quoted, ", ") + " FROM " + quoteEngineRelation(binding.Table)
		payload, rows, _, err := store.QuerySandboxedJSON(sql, caps)
		if err != nil {
			t.Fatalf("%s: %s: %v", binding.Schema, sql, err)
		}
		if rows != 1 {
			t.Fatalf("%s: %d rows in %s, want 1", binding.Schema, rows, binding.Table)
		}
		// THE ASSERTION THAT FAILED BEFORE THE FIX. json.Valid is lenient
		// about the bytes inside a string, so UTF-8 validity is checked
		// directly: a JSON text that is not valid UTF-8 is not JSON.
		if !utf8.Valid(payload) {
			t.Errorf("%s: public JSON for %s is not valid UTF-8: %q", binding.Schema, sql, payload)
		}
		if !json.Valid(payload) {
			t.Errorf("%s: public JSON for %s does not parse: %q", binding.Schema, sql, payload)
		}
		var decoded []map[string]any
		if err := json.Unmarshal(payload, &decoded); err != nil {
			t.Errorf("%s: public JSON for %s: %v", binding.Schema, sql, err)
		}
		for _, row := range decoded {
			for name, cell := range row {
				if s, isText := cell.(string); isText && !utf8.ValidString(s) {
					t.Errorf("%s.%s carries invalid UTF-8 into the public response: %q",
						binding.Table, name, s)
				}
			}
		}

		// THE WHOLE RECORD STILL TRAVELS. The columns are for predicates; the
		// FlatBuffer is the payload, and it must round-trip byte for byte.
		stream, err := store.QuerySandboxedStream(
			`SELECT _data FROM `+quoteEngineRelation(binding.Table)+` ORDER BY _rowid DESC LIMIT ?`, caps, 1)
		if err != nil {
			t.Fatalf("%s: _data stream: %v", binding.Schema, err)
		}
		frames, err := flatsqlrt.DecodeSizePrefixedStream(stream.Bytes)
		if err != nil {
			t.Fatalf("%s: decode _data stream: %v", binding.Schema, err)
		}
		if len(frames) != 1 || !bytes.Equal(frames[0], record) {
			t.Errorf("%s: _data round trip returned %d frames, byte-identical=%v",
				binding.Schema, len(frames), len(frames) == 1 && bytes.Equal(frames[0], record))
		}
		checked++
	}

	if want := len(engineUnprojectableFirstFields) - unvendoredFallbackCount(catalog); checked != want {
		t.Fatalf("checked %d fallback standards, want %d", checked, want)
	}
	t.Logf("checked %d fallback standards end to end through the public JSON surface", checked)
}

func unvendoredFallbackCount(catalog *enginecatalog.Catalog) int {
	n := 0
	for _, binding := range catalog.Bindings {
		if _, unvendored := engineFlatcUnvendoredStandards[binding.Schema]; unvendored && binding.Columns[0].Junk {
			n++
		}
	}
	return n
}

// TestFallbackColumnDeclaredAsTextLeaksRawRecordBytes is the guard's own
// test: it re-declares the fallback the way it USED to be (`string`) and
// proves the defect it caused is real and detected. Without this, the test
// above could pass because the record happens to be tame rather than because
// the column type is safe.
func TestFallbackColumnDeclaredAsTextLeaksRawRecordBytes(t *testing.T) {
	root, err := loadFlatcRoot("LDM")
	if err != nil {
		t.Fatalf("flatc oracle: %v", err)
	}
	record := unprojectableRecord(t, root)

	engine, err := flatsqlrt.New(flatsqlrt.WithPrecompiledAOTCache(engineAOTCacheDir()))
	if err != nil {
		t.Fatalf("start engine: %v", err)
	}
	defer engine.Close()

	caps := flatsqlrt.SandboxCaps{MaxRows: 4, MaxBytes: 1 << 20, Timeout: 30 * time.Second}
	field := root.Fields[0].Name

	for _, tc := range []struct {
		typ      string
		wantUTF8 bool
	}{
		{"string", false}, // the defect
		{"ubyte", true},   // the fix
	} {
		db, err := engine.CreateDatabase(mutatedSchemaText("LDM", []enginecatalog.Column{
			{Name: field, Type: tc.typ},
		}), "fallback-"+tc.typ)
		if err != nil {
			t.Fatalf("create database: %v", err)
		}
		if err := db.RegisterFileID(root.FileID, "LDM"); err != nil {
			t.Fatalf("register: %v", err)
		}
		if err := db.RegisterSource(engineDefaultSource); err != nil {
			t.Fatalf("register source: %v", err)
		}
		if err := db.CreateUnifiedViews(); err != nil {
			t.Fatalf("create views: %v", err)
		}
		if _, err := db.IngestOneWithSource(record, engineDefaultSource); err != nil {
			t.Fatalf("ingest: %v", err)
		}
		payload, _, _, err := db.QuerySandboxedJSON(
			"SELECT "+quoteEngineRelation(field)+" FROM LDM", caps)
		if err != nil {
			t.Fatalf("%s: sandboxed JSON: %v", tc.typ, err)
		}
		if got := utf8.Valid(payload); got != tc.wantUTF8 {
			t.Errorf("fallback column declared %s: public JSON valid UTF-8 = %v, want %v (%q)",
				tc.typ, got, tc.wantUTF8, payload)
		}
		db.Destroy()
	}
}

// TestGeneratedFallbackMatchesARealFlatcBuiltRecord anchors the generic
// oracle-built records above to a record built with the vendored flatc
// BUILDER for a standard that falls back: a genuine $LDM whose SITE is a real
// $SIT sub-table. If the generic construction ever stopped resembling a real
// record, the fallback tests would be measuring a fixture instead of the
// product.
func TestGeneratedFallbackMatchesARealFlatcBuiltRecord(t *testing.T) {
	b := flatbuffers.NewBuilder(1024)
	siteName := b.CreateString("HERMES-TEST-SITE")
	LDM.SITStart(b)
	LDM.SITAddNAME(b, siteName)
	LDM.SITAddLATITUDE(b, 38.8977)
	LDM.SITAddLONGITUDE(b, -77.0365)
	site := LDM.SITEnd(b)
	LDM.LDMStart(b)
	LDM.LDMAddSITE(b, site)
	LDM.LDMAddAZIMUTH(b, 123.5)
	LDM.FinishLDMBuffer(b, LDM.LDMEnd(b))
	record := b.FinishedBytes()

	store := newEngineRecordsStore(t, filepath.Join(t.TempDir(), "store"))
	defer store.Close()

	tags := SourceTags{ProviderID: "junk", SourceName: "fallback-fixture", BatchID: "ldm@1"}
	if _, err := store.StoreWithSourceTags("LDM.fbs", record, "peer-junk", nil, tags); err != nil {
		t.Fatalf("store real $LDM: %v", err)
	}

	caps := flatsqlrt.SandboxCaps{MaxRows: 4, MaxBytes: 1 << 20, Timeout: 30 * time.Second}
	payload, rows, _, err := store.QuerySandboxedJSON(`SELECT SITE FROM LDM`, caps)
	if err != nil {
		t.Fatalf("SELECT SITE FROM LDM: %v", err)
	}
	if rows != 1 {
		t.Fatalf("%d rows, want 1", rows)
	}
	if !utf8.Valid(payload) {
		t.Fatalf("a real $LDM makes the public JSON invalid UTF-8: %q", payload)
	}
	var decoded []map[string]any
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatalf("public JSON: %v (%q)", err, payload)
	}
	if len(decoded) != 1 {
		t.Fatalf("%d JSON rows, want 1", len(decoded))
	}
	// The cell must be a NUMBER: the fallback never carries the sub-table's
	// bytes, and a consumer that asked for SITE gets something it cannot
	// mistake for a site.
	if _, isNumber := decoded[0]["SITE"].(float64); !isNumber {
		t.Fatalf("LDM.SITE came back as %T (%v), want the fixed-width fallback number",
			decoded[0]["SITE"], decoded[0]["SITE"])
	}

	// The real record is what a consumer reads, and it must survive whole.
	stream, err := store.QuerySandboxedStream(`SELECT _data FROM LDM ORDER BY _rowid DESC LIMIT ?`, caps, 1)
	if err != nil {
		t.Fatalf("_data stream: %v", err)
	}
	frames, err := flatsqlrt.DecodeSizePrefixedStream(stream.Bytes)
	if err != nil {
		t.Fatalf("decode _data: %v", err)
	}
	if len(frames) != 1 || !bytes.Equal(frames[0], record) {
		t.Fatalf("_data round trip lost the record (%d frames)", len(frames))
	}
	if !LDM.LDMBufferHasIdentifier(frames[0]) {
		t.Fatal("_data frame is not a $LDM buffer")
	}
	decodedLDM := LDM.GetRootAsLDM(frames[0], 0)
	var sit LDM.SIT
	if decodedLDM.SITE(&sit) == nil || string(sit.NAME()) != "HERMES-TEST-SITE" {
		t.Fatal("_data frame did not decode back to the site it was built with")
	}
}
