package channels

import (
	"encoding/binary"
	"fmt"
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
	frameCount, err := CountNativeStreamFrames(stream)
	if err != nil {
		return NativeStreamSnapshot{}, err
	}
	snapshot := NativeStreamSnapshot{
		ChannelID:  channel.ChannelID,
		Bytes:      append([]byte(nil), stream...),
		ByteCount:  len(stream),
		FrameCount: frameCount,
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
	if len(stream) == 0 {
		return 0, fmt.Errorf("native FlatBuffer stream is empty")
	}
	offset := 0
	frameCount := 0
	for offset < len(stream) {
		if len(stream)-offset < 4 {
			return 0, fmt.Errorf("truncated native FlatBuffer stream header at offset %d", offset)
		}
		size := int(binary.LittleEndian.Uint32(stream[offset : offset+4]))
		if size < 4 {
			return 0, fmt.Errorf("invalid native FlatBuffer stream frame size %d at offset %d", size, offset)
		}
		frameEnd := offset + 4 + size
		if frameEnd > len(stream) {
			return 0, fmt.Errorf("truncated native FlatBuffer stream frame at offset %d", offset)
		}
		if !isFourByteFileIdentifier(stream[offset+4 : offset+8]) {
			return 0, fmt.Errorf("invalid native FlatBuffer file identifier at offset %d", offset+4)
		}
		frameCount++
		offset = frameEnd
	}
	return frameCount, nil
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
