package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
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
			req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/dataset-updates/batch", strings.NewReader(`{"schema":"CAT.fbs","providerId":"synthetic","sourceName":"index","batchId":"failed","action":"requeue"}`))
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
