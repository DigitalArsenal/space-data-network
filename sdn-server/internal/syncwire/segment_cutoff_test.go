package syncwire

import (
	"reflect"
	"testing"
)

func TestSegmentSnapshotCutoffsAndLegacyReaders(t *testing.T) {
	cutoff, source, zero := int64(33), int64(7), int64(0)
	want := []Segment{
		{SchemaName: "CAT.fbs", Offset: 8, PublicationCutoff: &cutoff, PublicationSourceCutoff: &source},
		{SchemaName: "CAT.fbs", BatchID: "ordinary", Offset: 16},
		{SchemaName: "OCM.fbs", PublicationCutoff: &zero, PublicationSourceCutoff: &zero},
	}
	oldCount := len(segmentColumns) - 2
	if SegmentColumns[oldCount].Name != "PUBLICATION_CUTOFF" || SegmentColumns[oldCount+1].Name != "PUBLICATION_SOURCE_CUTOFF" {
		t.Fatal("cutoffs must be appended after all existing columns")
	}
	q, err := ParseQRP(SegmentPage(QRPFields{}, want))
	if err != nil {
		t.Fatal(err)
	}
	got, err := Segments(q)
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("typed cutoff round trip: %v, %v", got, err)
	}
	columns, rows := Rows(q)
	for _, name := range []string{"PUBLICATION_CUTOFF", "PUBLICATION_SOURCE_CUTOFF"} {
		ordinary := rows[1][columns[name]]
		if ordinary.IsInt || ordinary.IsText || ordinary.IsBytes {
			t.Fatalf("ordinary row %s is not Null", name)
		}
		if cell := rows[2][columns[name]]; !cell.IsInt || cell.Int != 0 {
			t.Fatalf("explicit zero %s was lost", name)
		}
	}
	legacyWant := append([]Segment(nil), want...)
	for i := range legacyWant {
		legacyWant[i].PublicationCutoff, legacyWant[i].PublicationSourceCutoff = nil, nil
	}
	// The decoder with the old descriptor set ignores new named columns.
	legacy, err := segmentsWithColumns(q, segmentColumns[:oldCount])
	if err != nil || !reflect.DeepEqual(legacy, legacyWant) {
		t.Fatalf("old reader lost rows: %v, %v", legacy, err)
	}
	// Old-shaped pages remain readable by the new decoder.
	oldRows := SegmentRows(want)
	for i := range oldRows {
		oldRows[i] = oldRows[i][:oldCount]
	}
	oldPage, err := ParseQRP(BuildQRP(QRPFields{Kind: QRPKindPage, Status: QRPStatusOk, Columns: SegmentColumns[:oldCount], Rows: oldRows}))
	if err != nil {
		t.Fatal(err)
	}
	got, err = Segments(oldPage)
	if err != nil || !reflect.DeepEqual(got, legacyWant) {
		t.Fatalf("new reader lost old rows: %v, %v", got, err)
	}
	// Reordering and adding an unknown column cannot change name-based reads.
	fields := append([]QRPColumnField{{Name: "FUTURE_COLUMN", Type: "string"}}, SegmentColumns...)
	reordered := SegmentRows(want)
	for i := range reordered {
		reordered[i] = append([]QRPCell{TextCell("ignored")}, reordered[i]...)
		reordered[i][1], reordered[i][8] = reordered[i][8], reordered[i][1]
	}
	fields[1], fields[8] = fields[8], fields[1]
	page, err := ParseQRP(BuildQRP(QRPFields{Kind: QRPKindPage, Status: QRPStatusOk, Columns: fields, Rows: reordered}))
	if err != nil {
		t.Fatal(err)
	}
	got, err = Segments(page)
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("name-based reordered rows: %v, %v", got, err)
	}
}
