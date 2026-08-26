package caps

import (
	"encoding/base64"
	"encoding/json"
	"testing"

	flatbuffers "github.com/google/flatbuffers/go"

	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/IRM"

	"github.com/spacedatanetwork/sdn-server/internal/modulert"
	"github.com/spacedatanetwork/sdn-server/internal/sds"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
)

// THE ADAPTER PATH IS THE ONE THAT BROKE.
//
// A module reading a standard this node has never stored must get ZERO ROWS,
// not an error: an error there is indistinguishable from a real failure, and
// that is exactly how the cellular ingest flow turned its FIRST run into a
// fault envelope — the 400 the module reported came from
// storage.flatsql_query_stream, not from the store. The storage-package tests
// cover the store's own surface; nothing drove the capability adapter that
// production actually calls, with production's SQL shape and tagged
// parameters.

func newEmptyCapStore(t *testing.T) *storage.FlatSQLStore {
	t.Helper()
	validator, err := sds.NewValidator(nil)
	if err != nil {
		t.Fatalf("validator: %v", err)
	}
	store, err := storage.NewFlatSQLStore(t.TempDir(), validator)
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

// buildCapIRM is one $IRM ingest resume mark, built with the vendored flatc
// binding — the same shape the ingest flow writes.
func buildCapIRM(t *testing.T, jobID string, sequence, nextOffset uint64) []byte {
	t.Helper()
	b := flatbuffers.NewBuilder(512)
	sourceURL := b.CreateString("https://example.invalid/bulk/catalog.csv")
	IRM.IRMSourceStart(b)
	IRM.IRMSourceAddSOURCE_URL(b, sourceURL)
	source := IRM.IRMSourceEnd(b)

	job := b.CreateString(jobID)
	provider := b.CreateString("mls")
	updated := b.CreateString("2026-08-25T00:00:00.000Z")
	IRM.IRMStart(b)
	IRM.IRMAddJOB_ID(b, job)
	IRM.IRMAddPROVIDER_ID(b, provider)
	IRM.IRMAddSOURCE(b, source)
	IRM.IRMAddSEQUENCE(b, sequence)
	IRM.IRMAddNEXT_OFFSET(b, nextOffset)
	IRM.IRMAddUPDATED_AT(b, updated)
	IRM.FinishIRMBuffer(b, IRM.IRMEnd(b))
	return b.FinishedBytes()
}

// TestFlatSQLQueryStreamAnswersEmptyForAnUnwrittenStandard drives the exact
// hostcall the ingest module makes, on a store that has never seen an $IRM.
func TestFlatSQLQueryStreamAnswersEmptyForAnUnwrittenStandard(t *testing.T) {
	store := newEmptyCapStore(t)
	result, segments := callOp(t, store, "storage.flatsql_query_stream", map[string]interface{}{
		"sql":    `SELECT _data FROM IRM ORDER BY _rowid DESC LIMIT ?`,
		"params": []map[string]interface{}{{"t": "i64", "v": 1}},
	})
	if rows, _ := result["rows"].(float64); rows != 0 {
		t.Fatalf("rows = %v on a fresh store, want 0", result["rows"])
	}
	if frames := decodeStreamFrames(t, result, segments); len(frames) != 0 {
		t.Fatalf("%d frames on a fresh store, want an empty stream", len(frames))
	}
}

// TestFlatSQLQueryStreamReturnsAMarkWrittenThroughTheCap is the other half:
// the mark a module WRITES through storage.write comes back through the same
// read op, in the wire format the module decodes.
func TestFlatSQLQueryStreamReturnsAMarkWrittenThroughTheCap(t *testing.T) {
	store := newEmptyCapStore(t)
	record := buildCapIRM(t, "cellular-worldwide", 1, 4096)

	handler := NewStorageCapFactory(store)(nil,
		modulert.NewHostBridge(nil, []string{"storage_query", "storage_write"}))
	body, _ := json.Marshal(map[string]interface{}{
		"schema": "IRM.fbs",
		"data":   base64.StdEncoding.EncodeToString(record),
	})
	resp, err := handler("storage.write", body)
	if err != nil {
		t.Fatalf("storage.write: %v", err)
	}
	envelope, _ := decodeCapResponse(t, resp)
	if ok, _ := envelope["ok"].(bool); !ok {
		t.Fatalf("storage.write refused the mark: %v", envelope["error"])
	}

	result, segments := callOp(t, store, "storage.flatsql_query_stream", map[string]interface{}{
		"sql":    `SELECT _data FROM IRM ORDER BY _rowid DESC LIMIT ?`,
		"params": []map[string]interface{}{{"t": "i64", "v": 1}},
	})
	if rows, _ := result["rows"].(float64); rows != 1 {
		t.Fatalf("rows = %v after one $IRM, want 1", result["rows"])
	}
	frames := decodeStreamFrames(t, result, segments)
	if len(frames) != 1 {
		t.Fatalf("%d frames after one $IRM, want 1", len(frames))
	}
	if !IRM.IRMBufferHasIdentifier(frames[0]) {
		t.Fatal("the frame is not a $IRM buffer")
	}
	mark := IRM.GetRootAsIRM(frames[0], 0)
	if got := string(mark.JOB_ID()); got != "cellular-worldwide" {
		t.Fatalf("JOB_ID = %q, want the mark that was written", got)
	}
	if mark.SEQUENCE() != 1 || mark.NEXT_OFFSET() != 4096 {
		t.Fatalf("mark came back as sequence=%d next_offset=%d, want 1/4096",
			mark.SEQUENCE(), mark.NEXT_OFFSET())
	}
}
