package storage

// A record that carries its own size prefix (a FinishSizePrefixed buffer
// stored as is: the dataset-publication PNM, the local EPM) is stored on
// format 2 as on format 1: the same CID, the same bytes back, found by its
// indexed column (flatsql PARTITION-STORE.md §38).

import (
	"bytes"
	"testing"
	"time"

	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/EPM"
	flatbuffers "github.com/google/flatbuffers/go"
)

func f2TestEPM(dn, legalName string, prefixed bool) []byte {
	b := flatbuffers.NewBuilder(256)
	d := b.CreateString(dn)
	l := b.CreateString(legalName)
	EPM.EPMStart(b)
	EPM.EPMAddDN(b, d)
	EPM.EPMAddLEGAL_NAME(b, l)
	e := EPM.EPMEnd(b)
	if prefixed {
		EPM.FinishSizePrefixedEPMBuffer(b, e)
	} else {
		EPM.FinishEPMBuffer(b, e)
	}
	return append([]byte(nil), b.FinishedBytes()...)
}

func TestFormat2StoresSizePrefixedRecords(t *testing.T) {
	requireFormat2Engine(t)
	legacy := reopenDeferred(t, t.TempDir())
	defer legacy.Close()
	f2 := openFormat2ForTest(t, t.TempDir())
	defer f2.Close()
	pnm, err := BuildDatasetPublicationPNM(&DatasetPublicationManifest{Path: "omm-shard.dpm", CID: "bafkreihdwdcefgh4dqkjv67uzcmw7ojee6xedzdetojuzjevtenxquvyku",
		FileID: "OMM:space-data-network-02:celestrak-gp:b1:0:1000"}, DatasetPublicationPNMOptions{PublishedAt: time.Date(2026, 9, 29, 0, 0, 0, 0, time.UTC)})
	if err != nil {
		t.Fatal(err)
	}
	type rec struct {
		schema string
		data   []byte
	}
	recs := []rec{
		{"PNM.fbs", pnm},
		{"EPM.fbs", f2TestEPM("CN=node-a", "Node A", true)},
		{"EPM.fbs", f2TestEPM("CN=node-b", "Node B", false)},
		{"OMM.fbs", f2TestOMM(40001, time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC), "OBJ-P")},
	}
	for _, r := range recs {
		a, err := legacy.Store(r.schema, r.data, "16Uiu2HAm1LbvwjEHW2GDP2ZQZvwHLZrz2jbYoRLQmJEQ3wZ5Fm45", nil)
		if err != nil {
			t.Fatalf("%s: format 1: %v", r.schema, err)
		}
		b, err := f2.Store(r.schema, r.data, "16Uiu2HAm1LbvwjEHW2GDP2ZQZvwHLZrz2jbYoRLQmJEQ3wZ5Fm45", nil)
		if err != nil {
			t.Fatalf("%s: format 2 refused the record: %v", r.schema, err)
		}
		if a != b {
			t.Fatalf("%s: format 2 CID %s, format 1 %s", r.schema, b, a)
		}
		ga, err := legacy.Get(r.schema, a)
		if err != nil {
			t.Fatal(err)
		}
		gb, err := f2.Get(r.schema, b)
		if err != nil {
			t.Fatalf("%s %s: format 2: %v", r.schema, b, err)
		}
		if !bytes.Equal(ga, gb) || !bytes.Equal(gb, r.data) {
			t.Fatalf("%s %s: format 2 returned %d bytes, format 1 %d, stored %d", r.schema, b, len(gb), len(ga), len(r.data))
		}
	}
	// The PNM's FILE_ID column index finds it (its keys were extracted after
	// its own prefix).
	for _, s := range []*FlatSQLStore{legacy, f2} {
		got, err := s.QueryRawRecords(RawRecordQuery{SchemaName: "PNM.fbs", SyncFilter: "FILE_ID = 'OMM:space-data-network-02:celestrak-gp:b1:0:1000'", Limit: 10})
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 1 || !bytes.Equal(got[0].Data, pnm) {
			t.Fatalf("PNM by FILE_ID (format 2: %v): %d records", s.Format2(), len(got))
		}
	}
}
