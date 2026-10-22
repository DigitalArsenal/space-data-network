package caps

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
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
//	    "timeout_ms": 30000,           // total budget; host ceiling: 300000 ms
//	    "max_bytes": 16777216,           // optional response-size clamp
//	    "follow_redirects": false,      // optional; defaults to true
//	    "response_encoding": "binary", // optional; raw hostcall segment
//	}
//	→ {"status": 200, "headers": {...}, "header_values": {"Set-Cookie": ["..."]}, "body": "...", "body_encoding": "utf8|base64"}
//
// Response bodies are bounded by httpCapMaxResponseBytes (100 MB — the same
// ceiling the in-daemon ingest runner reads sources with; capability-gated,
// same trust domain). A request may clamp LOWER via "max_bytes". A response
// exceeding the effective bound is an ERROR, never a silent truncation
// (loop C.8a: a silently truncated CelesTrak catalog would otherwise ingest
// a partial batch as if it were complete).
// A body read that makes no progress for 30 seconds cancels the request. A
// single-range 206 cut short resumes from its first missing byte
// (resumeHTTPCapRange).
func NewHTTPCapFactory() modulert.CapFactory {
	return func(_ *modulert.Module) modulert.CapHandler {
		return httpCapHandle
	}
}

// NewHTTPBridgeCapFactory binds outbound requests to the invocation that owns
// the bridge. Flow instances have no Module, so module lifecycle alone cannot
// cancel their hostcalls.
func NewHTTPBridgeCapFactory() modulert.BridgeCapFactory {
	return func(_ *modulert.Module, bridge *modulert.HostBridge) modulert.CapHandler {
		return func(operation string, payload []byte) ([]byte, error) {
			return httpCapHandleContext(bridge.InvocationContext(), operation, payload)
		}
	}
}

// httpCapMaxResponseBytes is the host-policy ceiling for http.request
// response bodies (parity with internal/ingest fetchBytesWithMetadata's
// 100 MB read limit).
const httpCapMaxResponseBytes = 100 * 1024 * 1024

const (
	httpCapMaxTimeout       = 5 * time.Minute
	httpCapBodyStallTimeout = 30 * time.Second
	// httpCapRangeResumes bounds the follow-up requests one interrupted
	// single-range GET may make (resumeHTTPCapRange).
	httpCapRangeResumes = 4
)

func httpCapHandle(operation string, payload []byte) ([]byte, error) {
	return httpCapHandleContext(context.Background(), operation, payload)
}

func httpCapHandleContext(parent context.Context, operation string, payload []byte) ([]byte, error) {
	if operation != "http.request" {
		return errCapJSON(fmt.Sprintf("unknown http operation: %s", operation)), nil
	}

	var req struct {
		Method           string            `json:"method"`
		URL              string            `json:"url"`
		Headers          map[string]string `json:"headers"`
		Body             string            `json:"body"`
		BodyEncoding     string            `json:"body_encoding"`
		ResponseEncoding string            `json:"response_encoding"`
		TimeoutMs        int               `json:"timeout_ms"`
		MaxBytes         int64             `json:"max_bytes"`
		FollowRedirects  *bool             `json:"follow_redirects"`
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
	// Clamp before duration conversion so a large guest integer cannot overflow.
	if req.TimeoutMs > int(httpCapMaxTimeout/time.Millisecond) {
		req.TimeoutMs = int(httpCapMaxTimeout / time.Millisecond)
	}
	deadlineCtx, cancelDeadline := context.WithTimeout(parent, time.Duration(req.TimeoutMs)*time.Millisecond)
	defer cancelDeadline()
	ctx, cancelRead := context.WithCancelCause(deadlineCtx)
	defer cancelRead(nil)
	started := time.Now()

	// Host egress pacing (see http_egress.go): serialize per destination host
	// and honor the minimum spacing that host is owed. Pure connector policy —
	// the calling module neither sees nor controls it. Waiting for the slot is
	// bounded by the caller's own timeout budget so a jammed destination can
	// never hold a flow instance open indefinitely.
	destHost := egressHostKey(req.URL)
	release, paceWait, paceErr := sharedEgressPacer.acquire(ctx, destHost)
	if paceErr != nil {
		msg := fmt.Sprintf("egress pacing for %s stopped after %s waiting for the request slot: %v", destHost, paceWait.Round(time.Millisecond), paceErr)
		observeFetch(req.URL, 0, 0, paceWait.Milliseconds(), msg)
		return errCapJSON(msg), nil
	}
	defer release()

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
	// Publisher politeness (fbcs): for an unauthenticated GET, present the
	// validators the last 2xx for this URL carried, unless the module set its
	// own. See http_validators.go.
	applyFetchValidators(httpReq)

	client := &http.Client{}
	if req.FollowRedirects != nil && !*req.FollowRedirects {
		// Return the original response, including its body and every header.
		// In particular, do not replay a POST body on a 307 or 308 redirect.
		client.CheckRedirect = func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		}
	}
	resp, err := client.Do(httpReq)
	if err != nil {
		observeFetch(req.URL, 0, 0, time.Since(started).Milliseconds(), err.Error())
		return errCapJSON("request failed: " + err.Error()), nil
	}
	defer resp.Body.Close()
	resp.Body = &httpCapBodyReader{ReadCloser: resp.Body, ctx: ctx, cancel: cancelRead}

	// 304 Not Modified: the publisher confirmed the bytes we hold are current.
	// No body is delivered; the parser treats the status as "unchanged".
	if resp.StatusCode == http.StatusNotModified {
		observeFetch(req.URL, resp.StatusCode, 0, time.Since(started).Milliseconds(), "")
		respHeaders := make(map[string]string, len(resp.Header))
		for k := range resp.Header {
			respHeaders[k] = resp.Header.Get(k)
		}
		return okCapJSON(map[string]interface{}{
			"status":        resp.StatusCode,
			"headers":       respHeaders,
			"header_values": resp.Header.Clone(),
			"body":          "",
			"body_encoding": "utf8",
		}), nil
	}

	limit := int64(httpCapMaxResponseBytes)
	if req.MaxBytes > 0 && req.MaxBytes < limit {
		limit = req.MaxBytes
	}
	respBody, err := readHTTPCapBody(resp, limit)
	if err != nil && (errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.EOF)) {
		respBody, err = resumeHTTPCapRange(client, httpReq, resp, respBody, ctx, cancelRead)
	}
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
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		observeFetchValidators(req.URL, resp.Header.Get("ETag"), resp.Header.Get("Last-Modified"))
	}

	// Collect response headers
	respHeaders := make(map[string]string)
	for k := range resp.Header {
		respHeaders[k] = resp.Header.Get(k)
	}

	// The existing binary hostcall envelope avoids JSON/base64 expansion for
	// callers that need exact source bytes. Default responses remain unchanged.
	if req.ResponseEncoding == "binary" {
		return modulert.PreEncodedEnvelope(map[string]interface{}{
			"ok": true, "result": map[string]interface{}{
				"status": resp.StatusCode, "headers": respHeaders,
				"header_values": resp.Header.Clone(),
				"body":          map[string]int{"$bin": 0}, "body_encoding": "binary",
			},
		}, [][]byte{respBody}), nil
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
		"header_values": resp.Header.Clone(),
		"body":          bodyOut,
		"body_encoding": bodyEncoding,
	}
	return okCapJSON(result), nil
}

// Each underlying read gets an inactivity budget. Progress resets that budget;
// the request context still bounds the complete transfer, including pacing.
// Canceling net/http's request interrupts the read without a detached reader
// goroutine or a second copy of the response buffer.
type httpCapBodyReader struct {
	io.ReadCloser
	ctx    context.Context
	cancel context.CancelCauseFunc
}

func (r *httpCapBodyReader) Read(p []byte) (int, error) {
	fired := make(chan struct{})
	timer := time.AfterFunc(httpCapBodyStallTimeout, func() {
		r.cancel(fmt.Errorf("HTTP response body stalled for %s", httpCapBodyStallTimeout))
		close(fired)
	})
	n, err := r.ReadCloser.Read(p)
	if !timer.Stop() {
		<-fired
	}
	if cause := context.Cause(r.ctx); cause != nil {
		return n, cause
	}
	return n, err
}

// readHTTPCapBody reads at most limit+1 bytes of a response body. With a
// Content-Length within the limit the body lands in ONE buffer of exactly
// that size: io.ReadAll grows its slice by doubling, so a 48 MiB ranged body
// passed through 512 B..64 MiB of garbage and peaked near twice its size in
// the node's heap (host-01, 7 GB, coordinator D3; owner A1 2026-10-13).
func readHTTPCapBody(resp *http.Response, limit int64) ([]byte, error) {
	// These responses have no message body. In particular, HEAD's
	// Content-Length describes the representation a GET would return.
	if (resp.Request != nil && resp.Request.Method == http.MethodHead) ||
		(resp.StatusCode >= 100 && resp.StatusCode < 200) ||
		resp.StatusCode == http.StatusNoContent || resp.StatusCode == http.StatusNotModified {
		return nil, nil
	}
	if n := resp.ContentLength; n >= 0 && n <= limit {
		body := make([]byte, n)
		if read, err := io.ReadFull(resp.Body, body); err != nil {
			return body[:read], err
		}
		var one [1]byte
		if extra, err := io.ReadFull(resp.Body, one[:]); extra > 0 {
			return body, fmt.Errorf("the body is longer than its Content-Length %d", n)
		} else if err != nil && err != io.EOF {
			return body, err
		}
		return body, nil
	}
	return io.ReadAll(io.LimitReader(resp.Body, limit+1))
}

// resumeHTTPCapRange finishes a single-range GET whose 206 body ended before
// its Content-Length: publishers such as Zenodo close long transfers mid-body
// (host-01, 2026-10-18: 5,823,748 of 6,312,960 bytes, "unexpected EOF"). Each
// follow-up asks for the first missing byte to the range's end. It must answer
// 206 at that byte with the first answer's complete length, and with If-Range
// whenever the first answer carried a strong ETag or a Last-Modified (Zenodo's
// downloads carry neither), so a changed representation fails instead of
// mixing bytes. It runs inside the request's own deadline and pacing slot,
// makes at most httpCapRangeResumes requests, and each must make progress.
// Anything else returns the original short read and error unchanged.
func resumeHTTPCapRange(client *http.Client, req *http.Request, first *http.Response, got []byte,
	ctx context.Context, cancel context.CancelCauseFunc) ([]byte, error) {
	failed := fmt.Errorf("%w after %d of %d bytes", io.ErrUnexpectedEOF, len(got), first.ContentLength)
	start, ok := singleByteRangeStart(req.Header.Get("Range"))
	answered, total, answeredOK := contentRange(first.Header.Get("Content-Range"))
	want := first.ContentLength
	if req.Method != http.MethodGet || !ok || first.StatusCode != http.StatusPartialContent || !answeredOK || answered != start ||
		want <= 0 || int64(cap(got)) != want {
		return got, failed
	}
	validator := first.Header.Get("ETag")
	if strings.HasPrefix(validator, "W/") {
		validator = "" // If-Range needs a strong validator
	}
	if validator == "" {
		validator = first.Header.Get("Last-Modified")
	}
	body, have := got[:want], int64(len(got))
	for attempt := 1; have < want; attempt++ {
		if attempt > httpCapRangeResumes {
			return body[:have], fmt.Errorf("%w; %d range resumes did not finish it", failed, httpCapRangeResumes)
		}
		next := req.Clone(ctx)
		next.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", start+have, start+want-1))
		if validator != "" {
			next.Header.Set("If-Range", validator)
		}
		resp, err := client.Do(next)
		if err != nil {
			return body[:have], fmt.Errorf("range resume at byte %d of %d: %w", have, want, err)
		}
		at, length, atOK := contentRange(resp.Header.Get("Content-Range"))
		if resp.StatusCode != http.StatusPartialContent || !atOK || at != start+have || length != total || resp.ContentLength != want-have {
			resp.Body.Close()
			return body[:have], fmt.Errorf("range resume at byte %d of %d answered %d %q: the representation changed or the server ignores the range",
				have, want, resp.StatusCode, resp.Header.Get("Content-Range"))
		}
		reader := &httpCapBodyReader{ReadCloser: resp.Body, ctx: ctx, cancel: cancel}
		n, err := io.ReadFull(reader, body[have:])
		resp.Body.Close()
		have += int64(n)
		if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
			return body[:have], fmt.Errorf("range resume at byte %d of %d: %w", have, want, err)
		}
		if n == 0 {
			return body[:have], fmt.Errorf("%w; a range resume made no progress", failed)
		}
	}
	return body, nil
}

// singleByteRangeStart returns a from a Range of "bytes=a-b"; any other form is refused.
func singleByteRangeStart(header string) (int64, bool) {
	spec, ok := strings.CutPrefix(strings.TrimSpace(header), "bytes=")
	first, last, dash := strings.Cut(spec, "-")
	if !ok || !dash || strings.Contains(last, ",") {
		return 0, false
	}
	a, errA := strconv.ParseInt(first, 10, 64)
	b, errB := strconv.ParseInt(last, 10, 64)
	return a, errA == nil && errB == nil && a >= 0 && a <= b
}

// contentRange returns a and total from a Content-Range of "bytes a-b/total"
// (total "*", unknown, is refused).
func contentRange(header string) (start, total int64, ok bool) {
	spec, ok := strings.CutPrefix(strings.TrimSpace(header), "bytes ")
	span, length, slash := strings.Cut(spec, "/")
	first, _, dash := strings.Cut(span, "-")
	start, errStart := strconv.ParseInt(first, 10, 64)
	total, errTotal := strconv.ParseInt(length, 10, 64)
	return start, total, ok && slash && dash && errStart == nil && errTotal == nil && start >= 0 && start < total
}
