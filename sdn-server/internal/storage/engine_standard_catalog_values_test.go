package storage

import (
	"bytes"
	"fmt"
	"math"
	"path/filepath"
	"strings"
	"testing"

	flatbuffers "github.com/google/flatbuffers/go"

	"github.com/spacedatanetwork/sdn-server/internal/storage/enginecatalog"
)

// TestGeneratedColumnsReadTheirOwnFieldValue is the value assertion the
// generated catalog was missing.
//
// The other catalog tests pin the routed SET, the >=1-column invariant,
// determinism and byte-identical regeneration — none of which can catch the
// thing that actually silently corrupts data here: the LEADING-RUN SLOT
// ARITHMETIC. Vtable slots are positional, so an enum resolved to the wrong
// underlying scalar, a union that does not stop the run at its two-slot
// boundary, or a dropped deprecated field would make a column decode its
// NEIGHBOUR'S value — a wrong number, never a missing one, and nothing else in
// the suite would notice. A future SDS pin bump that changes a field type would
// otherwise only be caught by the golden regeneration test, which compares
// generated TEXT and knows nothing about what the engine reads.
//
// So: for every generated standard, build a synthetic root buffer with a
// DISTINCT typed value in every leading-run slot, route it through the real
// engine by its own file identifier, and read every projected column back.
func TestGeneratedColumnsReadTheirOwnFieldValue(t *testing.T) {
	catalog, err := enginecatalog.Build(embeddedSchemaDir, enginecatalog.PinnedSchemas)
	if err != nil {
		t.Fatalf("build catalog: %v", err)
	}
	if len(catalog.Bindings) < 100 {
		t.Fatalf("only %d generated bindings — this test is meaningless unless the whole catalog is routed", len(catalog.Bindings))
	}

	store := newEngineRecordsStore(t, filepath.Join(t.TempDir(), "store"))
	defer store.Close()

	checked, skipped := 0, 0
	for _, binding := range catalog.Bindings {
		if !store.engineRoutesSchema(binding.Schema) {
			skipped++
			continue
		}
		// A junk column reads a slot whose real type is not representable, so
		// there is no value to assert against. That fallback is pinned by
		// TestEveryRoutedTableDeclaresAtLeastOneColumn instead.
		junk := false
		for _, col := range binding.Columns {
			if col.Junk {
				junk = true
			}
		}
		if junk {
			skipped++
			continue
		}

		want, buf, ok := buildSyntheticRoot(t, binding)
		if !ok {
			skipped++
			continue
		}
		if _, err := store.engineDB.IngestOneWithSource(buf, engineDefaultSource); err != nil {
			t.Fatalf("%s: ingest synthetic record: %v", binding.Schema, err)
		}

		quoted := make([]string, 0, len(binding.Columns))
		for _, col := range binding.Columns {
			quoted = append(quoted, quoteEngineRelation(col.Name))
		}
		res, err := store.engineDB.Query(
			"SELECT " + strings.Join(quoted, ", ") + " FROM " + quoteEngineRelation(binding.Table))
		if err != nil {
			t.Fatalf("%s: read projected columns: %v", binding.Schema, err)
		}
		if len(res.Rows) != 1 {
			t.Fatalf("%s: %d rows in %s after one ingest, want 1", binding.Schema, len(res.Rows), binding.Table)
		}
		row := res.Rows[0]
		if len(row) != len(binding.Columns) {
			t.Fatalf("%s: %d cells for %d projected columns", binding.Schema, len(row), len(binding.Columns))
		}
		for i, col := range binding.Columns {
			if !engineCellEquals(want[i], row[i]) {
				t.Errorf("%s.%s (slot %d, %s) = %#v, want %#v — a projected column is reading the wrong vtable slot",
					binding.Table, col.Name, i, col.Type, row[i], want[i])
			}
		}
		checked++
	}

	if checked < 100 {
		t.Fatalf("only %d standards carried a value assertion (%d skipped)", checked, skipped)
	}
	t.Logf("value-checked %d generated standards (%d skipped as junk-only or unrouted in this store)", checked, skipped)
}

// buildSyntheticRoot writes one FlatBuffer root table whose leading-run slots
// each hold a DISTINCT value of the projected type, finished with the
// standard's own file identifier so the engine routes it by the same four
// header bytes ingest uses in production.
func buildSyntheticRoot(t *testing.T, binding enginecatalog.Binding) ([]any, []byte, bool) {
	t.Helper()
	b := flatbuffers.NewBuilder(2048)

	// Every offset must be created BEFORE the object is started.
	want := make([]any, len(binding.Columns))
	offsets := make(map[int]flatbuffers.UOffsetT, len(binding.Columns))
	for i, col := range binding.Columns {
		switch col.Type {
		case "string":
			v := fmt.Sprintf("slot-%d-%s", i, binding.Table)
			want[i] = v
			offsets[i] = b.CreateString(v)
		case "[ubyte]":
			v := []byte{byte(i%251 + 1), 0xA5, byte((i+7)%251 + 1)}
			want[i] = v
			offsets[i] = b.CreateByteVector(v)
		}
	}

	b.StartObject(len(binding.Columns))
	for i, col := range binding.Columns {
		slot := flatbuffers.VOffsetT(i)
		switch col.Type {
		case "string", "[ubyte]":
			b.PrependUOffsetTSlot(int(slot), offsets[i], 0)
		case "bool":
			want[i] = true
			b.PrependBoolSlot(int(slot), true, false)
		case "byte", "int8":
			v := int8(i%100 + 1)
			want[i] = int64(v)
			b.PrependInt8Slot(int(slot), v, 0)
		case "ubyte", "uint8":
			v := uint8(i%200 + 1)
			want[i] = int64(v)
			b.PrependUint8Slot(int(slot), v, 0)
		case "short", "int16":
			v := int16((i + 1) * 7)
			want[i] = int64(v)
			b.PrependInt16Slot(int(slot), v, 0)
		case "ushort", "uint16":
			v := uint16((i + 1) * 9)
			want[i] = int64(v)
			b.PrependUint16Slot(int(slot), v, 0)
		case "int", "int32":
			v := int32((i+1)*1000 + 7)
			want[i] = int64(v)
			b.PrependInt32Slot(int(slot), v, 0)
		case "uint", "uint32":
			v := uint32((i+1)*1000 + 11)
			want[i] = int64(v)
			b.PrependUint32Slot(int(slot), v, 0)
		case "long", "int64":
			v := int64((i+1)*1_000_003 + 13)
			want[i] = v
			b.PrependInt64Slot(int(slot), v, 0)
		case "ulong", "uint64":
			v := uint64((i+1)*1_000_003 + 17)
			want[i] = int64(v)
			b.PrependUint64Slot(int(slot), v, 0)
		case "float", "float32":
			// Exactly representable in float32, so the widening read back to
			// float64 is lossless and the comparison is exact.
			v := float32(i+1) + 0.5
			want[i] = float64(v)
			b.PrependFloat32Slot(int(slot), v, 0)
		case "double", "float64":
			v := float64(i+1) + 0.25
			want[i] = v
			b.PrependFloat64Slot(int(slot), v, 0)
		default:
			t.Fatalf("%s.%s: unhandled projected type %q — the generator emitted a type this test cannot construct, which means it cannot be value-checked either",
				binding.Table, col.Name, col.Type)
		}
	}
	root := b.EndObject()
	b.FinishWithFileIdentifier(root, []byte(binding.FileID))
	return want, b.FinishedBytes(), true
}

// engineCellEquals compares an expected Go value with what the engine returned
// for that column, tolerating the widening the vtab does on integers.
func engineCellEquals(want, got any) bool {
	switch w := want.(type) {
	case string:
		g, ok := got.(string)
		return ok && g == w
	case []byte:
		switch g := got.(type) {
		case []byte:
			return bytes.Equal(g, w)
		case string:
			return g == string(w)
		}
		return false
	case bool:
		switch g := got.(type) {
		case bool:
			return g == w
		case int64:
			return (g != 0) == w
		case float64:
			return (g != 0) == w
		}
		return false
	case int64:
		switch g := got.(type) {
		case int64:
			return g == w
		case float64:
			return g == float64(w)
		}
		return false
	case float64:
		switch g := got.(type) {
		case float64:
			return math.Abs(g-w) <= 1e-9*math.Max(1, math.Abs(w))
		case int64:
			return float64(g) == w
		}
		return false
	}
	return false
}
