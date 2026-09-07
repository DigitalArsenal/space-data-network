package flatsqlrt

import (
	"encoding/base64"
	flatbuffers "github.com/google/flatbuffers/go"
	"strings"
	"testing"
)

func TestFullTextRecordExtractionAndIndex(t *testing.T) {
	rt := newTestRuntime(t)
	db, err := rt.CreateDatabase(`table SearchRecord { name:string; object_id:uint; secret:string (encrypted); }`, "fts-archive")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Destroy()
	// File identifiers may be registered after an initial metadata query.
	if _, err = db.Query("SELECT 1"); err != nil {
		t.Fatal(err)
	}
	if err = db.RegisterFileID("$TST", "SearchRecord"); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Query(`CREATE VIRTUAL TABLE record_fts USING fts5(text)`); err != nil {
		t.Fatal(err)
	}
	b := flatbuffers.NewBuilder(128)
	name := b.CreateString("München optical payload")
	secret := b.CreateString("do-not-index")
	b.StartObject(3)
	b.PrependUOffsetTSlot(0, name, 0)
	b.PrependUint32Slot(1, 25544, 0)
	b.PrependUOffsetTSlot(2, secret, 0)
	root := b.EndObject()
	b.FinishWithFileIdentifier(root, []byte("$TST"))
	data := b.FinishedBytes()
	if _, err = db.Query(`INSERT INTO record_fts(rowid,text) VALUES(205,flatsql_record_text('SearchRecord',?))`, data); err != nil {
		t.Fatal(err)
	}
	result, err := db.Query(`SELECT rowid FROM record_fts WHERE record_fts MATCH ?`, "munchen AND 25544")
	if err != nil || len(result.Rows) != 1 {
		t.Fatalf("search: %#v %v", result, err)
	}
	result, err = db.Query(`SELECT text FROM record_fts WHERE rowid=205`)
	if err != nil || strings.Contains(result.Rows[0][0].(string), "do-not-index") {
		t.Fatal("encrypted field was indexed", err)
	}
	if _, err = db.Query(`SELECT flatsql_record_text('SearchRecord',?)`, []byte{0, 0, 0, 0}); err == nil {
		t.Fatal("malformed record accepted")
	}
	if rt.Poisoned() {
		t.Fatal("an invalid search record poisoned the runtime")
	}
	if _, err = db.Query(`UPDATE record_fts SET text='radio' WHERE rowid=205`); err != nil {
		t.Fatal(err)
	}
	result, err = db.Query(`SELECT rowid FROM record_fts WHERE record_fts MATCH 'optical'`)
	if err != nil || len(result.Rows) != 0 {
		t.Fatalf("stale search term: %#v %v", result, err)
	}
}

func TestFullTextOrbitRecordWithNondefaultEnum(t *testing.T) {
	rt := newTestRuntime(t)
	db := newOMMDatabase(t, rt, "fts-typed-orbit")
	b := flatbuffers.NewBuilder(128)
	name := b.CreateString("searchable payload")
	b.StartObject(40)
	b.PrependUOffsetTSlot(3, name, 0)
	// EPHEMERIS_TYPE has a byte layout; the engine's table-only schema
	// does not declare the enum. It must never read this as a string offset.
	b.PrependInt8Slot(25, 4, 0)
	b.PrependUint32Slot(27, 12345, 0)
	b.FinishWithFileIdentifier(b.EndObject(), []byte("$OMM"))
	result, err := db.Query(`SELECT flatsql_record_text('OMM',?)`, b.FinishedBytes())
	if err != nil || len(result.Rows) != 1 || result.Rows[0][0] != "searchable payload\n12345\n" {
		t.Fatalf("nondefault orbital enum broke text extraction: %#v %v", result, err)
	}
	// Exact public CelesTrak NGRST record that stopped the live archive scan:
	// bafkreigzy7naeyefjoo54wuxlwfgvs3dyzuglj5mel4sxvzv3473yzoubq.
	const liveRecord = "SAAAACRPTU0AAD4AXAAAABQAEABYAFQAGAAAAAAAAAAAAAAATAAAAEQAPAA0ACwAJAAcAAAAAAAAAAAAAAAAAAcADABQAAgAPgAAAAAAAADnAwAAUAAAAFQAAABgAAAAeAAAAHE9CtejNHVASOF6FK6HWEAKaCJseDooQOxRuB6Fy0BA8WjjiLX4FD+yNCpPpFYwQFAAAAC0iAEAZAAAAHAAAAABAAAAVQAAAAkAAABDRUxFU1RSQUsAAAAUAAAAMjAyNi0wOC0zMFQxMTozNDozM1oAAAAABQAAAEVBUlRIAAAAFAAAADIwMjYtMDgtMzBUMTE6MzQ6MzNaAAAAAAkAAAAyMDI2LTE5OUEAAAAFAAAATkdSU1QAAAA="
	data, err := base64.StdEncoding.DecodeString(liveRecord)
	if err != nil {
		t.Fatal(err)
	}
	result, err = db.Query(`SELECT flatsql_record_text('OMM',?)`, data)
	if err != nil || len(result.Rows) != 1 || !strings.Contains(result.Rows[0][0].(string), "NGRST\n") {
		t.Fatalf("live orbital record remains unsearchable: %#v %v", result, err)
	}
}
