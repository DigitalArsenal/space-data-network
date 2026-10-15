package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestWaitingDocumentPicksOnlyTheDocumentThatWaits(t *testing.T) {
	view := map[string]json.RawMessage{
		"state":    json.RawMessage(`"awaiting-manifest-signature"`),
		"manifest": json.RawMessage(`{"update_id":"u"}`),
		"signal":   json.RawMessage(`null`),
	}
	if doc, err := waitingDocument(view, "manifest"); err != nil || string(doc) != `{"update_id":"u"}` {
		t.Fatalf("manifest = %s, %v", doc, err)
	}
	if _, err := waitingDocument(view, "signal"); err == nil {
		t.Fatal("a signal was handed out while the manifest waits")
	}
	view["state"] = json.RawMessage(`"manifest-signed"`)
	if _, err := waitingDocument(view, "manifest"); err == nil {
		t.Fatal("an already-signed manifest was handed out")
	}
	if _, err := distributionDocumentKind("module"); err == nil {
		t.Fatal("an unknown document kind was accepted")
	}
}

// The get/submit round trip the runbook's step 4 uses, against a stand-in
// coordinator: the session cookie is sent, and the signed body reaches the
// document's endpoint unchanged in meaning.
func TestDistributionGetAndSubmitRoundTrip(t *testing.T) {
	var submitted string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if c, err := r.Cookie(sessionCookieName); err != nil || c.Value != "tok" {
			http.Error(w, "no session", http.StatusUnauthorized)
			return
		}
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/admin/updates/distributions/d1":
			_, _ = io.WriteString(w, `{"id":"d1","state":"awaiting-manifest-signature","manifest":{"update_id":"u","signing":{"statement_domain":"SDN-UPDATE-MANIFEST-V1"}}}`)
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/admin/updates/distributions/d1/manifest":
			body, _ := io.ReadAll(r.Body)
			submitted = string(body)
			_, _ = io.WriteString(w, `{"id":"d1","state":"manifest-signed","key_id":"abcdef012345"}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	c := &adminClient{baseURL: srv.URL, http: &http.Client{Timeout: 5 * time.Second}, token: "tok"}

	var view map[string]json.RawMessage
	if err := c.get(context.Background(), "/api/v1/admin/updates/distributions/d1", &view); err != nil {
		t.Fatal(err)
	}
	doc, err := waitingDocument(view, "manifest")
	if err != nil || !strings.Contains(string(doc), `"update_id":"u"`) {
		t.Fatalf("doc = %s, %v", doc, err)
	}
	signed := json.RawMessage(`{"update_id":"u","signing":{"key_id":"abcdef012345","signature":"c2ln"}}`)
	var out map[string]json.RawMessage
	if err := c.postJSON(context.Background(), "/api/v1/admin/updates/distributions/d1/manifest", signed, &out, ""); err != nil {
		t.Fatal(err)
	}
	var sent, want any
	_ = json.Unmarshal([]byte(submitted), &sent)
	_ = json.Unmarshal(signed, &want)
	if js, _ := json.Marshal(sent); string(js) != string(func() []byte { b, _ := json.Marshal(want); return b }()) {
		t.Fatalf("submitted %s, want %s", submitted, signed)
	}
}
