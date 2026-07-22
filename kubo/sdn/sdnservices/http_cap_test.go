package sdnservices

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ipfs/kubo/sdn/modulert"
)

var preEncodedHTTPEnvelopeMagic = []byte{0x00, 'S', 'D', 'N', 'E', 'N', 'V', '1'}

type decodedHTTPEnvelope struct {
	meta     map[string]interface{}
	segments [][]byte
}

func decodeHTTPEnvelope(t *testing.T, response []byte) decodedHTTPEnvelope {
	t.Helper()
	if !bytes.HasPrefix(response, preEncodedHTTPEnvelopeMagic) {
		var meta map[string]interface{}
		if err := json.Unmarshal(response, &meta); err != nil {
			t.Fatalf("cap response not JSON or a binary envelope: %v", err)
		}
		return decodedHTTPEnvelope{meta: meta}
	}

	payload := response[len(preEncodedHTTPEnvelopeMagic):]
	if len(payload) < 8 {
		t.Fatalf("binary cap envelope is truncated: %d bytes", len(payload))
	}
	offset := 0
	metaLen := int(binary.LittleEndian.Uint32(payload[offset:]))
	offset += 4
	if offset+metaLen+4 > len(payload) {
		t.Fatalf("binary cap envelope metadata exceeds bounds")
	}
	var meta map[string]interface{}
	if err := json.Unmarshal(payload[offset:offset+metaLen], &meta); err != nil {
		t.Fatalf("decode binary cap envelope metadata: %v", err)
	}
	offset += metaLen
	segmentCount := int(binary.LittleEndian.Uint32(payload[offset:]))
	offset += 4
	segments := make([][]byte, 0, segmentCount)
	for index := 0; index < segmentCount; index++ {
		if offset+4 > len(payload) {
			t.Fatalf("binary cap envelope segment table is truncated")
		}
		segmentLen := int(binary.LittleEndian.Uint32(payload[offset:]))
		offset += 4
		if offset+segmentLen > len(payload) {
			t.Fatalf("binary cap envelope segment %d exceeds bounds", index)
		}
		segments = append(segments, append([]byte(nil), payload[offset:offset+segmentLen]...))
		offset += segmentLen
	}
	if offset != len(payload) {
		t.Fatalf("binary cap envelope has %d trailing bytes", len(payload)-offset)
	}
	return decodedHTTPEnvelope{meta: meta, segments: segments}
}

// httpCall drives one http.request through the cap handler bound to a bridge
// with the given granted capabilities, returning the decoded cap envelope.
func httpCall(t *testing.T, granted []string, req map[string]interface{}) map[string]interface{} {
	t.Helper()
	bridge := modulert.NewHostBridge(&modulert.NodeContext{}, granted)
	handler := NewHTTPCapFactory()(nil, bridge)
	payload, _ := json.Marshal(req)
	resp, err := handler("http.request", payload)
	if err != nil {
		t.Fatalf("http cap handler error: %v", err)
	}
	return decodeHTTPEnvelope(t, resp).meta
}

// TestHTTPCapFetchesFromStub proves the happy path: a granted module fetches a
// stub URL and receives its body.
func TestHTTPCapFetchesFromStub(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/csv")
		_, _ = w.Write([]byte("DATE,F10.7\n2026-01-02,151.5\n"))
	}))
	defer srv.Close()

	bridge := modulert.NewHostBridge(&modulert.NodeContext{}, []string{"http"})
	handler := NewHTTPCapFactory()(nil, bridge)
	payload, _ := json.Marshal(map[string]interface{}{"url": srv.URL})
	response, err := handler("http.request", payload)
	if err != nil {
		t.Fatalf("http cap handler error: %v", err)
	}
	envelope := decodeHTTPEnvelope(t, response)
	meta := envelope.meta
	if ok, _ := meta["ok"].(bool); !ok {
		t.Fatalf("fetch failed: %v", meta)
	}
	result := meta["result"].(map[string]interface{})
	if status, _ := result["status"].(float64); status != 200 {
		t.Fatalf("status = %v, want 200", result["status"])
	}
	bodyRefObject, bodyRefOK := result["body"].(map[string]interface{})
	bodyRef, bodyIndexOK := bodyRefObject["$bin"].(float64)
	if !bodyRefOK || !bodyIndexOK || bodyRef != 0 || len(envelope.segments) != 1 {
		t.Fatalf("body = %v with %d segments, want $bin 0 with one segment", result["body"], len(envelope.segments))
	}
	if body := string(envelope.segments[0]); !strings.Contains(body, "F10.7") {
		t.Fatalf("body segment missing payload: %q", body)
	}
}

func TestHTTPCapPreservesInvalidUTF8AsExactBytes(t *testing.T) {
	want := []byte{0x00, 0xff, 0x80, 0x41, 0xc3, 0x28}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write(want)
	}))
	defer srv.Close()

	bridge := modulert.NewHostBridge(&modulert.NodeContext{}, []string{"http"})
	handler := NewHTTPCapFactory()(nil, bridge)
	payload, _ := json.Marshal(map[string]interface{}{"url": srv.URL})
	response, err := handler("http.request", payload)
	if err != nil {
		t.Fatalf("http cap handler error: %v", err)
	}
	envelope := decodeHTTPEnvelope(t, response)
	if len(envelope.segments) != 1 || !bytes.Equal(envelope.segments[0], want) {
		t.Fatalf("response bytes = %x, want %x", envelope.segments, want)
	}
}

// TestHTTPCapFailClosedWithoutGrant proves the fail-closed gate: a bridge NOT
// granted "http" cannot fetch, even calling the handler directly.
func TestHTTPCapFailClosedWithoutGrant(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("stub was contacted by a module without the http grant")
	}))
	defer srv.Close()

	meta := httpCall(t, nil /* no grant */, map[string]interface{}{"url": srv.URL})
	if ok, _ := meta["ok"].(bool); ok {
		t.Fatalf("ungranted module fetched: %v", meta)
	}
	msg, _ := meta["error"].(map[string]interface{})["message"].(string)
	if !strings.Contains(msg, "http capability grant") {
		t.Fatalf("refusal does not name the missing grant: %v", meta)
	}
}

// TestHTTPCapMaxBytesErrorsInsteadOfTruncating proves an oversized response is
// refused, never silently truncated.
func TestHTTPCapMaxBytesErrorsInsteadOfTruncating(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(make([]byte, 64*1024))
	}))
	defer srv.Close()

	meta := httpCall(t, []string{"http"}, map[string]interface{}{"url": srv.URL, "max_bytes": 32 * 1024})
	if ok, _ := meta["ok"].(bool); ok {
		t.Fatalf("oversized response delivered instead of refused: %v", meta)
	}
	msg, _ := meta["error"].(map[string]interface{})["message"].(string)
	if !strings.Contains(msg, "exceeds") {
		t.Fatalf("error does not name the size violation: %v", meta)
	}
}

// TestHTTPCapGlobalResponseCeilingIsBounded records the host-wide per-response
// byte ceiling. Fetch and pagination policy remain entirely outside the host.
func TestHTTPCapGlobalResponseCeilingIsBounded(t *testing.T) {
	const want = 64 * 1024 * 1024
	if httpCapMaxResponseBytes != want {
		t.Fatalf("http response ceiling = %d bytes, want %d", httpCapMaxResponseBytes, want)
	}
}
