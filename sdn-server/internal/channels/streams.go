package channels

import (
	"encoding/binary"
	"fmt"
	"strings"
	"sync"
	"time"
)

type NativeStreamSnapshot struct {
	ChannelID  string
	Bytes      []byte
	ByteCount  int
	FrameCount int
	UpdatedAt  time.Time
}

type NativeStreamRegistry struct {
	mu      sync.RWMutex
	streams map[string]NativeStreamSnapshot
}

func NewNativeStreamRegistry() *NativeStreamRegistry {
	return &NativeStreamRegistry{streams: make(map[string]NativeStreamSnapshot)}
}

func (r *NativeStreamRegistry) Store(channel ChannelID, stream []byte) (NativeStreamSnapshot, error) {
	frames, err := SplitNativeStreamFramesForChannel(channel, stream)
	if err != nil {
		return NativeStreamSnapshot{}, err
	}
	snapshot := NativeStreamSnapshot{
		ChannelID:  channel.ChannelID,
		Bytes:      append([]byte(nil), stream...),
		ByteCount:  len(stream),
		FrameCount: len(frames),
		UpdatedAt:  time.Now().UTC(),
	}
	if r == nil {
		return snapshot, nil
	}
	r.mu.Lock()
	r.streams[channel.ChannelID] = snapshot
	r.mu.Unlock()
	return snapshot, nil
}

func (r *NativeStreamRegistry) Get(channel ChannelID) (NativeStreamSnapshot, bool) {
	if r == nil {
		return NativeStreamSnapshot{}, false
	}
	r.mu.RLock()
	snapshot, ok := r.streams[channel.ChannelID]
	r.mu.RUnlock()
	if !ok {
		return NativeStreamSnapshot{}, false
	}
	snapshot.Bytes = append([]byte(nil), snapshot.Bytes...)
	return snapshot, true
}

func CountNativeStreamFrames(stream []byte) (int, error) {
	frames, err := SplitNativeStreamFrames(stream)
	if err != nil {
		return 0, err
	}
	return len(frames), nil
}

func SplitNativeStreamFrames(stream []byte) ([][]byte, error) {
	return splitNativeStreamFrames(stream, "")
}

func SplitNativeStreamFramesForChannel(channel ChannelID, stream []byte) ([][]byte, error) {
	return splitNativeStreamFrames(stream, channel.StandardCode)
}

func splitNativeStreamFrames(stream []byte, expectedStandardCode string) ([][]byte, error) {
	if len(stream) == 0 {
		return nil, fmt.Errorf("native FlatBuffer stream is empty")
	}
	expectedStandardCode = strings.TrimSpace(expectedStandardCode)
	offset := 0
	frames := make([][]byte, 0)
	for offset < len(stream) {
		if len(stream)-offset < 4 {
			return nil, fmt.Errorf("truncated native FlatBuffer stream header at offset %d", offset)
		}
		size := int(binary.LittleEndian.Uint32(stream[offset : offset+4]))
		if size < 4 {
			return nil, fmt.Errorf("invalid native FlatBuffer stream frame size %d at offset %d", size, offset)
		}
		frameEnd := offset + 4 + size
		if frameEnd > len(stream) {
			return nil, fmt.Errorf("truncated native FlatBuffer stream frame at offset %d", offset)
		}
		fileIdentifier, identifierOffset, ok := nativeStreamFrameFileIdentifier(stream, offset, frameEnd)
		if !ok {
			return nil, fmt.Errorf("invalid native FlatBuffer file identifier at offset %d", identifierOffset)
		}
		if expectedStandardCode != "" {
			frameStandardCode, ok := nativeFrameStandardCode(fileIdentifier)
			if !ok || frameStandardCode != expectedStandardCode {
				return nil, fmt.Errorf("native FlatBuffer file identifier %q does not match channel standardCode %s", fileIdentifier, expectedStandardCode)
			}
		}
		frames = append(frames, append([]byte(nil), stream[offset:frameEnd]...))
		offset = frameEnd
	}
	return frames, nil
}

func nativeStreamFrameFileIdentifier(stream []byte, offset, frameEnd int) (string, int, bool) {
	legacyOffset := offset + 4
	if legacyOffset+4 <= frameEnd && isFourByteFileIdentifier(stream[legacyOffset:legacyOffset+4]) {
		return string(stream[legacyOffset : legacyOffset+4]), legacyOffset, true
	}
	flatBufferOffset := offset + 12
	if flatBufferOffset+4 <= frameEnd && isFourByteFileIdentifier(stream[flatBufferOffset:flatBufferOffset+4]) {
		return string(stream[flatBufferOffset : flatBufferOffset+4]), flatBufferOffset, true
	}
	return "", legacyOffset, false
}

func nativeFrameStandardCode(fileIdentifier string) (string, bool) {
	fileIdentifier = strings.TrimSpace(fileIdentifier)
	if len(fileIdentifier) != 4 {
		return "", false
	}
	if strings.HasPrefix(fileIdentifier, "$") {
		code := fileIdentifier[1:]
		return code, standardCodePattern.MatchString(code)
	}
	code := fileIdentifier[:3]
	return code, standardCodePattern.MatchString(code)
}

func isFourByteFileIdentifier(value []byte) bool {
	if len(value) != 4 {
		return false
	}
	for _, b := range value {
		if b < 0x20 || b > 0x7e {
			return false
		}
	}
	return true
}
