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
	"runtime"
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

	cidValue, err := PinAssetGLB(context.Background(), server.URL+"?only-hash=true&nocopy=true", assetPath)
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

func TestCalculateAssetGLBCIDUsesOnlyHashWithoutPinning(t *testing.T) {
	assetBytes := []byte("glTF fixture")
	assetPath := filepath.Join(t.TempDir(), "vehicle.glb")
	if err := os.WriteFile(assetPath, assetBytes, 0o600); err != nil {
		t.Fatalf("write asset fixture: %v", err)
	}
	expectedQuery := url.Values{
		"chunker":             {"size-262144"},
		"cid-version":         {"1"},
		"hash":                {"sha2-256"},
		"only-hash":           {"true"},
		"pin":                 {"false"},
		"progress":            {"false"},
		"raw-leaves":          {"true"},
		"wrap-with-directory": {"false"},
	}.Encode()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %q, want POST", r.Method)
		}
		if r.URL.Path != "/api/v0/add" {
			t.Errorf("path = %q, want /api/v0/add", r.URL.Path)
		}
		if r.URL.RawQuery != expectedQuery {
			t.Errorf("query = %q, want %q", r.URL.RawQuery, expectedQuery)
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
		if !bytes.Equal(body, assetBytes) {
			t.Errorf("multipart body = %q, want %q", body, assetBytes)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"Name":"vehicle.glb","Hash":"`+testAssetCID+`","Size":"12"}`+"\n")
	}))
	defer server.Close()

	cidValue, err := CalculateAssetGLBCID(context.Background(), server.URL+"?pin=true&nocopy=true", assetPath)
	if err != nil {
		t.Fatalf("CalculateAssetGLBCID failed: %v", err)
	}
	if cidValue != testAssetCID {
		t.Fatalf("CID = %q, want %q", cidValue, testAssetCID)
	}
}

func TestCalculateAndPinAssetGLBCIDsMatch(t *testing.T) {
	assetPath := filepath.Join(t.TempDir(), "vehicle.glb")
	if err := os.WriteFile(assetPath, []byte("glTF fixture"), 0o600); err != nil {
		t.Fatalf("write asset fixture: %v", err)
	}
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"Name":"vehicle.glb","Hash":"`+testAssetCID+`","Size":"12"}`+"\n")
	}))
	defer server.Close()

	plannedCID, err := CalculateAssetGLBCID(context.Background(), server.URL, assetPath)
	if err != nil {
		t.Fatalf("CalculateAssetGLBCID failed: %v", err)
	}
	pinnedCID, err := PinAssetGLB(context.Background(), server.URL, assetPath)
	if err != nil {
		t.Fatalf("PinAssetGLB failed: %v", err)
	}
	if plannedCID != pinnedCID {
		t.Fatalf("planned CID = %q, pinned CID = %q", plannedCID, pinnedCID)
	}
	if requests != 2 {
		t.Fatalf("requests = %d, want calculate then pin", requests)
	}
}

func TestAssetGLBAddRejectsUnsafeKuboResponses(t *testing.T) {
	parsedCID, err := cid.Decode(testAssetCID)
	if err != nil {
		t.Fatalf("decode asset CID fixture: %v", err)
	}
	cidV0 := cid.NewCidV0(parsedCID.Hash()).String()
	validResponse := `{"Name":"vehicle.glb","Hash":"` + testAssetCID + `","Size":"12"}`
	tests := []struct {
		name string
		body string
	}{
		{name: "empty response", body: ""},
		{name: "missing CID", body: `{"Name":"vehicle.glb"}`},
		{name: "CIDv0", body: `{"Hash":"` + cidV0 + `"}`},
		{name: "conflicting CID fields", body: `{"Hash":"` + testAssetCID + `","Cid":"` + testOtherCID + `"}`},
		{name: "multiple objects", body: validResponse + "\n" + validResponse + "\n"},
		{name: "malformed JSON", body: `{"Hash":`},
		{name: "non-JSON trailer", body: validResponse + " trailing"},
		{name: "oversized response", body: `{"Name":"` + strings.Repeat("x", (64<<10)+1) + `","Hash":"` + testAssetCID + `"}`},
	}
	operations := []struct {
		name string
		run  func(context.Context, string, string) (string, error)
	}{
		{name: "calculate", run: CalculateAssetGLBCID},
		{name: "pin", run: PinAssetGLB},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			for _, operation := range operations {
				t.Run(operation.name, func(t *testing.T) {
					assetPath := filepath.Join(t.TempDir(), "vehicle.glb")
					if err := os.WriteFile(assetPath, []byte("glTF fixture"), 0o600); err != nil {
						t.Fatalf("write asset fixture: %v", err)
					}
					server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						_, _ = io.Copy(io.Discard, r.Body)
						w.Header().Set("Content-Type", "application/json")
						_, _ = io.WriteString(w, test.body)
					}))
					defer server.Close()

					cidValue, err := operation.run(context.Background(), server.URL, assetPath)
					if err == nil {
						t.Fatalf("result = %q, want unsafe Kubo response error", cidValue)
					}
					if cidValue != "" {
						t.Fatalf("CID = %q, want empty result on unsafe Kubo response", cidValue)
					}
				})
			}
		})
	}
}

func TestAssetGLBAddAcceptsOneEquivalentCIDObjectWithTrailingWhitespace(t *testing.T) {
	parsedCID, err := cid.Decode(testAssetCID)
	if err != nil {
		t.Fatalf("decode asset CID fixture: %v", err)
	}
	alternateCID, err := parsedCID.StringOfBase(multibase.Base58BTC)
	if err != nil {
		t.Fatalf("encode alternate asset CID: %v", err)
	}
	response := `{"Hash":"` + testAssetCID + `","Cid":"` + alternateCID + `"}` + "\n\t "
	operations := []struct {
		name string
		run  func(context.Context, string, string) (string, error)
	}{
		{name: "calculate", run: CalculateAssetGLBCID},
		{name: "pin", run: PinAssetGLB},
	}

	for _, operation := range operations {
		t.Run(operation.name, func(t *testing.T) {
			assetPath := filepath.Join(t.TempDir(), "vehicle.glb")
			if err := os.WriteFile(assetPath, []byte("glTF fixture"), 0o600); err != nil {
				t.Fatalf("write asset fixture: %v", err)
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.Copy(io.Discard, r.Body)
				_, _ = io.WriteString(w, response)
			}))
			defer server.Close()

			cidValue, err := operation.run(context.Background(), server.URL, assetPath)
			if err != nil {
				t.Fatalf("%s failed: %v", operation.name, err)
			}
			if cidValue != testAssetCID {
				t.Fatalf("CID = %q, want canonical %q", cidValue, testAssetCID)
			}
		})
	}
}

func TestAssetKuboClientHasServerControlledTimeout(t *testing.T) {
	client := newAssetKuboHTTPClient()
	if client.Timeout != 30*time.Second {
		t.Fatalf("asset Kubo timeout = %v, want 30s", client.Timeout)
	}
}

func TestAssetGLBAddClientTimeoutStopsStalledResponse(t *testing.T) {
	assetPath := filepath.Join(t.TempDir(), "vehicle.glb")
	if err := os.WriteFile(assetPath, []byte("glTF fixture"), 0o600); err != nil {
		t.Fatalf("write asset fixture: %v", err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer server.Close()

	client := &http.Client{Timeout: 50 * time.Millisecond}
	startedAt := time.Now()
	_, err := addAssetGLBWithClient(context.Background(), client, server.URL, assetPath, assetKuboAddOptions{OnlyHash: true})
	if err == nil {
		t.Fatal("asset add succeeded with stalled Kubo response")
	}
	if elapsed := time.Since(startedAt); elapsed > time.Second {
		t.Fatalf("stalled asset add returned after %v, want bounded client timeout", elapsed)
	}
}

func TestCalculateAssetGLBCIDHonorsCallerCancellation(t *testing.T) {
	assetPath := filepath.Join(t.TempDir(), "vehicle.glb")
	if err := os.WriteFile(assetPath, []byte("glTF fixture"), 0o600); err != nil {
		t.Fatalf("write asset fixture: %v", err)
	}
	responseStarted := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		close(responseStarted)
		<-r.Context().Done()
	}))
	defer server.Close()

	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		_, err := CalculateAssetGLBCID(ctx, server.URL, assetPath)
		result <- err
	}()
	select {
	case <-responseStarted:
	case <-time.After(time.Second):
		t.Fatal("Kubo response did not start")
	}
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("CalculateAssetGLBCID error = %v, want context canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("CalculateAssetGLBCID did not return after cancellation")
	}
}

func TestAssetGLBAddBoundsNonSuccessResponse(t *testing.T) {
	assetPath := filepath.Join(t.TempDir(), "vehicle.glb")
	if err := os.WriteFile(assetPath, []byte("glTF fixture"), 0o600); err != nil {
		t.Fatalf("write asset fixture: %v", err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write(bytes.Repeat([]byte("x"), maxAssetKuboAddResponseBytes*2))
	}))
	defer server.Close()

	_, err := CalculateAssetGLBCID(context.Background(), server.URL, assetPath)
	if err == nil {
		t.Fatal("CalculateAssetGLBCID succeeded, want non-2xx error")
	}
	if !strings.Contains(err.Error(), "[truncated]") {
		t.Fatalf("error missing truncation marker: %v", err)
	}
	if len(err.Error()) > maxAssetKuboAddResponseBytes+512 {
		t.Fatalf("error length = %d, want bounded preview", len(err.Error()))
	}
}

func TestAssetGLBAddEarlyExitDoesNotLeakMultipartWriter(t *testing.T) {
	assetPath := filepath.Join(t.TempDir(), "vehicle.glb")
	if err := os.WriteFile(assetPath, bytes.Repeat([]byte("x"), 1<<20), 0o600); err != nil {
		t.Fatalf("write asset fixture: %v", err)
	}
	tests := []struct {
		name   string
		client assetKuboDoer
	}{
		{
			name: "transport error",
			client: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				return nil, errors.New("transport failed before reading body")
			})},
		},
		{
			name: "early response",
			client: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode: http.StatusBadRequest,
					Status:     "400 Bad Request",
					Header:     make(http.Header),
					Body:       io.NopCloser(strings.NewReader("rejected")),
					Request:    req,
				}, nil
			})},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runtime.GC()
			baseline := runtime.NumGoroutine()
			for attempt := 0; attempt < 20; attempt++ {
				result := make(chan error, 1)
				go func() {
					_, err := addAssetGLBWithClient(context.Background(), test.client, "https://kubo.invalid", assetPath, assetKuboAddOptions{Pin: true})
					result <- err
				}()
				select {
				case err := <-result:
					if err == nil {
						t.Fatal("asset add succeeded, want early-exit error")
					}
				case <-time.After(time.Second):
					t.Fatal("asset add hung waiting for multipart writer")
				}
			}

			deadline := time.Now().Add(time.Second)
			for runtime.NumGoroutine() > baseline+4 && time.Now().Before(deadline) {
				runtime.GC()
				time.Sleep(10 * time.Millisecond)
			}
			if got := runtime.NumGoroutine(); got > baseline+4 {
				t.Fatalf("goroutines after early exits = %d, baseline %d", got, baseline)
			}
		})
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

func TestUnpinAssetCIDTreatsCompleteNotPinnedResponseAsSuccess(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = io.WriteString(w, "Error: CID is not pinned")
	}))
	defer server.Close()

	if err := UnpinAssetCID(context.Background(), server.URL, testAssetCID); err != nil {
		t.Fatalf("UnpinAssetCID failed for already-missing pin: %v", err)
	}
}

func TestUnpinAssetCIDRejectsTruncatedNotPinnedPrefix(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = io.WriteString(w, "not pinned "+strings.Repeat("x", maxKuboCommandErrorBodyBytes*2))
	}))
	defer server.Close()

	err := UnpinAssetCID(context.Background(), server.URL, testAssetCID)
	if err == nil {
		t.Fatal("UnpinAssetCID accepted truncated not-pinned prefix")
	}
	if !strings.Contains(err.Error(), "[truncated]") {
		t.Fatalf("error = %q, want explicit truncation marker", err)
	}
}

func TestPostKuboCommandClientTimeoutStopsStalledResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer server.Close()

	client := &http.Client{Timeout: 50 * time.Millisecond}
	startedAt := time.Now()
	err := postKuboCommandWithClient(context.Background(), client, server.URL, "/api/v0/pin/rm", url.Values{"arg": {testAssetCID}})
	if err == nil {
		t.Fatal("Kubo command succeeded with stalled response")
	}
	if elapsed := time.Since(startedAt); elapsed > time.Second {
		t.Fatalf("stalled Kubo command returned after %v, want bounded client timeout", elapsed)
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
	client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Status:     "200 OK",
			Header:     make(http.Header),
			Body:       body,
			Request:    req,
		}, nil
	})}

	if err := postKuboCommandWithClient(context.Background(), client, "https://kubo.invalid", "/api/v0/pin/rm", url.Values{"arg": {testAssetCID}}); err != nil {
		t.Fatalf("postKuboCommandWithClient failed: %v", err)
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
	client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		close(requestStarted)
		<-req.Context().Done()
		return nil, req.Context().Err()
	})}

	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		result <- postKuboCommandWithClient(ctx, client, "https://kubo.invalid", "/api/v0/pin/rm", url.Values{"arg": {testAssetCID}})
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

const (
	// testIPNSName mirrors a kubo name/publish response Name field for the
	// daemon's own identity (host-01's production IPNS name).
	testIPNSName = "k51qzi5uqu5dknk4691lf0hb9u8yqtly1cw2nvhuvdsrb5nem0b1695tl4xp52"
)

func TestPublishIPNSNamePostsNamePublishForDaemonOwnIdentity(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.Method != http.MethodPost {
			t.Fatalf("method = %q, want POST", r.Method)
		}
		if r.URL.Path != "/api/v0/name/publish" {
			t.Fatalf("path = %q, want /api/v0/name/publish", r.URL.Path)
		}
		query := r.URL.Query()
		if got := query.Get("arg"); got != "/ipfs/"+testAssetCID {
			t.Fatalf("arg = %q, want /ipfs/%s", got, testAssetCID)
		}
		if got := query.Get("lifetime"); got != ipnsRecordLifetime.String() {
			t.Fatalf("lifetime = %q, want %s", got, ipnsRecordLifetime.String())
		}
		if got := query.Get("allow-offline"); got != "true" {
			t.Fatalf("allow-offline = %q, want true", got)
		}
		if got := query.Get("key"); got != "" {
			t.Fatalf("key = %q, want unset (daemon's own identity key)", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"Name":"/ipns/` + testIPNSName + `","Value":"/ipfs/` + testAssetCID + `"}`))
	}))
	defer server.Close()

	ipnsName, err := PublishIPNSName(context.Background(), server.URL, testAssetCID)
	if err != nil {
		t.Fatalf("PublishIPNSName failed: %v", err)
	}
	if ipnsName != testIPNSName {
		t.Fatalf("IPNS name = %q, want %q", ipnsName, testIPNSName)
	}
	if requests != 1 {
		t.Fatalf("requests = %d, want 1", requests)
	}
}

func TestPublishIPNSNameRejectsValueMismatch(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"Name":"/ipns/` + testIPNSName + `","Value":"/ipfs/` + testOtherCID + `"}`))
	}))
	defer server.Close()

	_, err := PublishIPNSName(context.Background(), server.URL, testAssetCID)
	if err == nil {
		t.Fatal("PublishIPNSName succeeded with mismatched Value")
	}
	if !strings.Contains(err.Error(), "want /ipfs/"+testAssetCID) {
		t.Fatalf("error = %q, want mismatch against %s", err, testAssetCID)
	}
}

func TestPublishIPNSNameRejectsInvalidInputs(t *testing.T) {
	cases := []struct {
		name string
		api  string
		cid  string
	}{
		{name: "empty api url", api: "  ", cid: testAssetCID},
		{name: "empty cid", api: "https://kubo.invalid", cid: "  "},
		{name: "invalid cid", api: "https://kubo.invalid", cid: "not-a-cid"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := PublishIPNSName(context.Background(), tc.api, tc.cid); err == nil {
				t.Fatal("PublishIPNSName succeeded, want input error")
			}
		})
	}
}

func TestPublishIPNSNameBoundsNonSuccessResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte("ipns record expires soon, republish required"))
	}))
	defer server.Close()

	_, err := PublishIPNSName(context.Background(), server.URL, testAssetCID)
	if err == nil {
		t.Fatal("PublishIPNSName succeeded, want non-2xx error")
	}
	if !strings.Contains(err.Error(), "502") || !strings.Contains(err.Error(), "republish required") {
		t.Fatalf("error = %q, want status and body", err)
	}
}

func TestPublishIPNSNameRejectsOversizedSuccessResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(bytes.Repeat([]byte("x"), maxKuboCommandErrorBodyBytes+1))
	}))
	defer server.Close()

	_, err := PublishIPNSName(context.Background(), server.URL, testAssetCID)
	if err == nil {
		t.Fatal("PublishIPNSName succeeded with oversized response")
	}
	if !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("error = %q, want response size bound complaint", err)
	}
}

func TestPublishIPNSNameClientTimeoutStopsStalledResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer server.Close()

	client := &http.Client{Timeout: 50 * time.Millisecond}
	startedAt := time.Now()
	_, err := publishIPNSNameWithClient(context.Background(), client, server.URL, testAssetCID)
	if err == nil {
		t.Fatal("PublishIPNSName succeeded with stalled response")
	}
	if elapsed := time.Since(startedAt); elapsed > time.Second {
		t.Fatalf("stalled name/publish returned after %v, want bounded client timeout", elapsed)
	}
}

func TestPublishIPNSNameHonorsContextCancellation(t *testing.T) {
	requestStarted := make(chan struct{})
	client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		close(requestStarted)
		<-req.Context().Done()
		return nil, req.Context().Err()
	})}

	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		_, err := publishIPNSNameWithClient(ctx, client, "https://kubo.invalid", testAssetCID)
		result <- err
	}()

	select {
	case <-requestStarted:
	case <-time.After(time.Second):
		t.Fatal("name/publish request did not start")
	}
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("PublishIPNSName error = %v, want context canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("PublishIPNSName did not return after cancellation")
	}
}
