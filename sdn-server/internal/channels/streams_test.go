package channels

import (
	"encoding/binary"
	"testing"
)

func TestNativeStreamRegistryStoresDispatcherFrames(t *testing.T) {
	t.Parallel()

	channel := mustParseChannelID(t, "spaceaware-OMM")
	registry := NewNativeStreamRegistry()
	stream := append(nativeStreamFrame("OMM1", []byte{1, 2}), nativeStreamFrame("OMM1", []byte{3, 4, 5})...)

	snapshot, err := registry.Store(channel, stream)
	if err != nil {
		t.Fatalf("store native stream: %v", err)
	}
	if snapshot.ByteCount != len(stream) || snapshot.FrameCount != 2 {
		t.Fatalf("unexpected snapshot counts: %#v", snapshot)
	}

	got, ok := registry.Get(channel)
	if !ok {
		t.Fatal("stored native stream was not found")
	}
	if got.ByteCount != len(stream) || got.FrameCount != 2 {
		t.Fatalf("unexpected stored counts: %#v", got)
	}
	if string(got.Bytes) != string(stream) {
		t.Fatal("stored native stream bytes changed")
	}
}

func TestNativeStreamRegistryRejectsMalformedDispatcherFrames(t *testing.T) {
	t.Parallel()

	channel := mustParseChannelID(t, "spaceaware-OMM")
	registry := NewNativeStreamRegistry()

	for _, tc := range []struct {
		name  string
		bytes []byte
	}{
		{name: "truncated header", bytes: []byte{1, 2, 3}},
		{name: "too small", bytes: []byte{3, 0, 0, 0, 'O', 'M', 'M'}},
		{name: "truncated frame", bytes: []byte{8, 0, 0, 0, 'O', 'M', 'M', '1'}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if _, err := registry.Store(channel, tc.bytes); err == nil {
				t.Fatal("expected malformed native stream to be rejected")
			}
		})
	}
}

func TestNativeStreamRegistryRejectsFramesOutsideChannelStandard(t *testing.T) {
	t.Parallel()

	channel := mustParseChannelID(t, "spaceaware-OMM")
	registry := NewNativeStreamRegistry()
	stream := append(nativeStreamFrame("OMM1", []byte{1, 2}), nativeStreamFrame("CDM1", []byte{3, 4})...)

	if _, err := registry.Store(channel, stream); err == nil {
		t.Fatal("expected mixed standard native stream to be rejected")
	}
}

func mustParseChannelID(t *testing.T, id string) ChannelID {
	t.Helper()
	parsed, err := ParseChannelID(id)
	if err != nil {
		t.Fatalf("parse channel ID: %v", err)
	}
	return parsed
}

func nativeStreamFrame(fileIdentifier string, payload []byte) []byte {
	frame := make([]byte, 8+len(payload))
	binary.LittleEndian.PutUint32(frame[:4], uint32(4+len(payload)))
	copy(frame[4:8], []byte(fileIdentifier))
	copy(frame[8:], payload)
	return frame
}
