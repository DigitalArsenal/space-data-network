package protocols

import (
	"fmt"

	flatbuffers "github.com/google/flatbuffers/go"
)

// BuildSizePrefixedFlatBuffer creates a size-prefixed FlatBuffer message using the provided builder and offset.
func BuildSizePrefixedFlatBuffer(builder *flatbuffers.Builder, offset flatbuffers.UOffsetT) []byte {
	builder.FinishSizePrefixed(offset)
	return builder.FinishedBytes()
}

// ReadIdentifier reads and returns the FlatBuffer identifier from the given buffer.
func ReadIdentifier(buffer []byte) string {
	if len(buffer) < 12 { // Minimum length to include size prefix + identifier
		return ""
	}
	return string(buffer[8:12])
}

// ExtractFlatBufferData extracts and returns the data from a size-prefixed FlatBuffer message.
func ExtractFlatBufferData(buffer []byte) ([]byte, error) {
	if len(buffer) < 4 { // Minimum length to include the size prefix
		return nil, fmt.Errorf("buffer too short to contain size prefix")
	}

	size := flatbuffers.GetSizePrefix(buffer, 0)
	if len(buffer) < int(size)+4 { // Ensure buffer is large enough to contain the size-prefixed data
		return nil, fmt.Errorf("buffer too short to contain the expected data")
	}

	data := buffer[4 : 4+size] // Skip 4 bytes of size prefix
	return data, nil
}
