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

func TestWriteDaemonStatusReportsHealthyDaemon(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/data/health" {
			t.Fatalf("status probe path = %q, want /api/v1/data/health", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"healthy": true,
			"details": map[string]any{
				"runtime": "daemon",
				"message": "ready",
			},
		})
	}))
	defer server.Close()

	var out bytes.Buffer
	if err := writeDaemonStatus(context.Background(), &out, server.URL); err != nil {
		t.Fatalf("writeDaemonStatus failed: %v", err)
	}

	for _, want := range []string{
		"daemon_status=running",
		"data_health=healthy",
		"data_runtime=daemon",
		"data_message=ready",
	} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("status output missing %q:\n%s", want, out.String())
		}
	}
}

func TestWriteDaemonStatusReportsUnavailableDaemon(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	baseURL := server.URL
	server.Close()

	var out bytes.Buffer
	if err := writeDaemonStatus(context.Background(), &out, baseURL); err != nil {
		t.Fatalf("writeDaemonStatus returned error for unavailable daemon: %v", err)
	}
	if !strings.Contains(out.String(), "daemon_status=unavailable") {
		t.Fatalf("status output did not report unavailable daemon:\n%s", out.String())
	}
}
