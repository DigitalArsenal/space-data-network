package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/spacedatanetwork/sdn-server/internal/versioninfo"
)

// Owner 2026-07-31: the header renders the SDN suite version AND the Kubo
// version the node is based on — /api/v1/version must carry both, and the
// kubo value must come from the generated single source of truth.
func TestVersionCarriesKubo(t *testing.T) {
	h := &CoreAPIHandler{}
	rec := httptest.NewRecorder()
	h.handleVersion(rec, httptest.NewRequest(http.MethodGet, "/api/v1/version", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("json: %v", err)
	}
	if body["kubo_version"] != versioninfo.KuboVersion || body["kubo_version"] == "" {
		t.Errorf("kubo_version = %q, want %q (non-empty)", body["kubo_version"], versioninfo.KuboVersion)
	}
	if !strings.HasPrefix(body["agent_version"], "spacedatanetwork/") {
		t.Errorf("agent_version = %q", body["agent_version"])
	}
	if body["suite_version"] == "" || body["standards_version"] == "" {
		t.Errorf("missing suite/standards: %v", body)
	}
}

// A release build names its tag so the admin UI can link the release notes; a
// development build carries no release_tag at all.
func TestVersionReleaseTag(t *testing.T) {
	get := func() map[string]string {
		h := &CoreAPIHandler{}
		rec := httptest.NewRecorder()
		h.handleVersion(rec, httptest.NewRequest(http.MethodGet, "/api/v1/version", nil))
		var body map[string]string
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("json: %v", err)
		}
		return body
	}
	prev := versioninfo.ReleaseTag
	defer func() { versioninfo.ReleaseTag = prev }()

	versioninfo.ReleaseTag = ""
	if tag, ok := get()["release_tag"]; ok {
		t.Errorf("development build reported release_tag %q", tag)
	}
	versioninfo.ReleaseTag = "v1.2.3"
	if tag := get()["release_tag"]; tag != "v1.2.3" {
		t.Errorf("release_tag = %q, want v1.2.3", tag)
	}
}
