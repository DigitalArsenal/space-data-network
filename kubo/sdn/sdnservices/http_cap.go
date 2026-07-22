package sdnservices

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/ipfs/kubo/sdn/modulert"
)

// NewHTTPCapFactory returns a BridgeCapFactory serving the "http" capability
// against the Go net/http client — the kubo port of the sdn-server http cap
// (sdn-server/internal/modulert/caps/http.go) so a flow's hostcap/http-request
// node can perform an outbound HTTP GET/fetch.
//
// Supported operation:
//
//	http.request — {
//	    "method": "GET|POST|...",
//	    "url": "https://...",
//	    "headers": {"Content-Type": "application/json"},
//	    "body": "utf8 string or base64 bytes",
//	    "body_encoding": "utf8|base64",  // default utf8
//	    "timeout_ms": 30000,
//	    "max_bytes": 16777216,           // optional response-size clamp
//	}
//	→ {"status":200,"headers":{...},"body":{"$bin":0}} + raw byte segment 0
//
// Fail closed: "http" is an operator-gated SENSITIVE capability (modulert
// sensitiveCapabilities), so a bridge only receives the grant after the
// capability-policy gate approves the module's content hash. Every call ALSO
// re-checks bridge.HasCapability("http") (defense in depth): a module not
// granted http cannot fetch, even if the handler were somehow reachable.
func NewHTTPCapFactory() modulert.BridgeCapFactory {
	return func(_ *modulert.Module, bridge *modulert.HostBridge) modulert.CapHandler {
		h := &httpCapAdapter{bridge: bridge}
		return h.handle
	}
}

// httpCapMaxResponseBytes is the application-blind host ceiling for one
// http.request response body. Signed nodes own fetch and pagination policy; the
// host only refuses an oversized byte response, never silently truncates it.
const httpCapMaxResponseBytes = 64 * 1024 * 1024

type httpCapAdapter struct {
	bridge *modulert.HostBridge
}

func (h *httpCapAdapter) has(cap string) bool {
	return h.bridge != nil && h.bridge.HasCapability(cap)
}

func (h *httpCapAdapter) handle(operation string, payload []byte) ([]byte, error) {
	if operation != "http.request" {
		return errCapJSON(fmt.Sprintf("unknown http operation: %s", operation)), nil
	}
	// POLICY (fail closed, defense in depth): the http grant is required even
	// to reach the network. A bridge without it never fetches.
	if !h.has("http") {
		return errCapJSON("http.request requires the http capability grant"), nil
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
	// Parent from the current trigger context (nil-safe -> Background) so caller
	// cancellation aborts this in-flight fetch.
	ctx, cancel := context.WithTimeout(h.bridge.FireContext(), time.Duration(req.TimeoutMs)*time.Millisecond)
	defer cancel()

	var bodyReader io.Reader
	if req.Body != "" {
		if req.BodyEncoding == "base64" {
			bodyReader = bytes.NewReader(decodeBase64Cap(req.Body))
		} else {
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
		return errCapJSON("request failed: " + err.Error()), nil
	}
	defer resp.Body.Close()

	limit := int64(httpCapMaxResponseBytes)
	if req.MaxBytes > 0 && req.MaxBytes < limit {
		limit = req.MaxBytes
	}
	respBody, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return errCapJSON("failed to read response body: " + err.Error()), nil
	}
	if int64(len(respBody)) > limit {
		return errCapJSON(fmt.Sprintf("response body exceeds the %d-byte limit — refusing to deliver a truncated payload", limit)), nil
	}

	respHeaders := make(map[string]string, len(resp.Header))
	for k := range resp.Header {
		respHeaders[k] = resp.Header.Get(k)
	}

	return modulert.PreEncodedEnvelope(map[string]interface{}{
		"ok": true,
		"result": map[string]interface{}{
			"status":  resp.StatusCode,
			"headers": respHeaders,
			"body":    map[string]interface{}{"$bin": 0},
		},
	}, [][]byte{respBody}), nil
}
