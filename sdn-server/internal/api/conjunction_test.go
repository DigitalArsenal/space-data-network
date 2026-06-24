package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestConjunctionScreenRoutePreservesEncryptedManeuverWorkflow(t *testing.T) {
	t.Parallel()

	mux := http.NewServeMux()
	NewConjunctionHandler(nil).RegisterRoutes(mux)

	body := bytes.NewBufferString(`{
		"primary_schema":"MPE.fbs",
		"secondary_schema":"OMM.fbs",
		"encrypted":true,
		"grant_id":"grant-private-mpe",
		"channel_id":"channel-private-ca",
		"assessor_peer_id":"16Uiu2HAssessor",
		"include_provenance":true,
		"limit":25
	}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/conjunction/screen", body)
	rec := httptest.NewRecorder()

	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}

	var payload map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	if payload["workflow"] != "encrypted-conjunction-assessment" {
		t.Fatalf("workflow = %v", payload["workflow"])
	}
	if payload["mode"] != "private-maneuver-ephemeris" {
		t.Fatalf("mode = %v", payload["mode"])
	}
	if payload["primary_schema"] != "MPE.fbs" || payload["secondary_schema"] != "OMM.fbs" {
		t.Fatalf("schemas = %v/%v", payload["primary_schema"], payload["secondary_schema"])
	}
	if payload["encrypted"] != true {
		t.Fatalf("encrypted = %v", payload["encrypted"])
	}
	if payload["grant_id"] != "grant-private-mpe" || payload["channel_id"] != "channel-private-ca" {
		t.Fatalf("grant/channel = %v/%v", payload["grant_id"], payload["channel_id"])
	}
	provenance, ok := payload["provenance"].(map[string]interface{})
	if !ok {
		t.Fatalf("provenance missing or wrong type: %#v", payload["provenance"])
	}
	if provenance["assessor_peer_id"] != "16Uiu2HAssessor" {
		t.Fatalf("assessor provenance = %v", provenance["assessor_peer_id"])
	}
	if _, ok := payload["events"].([]interface{}); !ok {
		t.Fatalf("events missing or wrong type: %#v", payload["events"])
	}
}

func TestConjunctionScreenRejectsNonPost(t *testing.T) {
	t.Parallel()

	mux := http.NewServeMux()
	NewConjunctionHandler(nil).RegisterRoutes(mux)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/conjunction/screen", nil)
	rec := httptest.NewRecorder()

	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusMethodNotAllowed)
	}
}
