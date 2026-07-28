package caps

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/modulert"
)

// FetchObserver books one completed outbound retrieval in the node's
// operational ledger (internal/sourcemetrics). It is a pure observer: it sees
// url/status/bytes/duration and NOTHING about what the payload means. Nil
// disables the ledger. Never returns an error — bookkeeping must not be able
// to fail a fetch.
type FetchObserver func(url string, status int, bytes int64, durationMs int64, errMsg string)

var (
	fetchObserverMu sync.RWMutex
	fetchObserver   FetchObserver
)

// SetFetchObserver installs the process-wide fetch ledger hook. Pass nil to
// disable.
func SetFetchObserver(observer FetchObserver) {
	fetchObserverMu.Lock()
	fetchObserver = observer
	fetchObserverMu.Unlock()
}

func observeFetch(url string, status int, bytes, durationMs int64, errMsg string) {
	fetchObserverMu.RLock()
	observer := fetchObserver
	fetchObserverMu.RUnlock()
	if observer != nil {
		observer(url, status, bytes, durationMs, errMsg)
	}
}

// NewHTTPCapFactory returns a CapFactory for the "http" capability.
// It allows modules to make outbound HTTP requests.
//
// Supported operations:
//
//	http.request — {
//	    "method": "GET|POST|PUT|DELETE|PATCH",
//	    "url": "https://...",
//	    "headers": {"Content-Type": "application/json"},
//	    "body": "utf8 string or base64 bytes",
//	    "body_encoding": "utf8|base64",  // default: utf8
//	    "timeout_ms": 30000,
//	    "max_bytes": 16777216,           // optional response-size clamp
//	}
//	→ {"status": 200, "headers": {...}, "body": "...", "body_encoding": "utf8|base64"}
//
// Response bodies are bounded by httpCapMaxResponseBytes (100 MB — the same
// ceiling the in-daemon ingest runner reads sources with; capability-gated,
// same trust domain). A request may clamp LOWER via "max_bytes". A response
// exceeding the effective bound is an ERROR, never a silent truncation
// (loop C.8a: a silently truncated CelesTrak catalog would otherwise ingest
// a partial batch as if it were complete).
func NewHTTPCapFactory() modulert.CapFactory {
	return func(_ *modulert.Module) modulert.CapHandler {
		return httpCapHandle
	}
}

// httpCapMaxResponseBytes is the host-policy ceiling for http.request
// response bodies (parity with internal/ingest fetchBytesWithMetadata's
// 100 MB read limit).
const httpCapMaxResponseBytes = 100 * 1024 * 1024

func httpCapHandle(operation string, payload []byte) ([]byte, error) {
	if operation != "http.request" {
		return errCapJSON(fmt.Sprintf("unknown http operation: %s", operation)), nil
	}

	var req struct {
		Method       string            `json:"method"`
		URL          string            `json:"url"`
		Headers      map[string]string `json:"headers"`
		Body         string            `json:"body"`
		BodyEncoding string            `json:"body_encoding"`
		TimeoutMs    int               `json:"timeout_ms"`
		MaxBytes     int64             `json:"max_bytes"`
	}
	if err := json.Unmarshal(payload, &req); err != nil {
		return errCapJSON("invalid request payload: " + err.Error()), nil
	}

	if req.URL == "" {
		return errCapJSON("missing url"), nil
	}
	if req.Method == "" {
		req.Method = "GET"
	}
	if req.TimeoutMs <= 0 {
		req.TimeoutMs = 30000
	}

	// Host egress pacing (see http_egress.go): serialize per destination host
	// and honour the minimum spacing that host is owed. Pure connector policy —
	// the calling module neither sees nor controls it. Waiting for the slot is
	// bounded by the caller's own timeout budget so a jammed destination can
	// never hold a flow instance open indefinitely.
	destHost := egressHostKey(req.URL)
	paceCtx, paceCancel := context.WithTimeout(context.Background(), time.Duration(req.TimeoutMs)*time.Millisecond)
	release, paceWait, paceErr := sharedEgressPacer.acquire(paceCtx, destHost)
	paceCancel()
	if paceErr != nil {
		msg := fmt.Sprintf("egress pacing for %s timed out after %s waiting for the request slot", destHost, paceWait.Round(time.Millisecond))
		observeFetch(req.URL, 0, 0, paceWait.Milliseconds(), msg)
		return errCapJSON(msg), nil
	}
	defer release()

	started := time.Now()

	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(req.TimeoutMs)*time.Millisecond)
	defer cancel()

	var bodyReader io.Reader
	if req.Body != "" {
		switch req.BodyEncoding {
		case "base64":
			decoded := decodeBase64Cap(req.Body)
			bodyReader = bytes.NewReader(decoded)
		default:
			bodyReader = strings.NewReader(req.Body)
		}
	}

	httpReq, err := http.NewRequestWithContext(ctx, strings.ToUpper(req.Method), req.URL, bodyReader)
	if err != nil {
		return errCapJSON("invalid request: " + err.Error()), nil
	}

	for k, v := range req.Headers {
		httpReq.Header.Set(k, v)
	}

	client := &http.Client{Timeout: time.Duration(req.TimeoutMs) * time.Millisecond}
	resp, err := client.Do(httpReq)
	if err != nil {
		observeFetch(req.URL, 0, 0, time.Since(started).Milliseconds(), err.Error())
		return errCapJSON("request failed: " + err.Error()), nil
	}
	defer resp.Body.Close()

	limit := int64(httpCapMaxResponseBytes)
	if req.MaxBytes > 0 && req.MaxBytes < limit {
		limit = req.MaxBytes
	}
	respBody, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		observeFetch(req.URL, resp.StatusCode, int64(len(respBody)), time.Since(started).Milliseconds(), err.Error())
		return errCapJSON("failed to read response body: " + err.Error()), nil
	}
	if int64(len(respBody)) > limit {
		msg := fmt.Sprintf("response body exceeds the %d-byte limit — refusing to deliver a truncated payload", limit)
		observeFetch(req.URL, resp.StatusCode, int64(len(respBody)), time.Since(started).Milliseconds(), msg)
		return errCapJSON(msg), nil
	}
	observeFetch(req.URL, resp.StatusCode, int64(len(respBody)), time.Since(started).Milliseconds(), "")

	// Collect response headers
	respHeaders := make(map[string]string)
	for k := range resp.Header {
		respHeaders[k] = resp.Header.Get(k)
	}

	// Determine body encoding: use base64 for binary content
	bodyEncoding := "utf8"
	contentType := resp.Header.Get("Content-Type")
	isBinary := !strings.HasPrefix(contentType, "text/") &&
		!strings.Contains(contentType, "json") &&
		!strings.Contains(contentType, "xml")
	var bodyOut string
	if isBinary {
		bodyEncoding = "base64"
		bodyOut = encodeBase64Cap(respBody)
	} else {
		bodyOut = string(respBody)
	}

	result := map[string]interface{}{
		"status":        resp.StatusCode,
		"headers":       respHeaders,
		"body":          bodyOut,
		"body_encoding": bodyEncoding,
	}
	return okCapJSON(result), nil
}
