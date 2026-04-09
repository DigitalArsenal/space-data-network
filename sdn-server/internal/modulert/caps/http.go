package caps

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/modulert"
)

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
//	}
//	→ {"status": 200, "headers": {...}, "body": "...", "body_encoding": "utf8|base64"}
func NewHTTPCapFactory() modulert.CapFactory {
	return func(_ *modulert.Module) modulert.CapHandler {
		return httpCapHandle
	}
}

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
		return errCapJSON("request failed: " + err.Error()), nil
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 4*1024*1024))
	if err != nil {
		return errCapJSON("failed to read response body: " + err.Error()), nil
	}

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
