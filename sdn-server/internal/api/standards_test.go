package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestStandardsEndpointServesTheEmbeddedRegistry(t *testing.T) {
	h := &CoreAPIHandler{}
	rec := httptest.NewRecorder()
	h.handleStandards(rec, httptest.NewRequest(http.MethodGet, "/api/v1/standards", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Standards []standardRow `json:"standards"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Standards) < 200 {
		t.Fatalf("registry lists %d standards, want the whole embedded set", len(body.Standards))
	}
	var omm *standardRow
	for i := range body.Standards {
		if body.Standards[i].Code == "OMM" {
			omm = &body.Standards[i]
		}
	}
	if omm == nil || omm.Schema != "OMM.fbs" || omm.Name == "" {
		t.Fatalf("OMM row missing or unnamed: %+v", omm)
	}
	if name, desc := splitStandardDoc("Orbit Mean Elements Message - the CCSDS mean-element set"); name != "Orbit Mean Elements Message" || desc != "the CCSDS mean-element set" {
		t.Fatalf("split = %q / %q", name, desc)
	}
}
