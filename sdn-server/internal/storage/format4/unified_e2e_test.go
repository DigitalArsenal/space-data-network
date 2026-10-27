package format4_test

import (
	"bytes"
	"context"
	"encoding/binary"
	"math"
	"testing"
	"time"

	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/GJN"
	flatbuffers "github.com/google/flatbuffers/go"
	"github.com/spacedatanetwork/sdn-server/internal/storage/format2"
	"github.com/spacedatanetwork/sdn-server/internal/storage/format4"
	"github.com/spacedatanetwork/sdn-server/internal/wasmrt"
)

// One real WasmEdge end-to-end flow: the identical release artifact, canonical
// schemas/records, typed query protocol, durable metadata and unchanged bytes.
func TestUnifiedFlatBufferQueryReopen(t *testing.T) {
	if !wasmrt.SubstrateStatus().Patched() {
		t.Fatal("patched WasmEdge is required; this E2E must not skip")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	root := t.TempDir()
	e := openReal(t, root, format4.CreateFresh, 0, nil)
	for _, name := range []string{"GJN.fbs", "OMM.fbs"} {
		spec, err := format4.TypeSpecFor(name)
		if err != nil {
			t.Fatal(err)
		}
		if err = e.RegisterType(spec); err != nil {
			t.Fatal(err)
		}
	}
	// Empty registered schemas must reopen on the seven-import host ABI.
	if err := e.Close(ctx); err != nil {
		t.Fatal(err)
	}
	e = openReal(t, root, format4.OpenExisting, 0, nil)
	b := flatbuffers.NewBuilder(256)
	GJN.GJNPositionStart(b)
	GJN.GJNPositionAddLONGITUDE(b, 2)
	GJN.GJNPositionAddLATITUDE(b, 2)
	GJN.GJNPositionAddALTITUDE(b, 420)
	GJN.GJNPositionAddHAS_ALTITUDE(b, true)
	point := GJN.GJNPositionEnd(b)
	GJN.GJNGeometryStart(b)
	GJN.GJNGeometryAddTYPE(b, GJN.GJNGeometryTypePOINT)
	GJN.GJNGeometryAddPOINT(b, point)
	geom := GJN.GJNGeometryEnd(b)
	id := b.CreateString("customer-point")
	GJN.GJNFeatureStart(b)
	GJN.GJNFeatureAddID(b, id)
	GJN.GJNFeatureAddGEOMETRY(b, geom)
	GJN.GJNFeatureAddHAS_GEOMETRY(b, true)
	feature := GJN.GJNFeatureEnd(b)
	GJN.GJNStartFEATURESVector(b, 1)
	b.PrependUOffsetT(feature)
	features := b.EndVector(1)
	GJN.GJNStart(b)
	GJN.GJNAddFEATURES(b, features)
	GJN.FinishGJNBuffer(b, GJN.GJNEnd(b))
	plain := append([]byte(nil), b.FinishedBytes()...)
	in := putIn(plain, 1790000000)
	in.Sig = []byte{1, 2, 3, 4}
	out, err := e.Put(ctx, format4.Batch{Type: "GJN", Peer: "fixture-peer", Tags: []format4.Tag{{Provider: "customer", Source: "geo", Batch: "batch-1"}}, Records: []format4.In{in}})
	if err != nil || len(out) != 1 || out[0].Action != format4.ActNew {
		t.Fatalf("put: %+v %v", out, err)
	}
	query := func(sql string, params ...format2.Cell) *format4.QRPDecoder {
		t.Helper()
		d := new(format4.QRPDecoder)
		_, err := e.SQL(ctx, format4.SQLRequest{SQL: sql, Params: params}, func(chunk []byte) error {
			// Host transport may split even the four-byte frame prefix.
			for len(chunk) > 0 {
				n := min(7, len(chunk))
				if err := d.Feed(chunk[:n]); err != nil {
					return err
				}
				chunk = chunk[n:]
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		if !d.Done() || d.End.Status != 0 {
			t.Fatalf("unfinished query: %+v", d.End)
		}
		return d
	}
	d := query("SELECT geo_contains(_data,2,2),geo_contains(_data,3,3),geo_envelope(_data),_data FROM GJN")
	if len(d.Rows) != 1 || d.Rows[0][0].I != 1 || d.Rows[0][1].I != 0 || !bytes.Equal(d.Rows[0][3].B, plain) {
		t.Fatalf("geometry and bytes: %+v", d.Rows)
	}
	if !GJN.GJNBufferHasIdentifier(d.Rows[0][2].B) {
		t.Fatal("geometry result is not GJN")
	}
	d = query("SELECT ?1,?2,?3,?4,?5,geo_distance(0,0,0,1)", format2.Int(math.MaxInt64), format2.Real(1.25), format2.Text("typed"), format2.Blob(plain), format2.Null())
	row := d.Rows[0]
	if row[0].I != math.MaxInt64 || row[1].F != 1.25 || string(row[2].B) != "typed" || !bytes.Equal(row[3].B, plain) || row[4].Type != format2.CellNull || math.Abs(row[5].F-111.194926644559) > 1e-6 {
		t.Fatalf("typed projection: %+v", row)
	}
	var raw []byte
	if _, err = e.SQL(ctx, format4.SQLRequest{SQL: "SELECT _data FROM GJN", Raw: true}, func(p []byte) error { raw = append(raw, p...); return nil }); err != nil {
		t.Fatal(err)
	}
	if len(raw) != len(plain)+4 || int(binary.LittleEndian.Uint32(raw)) != len(plain) || !bytes.Equal(raw[4:], plain) {
		t.Fatal("raw record changed")
	}
	for _, sql := range []string{"SELECT geo_from_geojson('{}')", "SELECT json('{}')", "SELECT geo_contains('POINT(2 2)',2,2)"} {
		if _, err = e.SQL(ctx, format4.SQLRequest{SQL: sql}, func([]byte) error { return nil }); err == nil {
			t.Fatalf("removed conversion accepted: %s", sql)
		}
	}
	original, err := e.Get(ctx, "GJN", []string{in.CID}, false, true)
	if err != nil || len(original) != 1 {
		t.Fatalf("read metadata: %+v %v", original, err)
	}
	if !bytes.Equal(original[0].Sig, in.Sig) || !bytes.Equal(original[0].Data, plain) || original[0].CID != in.CID {
		t.Fatalf("metadata differs: %+v", original[0])
	}
	tags, err := e.Tags(ctx, "GJN", []string{in.CID})
	if err != nil || len(tags) != 1 || tags[0].Provider != "customer" || tags[0].Source != "geo" || tags[0].Batch != "batch-1" {
		t.Fatalf("tags differ: %+v %v", tags, err)
	}
	if err = e.Close(ctx); err != nil {
		t.Fatal(err)
	}
	e = openReal(t, root, format4.OpenExisting, 0, nil)
	restored, err := e.Get(ctx, "GJN", []string{in.CID}, false, true)
	if err != nil || len(restored) != 1 || !bytes.Equal(restored[0].Data, plain) || !bytes.Equal(restored[0].Sig, in.Sig) {
		t.Fatalf("reopen: %+v %v", restored, err)
	}
	query("SELECT geo_contains(_data,2,2) FROM \"GJN@geo\"")
	copy := in
	copy.Sig = []byte{8, 9}
	copy.TS++
	_, err = e.Put(ctx, format4.Batch{Type: "GJN", Peer: "second-peer", Tags: []format4.Tag{{Provider: "second", Source: "archive", Batch: "batch-2"}, {Provider: "second", Source: "geo", Batch: "batch-3"}}, Records: []format4.In{copy}})
	if err != nil {
		t.Fatal(err)
	}
	checkProvenance := func() {
		t.Helper()
		d := query("SELECT p.provider,p.source,p.batch,p.peer,p.ts,p.sig FROM (SELECT DISTINCT _cid FROM GJN) r JOIN flatsql_provenance('GJN',r._cid) p ORDER BY p.provider,p.source,p.batch")
		if len(d.Rows) != 3 {
			t.Fatalf("provenance copies: %+v", d.Rows)
		}
		for i, want := range [][4]string{{"customer", "geo", "batch-1", "fixture-peer"}, {"second", "archive", "batch-2", "second-peer"}, {"second", "geo", "batch-3", "second-peer"}} {
			for j, text := range want {
				if string(d.Rows[i][j].B) != text {
					t.Fatalf("provenance row %d field %d: %+v", i, j, d.Rows[i])
				}
			}
			wantCopy := in
			if i > 0 {
				wantCopy = copy
			}
			if d.Rows[i][4].I != wantCopy.TS || !bytes.Equal(d.Rows[i][5].B, wantCopy.Sig) {
				t.Fatalf("provenance signature/timestamp row %d: %+v", i, d.Rows[i])
			}
		}
	}
	checkProvenance()
	if err = e.Close(ctx); err != nil {
		t.Fatal(err)
	}
	e = openReal(t, root, format4.OpenExisting, 0, nil)
	checkProvenance()
	if err = e.Close(ctx); err != nil {
		t.Fatal(err)
	}
	t.Log("same WASM artifact: binary ingest, QRP projection, GJN, original bytes, metadata, empty/populated reopen PASS")
}
