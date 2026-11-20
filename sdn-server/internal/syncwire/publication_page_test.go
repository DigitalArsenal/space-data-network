package syncwire

import (
	"bytes"
	"reflect"
	"testing"
)

func TestPublicationColumnDoesNotChangeCanonicalSegmentPage(t *testing.T) {
	s := Segment{SchemaName: "CAT.fbs", PNMCID: "synthetic-announcement"}
	before := SegmentPage(QRPFields{SchemaName: s.SchemaName}, []Segment{s})
	listing := PublicationSegmentPage(QRPFields{SchemaName: s.SchemaName}, []Segment{s})
	page, _, err := ReadPage(bytes.NewReader(listing))
	if err != nil {
		t.Fatal(err)
	}
	cols, rows := Rows(page)
	if rows[0][cols["ANNOUNCEMENT_CID"]].Text != s.PNMCID {
		t.Fatal("missing content address")
	}
	decoded, err := Segments(page)
	if err != nil || len(decoded) != 1 || !reflect.DeepEqual(decoded[0], s) {
		t.Fatal("legacy decode changed", err)
	}
	after := SegmentPage(QRPFields{SchemaName: s.SchemaName}, []Segment{s})
	if !bytes.Equal(before, after) {
		t.Fatal("listing mutated canonical signature columns")
	}
	canonical, _, err := ReadPage(bytes.NewReader(after))
	if err != nil {
		t.Fatal(err)
	}
	columns, _ := Rows(canonical)
	if _, ok := columns["ANNOUNCEMENT_CID"]; ok {
		t.Fatal("listing field entered signature payload")
	}
}
