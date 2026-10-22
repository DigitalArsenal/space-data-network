package caps

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHTTPCapBodylessResponsesPreserveStatusAndHeaders(t *testing.T) {
	for _, tc := range []struct {
		name   string
		method string
		status int
	}{
		{"HEAD", http.MethodHead, http.StatusOK},
		{"101", http.MethodGet, http.StatusSwitchingProtocols},
		{"204", http.MethodGet, http.StatusNoContent},
		{"304", http.MethodGet, http.StatusNotModified},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != tc.method {
					t.Errorf("method = %q, want %q", r.Method, tc.method)
				}
				w.Header().Set("Content-Length", "1234")
				w.Header().Set("ETag", `"object-v1"`)
				w.Header().Set("Connection", "close")
				w.WriteHeader(tc.status)
			}))
			defer server.Close()

			meta := httpCapCall(t, map[string]interface{}{"url": server.URL, "method": tc.method})
			if meta["ok"] != true {
				t.Fatalf("bodyless response failed: %v", meta)
			}
			result := meta["result"].(map[string]interface{})
			if result["status"] != float64(tc.status) || result["body"] != "" {
				t.Fatalf("want status %d and empty body, got %v", tc.status, result)
			}
			headers := result["headers"].(map[string]interface{})
			if headers["Etag"] != `"object-v1"` {
				t.Fatalf("response headers lost: %v", headers)
			}
			if tc.method == http.MethodHead && headers["Content-Length"] != "1234" {
				t.Fatalf("HEAD Content-Length = %v, want 1234", headers["Content-Length"])
			}
		})
	}
}

func TestHTTPCapCredentialedRepeatGETReturnsFullBody(t *testing.T) {
	for _, tc := range []struct {
		name  string
		value string
	}{
		{"Authorization", "Bearer fixture-token"},
		{"authorization", ""},
		{"x-amz-date", "20261021T120000Z"},
		{"X-Amz-Security-Token", "fixture-token"},
		{"Cookie", "session=fixture-token"},
		{"Proxy-Authorization", "Basic Zml4dHVyZTpwdw=="},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ledger := &memoryValidators{}
			SetFetchValidatorSource(ledger.source)
			SetFetchValidatorObserver(ledger.observe)
			t.Cleanup(func() {
				SetFetchValidatorSource(nil)
				SetFetchValidatorObserver(nil)
			})

			const etag = `"object-v1"`
			const lastModified = "Wed, 21 Oct 2026 12:00:00 GMT"
			const body = "complete object-store contents"
			seen := make(chan http.Header, 2)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				seen <- r.Header.Clone()
				w.Header().Set("ETag", etag)
				w.Header().Set("Last-Modified", lastModified)
				if r.Header.Get("If-None-Match") != "" || r.Header.Get("If-Modified-Since") != "" {
					w.WriteHeader(http.StatusNotModified)
					return
				}
				w.Header().Set("Content-Type", "text/plain")
				_, _ = w.Write([]byte(body))
			}))
			defer server.Close()
			url := server.URL + "/bucket/key"

			for attempt := 1; attempt <= 2; attempt++ {
				meta := httpCapCall(t, map[string]interface{}{
					"url":     url,
					"headers": map[string]string{tc.name: tc.value},
				})
				if meta["ok"] != true {
					t.Fatalf("GET %d failed: %v", attempt, meta)
				}
				headers := <-seen
				if len(headers.Values(tc.name)) != 1 || headers.Get(tc.name) != tc.value {
					t.Fatalf("GET %d credential header changed: %v", attempt, headers)
				}
				if len(headers.Values("If-None-Match")) > 0 || len(headers.Values("If-Modified-Since")) > 0 {
					t.Errorf("GET %d received host validators: %v", attempt, headers)
				}
				result := meta["result"].(map[string]interface{})
				if result["status"] != float64(http.StatusOK) || result["body"] != body {
					t.Fatalf("GET %d did not return the full body: %v", attempt, result)
				}
				if gotETag, gotModified := ledger.source(url); gotETag != etag || gotModified != lastModified {
					t.Fatalf("GET %d did not record validators: %q %q", attempt, gotETag, gotModified)
				}
			}
		})
	}
}
