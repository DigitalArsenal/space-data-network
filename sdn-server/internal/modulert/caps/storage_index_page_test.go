package caps

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"testing"

	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/CAT"
	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/OCM"
	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/PIV"
	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/QRP"
	flatbuffers "github.com/google/flatbuffers/go"
	"github.com/spacedatanetwork/sdn-server/internal/datasync"
	"github.com/spacedatanetwork/sdn-server/internal/modulert"
	"github.com/spacedatanetwork/sdn-server/internal/sds"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
	"github.com/spacedatanetwork/sdn-server/internal/syncwire"
)

func indexSynthetic(schema string, n int) []byte {
	b := flatbuffers.NewBuilder(256)
	name := b.CreateString(fmt.Sprintf("SYNTHETIC-%08d", n))
	var root flatbuffers.UOffsetT
	if schema == "CAT.fbs" {
		CAT.CATStart(b)
		CAT.CATAddOBJECT_NAME(b, name)
		CAT.CATAddNORAD_CAT_ID(b, uint32(900000+n))
		root = CAT.CATEnd(b)
	} else {
		OCM.MetadataStart(b)
		OCM.MetadataAddOBJECT_NAME(b, name)
		meta := OCM.MetadataEnd(b)
		OCM.OCMStart(b)
		OCM.OCMAddMETADATA(b, meta)
		OCM.OCMAddSTATE_STEP_SIZE(b, float64(n+1))
		root = OCM.OCMEnd(b)
	}
	b.FinishWithFileIdentifier(root, []byte("$"+schema[:3]))
	return append([]byte(nil), b.FinishedBytes()...)
}

// indexTypedCall builds the real PIV wire request, including the raw QRP port.
func indexTypedCall(bridge *modulert.HostBridge, request []byte) *PIV.PIVResponse {
	b := flatbuffers.NewBuilder(len(request) + 256)
	op := b.CreateString("storage.index_page")
	port := b.CreateString("request")
	arena := b.CreateByteVector(request)
	PIV.TABStart(b)
	PIV.TABAddPortId(b, port)
	PIV.TABAddSize(b, uint32(len(request)))
	tab := PIV.TABEnd(b)
	PIV.PIVRequestStartInputsVector(b, 1)
	b.PrependUOffsetT(tab)
	inputs := b.EndVector(1)
	PIV.PIVRequestStart(b)
	PIV.PIVRequestAddMethodId(b, op)
	PIV.PIVRequestAddInputs(b, inputs)
	PIV.PIVRequestAddPayloadArena(b, arena)
	req := PIV.PIVRequestEnd(b)
	PIV.PIVStart(b)
	PIV.PIVAddRequest(b, req)
	PIV.FinishPIVBuffer(b, PIV.PIVEnd(b))
	out := bridge.DispatchTyped(b.FinishedBytes())
	return PIV.GetRootAsPIV(out, 0).Response(nil)
}

func indexRead(t *testing.T, bridge *modulert.HostBridge, fields syncwire.QRPFields) ([]byte, *QRP.QRP, [][]byte) {
	t.Helper()
	resp := indexTypedCall(bridge, syncwire.BuildQRP(fields))
	if resp == nil || resp.StatusCode() != 0 {
		t.Fatalf("typed page failed: %s", resp.ErrorMessage())
	}
	var stream []byte
	for i := 0; i < resp.OutputsLength(); i++ {
		var tab PIV.TAB
		resp.Outputs(&tab, i)
		if string(tab.PortId()) == "stream" {
			stream = resp.PayloadArenaBytes()[tab.Offset() : tab.Offset()+tab.Size()]
		}
	}
	if len(stream) < 12 {
		t.Fatal("missing page stream")
	}
	size := int(binary.LittleEndian.Uint32(stream))
	header, err := syncwire.ParseQRP(stream[:4+size])
	if err != nil {
		t.Fatal(err)
	}
	var records [][]byte
	for at := 4 + size; at < len(stream); {
		if at+4 > len(stream) {
			t.Fatal("truncated prefix")
		}
		n := int(binary.LittleEndian.Uint32(stream[at:]))
		at += 4
		if n == 0 {
			continue
		}
		if at+n > len(stream) {
			t.Fatal("truncated record")
		}
		records = append(records, stream[at:at+n])
		at += n
	}
	if int(header.RECORD_COUNT()) != len(records) {
		t.Fatal("page count mismatch")
	}
	return stream, header, records
}

func indexBridge(store *storage.FlatSQLStore, grants ...string) *modulert.HostBridge {
	b := modulert.NewHostBridge(nil, grants)
	b.RegisterCapHandler("storage", NewStorageCapFactory(store)(nil, b))
	return b
}

func TestStorageIndexPageCompleteSnapshots(t *testing.T) {
	t.Setenv("SDN_STORE_FORMAT", "4")
	for _, schema := range []string{"CAT.fbs", "OCM.fbs"} {
		for _, count := range []int{10001, 33000} {
			t.Run(fmt.Sprintf("%s/%d", schema, count), func(t *testing.T) {
				validator, e := sds.NewValidator(nil)
				if e != nil {
					t.Fatal(e)
				}
				store, e := storage.NewFlatSQLStore(t.TempDir(), validator)
				if e != nil {
					t.Fatal(e)
				}
				defer store.Close()
				records := make([][]byte, count)
				expected := make(map[[32]byte]int, count)
				for i := range records {
					records[i] = indexSynthetic(schema, i)
					expected[sha256.Sum256(records[i])] = i
				}
				tags := storage.SourceTags{ProviderID: "synthetic-provider", SourceName: "synthetic-source", BatchID: "initial"}
				n, e := store.StoreBatchWithSourceTags(schema, records, "synthetic", nil, tags)
				if e != nil || n != count {
					t.Fatalf("seed %d: %v", n, e)
				}
				bridge := indexBridge(store, "storage_query")
				request := syncwire.QRPFields{Kind: syncwire.QRPKindRequest, SchemaName: schema, ProviderID: tags.ProviderID, SourceName: tags.SourceName, Limit: 997, MaxBytes: 1 << 20}
				seen := make(map[[32]byte]bool, count)
				last := -1
				pages := 0
				inserted := false
				var pinned int64
				for {
					stream, h, rows := indexRead(t, bridge, request)
					pages++
					if len(stream) > int(request.MaxBytes) || len(rows) > int(request.Limit) {
						t.Fatal("page bound exceeded")
					}
					if int(h.TOTAL_COUNT()) != count {
						t.Fatalf("snapshot total %d, want %d", h.TOTAL_COUNT(), count)
					}
					c, ok, e := datasync.ParseRawRecordCursor(string(h.CURSOR()))
					if e != nil || !ok {
						t.Fatal("missing cursor")
					}
					if pinned == 0 {
						pinned = c.MaxRowID
					}
					if pinned != c.MaxRowID {
						t.Fatal("snapshot changed")
					}
					for _, raw := range rows {
						digest := sha256.Sum256(raw)
						index, ok := expected[digest]
						if !ok || seen[digest] || index <= last {
							t.Fatal("lost order, duplicate, unexpected bytes or post-snapshot record")
						}
						seen[digest] = true
						last = index
					}
					if !inserted {
						// New records arrive after the first page pins the head, on a separate ingestion goroutine.
						done := make(chan error, 1)
						go func() {
							extra := make([][]byte, 37)
							for i := range extra {
								extra[i] = indexSynthetic(schema, count+i)
							}
							_, err := store.StoreBatchWithSourceTags(schema, extra, "synthetic", nil, tags)
							done <- err
						}()
						if e := <-done; e != nil {
							t.Fatal(e)
						}
						inserted = true
					}
					request.Cursor = string(h.NEXT_CURSOR())
					if request.Cursor == "" {
						break
					}
					if pages > count {
						t.Fatal("non-progressing cursor")
					}
				}
				if len(seen) != count {
					t.Fatalf("complete walk %d/%d", len(seen), count)
				}
				request.Cursor = ""
				_, h, _ := indexRead(t, bridge, request)
				if int(h.TOTAL_COUNT()) != count+37 {
					t.Fatal("new snapshot omitted new ingestion")
				}
				t.Logf("synthetic complete walk: records=%d pages=%d concurrent_additions=37 bytes_exact=true", count, pages)
			})
		}
	}
}

func TestStorageIndexPageFiltersBoundsAndGrants(t *testing.T) {
	t.Setenv("SDN_STORE_FORMAT", "4")
	store := newCapTestStore(t)
	bridge := indexBridge(store, "storage_query")
	records := [][]byte{indexSynthetic("CAT.fbs", 1), indexSynthetic("CAT.fbs", 2), indexSynthetic("CAT.fbs", 3)}
	for i, tag := range []storage.SourceTags{{ProviderID: "a", SourceName: "x", BatchID: "b"}, {ProviderID: "a", SourceName: "y", BatchID: "b"}, {ProviderID: "z", SourceName: "x", BatchID: "c"}} {
		if _, e := store.StoreWithSourceTags("CAT.fbs", records[i], "synthetic", nil, tag); e != nil {
			t.Fatal(e)
		}
	}
	req := syncwire.QRPFields{Kind: syncwire.QRPKindRequest, SchemaName: "CAT.fbs", Limit: 1}
	for _, tc := range []struct {
		p, s, b string
		want    int
	}{{"", "", "", 3}, {"a", "", "", 2}, {"", "x", "", 2}, {"a", "x", "b", 1}, {"z", "x", "b", 0}} {
		q := req
		q.ProviderID, q.SourceName, q.BatchID = tc.p, tc.s, tc.b
		_, h, _ := indexRead(t, bridge, q)
		if int(h.TOTAL_COUNT()) != tc.want {
			t.Fatal("filter mismatch")
		}
	}
	_, first, _ := indexRead(t, bridge, req)
	cursor := string(first.NEXT_CURSOR())
	reject := func(b *modulert.HostBridge, q syncwire.QRPFields) {
		t.Helper()
		r := indexTypedCall(b, syncwire.BuildQRP(q))
		if r.StatusCode() == 0 {
			t.Fatal("invalid request accepted")
		}
	}
	for _, grants := range [][]string{nil, {"storage_write"}, {"storage_ingest"}, {"storage_adapter"}} {
		reject(indexBridge(store, grants...), req)
	}
	bad := req
	bad.Cursor = "bad"
	reject(bridge, bad)
	bad.Cursor = cursor
	bad.SourceName = "x"
	reject(bridge, bad)
	bad = req
	bad.Limit = 1001
	reject(bridge, bad)
	bad = req
	bad.MaxBytes = indexPageMaxBytes + 1
	reject(bridge, bad)
	bad.MaxBytes = 1
	reject(bridge, bad)
	bad = req
	bad.Search = "ignored?"
	reject(bridge, bad)
	bad = req
	bad.Page = 2
	reject(bridge, bad)
	// Exact byte bound: force truncation after one record, then walk the remainder.
	one, h, rows := indexRead(t, bridge, req)
	if !bytes.Equal(rows[0], records[0]) {
		t.Fatal("record bytes changed")
	}
	q := req
	q.Limit = 3
	q.MaxBytes = uint64(len(one))
	_, h, rows = indexRead(t, bridge, q)
	if len(rows) != 1 || len(h.NEXT_CURSOR()) == 0 {
		t.Fatal("byte bound did not stop after one record")
	}
	q.Cursor = string(h.NEXT_CURSOR())
	_, _, rows = indexRead(t, bridge, q)
	if len(rows) == 0 || !bytes.Equal(rows[0], records[1]) {
		t.Fatal("byte-bound resume skipped a record")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	bridge.SetInvocationContext(ctx)
	reject(bridge, req)
}

// A bare empty table's root offset can equal len(buffer)-4. Length alone
// does not identify a size prefix; the SDS identifier distinguishes it.
func TestStorageIndexPageSmallRecordFraming(t *testing.T) {
	b := flatbuffers.NewBuilder(32)
	CAT.CATStart(b)
	root := CAT.CATEnd(b)
	b.FinishWithFileIdentifier(root, []byte("$CAT"))
	raw := b.FinishedBytes()
	framed := indexRecordFrame(raw)
	if len(framed) != len(raw)+4 || !bytes.Equal(framed[4:], raw) {
		t.Fatal("bare record mistaken for a prefixed frame")
	}
	if !bytes.Equal(indexRecordFrame(framed), framed) {
		t.Fatal("size-prefixed record changed")
	}
}
