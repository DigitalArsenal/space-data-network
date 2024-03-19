package spacedatastandards_utils

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"io"
)

func ReadDataFromSource(ctx context.Context, src interface{}) ([]byte, error) {
	var stream io.Reader
	switch s := src.(type) {
	case io.Reader:
		stream = s
	case []byte:
		stream = bytes.NewReader(s)
	default:
		return nil, fmt.Errorf("unsupported source type: %T", src)
	}

	// Read the total size prefix from the stream
	totalSizeBuf := make([]byte, 4)
	if _, err := io.ReadFull(stream, totalSizeBuf); err != nil {
		return nil, fmt.Errorf("failed to read total size prefix: %v", err)
	}
	totalSize := binary.LittleEndian.Uint32(totalSizeBuf)

	// Initialize a buffer to hold the incoming data, including the size prefix
	data := make([]byte, 0, totalSize+4) // +4 to account for the size prefix
	data = append(data, totalSizeBuf...)

	// Keep reading data until the buffer is filled to the expected size
	for uint32(len(data)-4) < totalSize { // -4 to account for the already included size prefix
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
			chunkSize := totalSize - uint32(len(data)-4)
			chunk := make([]byte, chunkSize)
			n, err := io.ReadFull(stream, chunk)
			if err != nil {
				return nil, fmt.Errorf("failed to read data: %v", err)
			}
			data = append(data, chunk[:n]...)
		}
	}

	return data, nil
}
