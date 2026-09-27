package flatsqlrt

import (
	"fmt"
	"strings"
	"testing"

	"github.com/spacedatanetwork/sdn-server/internal/wasmrt"
)

// Sparse guest memory exercises the wasm32 ceiling without allocating 4 GiB.
type cstringMemory struct {
	wasmrt.GuestCaller
	size uint64
	ptr  uint32
	data []byte
}

func (m cstringMemory) MemoryStats() (wasmrt.MemoryStats, error) {
	return wasmrt.MemoryStats{Bytes: m.size}, nil
}

func (m cstringMemory) ReadMemory(offset, length uint32) ([]byte, error) {
	end := uint64(offset) + uint64(length)
	if end > m.size {
		return nil, fmt.Errorf("out of bounds: %d+%d > %d", offset, length, m.size)
	}
	out := make([]byte, length)
	for i := range out {
		pos := uint64(offset) + uint64(i)
		if pos >= uint64(m.ptr) && pos-uint64(m.ptr) < uint64(len(m.data)) {
			out[i] = m.data[pos-uint64(m.ptr)]
		}
	}
	return out, nil
}

func TestReadCStringAtWasm32MemoryCeiling(t *testing.T) {
	const ceiling = uint64(1) << 32
	for _, tc := range []struct {
		name string
		size uint64
		ptr  uint32
		want string
	}{
		{"ordinary memory", 65536, 128, "cell-tower-bulk"},
		{"full memory low pointer", ceiling, 128, "cell-tower-bulk"},
		{"terminator at final byte", ceiling, uint32(ceiling - 4), "TBS"},
		{"multiple chunks at ceiling", ceiling, uint32(ceiling - 601), strings.Repeat("x", 600)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mem := cstringMemory{size: tc.size, ptr: tc.ptr, data: []byte(tc.want + "\x00")}
			got, err := new(Runtime).readCStringVia(mem, tc.ptr)
			if err != nil || got != tc.want {
				t.Fatalf("readCString = %q, %v; want %q", got, err, tc.want)
			}
		})
	}
}

func TestReadCStringRejectsInvalidGuestStrings(t *testing.T) {
	for _, tc := range []struct {
		name string
		ptr  uint32
		data string
	}{
		{"pointer at memory end", 1024, ""},
		{"pointer beyond memory", 2048, ""},
		{"missing terminator", 1020, "abcd"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mem := cstringMemory{size: 1024, ptr: tc.ptr, data: []byte(tc.data)}
			if got, err := new(Runtime).readCStringVia(mem, tc.ptr); err == nil {
				t.Fatalf("invalid guest string returned %q without an error", got)
			}
		})
	}
	if got, err := new(Runtime).readCStringVia(cstringMemory{}, 0); err != nil || got != "" {
		t.Fatalf("null pointer = %q, %v; want empty string", got, err)
	}
}
