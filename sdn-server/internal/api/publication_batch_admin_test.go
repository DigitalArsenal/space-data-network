package api

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/spacedatanetwork/sdn-server/internal/publicationstate"
	"github.com/spacedatanetwork/sdn-server/internal/publicationstate/pst"
)

type batchActionService struct {
	fakeDatasetPublicationService
	calledAction string
}

func (s *batchActionService) ManagePublicationBatch(schema, provider, source, batch, action string) error {
	s.calledAction = action
	return nil
}
func TestPublicationBatchAdminLocalGate(t *testing.T) {
	for _, remote := range []bool{false, true} {
		for _, proxy := range []bool{false, true} {
			s := &batchActionService{}
			h := NewDatasetPublicationHandler(s)
			mux := http.NewServeMux()
			h.RegisterRoutes(mux)
			req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/dataset-updates/batch?format=json", strings.NewReader(`{"schema":"CAT.fbs","providerId":"synthetic","sourceName":"index","batchId":"failed","action":"requeue"}`))
			req.RemoteAddr = "127.0.0.1:1234"
			if remote {
				req.RemoteAddr = "203.0.113.9:1234"
			}
			if proxy {
				req.Header.Set("X-Forwarded-For", "203.0.113.9")
			}
			w := httptest.NewRecorder()
			mux.ServeHTTP(w, req)
			if remote || proxy {
				if w.Code != http.StatusForbidden || s.calledAction != "" {
					t.Fatal("remote management allowed")
				}
			} else if w.Code != http.StatusOK || s.calledAction != "requeue" {
				t.Fatal("local recovery unavailable", w.Code, w.Body.String())
			}
		}
	}
}

func TestPublicationBatchAdminFlatBuffers(t *testing.T) {
	raw := publicationstate.Encode(&pst.BodyT{Type: pst.BodyBatchAction, Value: &pst.BatchActionT{Schema: "CAT.fbs", Provider: "synthetic", Source: "index", Batch: "failed", Action: "requeue"}})
	flipped := bytes.Clone(raw)
	flipped[len(flipped)-1] ^= 1
	for _, tc := range []struct {
		name   string
		data   []byte
		status int
	}{
		{"valid", raw, http.StatusOK},
		{"torn", raw[:len(raw)-1], http.StatusBadRequest},
		{"flipped", flipped, http.StatusBadRequest},
		{"implicit-json", []byte(`{"schema":"CAT.fbs"}`), http.StatusBadRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := &batchActionService{}
			h := NewDatasetPublicationHandler(s)
			req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/dataset-updates/batch", bytes.NewReader(tc.data))
			req.RemoteAddr = "127.0.0.1:1234"
			w := httptest.NewRecorder()
			h.handlePublicationBatch(w, req)
			if w.Code != tc.status || w.Header().Get("Content-Type") != StreamContentType {
				t.Fatal(w.Code, w.Header())
			}
			if _, err := ParseQRP(w.Body.Bytes()); err != nil {
				t.Fatal(err)
			}
			if (s.calledAction != "") != (tc.status == http.StatusOK) {
				t.Fatal("action invoked for invalid input")
			}
		})
	}
}
