package protocol

import (
	"bytes"
	"testing"

	"github.com/spacedatanetwork/sdn-server/internal/sds"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
)

func TestFlatSQLSyncReadChunkSourceNameOnlyFilterReturnsAbsentAndPresentRows(t *testing.T) {
	store := newFlatSQLSyncTestStore(t)
	wantPayloads := [][]byte{
		storeFlatSQLSyncTestOMM(t, store, 73001, "SOURCE-ALPHA-1"),
		storeFlatSQLSyncTestOMM(t, store, 73002, "SOURCE-ALPHA-2"),
	}
	beta := sds.NewOMMBuilder().
		WithNoradCatID(74001).
		WithObjectID("2026-74001").
		WithObjectName("SOURCE-BETA-1").
		WithEpoch("2026-09-10T18:30:00Z").
		Build()
	if _, err := store.StoreWithSourceTags("OMM.fbs", beta, "source:filter-test", nil, storage.SourceTags{
		ProviderID: "provider-beta",
		SourceName: "source-beta",
		BatchID:    "batch-beta",
	}); err != nil {
		t.Fatalf("store beta record: %v", err)
	}

	handler := NewFlatSQLSyncHandler(store)
	var absentOut bytes.Buffer
	if err := handler.handleReadChunk(&absentOut, flatSQLSyncRequest{
		Op:         "read_chunk",
		Schema:     "OMM.fbs",
		SourceName: "source-absent",
		Limit:      73,
	}); err != nil {
		t.Fatalf("absent source handleReadChunk: %v", err)
	}
	absentHeader := readSourceFilterSyncHeader(t, &absentOut)
	if absentHeader.TotalCount != 0 || absentHeader.Count != 0 || len(absentHeader.Results) != 0 || absentOut.Len() != 0 {
		t.Fatalf("absent source returned total=%d count=%d results=%d trailing_bytes=%d",
			absentHeader.TotalCount, absentHeader.Count, len(absentHeader.Results), absentOut.Len())
	}

	var presentOut bytes.Buffer
	if err := handler.handleReadChunk(&presentOut, flatSQLSyncRequest{
		Op:         "read_chunk",
		Schema:     "OMM.fbs",
		SourceName: "celestrak-gp",
		Limit:      73,
	}); err != nil {
		t.Fatalf("present source handleReadChunk: %v", err)
	}
	presentHeader := readSourceFilterSyncHeader(t, &presentOut)
	if presentHeader.TotalCount != 2 || presentHeader.Count != 2 || len(presentHeader.Results) != 2 {
		t.Fatalf("present source returned total=%d count=%d results=%d; want 2",
			presentHeader.TotalCount, presentHeader.Count, len(presentHeader.Results))
	}
	gotPayloads := readFlatSQLSyncTestRawFrames(t, &presentOut)
	if len(gotPayloads) != len(wantPayloads) {
		t.Fatalf("present source returned %d payloads; want %d", len(gotPayloads), len(wantPayloads))
	}
	for i := range wantPayloads {
		if !bytes.Equal(gotPayloads[i], wantPayloads[i]) {
			t.Fatalf("present source payload %d does not match", i)
		}
	}
}

type sourceFilterSyncHeader struct {
	TotalCount int64 `json:"total_count"`
	Count      int   `json:"count"`
	Results    []struct {
		SourceName string `json:"source_name"`
	} `json:"results"`
}

func readSourceFilterSyncHeader(t *testing.T, reader *bytes.Buffer) sourceFilterSyncHeader {
	t.Helper()
	var header sourceFilterSyncHeader
	readFlatSQLSyncTestJSONFrame(t, reader, &header)
	return header
}
