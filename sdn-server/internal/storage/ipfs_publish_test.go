package storage

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPinAssetGLBUsesDeterministicKuboAddOptions(t *testing.T) {
	assetBytes := []byte("glTF fixture")
	assetPath := filepath.Join(t.TempDir(), "vehicle.glb")
	if err := os.WriteFile(assetPath, assetBytes, 0o600); err != nil {
		t.Fatalf("write asset fixture: %v", err)
	}

	expectedQuery := url.Values{
		"chunker":             {"size-262144"},
		"cid-version":         {"1"},
		"hash":                {"sha2-256"},
		"pin":                 {"true"},
		"progress":            {"false"},
		"raw-leaves":          {"true"},
		"wrap-with-directory": {"false"},
	}.Encode()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %q, want POST", r.Method)
			http.Error(w, "wrong method", http.StatusBadRequest)
			return
		}
		if r.URL.Path != "/api/v0/add" {
			t.Errorf("path = %q, want /api/v0/add", r.URL.Path)
			http.Error(w, "wrong path", http.StatusBadRequest)
			return
		}
		if r.URL.RawQuery != expectedQuery {
			t.Errorf("query = %q, want %q", r.URL.RawQuery, expectedQuery)
			http.Error(w, "wrong query", http.StatusBadRequest)
			return
		}

		reader, err := r.MultipartReader()
		if err != nil {
			t.Errorf("create multipart reader: %v", err)
			http.Error(w, "invalid multipart body", http.StatusBadRequest)
			return
		}
		part, err := reader.NextPart()
		if err != nil {
			t.Errorf("read multipart part: %v", err)
			http.Error(w, "missing multipart part", http.StatusBadRequest)
			return
		}
		body, err := io.ReadAll(part)
		if err != nil {
			t.Errorf("read multipart body: %v", err)
			http.Error(w, "unreadable multipart body", http.StatusBadRequest)
			return
		}
		if string(body) != string(assetBytes) {
			t.Errorf("multipart body = %q, want %q", body, assetBytes)
			http.Error(w, "wrong multipart body", http.StatusBadRequest)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"Name":"vehicle.glb","Hash":"bafyassetfixture","Size":"12"}`+"\n")
	}))
	defer server.Close()

	cidValue, err := PinAssetGLB(context.Background(), server.URL, assetPath)
	if err != nil {
		t.Fatalf("PinAssetGLB failed: %v", err)
	}
	if cidValue != "bafyassetfixture" {
		t.Fatalf("CID = %q, want bafyassetfixture", cidValue)
	}
}

func TestUnpinAssetCIDPostsNarrowKuboCommand(t *testing.T) {
	expectedQuery := url.Values{"arg": {"bafyassetfixture"}}.Encode()
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.Method != http.MethodPost {
			t.Errorf("method = %q, want POST", r.Method)
			http.Error(w, "wrong method", http.StatusBadRequest)
			return
		}
		if r.URL.Path != "/api/v0/pin/rm" {
			t.Errorf("path = %q, want /api/v0/pin/rm", r.URL.Path)
			http.Error(w, "wrong path", http.StatusBadRequest)
			return
		}
		if r.URL.RawQuery != expectedQuery {
			t.Errorf("query = %q, want %q", r.URL.RawQuery, expectedQuery)
			http.Error(w, "wrong query", http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	if err := UnpinAssetCID(context.Background(), server.URL, "  bafyassetfixture  "); err != nil {
		t.Fatalf("UnpinAssetCID failed: %v", err)
	}
	if requests != 1 {
		t.Fatalf("requests = %d, want 1", requests)
	}
}

func TestUnpinAssetCIDRejectsMissingInputs(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests++
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	if err := UnpinAssetCID(context.Background(), "", "bafyassetfixture"); err == nil || !strings.Contains(err.Error(), "api url is required") {
		t.Fatalf("empty API URL error = %v, want api url is required", err)
	}
	if err := UnpinAssetCID(context.Background(), server.URL, " \t\n "); err == nil || !strings.Contains(err.Error(), "cid is required") {
		t.Fatalf("empty CID error = %v, want cid is required", err)
	}
	if requests != 0 {
		t.Fatalf("requests = %d, want no request for invalid input", requests)
	}
}

func TestUnpinAssetCIDBoundsNonSuccessResponse(t *testing.T) {
	const responseBytes = 64 * 1024
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write(bytes.Repeat([]byte("x"), responseBytes))
	}))
	defer server.Close()

	err := UnpinAssetCID(context.Background(), server.URL, "bafyassetfixture")
	if err == nil {
		t.Fatal("UnpinAssetCID succeeded, want non-2xx error")
	}
	if !strings.Contains(err.Error(), "status 502") {
		t.Fatalf("error = %q, want HTTP status", err)
	}
	if len(err.Error()) > 4600 {
		t.Fatalf("error length = %d, want a bounded response preview", len(err.Error()))
	}
}
