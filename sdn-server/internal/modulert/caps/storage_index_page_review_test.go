package caps

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	logging "github.com/ipfs/go-log/v2"
	"github.com/spacedatanetwork/sdn-server/internal/encfield"
	"github.com/spacedatanetwork/sdn-server/internal/modulert"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
	"github.com/spacedatanetwork/sdn-server/internal/syncwire"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

type indexReviewReader struct {
	*storage.FlatSQLStore
	afterIndex func(storage.RecordIndexSeqPage)
}

func (s indexReviewReader) RecordIndexSeqPage(q storage.RecordIndexSeqQuery) (storage.RecordIndexSeqPage, error) {
	if s.FlatSQLStore == nil {
		panic("controlled index failure")
	}
	p, err := s.FlatSQLStore.RecordIndexSeqPage(q)
	if err == nil && s.afterIndex != nil {
		s.afterIndex(p)
	}
	return p, err
}

func TestStorageIndexPageRefusesFieldSealedSchemas(t *testing.T) {
	// A reader that panics proves refusal happens before any index or record read.
	for _, schema := range []string{"KMF.fbs", "KMF", "$KMF"} {
		bridge := modulert.NewHostBridge(nil, []string{"storage_query"})
		adapter := &storageCapAdapter{bridge: bridge}
		answer := adapter.indexPageWithReader(syncwire.BuildQRP(syncwire.QRPFields{Kind: syncwire.QRPKindRequest, SchemaName: schema}), indexReviewReader{})
		envelope, segments := decodeCapResponse(t, answer)
		if envelope["ok"] == true || len(segments) != 0 || !bytes.Contains(answer, []byte("field-sealed")) {
			t.Fatalf("sealed schema was not refused: %s", answer)
		}
	}
	// The policy follows the field registry rather than a hard-coded KMF list.
	old := encfield.EncryptedFields("PNM")
	encfield.RegisterSchema("PNM", []encfield.FieldSpec{{Name: "SIGNATURE", FieldID: 5}})
	defer encfield.RegisterSchema("PNM", old)
	response := indexTypedCall(indexBridge(nil, "storage_query"), syncwire.BuildQRP(syncwire.QRPFields{Kind: syncwire.QRPKindRequest, SchemaName: "PNM.fbs"}))
	if response.StatusCode() == 0 || response.OutputsLength() != 0 || !strings.Contains(string(response.ErrorMessage()), "field-sealed") {
		t.Fatal("registered sealed schema exposed a result")
	}
}

func TestStorageIndexPageMatchesSQLReadGrant(t *testing.T) {
	store := newCapTestStore(t)
	result, segments := callOp(t, store, "storage.flatsql_query_stream", map[string]interface{}{"sql": `SELECT _data FROM "OMM@provider-gp"`})
	sqlRecords := decodeStreamFrames(t, result, segments)
	_, _, pageRecords := indexRead(t, indexBridge(store, "storage_query"), syncwire.QRPFields{Kind: syncwire.QRPKindRequest, SchemaName: "OMM.fbs", ProviderID: "prov", SourceName: "provider-gp"})
	if len(sqlRecords) != len(pageRecords) {
		t.Fatalf("SQL %d index %d", len(sqlRecords), len(pageRecords))
	}
	expected := map[string]bool{}
	for _, row := range sqlRecords {
		expected[string(row)] = true
	}
	for _, row := range pageRecords {
		if !expected[string(row)] {
			t.Fatal("index returned bytes outside SQL result")
		}
	}
	// The existing SQL operation excludes field-sealed schemas as well.
	handler := NewStorageCapFactory(store)(nil, modulert.NewHostBridge(nil, []string{"storage_query"}))
	raw, err := handler("storage.flatsql_query_stream", []byte(`{"sql":"SELECT _data FROM KMF"}`))
	if err != nil {
		t.Fatal(err)
	}
	envelope, parts := decodeCapResponse(t, raw)
	if envelope["ok"] == true {
		result := envelope["result"].(map[string]interface{})
		if len(decodeStreamFrames(t, result, parts)) != 0 {
			t.Fatal("sealed schema exposed SQL records")
		}
	}
}

func TestStorageIndexPageLogsPanic(t *testing.T) {
	config := logging.GetConfig()
	defer logging.SetupLogging(config)
	var output bytes.Buffer
	logging.SetPrimaryCore(zapcore.NewCore(zapcore.NewConsoleEncoder(zap.NewDevelopmentEncoderConfig()), zapcore.AddSync(&output), zapcore.DebugLevel))
	adapter := &storageCapAdapter{store: &storage.FlatSQLStore{}, bridge: modulert.NewHostBridge(nil, []string{"storage_query"})}
	answer := adapter.indexPageWithReader(syncwire.BuildQRP(syncwire.QRPFields{Kind: syncwire.QRPKindRequest, SchemaName: "CAT.fbs"}), indexReviewReader{})
	if !bytes.Contains(answer, []byte("invalid QRP page request")) {
		t.Fatal("panic response changed")
	}
	for _, want := range []string{"controlled index failure", "goroutine", "storage_index_page.go"} {
		if !strings.Contains(output.String(), want) {
			t.Fatalf("panic log lacks %q", want)
		}
	}
	if bytes.Contains(answer, []byte("controlled index failure")) {
		t.Fatal("internal panic exposed to caller")
	}
}

func TestStorageIndexPageRetryAfterDelete(t *testing.T) {
	store := newCapTestStore(t)
	var records [][]byte
	for i := 0; i < 8; i++ {
		records = append(records, indexSynthetic("CAT.fbs", i))
	}
	if n, err := store.StoreBatchWithSourceTags("CAT.fbs", records, "synthetic", nil, storage.SourceTags{ProviderID: "delete-proof", SourceName: "source"}); err != nil || n != 8 {
		t.Fatalf("seed: %d %v", n, err)
	}
	bridge := indexBridge(store, "storage_query")
	request := syncwire.QRPFields{Kind: syncwire.QRPKindRequest, SchemaName: "CAT.fbs", ProviderID: "delete-proof", Limit: 2}
	_, header, first := indexRead(t, bridge, request)
	request.Cursor = string(header.NEXT_CURSOR())
	seen := map[string]bool{}
	for _, row := range first {
		seen[string(row)] = true
	}
	var deleted string
	reader := indexReviewReader{FlatSQLStore: store, afterIndex: func(p storage.RecordIndexSeqPage) {
		if len(p.Rows) == 0 {
			t.Fatal("no row to delete")
		}
		deleted = p.Rows[0].CID
		if err := store.Delete("CAT.fbs", deleted); err != nil {
			t.Fatal(err)
		}
	}}
	adapter := &storageCapAdapter{store: store, bridge: bridge}
	failed := adapter.indexPageWithReader(syncwire.BuildQRP(request), reader)
	envelope, segments := decodeCapResponse(t, failed)
	if envelope["ok"] == true || len(segments) != 0 || !bytes.Contains(failed, []byte("snapshot record unavailable")) {
		t.Fatal("partial page escaped after deletion")
	}
	// Retain the preceding successful page's NEXT_CURSOR; failed pages advance nothing.
	for {
		_, h, rows := indexRead(t, bridge, request)
		for _, row := range rows {
			if seen[string(row)] {
				t.Fatal("duplicate after retry")
			}
			seen[string(row)] = true
		}
		if len(h.NEXT_CURSOR()) == 0 {
			break
		}
		request.Cursor = string(h.NEXT_CURSOR())
	}
	if len(seen) != 7 {
		t.Fatalf("survivors %d want 7", len(seen))
	}
	for _, row := range records {
		if storage.ComputeCID(row) == deleted {
			if seen[string(row)] {
				t.Fatal("deleted record returned")
			}
			continue
		}
		if !seen[string(row)] {
			t.Fatal("gap after retry")
		}
	}
	if _, err := store.Get("CAT.fbs", deleted); err == nil {
		t.Fatal(errors.New("deletion control failed"))
	}
}
