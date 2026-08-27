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

// ==========================================================================
// EVERY PROJECTED string COLUMN IS A PUBLIC SURFACE, NOT JUST THE FALLBACK
// ==========================================================================
//
// The fallback column above was ONE way raw record bytes reached the public
// /api/v1/query body. It was never the only one, and fixing it fixed only it.
//
// The general case: the engine's vtab hands ANY string cell to
// sqlite3_result_text and the in-wasm JSON writer passes every byte >= 0x20
// through verbatim, so a `string` column carries the record's bytes into the
// response exactly as they were stored. FlatBuffers does not require a string
// to be valid UTF-8 and neither does the store's write path (it checks the
// file identifier, not the encoding), so a peer-supplied record can put
// arbitrary bytes in front of hundreds of projected string columns.
//
// THIS CLASS IS INHERITED, NOT INTRODUCED HERE: the pinned $OMM table has
// declared CREATION_DATE / OBJECT_NAME as `string` since loop B.3, and the
// subtest below measures it on $OMM too. What routing every standard changed
// is the BLAST RADIUS — from two tables to every embedded standard, most of
// them peer-writable — which is why the guard belongs in this branch:
// storage.QuerySandboxedJSON now makes the assembled body valid UTF-8 at the
// boundary (see its doc for why that is structure-preserving).

// hostileUTF8 is a byte run that is not valid UTF-8 anywhere: a lone 0xFF,
// a truncated surrogate-range lead, a bare continuation byte and a two-byte
// sequence whose continuation is missing.
var hostileUTF8 = []byte{0xff, 0xfe, 0x80, 'A', 0xc3, '('}

// hostileStringRecord builds a real record of root's standard whose first
// string field carries hostileUTF8 — the shape a hostile or merely
// mis-encoded peer can store through the ordinary write path.
func hostileStringRecord(t *testing.T, root *flatcRoot, slot int) []byte {
	t.Helper()
	b := flatbuffers.NewBuilder(256)
	off := b.CreateString(string(hostileUTF8))
	b.StartObject(root.Slots)
	b.PrependUOffsetTSlot(slot, off, 0)
	b.FinishWithFileIdentifier(b.EndObject(), []byte(root.FileID))
	return b.FinishedBytes()
}

// firstStringColumn returns the projected column index that reads a genuine
// `string` slot, per BOTH the catalog and the flatc oracle.
func firstStringColumn(binding enginecatalog.Binding, root *flatcRoot) (int, bool) {
	for i, col := range binding.Columns {
		if col.Junk || col.Type != "string" {
			continue
		}
		if i < len(root.Fields) && root.Fields[i].Kind == flatcString && root.Fields[i].Name == col.Name {
			return i, true
		}
	}
	return 0, false
}

// TestProjectedStringColumnsAreJSONSafe stores a record whose string field is
// not valid UTF-8 through the PRODUCTION write path for a spread of
// generically routed standards and reads it back through the public sandboxed
// JSON surface. The response body must be a valid JSON text.
func TestProjectedStringColumnsAreJSONSafe(t *testing.T) {
	catalog, err := enginecatalog.Build(embeddedSchemaDir, enginecatalog.PinnedSchemas)
	if err != nil {
		t.Fatalf("build catalog: %v", err)
	}
	roots := loadFlatcOracle(t, catalog.Bindings)

	type target struct {
		schema string
		table  string
		column string
		record []byte
	}
	var targets []target
	for _, binding := range catalog.Bindings {
		root, ok := roots[binding.Schema]
		if !ok {
			continue
		}
		idx, ok := firstStringColumn(binding, root)
		if !ok {
			continue
		}
		targets = append(targets, target{
			schema: binding.Schema,
			table:  binding.Table,
			column: binding.Columns[idx].Name,
			record: hostileStringRecord(t, root, idx),
		})
	}
	// EVERY routed standard that projects a string, not a sample of them: a
	// sample is exactly how a guard ends up proving something about the first
	// eight entries of a catalog and nothing about the surface it names.
	//
	// THE COUNT IS EXACT, NOT A FLOOR. It is derivable and it is the whole
	// claim this test makes about its own coverage: of the generated tables,
	// the ones declaring at least one `string` column, minus the standards
	// with no flatc oracle in this checkout (loadFlatcOracle skips them).
	// A floor of ">= 100" would let coverage collapse from 179 to 101 in
	// silence. When a catalog change moves it, update the number here — the
	// same discipline TestEngineDerivedRTreesAreTheDisclosedSet applies to
	// its disclosed list.
	//
	// 176 -> 179 on the SDS v1.198.0 pin, and the delta is exactly the three
	// standards that pin embedded: $VCF (3 string columns), $TXS (8) and $STX
	// (12). Each projects at least one string, so each earns hostile-string
	// coverage; none was exempted. The raw catalog count is 183 tables
	// declaring a string column, and the 4 that do not appear here are the
	// standards with no flatc oracle in this checkout (loadFlatcOracle skips
	// them) — the same 4 as before this pin, so the gap did not move.
	const stringProjectingRoutedStandards = 179
	if len(targets) != stringProjectingRoutedStandards {
		t.Fatalf("the hostile-string guard covers %d generically routed standards, want exactly %d — a catalog change moved the projected-string set (or the flatc oracle is incomplete); confirm with `awk '/^  table [A-Z]/{tbl=$2; has=0} /:string;/{if(tbl)has=1} /^  }$/{if(tbl&&has)n++; tbl=\"\"} END{print n}' internal/storage/engine_standard_catalog.go` minus the unvendored standards",
			len(targets), stringProjectingRoutedStandards)
	}
	t.Logf("hostile-string guard covers %d routed standards (plus the pinned $OMM below)", len(targets))

	// THE INHERITED CASE TOO. $OMM's table text is pinned, so it is not in the
	// generated catalog; its CREATION_DATE is a `string` column that predates
	// the routing flip and behaves identically.
	ommRoot, err := loadFlatcRoot("OMM")
	if err != nil {
		t.Fatalf("flatc oracle for OMM: %v", err)
	}
	ommSlot := -1
	for _, f := range ommRoot.Fields {
		if f.Kind == flatcString && f.Name == "CREATION_DATE" {
			ommSlot = f.Slot
			break
		}
	}
	if ommSlot < 0 {
		t.Fatal("flatc says $OMM has no CREATION_DATE string field")
	}
	targets = append(targets, target{
		schema: "OMM.fbs", table: "OMM", column: "CREATION_DATE",
		record: hostileStringRecord(t, ommRoot, ommSlot),
	})

	store := newEngineRecordsStore(t, filepath.Join(t.TempDir(), "store"))
	defer store.Close()

	caps := flatsqlrt.SandboxCaps{MaxRows: 8, MaxBytes: 1 << 20, Timeout: 30 * time.Second}
	tags := SourceTags{ProviderID: "hostile", SourceName: "hostile-peer", BatchID: "hostile@1"}

	sanitizerWasLoadBearing := false
	for _, tc := range targets {
		t.Run(tc.schema, func(t *testing.T) {
			// The write path ACCEPTS it: nothing validates string encoding,
			// which is the premise of the whole test.
			if _, err := store.StoreWithSourceTags(tc.schema, tc.record, "peer-hostile", nil, tags); err != nil {
				t.Fatalf("store hostile %s record: %v", tc.schema, err)
			}
			sql := "SELECT " + quoteEngineRelation(tc.column) + " FROM " + quoteEngineRelation(tc.table)

			// MEASURED, NOT ASSUMED. The engine's own answer still carries the
			// raw bytes — if it ever stops, this test says so instead of
			// quietly passing for the wrong reason.
			raw, _, _, err := store.engineDB.QuerySandboxedJSON(sql, caps)
			if err != nil {
				t.Fatalf("engine %s: %v", sql, err)
			}
			if !utf8.Valid(raw) {
				sanitizerWasLoadBearing = true
			}

			payload, rows, _, err := store.QuerySandboxedJSON(sql, caps)
			if err != nil {
				t.Fatalf("%s: %v", sql, err)
			}
			if rows < 1 {
				t.Fatalf("%s returned %d rows", sql, rows)
			}
			if !utf8.Valid(payload) {
				t.Errorf("public JSON for %s is not valid UTF-8: %q", sql, payload)
			}
			if !json.Valid(payload) {
				t.Errorf("public JSON for %s does not parse: %q", sql, payload)
			}
			var decoded []map[string]any
			if err := json.Unmarshal(payload, &decoded); err != nil {
				t.Fatalf("public JSON for %s: %v (%q)", sql, err, payload)
			}
			for _, row := range decoded {
				if s, isText := row[tc.column].(string); isText && !utf8.ValidString(s) {
					t.Errorf("%s.%s carries invalid UTF-8 into the public response: %q", tc.table, tc.column, s)
				}
			}

			// THE RECORD IS NOT REWRITTEN. Sanitizing is a property of the
			// JSON body only: the FlatBuffer keeps the bytes the peer sent.
			stream, err := store.QuerySandboxedStream(
				`SELECT _data FROM `+quoteEngineRelation(tc.table)+` ORDER BY _rowid DESC LIMIT ?`, caps, 1)
			if err != nil {
				t.Fatalf("%s: _data stream: %v", tc.schema, err)
			}
			frames, err := flatsqlrt.DecodeSizePrefixedStream(stream.Bytes)
			if err != nil {
				t.Fatalf("%s: decode _data: %v", tc.schema, err)
			}
			if len(frames) != 1 || !bytes.Equal(frames[0], tc.record) {
				t.Fatalf("%s: _data round trip did not return the stored record byte for byte", tc.schema)
			}
			if !bytes.Contains(frames[0], hostileUTF8) {
				t.Fatalf("%s: _data lost the original bytes", tc.schema)
			}
		})
	}

	if !sanitizerWasLoadBearing {
		t.Fatal("no engine answer was invalid UTF-8: the fixture stopped exercising the defect, so the guard proved nothing")
	}
}

// ==========================================================================
// SANITIZING MUST NOT TURN A WITHIN-CAP ANSWER INTO A PEER-TRIGGERED DENIAL
// ==========================================================================
//
// U+FFFD is three bytes, so replacing a LONE invalid byte grows the body.
// bytes.ToValidUTF8 collapses each RUN to one U+FFFD, so a record full of
// 0xFF actually SHRINKS; the growth case is invalid bytes ISOLATED between
// valid ones (0xFF,'a',0xFF,'a',...), where 2 bytes become 4 — measured
// below, not assumed.
//
// WHO CONTROLS THAT GROWTH DECIDES WHERE THE CAP IS WORN. The bytes are a
// PEER's: nothing on the write path requires a FlatBuffers string to be valid
// UTF-8. Re-checking SandboxCaps.MaxBytes after sanitizing therefore hands
// that peer a switch — publish a record with isolated invalid bytes and a
// query that FITS the cap starts failing outright, for every caller, on an
// anonymous public endpoint. The cap is a volumetric guard, not a
// content-integrity rule, and the engine still wears it: it REJECTS (never
// truncates) a result over MaxBytes when it assembles the body. So the
// boundary returns the widened body — bounded by construction at 3x, since
// every invalid byte becomes at most three — rather than converting an answer
// the caller is entitled to into a 4xx/5xx a peer chose.
func TestSanitizingNeverTurnsAWithinCapAnswerIntoAFailure(t *testing.T) {
	store := newEngineRecordsStore(t, filepath.Join(t.TempDir(), "store"))
	defer store.Close()

	root, err := loadFlatcRoot("OMM")
	if err != nil {
		t.Fatalf("flatc oracle for OMM: %v", err)
	}
	slot := -1
	for _, f := range root.Fields {
		if f.Kind == flatcString && f.Name == "CREATION_DATE" {
			slot = f.Slot
			break
		}
	}
	if slot < 0 {
		t.Fatal("flatc says $OMM has no CREATION_DATE string field")
	}

	// Distinct records (identical bytes dedupe), each carrying a long run of
	// lone invalid bytes — the cheapest way for a peer to buy amplification.
	tags := SourceTags{ProviderID: "hostile", SourceName: "hostile-peer", BatchID: "amplify@1"}
	for i := 0; i < 8; i++ {
		hostile := append(bytes.Repeat([]byte{0xff, 'a'}, 48), byte('0'+i))
		b := flatbuffers.NewBuilder(256)
		off := b.CreateString(string(hostile))
		b.StartObject(root.Slots)
		b.PrependUOffsetTSlot(slot, off, 0)
		b.FinishWithFileIdentifier(b.EndObject(), []byte(root.FileID))
		if _, err := store.StoreWithSourceTags("OMM.fbs", b.FinishedBytes(), "peer-hostile", nil, tags); err != nil {
			t.Fatalf("store hostile OMM record %d: %v", i, err)
		}
	}

	sql := `SELECT CREATION_DATE FROM "OMM"`
	generous := flatsqlrt.SandboxCaps{MaxRows: 64, MaxBytes: 1 << 20, Timeout: 30 * time.Second}

	// MEASURED: what the engine assembled, and what sanitizing costs.
	raw, _, _, err := store.engineDB.QuerySandboxedJSON(sql, generous)
	if err != nil {
		t.Fatalf("engine %s: %v", sql, err)
	}
	if utf8.Valid(raw) {
		t.Fatal("the engine answer is already valid UTF-8 — the fixture no longer exercises amplification")
	}
	sanitized, _, _, err := store.QuerySandboxedJSON(sql, generous)
	if err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
	if len(sanitized) <= len(raw) {
		t.Fatalf("sanitized body is %d bytes, engine body %d — no growth to guard against", len(sanitized), len(raw))
	}
	t.Logf("engine body %d bytes -> %d bytes after U+FFFD replacement (%.2fx)",
		len(raw), len(sanitized), float64(len(sanitized))/float64(len(raw)))

	// A cap the ENGINE accepts — it is exactly the size of the body the
	// engine assembled — which the SANITIZED body exceeds. The caller still
	// gets its rows.
	tight := flatsqlrt.SandboxCaps{MaxRows: 64, MaxBytes: uint64(len(raw)), Timeout: 30 * time.Second}
	if _, _, _, err := store.engineDB.QuerySandboxedJSON(sql, tight); err != nil {
		t.Fatalf("premise broken: the engine itself rejects its own body size: %v", err)
	}
	payload, rows, _, err := store.QuerySandboxedJSON(sql, tight)
	if err != nil {
		t.Fatalf("a within-cap answer was refused because a peer's bytes widened it: %v", err)
	}
	if rows < 1 {
		t.Fatalf("within-cap answer returned %d rows", rows)
	}
	if uint64(len(payload)) <= tight.MaxBytes {
		t.Fatalf("the fixture stopped exercising widening: %d bytes under a %d cap", len(payload), tight.MaxBytes)
	}
	if !utf8.Valid(payload) || !json.Valid(payload) {
		t.Fatalf("the widened body is not a JSON text: %q", payload)
	}
	// THE WIDENING IS BOUNDED, which is why returning it is safe: one invalid
	// byte becomes at most three.
	if uint64(len(payload)) > 3*tight.MaxBytes {
		t.Fatalf("sanitized body is %d bytes under a %d cap — more than the 3x U+FFFD bound",
			len(payload), tight.MaxBytes)
	}

	// THE VOLUMETRIC CONTRACT IS INTACT: a cap below what the engine assembles
	// is still the engine's own typed rejection, and it hands back no bytes.
	overrun := flatsqlrt.SandboxCaps{MaxRows: 64, MaxBytes: uint64(len(raw)) - 1, Timeout: 30 * time.Second}
	payload, _, _, err = store.QuerySandboxedJSON(sql, overrun)
	if err == nil {
		t.Fatalf("cap %d let a %d-byte body through", overrun.MaxBytes, len(payload))
	}
	se, ok := flatsqlrt.AsSandboxError(err)
	if !ok || se.Code != flatsqlrt.SandboxCodeByteCap {
		t.Fatalf("rejection = %v, want a typed %s sandbox error", err, flatsqlrt.SandboxCodeByteCap)
	}
	if payload != nil {
		t.Fatal("a rejected query must not hand back bytes")
	}
}

// TestThePublicSurfaceMarksEveryPlaceholderColumn holds the listing to the
// same honesty the column type already buys. The fallback column is named
// after the IDL field it could not project (`SITE` on `LDM`), so a caller
// reading the surface has no way to tell it from a real column — it returns a
// small integer, which is loud at the value level and silent at the listing
// level. Every standard the generator declares unprojectable is marked, so
// the marking cannot drift from the catalog.
func TestThePublicSurfaceMarksEveryPlaceholderColumn(t *testing.T) {
	store := newEngineRecordsStore(t, filepath.Join(t.TempDir(), "store"))
	defer store.Close()

	surface, err := store.PublicQuerySurface()
	if err != nil {
		t.Fatalf("public query surface: %v", err)
	}
	if len(engineUnprojectableFirstFields) == 0 {
		t.Fatal("no standard falls back — this guard has nothing to check")
	}

	seen := make(map[string]bool, len(engineUnprojectableFirstFields))
	for _, rel := range surface {
		base, _, _ := strings.Cut(rel.Name, "@")
		schema := base + ".fbs"
		field, junk := engineUnprojectableFirstFields[schema]
		if !junk {
			if len(rel.PlaceholderColumns) != 0 {
				t.Errorf("%s is marked %v but projects real columns", rel.Name, rel.PlaceholderColumns)
			}
			continue
		}
		seen[schema] = true
		if len(rel.PlaceholderColumns) != 1 || rel.PlaceholderColumns[0] != field {
			t.Errorf("%s placeholder_columns = %v, want [%s]", rel.Name, rel.PlaceholderColumns, field)
			continue
		}
		found := false
		for _, col := range rel.Columns {
			if col == field {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("%s marks %q as a placeholder but does not list it as a column", rel.Name, field)
		}
	}
	for schema := range engineUnprojectableFirstFields {
		if !seen[schema] {
			t.Errorf("%s falls back but never appears on the public surface", schema)
		}
	}
}
