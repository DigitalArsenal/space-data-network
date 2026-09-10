package flatsqlrt

import (
	"encoding/base64"
	"strings"
	"testing"

	flatbuffers "github.com/google/flatbuffers/go"
)

// Generated with flatc -b --schema --bfbs-builtins from:
// attribute "encrypted";
// table Child { label:string; private_text:string (encrypted); }
// table Record { name:string; comments:[string]; child:Child; late:string;
//
//	secret:string (encrypted); payload:[ubyte]; }
//
// root_type Record; file_identifier "FTSR";
const completeSearchTestSchema = "GAAAAEJGQlMQABwABAAIAAwAEAAUABgAEAAAADQAAAAsAAAAHAAAABAAAAAwAAAABAAAAAAAAAAAAAAAAAAAAAQAAABGVFNSAAAAAAAAAAACAAAA0AEAAAQAAABM/v//LAAAAAwAAAABAAAAzAEAAAYAAADYAAAAMAEAAKQAAABoAQAAFAAAADwAAAAGAAAAUmVjb3JkAAAM////AAAAAQUADgAUAAAABAAAAPD+//8AAA4EAQAAAAcAAABwYXlsb2FkAGz+//8AAAABBAAMAEQAAAA0AAAABAAAAAEAAAAEAAAAUP7//xAAAAAEAAAAAQAAADAAAAAJAAAAZW5jcnlwdGVkAAAA/P3//wAAAA0BAAAABgAAAHNlY3JldAAAlP///wAAAAEDAAoAFAAAAAQAAAAo/v//AAAADQEAAAAEAAAAbGF0ZQAAAADA////AAAAAQIACAAoAAAAFAAAABAAEAAHAAAACAAAAAAADAAQAAAAAAAADwAAAAABAAAABQAAAGNoaWxkAAAAHAAUAAwAEAAIAAoAAAAAAAAAAAAAAAAAAAAHABwAAAAAAAABAQAGACQAAAAUAAAAEAAMAAYABwAAAAAAAAAIABAAAAAAAA4NBAAAAAgAAABjb21tZW50cwAAAAAI////AAEEABQAAAAEAAAA7P7//wAAAA0BAAAABAAAAG5hbWUAAAAAFAAUAAQACAAAAAwAAAAAAAAAEAAUAAAAJAAAABQAAAABAAAABAAAAAAAAAAAAAAAAgAAALgAAAAsAAAABQAAAENoaWxkAAAAHAAYAAwAEAAIAAoAAAAAAAAAAAAAABQAAAAHABwAAAAAAAABAQAGAEwAAAA8AAAABAAAAAEAAAAMAAAACAAMAAQACAAIAAAAEAAAAAQAAAABAAAAMAAAAAkAAABlbmNyeXB0ZWQAAAC0////AAAADQEAAAAMAAAAcHJpdmF0ZV90ZXh0AAAAABwAEAAIAAwAAAAGAAAAAAAAAAAAAAAAAAAABQAcAAAAAAEEACQAAAAUAAAAEAAMAAcAAAAAAAAAAAAIABAAAAAAAAANAQAAAAUAAABsYWJlbAAAAA=="

func TestCompleteRecordReflectionInWASM(t *testing.T) {
	rt := newTestRuntime(t)
	db, err := rt.CreateDatabase(`table Record { name:string; }`, "complete-search")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Destroy()
	if err = db.RegisterFileID("FTSR", "Record"); err != nil {
		t.Fatal(err)
	}
	schema, err := base64.StdEncoding.DecodeString(completeSearchTestSchema)
	if err != nil {
		t.Fatal(err)
	}
	b := flatbuffers.NewBuilder(256)
	name := b.CreateString("firstneedle")
	comment := b.CreateString("vectorneedle")
	b.StartVector(4, 1, 4)
	b.PrependUOffsetT(comment)
	comments := b.EndVector(1)
	label := b.CreateString("nestedneedle")
	secret := b.CreateString("secretneedle")
	b.StartObject(2)
	b.PrependUOffsetTSlot(0, label, 0)
	b.PrependUOffsetTSlot(1, secret, 0)
	child := b.EndObject()
	late := b.CreateString("lateneedle")
	payload := b.CreateByteVector([]byte("opaqueneedle"))
	b.StartObject(6)
	b.PrependUOffsetTSlot(0, name, 0)
	b.PrependUOffsetTSlot(1, comments, 0)
	b.PrependUOffsetTSlot(2, child, 0)
	b.PrependUOffsetTSlot(3, late, 0)
	b.PrependUOffsetTSlot(4, secret, 0)
	b.PrependUOffsetTSlot(5, payload, 0)
	b.FinishSizePrefixedWithFileIdentifier(b.EndObject(), []byte("FTSR"))
	record := b.FinishedBytes()
	result, err := db.Query(`SELECT flatsql_record_text('Record',?,?)`, record, schema)
	if err != nil || len(result.Rows) != 1 {
		t.Fatalf("complete extraction: %#v %v", result, err)
	}
	text := result.Rows[0][0].(string)
	for _, term := range []string{"firstneedle", "vectorneedle", "nestedneedle", "lateneedle"} {
		if !strings.Contains(text, term) {
			t.Errorf("missing public field %s", term)
		}
	}
	for _, term := range []string{"secretneedle", "opaqueneedle"} {
		if strings.Contains(text, term) {
			t.Errorf("protected or opaque field indexed: %s", term)
		}
	}
	for _, bad := range []struct{ record, schema []byte }{
		{record[:8], schema}, {record, schema[:12]}, {[]byte{255, 255, 255, 255, 70, 84, 83, 82}, schema},
	} {
		if _, err = db.Query(`SELECT flatsql_record_text('Record',?,?)`, bad.record, bad.schema); err == nil {
			t.Error("invalid reflection input accepted")
		}
		if rt.Poisoned() {
			t.Fatal("invalid reflection input poisoned runtime")
		}
	}
	if _, err = db.Query(`SELECT 1`); err != nil {
		t.Fatal(err)
	}
}
