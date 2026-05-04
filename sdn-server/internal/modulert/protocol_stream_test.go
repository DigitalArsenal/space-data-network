package modulert

import (
	"errors"
	"io"
	"testing"
)

type blockingAfterFirstRead struct {
	payload []byte
	read    bool
}

func (r *blockingAfterFirstRead) Read(p []byte) (int, error) {
	if r.read {
		return 0, errors.New("unexpected second read")
	}
	r.read = true
	return copy(p, r.payload), nil
}

func TestReadProtocolRequestConsumesSingleBoundedFrame(t *testing.T) {
	reader := &blockingAfterFirstRead{payload: []byte("challenge")}

	got, err := readProtocolRequest(reader, 1024)
	if err != nil {
		t.Fatalf("readProtocolRequest returned error: %v", err)
	}
	if string(got) != "challenge" {
		t.Fatalf("readProtocolRequest payload = %q, want %q", string(got), "challenge")
	}
	if !reader.read {
		t.Fatal("expected reader to be consumed")
	}
}

func TestReadProtocolRequestRejectsOversizeFrame(t *testing.T) {
	_, err := readProtocolRequest(
		&blockingAfterFirstRead{payload: []byte("too-large")},
		3,
	)
	if err == nil {
		t.Fatal("expected oversize request to fail")
	}
}

func TestReadProtocolRequestReturnsEOFWithoutPayload(t *testing.T) {
	_, err := readProtocolRequest(
		readerFunc(func([]byte) (int, error) { return 0, io.EOF }),
		1024,
	)
	if !errors.Is(err, io.EOF) {
		t.Fatalf("readProtocolRequest error = %v, want EOF", err)
	}
}

type readerFunc func([]byte) (int, error)

func (f readerFunc) Read(p []byte) (int, error) {
	return f(p)
}
