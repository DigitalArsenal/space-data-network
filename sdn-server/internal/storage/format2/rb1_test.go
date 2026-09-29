package format2

import (
	"encoding/hex"
	"math"
	"os"
	"strings"
	"testing"
)

// The golden RB1 stream flatsql's C++ encoder writes (cpp/test/ps/vectors/
// rb1_int64.hex, flatsql 3.1.0): -2^63, 2^53+1 and 2^63-1 decode exactly, in
// any byte split (design T2 #3, T6 #6).
func TestRB1GoldenVectorDecodesExactly(t *testing.T) {
	raw, err := os.ReadFile("testdata/rb1_int64.hex")
	if err != nil {
		t.Fatal(err)
	}
	stream, err := hex.DecodeString(strings.TrimSpace(string(raw)))
	if err != nil {
		t.Fatal(err)
	}
	for _, split := range []int{1, 2, 3, 7, 13, 64, len(stream)} {
		var d RB1Decoder
		for at := 0; at < len(stream); at += split {
			end := at + split
			if end > len(stream) {
				end = len(stream)
			}
			if err := d.Feed(stream[at:end]); err != nil {
				t.Fatalf("split %d: %v", split, err)
			}
		}
		if !d.Done() {
			t.Fatalf("split %d: no end record", split)
		}
		if got := strings.Join(d.Names, ","); got != "min,p53,max,n53,zero,neg1" {
			t.Fatalf("names %q", got)
		}
		want := []int64{math.MinInt64, 1<<53 + 1, math.MaxInt64, -(1<<53 + 1), 0, -1}
		if len(d.Rows) != 2 {
			t.Fatalf("split %d: %d rows", split, len(d.Rows))
		}
		for i, v := range want {
			c := d.Rows[0][i]
			if c.Type != CellInt || c.I != v {
				t.Fatalf("split %d: cell %d = %+v, want INTEGER %d", split, i, c, v)
			}
		}
		r := d.Rows[1]
		if r[0].Type != CellReal || r[0].F != 9007199254740992.0 {
			t.Fatalf("REAL cell %+v", r[0])
		}
		if r[1].Type != CellText || string(r[1].B) != "t" || r[2].Type != CellBlob || string(r[2].B) != "\x00\x01" ||
			r[3].Type != CellNull || r[4].I != 1 || r[5].I != 2 {
			t.Fatalf("row 2 %+v", r)
		}
		if d.End.Status != 0 || d.End.Rows != 2 {
			t.Fatalf("end %+v", *d.End)
		}
	}
}

func TestRB1ParamsRoundTripThroughTheCellCodec(t *testing.T) {
	in := []Cell{Int(math.MinInt64), Int(1<<53 + 1), Real(2.5), Text("OMM"), Blob([]byte{0, 1, 2}), Null()}
	b := EncodeParams(in)
	if n := int(b[0]) | int(b[1])<<8; n != len(in) {
		t.Fatalf("count %d", n)
	}
	at := 4
	for i, want := range in {
		var c Cell
		n, err := decodeCell(b[at:], &c, true)
		if err != nil {
			t.Fatal(err)
		}
		at += n
		if c.Type != want.Type || c.I != want.I || c.F != want.F || string(c.B) != string(want.B) {
			t.Fatalf("param %d: %+v, want %+v", i, c, want)
		}
	}
	if at != len(b) {
		t.Fatalf("%d trailing bytes", len(b)-at)
	}
}
