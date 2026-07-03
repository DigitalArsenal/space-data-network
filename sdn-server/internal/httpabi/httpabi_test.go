package httpabi

import (
	"bytes"
	"testing"
)

func TestRequestRoundTrip(t *testing.T) {
	in := &Request{
		Method: "GET",
		Path:   "/api/v1/data/omm/bulk",
		Query:  "epoch=1782950400&limit=100&format=json",
		Headers: []Header{
			{Name: "if-none-match", Value: `W/"fnv1a64-0123456789abcdef"`},
			{Name: "accept", Value: "*/*"},
		},
		Body:   []byte{1, 2, 3, 4, 5},
		Remote: "127.0.0.1:54321",
	}
	out, err := DecodeRequest(EncodeRequest(in))
	if err != nil {
		t.Fatalf("DecodeRequest: %v", err)
	}
	if out.Method != in.Method || out.Path != in.Path || out.Query != in.Query || out.Remote != in.Remote {
		t.Fatalf("scalar fields mismatch: %+v", out)
	}
	if !bytes.Equal(out.Body, in.Body) {
		t.Fatalf("body mismatch: %v", out.Body)
	}
	// HttpHeader.NAME is a key field: the encoded vector must come back
	// sorted by name.
	if len(out.Headers) != 2 || out.Headers[0].Name != "accept" || out.Headers[1].Name != "if-none-match" {
		t.Fatalf("headers not sorted by NAME: %+v", out.Headers)
	}
	if out.Headers[1].Value != in.Headers[0].Value {
		t.Fatalf("header value mismatch: %+v", out.Headers)
	}
}

func TestResponseRoundTrip(t *testing.T) {
	in := &Response{
		Status: 304,
		Headers: []Header{
			{Name: "etag", Value: `W/"fnv1a64-deadbeefdeadbeef"`},
		},
	}
	out, err := DecodeResponse(EncodeResponse(in))
	if err != nil {
		t.Fatalf("DecodeResponse: %v", err)
	}
	if out.Status != 304 {
		t.Fatalf("status = %d, want 304", out.Status)
	}
	if len(out.Body) != 0 {
		t.Fatalf("body should be empty, got %d bytes", len(out.Body))
	}
	if len(out.Headers) != 1 || out.Headers[0].Name != "etag" || out.Headers[0].Value != in.Headers[0].Value {
		t.Fatalf("headers mismatch: %+v", out.Headers)
	}
}

func TestDecodeRejectsWrongIdentifier(t *testing.T) {
	if _, err := DecodeResponse(EncodeRequest(&Request{Method: "GET"})); err == nil {
		t.Fatal("DecodeResponse accepted a $HTQ buffer")
	}
	if _, err := DecodeRequest(EncodeResponse(&Response{Status: 200})); err == nil {
		t.Fatal("DecodeRequest accepted a $HTR buffer")
	}
}
