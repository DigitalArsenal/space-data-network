package api

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	flatbuffers "github.com/google/flatbuffers/go"

	"github.com/spacedatanetwork/sdn-server/internal/typedfb/ingestresult"
)

func ingestResultBody(schema, provider, source, batch, refused string) []byte {
	builder := flatbuffers.NewBuilder(128)
	schemaOff := builder.CreateString(schema)
	providerOff := builder.CreateString(provider)
	sourceOff := builder.CreateString(source)
	batchOff := builder.CreateString(batch)
	refusedOff := builder.CreateString(refused)
	ingestresult.IngestResultStart(builder)
	ingestresult.IngestResultAddSchema(builder, schemaOff)
	ingestresult.IngestResultAddProviderId(builder, providerOff)
	ingestresult.IngestResultAddSourceName(builder, sourceOff)
	ingestresult.IngestResultAddBatchId(builder, batchOff)
	ingestresult.IngestResultAddInserted(builder, 12)
	if refused != "" {
		ingestresult.IngestResultAddRefused(builder, refusedOff)
	}
	ingestresult.FinishIngestResultBuffer(builder, ingestresult.IngestResultEnd(builder))
	return builder.FinishedBytes()
}

func postIngestResult(t *testing.T, service *fakeDatasetPublicationService, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	mux := http.NewServeMux()
	NewDatasetPublicationHandler(service).RegisterRoutes(mux)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/dataset-updates/publish", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/octet-stream")
	req.RemoteAddr = "127.0.0.1:4321"
	res := httptest.NewRecorder()
	mux.ServeHTTP(res, req)
	return res
}

// A typed ingest flow asks for its batch's publication with the IngestResult
// its storage hostcall answered (cellular-network-ingest's publication node).
func TestDatasetPublicationHandlerPublishesTheBatchAnIngestResultNames(t *testing.T) {
	service := &fakeDatasetPublicationService{result: DatasetPublicationResult{Schema: "TBS.fbs", RecordCount: 12}}
	res := postIngestResult(t, service, ingestResultBody("TBS.fbs", "opencellid", "cell-tower-bulk", "opencellid@0", ""))
	if res.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want %d body=%s", res.Code, http.StatusAccepted, res.Body.String())
	}
	got := service.request
	if got.Schema != "TBS.fbs" || got.ProviderID != "opencellid" || got.SourceName != "cell-tower-bulk" || got.BatchID != "opencellid@0" {
		t.Fatalf("unexpected request: %#v", got)
	}
}

func TestDatasetPublicationHandlerRefusesARefusedOrMalformedIngestResult(t *testing.T) {
	service := &fakeDatasetPublicationService{}
	res := postIngestResult(t, service, ingestResultBody("TBS.fbs", "opencellid", "cell-tower-bulk", "opencellid@0", "schema mismatch"))
	if res.Code != http.StatusBadRequest || service.called || !strings.Contains(res.Body.String(), "refused") {
		t.Fatalf("a refused batch: status %d called %v body %s", res.Code, service.called, res.Body.String())
	}

	truncated := ingestResultBody("TBS.fbs", "opencellid", "cell-tower-bulk", "opencellid@0", "")[:12]
	res = postIngestResult(t, service, truncated)
	if res.Code != http.StatusBadRequest || service.called {
		t.Fatalf("a truncated IngestResult: status %d called %v body %s", res.Code, service.called, res.Body.String())
	}
}
