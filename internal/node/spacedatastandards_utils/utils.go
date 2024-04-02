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

	// Read the table offset after the total size prefix
	tableOffsetBuf := make([]byte, 4)
	if _, err := io.ReadFull(stream, tableOffsetBuf); err != nil {
		return nil, "", fmt.Errorf("failed to read table offset: %v", err)
	}
	_ = binary.LittleEndian.Uint32(tableOffsetBuf)

	// Read the file ID right after the table offset
	fileIDBuf := make([]byte, 4)
	if _, err := io.ReadFull(stream, fileIDBuf); err != nil {
		return nil, "", fmt.Errorf("failed to read file ID: %v", err)
	}
	fileID := string(fileIDBuf)

	// Initialize a buffer to hold the incoming data, including the size prefix, table offset, and file ID
	data := make([]byte, 0, totalSize+12) // +4 for the size prefix, +4 for the table offset, +4 for the file ID
	data = append(data, totalSizeBuf...)
	data = append(data, tableOffsetBuf...)
	data = append(data, fileIDBuf...)

	fmt.Println("HEADER", data)
	// Keep reading data until the buffer is filled to the expected size
	for uint32(len(data)-4) < totalSize { // minus size prefix
		select {
		case <-ctx.Done():
			return nil, "", ctx.Err()
		default:
			remainingSize := totalSize - uint32(len(data)-12) // Calculate remaining data size
			chunkSize := min(remainingSize, 4096)             // Read in chunks of 4096 bytes or the remaining size, whichever is smaller
			chunk := make([]byte, chunkSize)
			n, err := io.ReadFull(stream, chunk)
			if err == io.EOF || err == io.ErrUnexpectedEOF {
				// If EOF is reached, append what was read and break the loop
				data = append(data, chunk[:n]...)
				break
			} else if err != nil {
				return nil, "", fmt.Errorf("failed to read data: %v", err)
			}
			data = append(data, chunk[:n]...)
		}
	}

	finalDataSize := uint32(len(data)) // Size of the data buffer including headers
	expectedSize := totalSize + 4      // Expected size including the size prefix
	if finalDataSize != expectedSize {
		return nil, "", fmt.Errorf("data size mismatch: expected %d bytes, got %d bytes", expectedSize, finalDataSize)
	}

	return data, fileID, nil
}
