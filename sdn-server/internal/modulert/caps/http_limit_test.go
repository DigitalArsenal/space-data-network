package caps

// Loop C.8a: http.request response-size policy. The old behavior silently
// truncated bodies at 4 MiB — a truncated Provider catalog would have
// ingested a partial batch as if complete. Now: 100 MB ceiling (runner
// parity), optional per-request max_bytes clamp, and exceeding the bound is
// an explicit ERROR.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func httpCapCall(t *testing.T, payload map[string]interface{}) map[string]interface{} {
	t.Helper()
	body, _ := json.Marshal(payload)
	resp, err := httpCapHandle("http.request", body)
	if err != nil {
		t.Fatalf("httpCapHandle: %v", err)
	}
	var meta map[string]interface{}
	if err := json.Unmarshal(resp, &meta); err != nil {
		t.Fatalf("cap response is not JSON: %v", err)
	}
	return meta
}

func TestHTTPCapDeliversBodiesBeyondTheOld4MiBTruncation(t *testing.T) {
	// 10 MB — the size class of the real Provider GP full catalog, and 2.5x
	// the old silent-truncation limit.
	payload := strings.Repeat("NORAD_CAT_ID,EPOCH,MEAN_MOTION\n", 10*1024*1024/31)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/csv")
		w.Write([]byte(payload)) //nolint:errcheck
	}))
	defer server.Close()

	meta := httpCapCall(t, map[string]interface{}{"url": server.URL})
	if ok, _ := meta["ok"].(bool); !ok {
		t.Fatalf("10MB fetch failed: %v", meta)
	}
	result := meta["result"].(map[string]interface{})
	body, _ := result["body"].(string)
	if len(body) != len(payload) {
		t.Fatalf("body length %d, want %d (no truncation)", len(body), len(payload))
	}
}

func TestHTTPCapMaxBytesClampErrorsInsteadOfTruncating(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(make([]byte, 64*1024)) //nolint:errcheck
	}))
	defer server.Close()

	meta := httpCapCall(t, map[string]interface{}{"url": server.URL, "max_bytes": 32 * 1024})
	if ok, _ := meta["ok"].(bool); ok {
		t.Fatalf("oversized response was delivered instead of refused: %v", meta)
	}
	msg, _ := meta["error"].(map[string]interface{})["message"].(string)
	if !strings.Contains(msg, "exceeds") {
		t.Fatalf("error does not name the size violation: %v", meta)
	}
}
