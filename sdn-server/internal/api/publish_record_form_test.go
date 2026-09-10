package api

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/spacedatanetwork/sdn-server/internal/sds"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
)

// sizePrefixedCopy wraps a bare finished FlatBuffer behind its own u32 LE
// length, exactly what FinishSizePrefixed<X>Buffer emits.
func sizePrefixedCopy(rec []byte) []byte {
	out := make([]byte, 4+len(rec))
	binary.LittleEndian.PutUint32(out, uint32(len(rec)))
	copy(out[4:], rec)
	return out
}

// bareOMM returns the canonical (bare) form of the package's test OMM, whose
// builder finishes size-prefixed.
func bareOMM(t *testing.T) []byte {
	t.Helper()
	prefixed := buildMinimalOMM(t)
	if string(prefixed[8:12]) != "$OMM" {
		t.Fatalf("test OMM builder no longer finishes size-prefixed (bytes 8..12 = %q)", prefixed[8:12])
	}
	return prefixed[4:]
}

func TestDetectEnvelopeForm(t *testing.T) {
	_, _, validator := newDataAPITestStoreWithBasePath(t)
	bare := bareOMM(t)
	if form, err := validator.DetectEnvelopeForm("OMM.fbs", bare); err != nil || form != sds.EnvelopeBare {
		t.Fatalf("bare record: form=%q err=%v", form, err)
	}
	if form, err := validator.DetectEnvelopeForm("OMM.fbs", sizePrefixedCopy(bare)); err != nil || form != sds.EnvelopeSizePrefixed {
		t.Fatalf("size-prefixed record: form=%q err=%v", form, err)
	}
	if _, err := validator.DetectEnvelopeForm("OMM.fbs", sizePrefixedCopy(sizePrefixedCopy(bare))); err == nil {
		t.Fatal("a doubly prefixed buffer must not classify as either form")
	}
	if _, err := validator.DetectEnvelopeForm("OMM.fbs", []byte("JUNKJUNKJUNKJUNK")); err == nil {
		t.Fatal("junk must not classify as either form")
	}
}

func TestPublishNormalizesSizePrefixedRecord(t *testing.T) {
	mux, store := newLocalLaneTestMux(t)
	bare := bareOMM(t)
	wantCID := storage.ComputeCID(bare)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, localLaneRequest(http.MethodPost, "/api/v1/data/publish/OMM.fbs", sizePrefixedCopy(bare)))
	if rec.Code != http.StatusCreated {
		t.Fatalf("size-prefixed publish: status=%d body=%s", rec.Code, rec.Body.String())
	}
	var created struct {
		CID      string `json:"cid"`
		Envelope string `json:"envelope"`
		Bytes    int    `json:"bytes"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode publish response: %v", err)
	}
	if created.CID != wantCID || created.Envelope != "size-prefixed" || created.Bytes != len(bare) {
		t.Fatalf("response = %+v, want cid %s over the bare bytes (%d) with envelope size-prefixed", created, wantCID, len(bare))
	}
	got, err := store.GetRecord("OMM.fbs", wantCID)
	if err != nil {
		t.Fatalf("stored record not readable by the bare CID: %v", err)
	}
	if !bytes.Equal(got.Data, bare) {
		t.Fatalf("stored bytes are not the bare record (identifier at bytes 4..8 = %q)", got.Data[4:8])
	}

	// Publishing the bare form of the same record dedupes to the same CID.
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, localLaneRequest(http.MethodPost, "/api/v1/data/publish/OMM.fbs", bare))
	if rec.Code != http.StatusCreated || !strings.Contains(rec.Body.String(), wantCID) || !strings.Contains(rec.Body.String(), `"envelope":"bare"`) {
		t.Fatalf("bare publish: status=%d body=%s", rec.Code, rec.Body.String())
	}
	rows, err := store.QueryRawRecords(storage.RawRecordQuery{SchemaName: "OMM.fbs", Limit: 10})
	if err != nil {
		t.Fatalf("query rows: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("stored %d records, want 1 (one canonical record)", len(rows))
	}
}

func TestBatchPublishNormalizesSizePrefixedRecords(t *testing.T) {
	mux, store := newLocalLaneTestMux(t)
	bare := bareOMM(t)
	wantCID := storage.ComputeCID(bare)
	frame := func(rec []byte) []byte {
		var buf bytes.Buffer
		_ = binary.Write(&buf, binary.LittleEndian, uint32(len(rec)))
		buf.Write(rec)
		return buf.Bytes()
	}
	body := append(frame(sizePrefixedCopy(bare)), frame(bare)...)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, localLaneRequest(http.MethodPost, "/api/v1/data/publish/batch/OMM.fbs", body))
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var response struct {
		Results []struct {
			CID      string `json:"cid"`
			Envelope string `json:"envelope"`
			Error    string `json:"error"`
		} `json:"results"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode batch response: %v", err)
	}
	if len(response.Results) != 2 || response.Results[0].CID != wantCID || response.Results[1].CID != wantCID {
		t.Fatalf("results = %+v, want both frames stored under the bare CID %s", response.Results, wantCID)
	}
	if response.Results[0].Envelope != "size-prefixed" || response.Results[1].Envelope != "bare" {
		t.Fatalf("envelopes = %q/%q, want size-prefixed then bare", response.Results[0].Envelope, response.Results[1].Envelope)
	}
	rows, err := store.QueryRawRecords(storage.RawRecordQuery{SchemaName: "OMM.fbs", Limit: 10})
	if err != nil {
		t.Fatalf("query rows: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("stored %d records, want 1 (both frames are the same canonical record)", len(rows))
	}
	got, err := store.GetRecord("OMM.fbs", wantCID)
	if err != nil {
		t.Fatalf("stored record not readable by the bare CID: %v", err)
	}
	if !bytes.Equal(got.Data, bare) || string(got.Data[4:8]) != "$OMM" {
		t.Fatalf("stored bytes are not the bare record (identifier at bytes 4..8 = %q)", got.Data[4:8])
	}
}
