package storage

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/spacedatanetwork/sdn-server/internal/sds"
)

// THE REGRESSION BAR: wire == disk == query, byte for byte.
//
// This is the Go-host restatement of the check that proved parse-free
// streaming in flatsql-abi-stream-contract-read (sha256(wire) ==
// sha256(disk) == sha256(query) over real $OMM records). It is kept here, in
// the store, because it is the property every routing and sharding change must
// preserve: the host may decide WHERE a record goes, and how many records
// share a shard, but it may never touch the bytes.
//
// A failure here means something on the ingest or read path started decoding
// and re-encoding records — the exact defect the streaming contract forbids.
func sha256Hexs(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// frame renders one record the way the stream file holds it: a little-endian
// uint32 length followed by the stored bytes.
func frame(data []byte) []byte {
	out := make([]byte, 4, 4+len(data))
	binary.LittleEndian.PutUint32(out, uint32(len(data)))
	return append(out, data...)
}

func TestWireDiskQueryByteIdentity(t *testing.T) {
	basePath := filepath.Join(t.TempDir(), "store")
	store := newFixtureStore(t, basePath)
	defer store.Close()

	const records = 300
	var wire []byte
	for i := 0; i < records; i++ {
		data := sds.NewOMMBuilder().
			WithObjectName(fmt.Sprintf("IDENTITY-%05d", i)).
			WithNoradCatID(uint32(70000 + i)).
			WithEpoch("2026-08-07T00:00:00.000Z").
			Build()
		wire = append(wire, frame(data)...)
		if _, err := store.Store("OMM.fbs", data, "12D3KooWIdentity", nil); err != nil {
			t.Fatalf("store record %d: %v", i, err)
		}
	}
	wireSHA := sha256Hexs(wire)

	// QUERY: read every record back in index-rowid order — the order the
	// bytes were written, which is the order the wire produced them — and
	// re-frame it exactly as the wire does.
	got, err := store.QueryRawRecords(RawRecordQuery{SchemaName: "OMM.fbs", UseRowIDCursor: true, Limit: records})
	if err != nil {
		t.Fatalf("QueryRawRecords: %v", err)
	}
	if len(got) != records {
		t.Fatalf("query returned %d records, want %d", len(got), records)
	}
	var queryStream []byte
	for _, record := range got {
		queryStream = append(queryStream, frame(record.Data)...)
	}
	querySHA := sha256Hexs(queryStream)

	// DISK: the stored rows themselves, read straight out of the producer
	// table in index-rowid order, untouched by any read path.
	readSource, err := store.recordReadSource("OMM.fbs")
	if err != nil {
		t.Fatalf("read source: %v", err)
	}
	rows, err := store.db.Query(`SELECT rr.data FROM sdn_record_index idx JOIN ` + readSource + ` rr ON rr.cid = idx.cid WHERE idx.schema_name = 'OMM.fbs' ORDER BY idx.rowid ASC`)
	if err != nil {
		t.Fatalf("read stored rows: %v", err)
	}
	var diskBytes []byte
	for rows.Next() {
		var data []byte
		if err := rows.Scan(&data); err != nil {
			rows.Close()
			t.Fatalf("scan stored row: %v", err)
		}
		diskBytes = append(diskBytes, frame(data)...)
	}
	rows.Close()
	diskSHA := sha256Hexs(diskBytes)

	if wireSHA != diskSHA || diskSHA != querySHA {
		t.Fatalf("BYTE IDENTITY BROKEN\n  wire  %s (%d B)\n  disk  %s (%d B)\n  query %s (%d B)",
			wireSHA, len(wire), diskSHA, len(diskBytes), querySHA, len(queryStream))
	}
	t.Logf("wire == disk == query : %s (%d records, %d bytes)", wireSHA, records, len(wire))
}

// The same identity must survive a byte-budgeted shard cut: sharding decides
// how many records travel together, never what they contain.
func TestShardByteBudgetPreservesRecordBytes(t *testing.T) {
	basePath := filepath.Join(t.TempDir(), "store")
	store := newFixtureStore(t, basePath)
	defer store.Close()

	const records = 32
	frameSize := storeRealOMMs(t, store, records)

	filter := IndexedRecordQuery{SchemaName: "OMM.fbs", Limit: records, AllowLargeResultSet: true, OrderByCID: true}
	whole, err := store.QueryIndexedRecords(filter)
	if err != nil {
		t.Fatalf("QueryIndexedRecords: %v", err)
	}

	// Walk the same records in byte-budgeted windows and concatenate.
	var cut []byte
	offset := 0
	for offset < records {
		windowFilter := filter
		windowFilter.Offset = offset
		windowFilter.Limit = records - offset
		bounded, _, err := store.IndexedRecordWindowLimitForBytes(windowFilter, frameSize*5)
		if err != nil {
			t.Fatalf("IndexedRecordWindowLimitForBytes: %v", err)
		}
		if bounded <= 0 {
			t.Fatalf("byte budget admitted %d records at offset %d", bounded, offset)
		}
		windowFilter.Limit = bounded
		window, err := store.QueryIndexedRecords(windowFilter)
		if err != nil {
			t.Fatalf("QueryIndexedRecords window at %d: %v", offset, err)
		}
		if len(window) != bounded {
			t.Fatalf("window at %d returned %d records, probe said %d", offset, len(window), bounded)
		}
		for _, record := range window {
			cut = append(cut, frame(record.Data)...)
		}
		offset += bounded
	}

	var straight []byte
	for _, record := range whole {
		straight = append(straight, frame(record.Data)...)
	}
	if sha256Hexs(cut) != sha256Hexs(straight) {
		t.Fatal("byte-budgeted shard windows do not reassemble to the same bytes as one window")
	}
}
