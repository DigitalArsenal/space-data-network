package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The data-api liveness handler (internal/api/data.go handleHealth) emits
// {"status":"ok"} and no "healthy" boolean. Both CLI consumers historically
// decoded only "healthy", so every node printed unhealthy forever. These
// tests lock the tolerant contract: explicit "healthy" wins; otherwise
// status=="ok" is healthy.
func TestDaemonHealthPayloadOK(t *testing.T) {
	cases := []struct {
		name string
		body string
		want bool
	}{
		{"live handler shape", `{"status":"ok","component":"spaceaware-data-api"}`, true},
		{"explicit healthy true", `{"healthy":true}`, true},
		{"explicit healthy false wins over ok status", `{"healthy":false,"status":"ok"}`, false},
		{"empty object", `{}`, false},
		{"status not ok", `{"status":"degraded"}`, false},
		{"status case-insensitive", `{"status":"OK"}`, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var h daemonHealthPayload
			if err := json.Unmarshal([]byte(tc.body), &h); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if got := h.ok(); got != tc.want {
				t.Fatalf("ok() = %v, want %v for %s", got, tc.want, tc.body)
			}
		})
	}
}

func TestWriteDaemonStatusLiveHandlerShapeReportsRunning(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/data/health" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok","component":"spaceaware-data-api","time":"2026-07-30T00:00:00Z"}`))
	}))
	defer srv.Close()

	var out bytes.Buffer
	if err := writeDaemonStatus(context.Background(), &out, srv.URL); err != nil {
		t.Fatalf("writeDaemonStatus: %v", err)
	}
	got := out.String()
	if !strings.Contains(got, "daemon_status=running") || !strings.Contains(got, "data_health=healthy") {
		t.Fatalf("expected running/healthy, got:\n%s", got)
	}
}
