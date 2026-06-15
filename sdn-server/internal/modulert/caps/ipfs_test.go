package caps

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestIPFSAddAcceptsCanonicalContentBinaryPayload(t *testing.T) {
	var sawPayload bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v0/add" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		if got := r.URL.Query().Get("pin"); got != "true" {
			t.Fatalf("pin query = %q, want true", got)
		}
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Fatalf("parse multipart form: %v", err)
		}
		file, _, err := r.FormFile("file")
		if err != nil {
			t.Fatalf("read multipart file: %v", err)
		}
		defer file.Close()
		body, err := io.ReadAll(file)
		if err != nil {
			t.Fatalf("read payload: %v", err)
		}
		if string(body) != "protected module bytes" {
			t.Fatalf("payload = %q", string(body))
		}
		sawPayload = true
		_, _ = io.WriteString(w, `{"Hash":"bafy-test","Size":"22"}`)
	}))
	defer server.Close()

	handler := NewIPFSCapFactory(server.URL, server.Client())(nil)
	payload := `{"content":"` + base64.StdEncoding.EncodeToString([]byte("protected module bytes")) + `"}`
	response, err := handler("ipfs.add", []byte(payload))
	if err != nil {
		t.Fatalf("ipfs.add returned error: %v", err)
	}
	if !sawPayload {
		t.Fatal("server did not receive payload")
	}

	var envelope struct {
		OK     bool            `json:"ok"`
		Result json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(response, &envelope); err != nil {
		t.Fatalf("decode response envelope: %v", err)
	}
	if !envelope.OK {
		t.Fatalf("response ok = false: %s", string(response))
	}
	if !strings.Contains(string(envelope.Result), `"Hash":"bafy-test"`) {
		t.Fatalf("unexpected result: %s", string(envelope.Result))
	}
}
