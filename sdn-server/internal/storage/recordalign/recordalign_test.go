package recordalign

import (
	"bytes"
	"encoding/binary"
	"testing"
	"unsafe"

	flatbuffers "github.com/google/flatbuffers/go"
)

// build finishes a table holding a float64 in slot 0 and an int32 in slot
// 1, with file identifier "TEST", the way publishers build records: finished
// with a size prefix (then stripped, as the publish boundary stores it) or
// finished bare. It returns the stored record and the float64's offset from
// the record's first byte.
func build(t testing.TB, sizePrefixed bool, name string) ([]byte, int) {
	t.Helper()
	b := flatbuffers.NewBuilder(64)
	s := b.CreateString(name)
	b.StartObject(3)
	b.PrependFloat64Slot(0, 15.50103472, 0)
	b.PrependInt32Slot(1, 25544, 0)
	b.PrependUOffsetTSlot(2, s, 0)
	root := b.EndObject()
	var record []byte
	if sizePrefixed {
		b.FinishSizePrefixedWithFileIdentifier(root, []byte("TEST"))
		out := b.FinishedBytes()
		if got := binary.LittleEndian.Uint32(out); int(got) != len(out)-4 {
			t.Fatalf("size prefix %d, buffer %d", got, len(out))
		}
		record = bytes.Clone(out[4:])
	} else {
		b.FinishWithFileIdentifier(root, []byte("TEST"))
		record = bytes.Clone(b.FinishedBytes())
	}
	if string(record[4:8]) != "TEST" {
		t.Fatalf("file identifier at 4..8 = %q", record[4:8])
	}
	tab := flatbuffers.Table{Bytes: record, Pos: flatbuffers.GetUOffsetT(record)}
	off := tab.Offset(flatbuffers.VOffsetT(4)) // vtable slot 0
	if off == 0 {
		t.Fatal("float64 field absent")
	}
	return record, int(tab.Pos) + int(off)
}

// fieldAt reports where the float64 sits relative to a view's first byte:
// the offset a stock verifier checks for 8-byte alignment.
func fieldAt(v View, recordField int) int {
	if v.SizePrefixed {
		return PrefixLen + recordField
	}
	return recordField
}

func TestOriginFollowsTheBuilder(t *testing.T) {
	for _, name := range []string{"", "I", "ISS", "ISS (ZARYA)", "STARLINK-1007", "a longer object name that spans words"} {
		stripped, sf := build(t, true, name)
		bare, bf := build(t, false, name)

		if len(stripped)%8 != 4 || sf%8 != 4 {
			t.Fatalf("%q stripped: len %d, float64 at %d (want both 4 mod 8)", name, len(stripped), sf)
		}
		if len(bare)%8 != 0 || bf%8 != 0 {
			t.Fatalf("%q bare: len %d, float64 at %d (want both 0 mod 8)", name, len(bare), bf)
		}
		if OriginOffset(len(stripped)) != 4 || OriginOffset(len(bare)) != 0 {
			t.Fatalf("%q: origins %d, %d", name, OriginOffset(len(stripped)), OriginOffset(len(bare)))
		}

		// A stripped record is copied behind its u32 length into an 8-aligned
		// buffer; the float64 lands on 8 from the view's start and in memory.
		v := Record(stripped)
		if !v.SizePrefixed || !v.Copied || len(v.Buf) != 4+len(stripped) {
			t.Fatalf("%q stripped view: %+v", name, v)
		}
		if int(binary.LittleEndian.Uint32(v.Buf)) != len(stripped) || !bytes.Equal(v.Buf[4:], stripped) {
			t.Fatalf("%q: view is not [u32 len][record]", name)
		}
		if fieldAt(v, sf)%8 != 0 || uintptr(unsafe.Pointer(&v.Buf[0]))%8 != 0 {
			t.Fatalf("%q: float64 at %d of a buffer at %p", name, fieldAt(v, sf), &v.Buf[0])
		}

		// A bare build is its own view: no copy.
		v = Record(bare)
		if v.SizePrefixed || v.Copied || &v.Buf[0] != &bare[0] || fieldAt(v, bf)%8 != 0 {
			t.Fatalf("%q bare view: %+v", name, v)
		}
	}
}

func TestFrameIsTheViewWithoutACopy(t *testing.T) {
	for _, prefixed := range []bool{true, false} {
		record, field := build(t, prefixed, "ISS (ZARYA)")
		frame := binary.LittleEndian.AppendUint32(nil, uint32(len(record)))
		frame = append(frame, record...)
		v, ok := Frame(frame)
		if !ok || v.Copied {
			t.Fatalf("prefixed=%v: %+v %v", prefixed, v, ok)
		}
		if v.SizePrefixed != prefixed || fieldAt(v, field)%8 != 0 {
			t.Fatalf("prefixed=%v: view %+v puts the float64 at %d", prefixed, v, fieldAt(v, field))
		}
		if prefixed && &v.Buf[0] != &frame[0] || !prefixed && &v.Buf[0] != &frame[4] {
			t.Fatalf("prefixed=%v: the view does not alias the frame", prefixed)
		}
	}
	for _, bad := range [][]byte{nil, {1, 2}, {9, 0, 0, 0, 1, 2, 3, 4}, {0, 0, 0, 0, 1}} {
		if _, ok := Frame(bad); ok {
			t.Fatalf("Frame(%v) accepted a malformed frame", bad)
		}
	}
}

func TestAlignedIsEightAligned(t *testing.T) {
	for n := 1; n <= 72; n++ {
		b := Aligned(n)
		if len(b) != n || uintptr(unsafe.Pointer(&b[0]))%8 != 0 {
			t.Fatalf("Aligned(%d): len %d at %p", n, len(b), &b[0])
		}
	}
	if b := Aligned(0); b == nil || len(b) != 0 {
		t.Fatalf("Aligned(0) = %v", b)
	}
}

// Stored OMM records average ~330 bytes (the dev node: 172,690,560 bytes
// over 525,806 records); the benchmark copies a 332-byte one.
func BenchmarkRecordCopy(b *testing.B) {
	record, _ := build(b, true, "STARLINK-1007")
	record = append(record, make([]byte, 332-len(record))...)
	b.SetBytes(int64(len(record)))
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if !Record(record).Copied {
			b.Fatal("no copy")
		}
	}
}

func BenchmarkRecordInPlace(b *testing.B) {
	record, _ := build(b, false, "STARLINK-1007")
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if Record(record).Copied {
			b.Fatal("copied")
		}
	}
}

func BenchmarkFrame(b *testing.B) {
	record, _ := build(b, true, "STARLINK-1007")
	frame := append(binary.LittleEndian.AppendUint32(nil, uint32(len(record))), record...)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if v, ok := Frame(frame); !ok || v.Copied {
			b.Fatal("frame")
		}
	}
}
