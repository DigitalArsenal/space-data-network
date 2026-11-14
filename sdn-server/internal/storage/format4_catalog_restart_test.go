package storage

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/OCM"
	flatbuffers "github.com/google/flatbuffers/go"
	"testing"
)

func TestFormat4CatalogBoundRestart(t *testing.T) {
	onFormat4Engine(t, func(t *testing.T) {
		root := t.TempDir()
		store := openFormat4ForTest(t, root)
		records := make([][]byte, 20000)
		want := make(map[string]bool, len(records))
		for i := range records {
			b := flatbuffers.NewBuilder(128)
			desc := b.CreateString(fmt.Sprintf("synthetic-catalog-%05d", i))
			OCM.OCMStart(b)
			OCM.OCMAddTRAJ_TYPE_DESCRIPTION(b, desc)
			record := OCM.OCMEnd(b)
			OCM.FinishOCMBuffer(b, record)
			records[i] = append([]byte(nil), b.FinishedBytes()...)
			want[string(records[i])] = true
		}
		tags := SourceTags{ProviderID: "synthetic", SourceName: "catalog", BatchID: "one"}
		for at := 0; at < len(records); at += 1000 {
			n, err := store.StoreBatchWithSourceTags("OCM.fbs", records[at:at+1000], "source:catalog", nil, tags)
			if err != nil || n != 1000 {
				t.Fatalf("ingest %d: %d %v", at, n, err)
			}
		}
		for pass := 0; pass < 2; pass++ {
			result, err := store.QueryRawStream(`SELECT _data FROM "OCM@catalog"`)
			if err != nil {
				t.Fatal(err)
			}
			if result.FrameCount != len(records) {
				t.Fatalf("pass %d count=%d", pass, result.FrameCount)
			}
			seen := map[string]bool{}
			for b := result.Bytes; len(b) > 0; {
				if len(b) < 4 {
					t.Fatal("short frame")
				}
				n := int(binary.LittleEndian.Uint32(b))
				b = b[4:]
				if n > len(b) || !want[string(b[:n])] || seen[string(b[:n])] {
					t.Fatal("unexpected or repeated frame")
				}
				seen[string(b[:n])] = true
				b = b[n:]
			}
			if len(seen) != len(want) {
				t.Fatal("missing records")
			}
			for _, i := range []int{0, 9999, 19999} {
				r, err := store.GetRecord("OCM.fbs", ComputeCID(records[i]))
				if err != nil || !bytes.Equal(r.Data, records[i]) {
					t.Fatalf("record %d: %v", i, err)
				}
			}
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}
			if pass == 0 {
				store = openFormat4ForTest(t, root)
			}
		}
	})
}
