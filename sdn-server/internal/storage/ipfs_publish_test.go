package storage

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ipfs/go-cid"
	multibase "github.com/multiformats/go-multibase"
)

const (
	testAssetCID = "bafkreifzjut3te2nhyekklss27nh3k72ysco7y32koao5eei66wof36n5e"
	testOtherCID = "bafkreihdwdcefgh4dqkjv67uzcmw7ojee6xedzdetojuzjevtenxquvyku"
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
	parsedCID, err := cid.Decode(testAssetCID)
	if err != nil {
		t.Fatalf("decode asset CID fixture: %v", err)
	}
	alternateCID, err := parsedCID.StringOfBase(multibase.Base58BTC)
	if err != nil {
		t.Fatalf("encode alternate asset CID: %v", err)
	}

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
		if part.FormName() != "file" {
			t.Errorf("multipart form name = %q, want file", part.FormName())
			http.Error(w, "wrong multipart field", http.StatusBadRequest)
			return
		}
		if part.FileName() != filepath.Base(assetPath) {
			t.Errorf("multipart filename = %q, want %q", part.FileName(), filepath.Base(assetPath))
			http.Error(w, "wrong multipart filename", http.StatusBadRequest)
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
		if extraPart, err := reader.NextPart(); !errors.Is(err, io.EOF) {
			if err != nil {
				t.Errorf("read trailing multipart part: %v", err)
			} else {
				t.Errorf("unexpected trailing multipart part %q", extraPart.FormName())
				_ = extraPart.Close()
			}
			http.Error(w, "unexpected multipart part", http.StatusBadRequest)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"Name":"vehicle.glb","Hash":"`+alternateCID+`","Size":"12"}`+"\n")
	}))
	defer server.Close()

	cidValue, err := PinAssetGLB(context.Background(), server.URL, assetPath)
	if err != nil {
		t.Fatalf("PinAssetGLB failed: %v", err)
	}
	if cidValue != testAssetCID {
		t.Fatalf("CID = %q, want canonical %q", cidValue, testAssetCID)
	}
}

func TestPinAssetGLBRejectsMalformedKuboCID(t *testing.T) {
	assetPath := filepath.Join(t.TempDir(), "vehicle.glb")
	if err := os.WriteFile(assetPath, []byte("glTF fixture"), 0o600); err != nil {
		t.Fatalf("write asset fixture: %v", err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"Name":"vehicle.glb","Hash":"not-a-cid","Size":"12"}`+"\n")
	}))
	defer server.Close()

	cidValue, err := PinAssetGLB(context.Background(), server.URL, assetPath)
	if err == nil || !strings.Contains(err.Error(), "invalid cid") {
		t.Fatalf("PinAssetGLB result = %q, %v; want invalid cid error", cidValue, err)
	}
	if cidValue != "" {
		t.Fatalf("CID = %q, want empty result on malformed Kubo response", cidValue)
	}
}

func TestUnpinAssetCIDPostsNarrowKuboCommand(t *testing.T) {
	expectedQuery := url.Values{"arg": {testAssetCID}}.Encode()
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

	if err := UnpinAssetCID(context.Background(), server.URL, "  "+testAssetCID+"  "); err != nil {
		t.Fatalf("UnpinAssetCID failed: %v", err)
	}
	if requests != 1 {
		t.Fatalf("requests = %d, want 1", requests)
	}
}

func TestUnpinAssetCIDCanonicalizesAndOwnsCommandQuery(t *testing.T) {
	parsedCID, err := cid.Decode(testAssetCID)
	if err != nil {
		t.Fatalf("decode asset CID fixture: %v", err)
	}
	alternateCID, err := parsedCID.StringOfBase(multibase.Base58BTC)
	if err != nil {
		t.Fatalf("encode alternate asset CID: %v", err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %q, want POST", r.Method)
		}
		if r.URL.Path != "/prefix/api/v0/pin/rm" {
			t.Errorf("path = %q, want /prefix/api/v0/pin/rm", r.URL.Path)
		}
		query := r.URL.Query()
		if query.Get("keep") != "value" {
			t.Errorf("keep query = %q, want value", query.Get("keep"))
		}
		if args := query["arg"]; len(args) != 1 || args[0] != testAssetCID {
			t.Errorf("arg query = %q, want exactly [%s]", args, testAssetCID)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	apiURL := server.URL + "/prefix?keep=value&arg=" + url.QueryEscape(testOtherCID)
	if err := UnpinAssetCID(context.Background(), apiURL, alternateCID); err != nil {
		t.Fatalf("UnpinAssetCID failed: %v", err)
	}
}

func TestUnpinAssetCIDRejectsInvalidInputs(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests++
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	if err := UnpinAssetCID(context.Background(), "", testAssetCID); err == nil || !strings.Contains(err.Error(), "api url is required") {
		t.Fatalf("empty API URL error = %v, want api url is required", err)
	}
	if err := UnpinAssetCID(context.Background(), server.URL, " \t\n "); err == nil || !strings.Contains(err.Error(), "cid is required") {
		t.Fatalf("empty CID error = %v, want cid is required", err)
	}
	if err := UnpinAssetCID(context.Background(), server.URL, "not-a-cid"); err == nil || !strings.Contains(err.Error(), "invalid cid") {
		t.Fatalf("malformed CID error = %v, want invalid cid", err)
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

	err := UnpinAssetCID(context.Background(), server.URL, testAssetCID)
	if err == nil {
		t.Fatal("UnpinAssetCID succeeded, want non-2xx error")
	}
	if !strings.Contains(err.Error(), "status 502") {
		t.Fatalf("error = %q, want HTTP status", err)
	}
	if !strings.Contains(err.Error(), "[truncated]") {
		t.Fatalf("error = %q, want explicit truncation marker", err)
	}
	if len(err.Error()) > 4600 {
		t.Fatalf("error length = %d, want a bounded response preview", len(err.Error()))
	}
}

func TestUnpinAssetCIDDoesNotMarkCompleteErrorPreviewTruncated(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write(bytes.Repeat([]byte("x"), maxKuboCommandErrorBodyBytes))
	}))
	defer server.Close()

	err := UnpinAssetCID(context.Background(), server.URL, testAssetCID)
	if err == nil {
		t.Fatal("UnpinAssetCID succeeded, want non-2xx error")
	}
	if strings.Contains(err.Error(), "[truncated]") {
		t.Fatalf("error = %q, complete response must not be marked truncated", err)
	}
}

func TestUnpinAssetCIDBoundedlyDrainsSuccessResponse(t *testing.T) {
	body := &trackingResponseBody{data: bytes.Repeat([]byte("x"), 64*1024)}
	previousClient := http.DefaultClient
	http.DefaultClient = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Status:     "200 OK",
			Header:     make(http.Header),
			Body:       body,
			Request:    req,
		}, nil
	})}
	t.Cleanup(func() { http.DefaultClient = previousClient })

	if err := UnpinAssetCID(context.Background(), "https://kubo.invalid", testAssetCID); err != nil {
		t.Fatalf("UnpinAssetCID failed: %v", err)
	}
	if body.readBytes == 0 {
		t.Fatal("successful response body was not drained")
	}
	if body.readBytes > maxKuboCommandErrorBodyBytes+1 {
		t.Fatalf("drained bytes = %d, want at most %d", body.readBytes, maxKuboCommandErrorBodyBytes+1)
	}
	if !body.closed {
		t.Fatal("successful response body was not closed")
	}
}

func TestUnpinAssetCIDHonorsContextCancellation(t *testing.T) {
	requestStarted := make(chan struct{})
	previousClient := http.DefaultClient
	http.DefaultClient = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		close(requestStarted)
		<-req.Context().Done()
		return nil, req.Context().Err()
	})}
	t.Cleanup(func() { http.DefaultClient = previousClient })

	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		result <- UnpinAssetCID(ctx, "https://kubo.invalid", testAssetCID)
	}()

	select {
	case <-requestStarted:
	case <-time.After(time.Second):
		t.Fatal("Kubo request did not start")
	}
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("UnpinAssetCID error = %v, want context canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("UnpinAssetCID did not return after cancellation")
	}
}

type trackingResponseBody struct {
	data      []byte
	offset    int
	readBytes int
	closed    bool
}

func (body *trackingResponseBody) Read(buffer []byte) (int, error) {
	if body.offset >= len(body.data) {
		return 0, io.EOF
	}
	count := copy(buffer, body.data[body.offset:])
	body.offset += count
	body.readBytes += count
	return count, nil
}

func (body *trackingResponseBody) Close() error {
	body.closed = true
	return nil
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return fn(request)
}
