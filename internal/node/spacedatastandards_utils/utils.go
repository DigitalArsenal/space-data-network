package spacedatastandards_utils

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"io"
)

func ReadDataFromSource(ctx context.Context, src interface{}) ([]byte, string, error) {
	var stream io.Reader
	switch s := src.(type) {
	case io.Reader:
		stream = s
	case []byte:
		stream = bytes.NewReader(s)
	default:
		return nil, "", fmt.Errorf("unsupported source type: %T", src)
	}

	// Read the total size prefix from the stream
	totalSizeBuf := make([]byte, 4)
	if _, err := io.ReadFull(stream, totalSizeBuf); err != nil {
		return nil, "", fmt.Errorf("failed to read total size prefix: %v", err)
	}
	totalSize := binary.LittleEndian.Uint32(totalSizeBuf)

	// Read the file ID right after the total size prefix
	fileIDBuf := make([]byte, 4)
	if _, err := io.ReadFull(stream, fileIDBuf); err != nil {
		return nil, "", fmt.Errorf("failed to read file ID: %v", err)
	}
	fileID := string(fileIDBuf)

	// Initialize a buffer to hold the incoming data, including the size prefix and file ID
	data := make([]byte, 0, totalSize+8) // +4 for the size prefix, +4 for the file ID
	data = append(data, totalSizeBuf...)
	data = append(data, fileIDBuf...)

	// Keep reading data until the buffer is filled to the expected size
	for uint32(len(data)-8) < totalSize { // -8 to account for the already included size prefix and file ID
		select {
		case <-ctx.Done():
			return nil, "", ctx.Err()
		default:
			chunkSize := totalSize - uint32(len(data)-8) // -8 to account for the size prefix and file ID
			chunk := make([]byte, chunkSize)
			n, err := io.ReadFull(stream, chunk)
			if err != nil {
				return nil, "", fmt.Errorf("failed to read data: %v", err)
			}
			data = append(data, chunk[:n]...)
		}
	}

	return data, fileID, nil
}
