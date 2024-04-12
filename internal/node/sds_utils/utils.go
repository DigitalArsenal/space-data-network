package sds_utils

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"io"
)

// FID extracts the File ID from a FlatBuffer record.
func FID(fb []byte) string {
	if len(fb) < 12 {
		return ""
	}
	return string(fb[8:12])
}

// ReadDataFromSource checks the FlatBuffer records in the provided source and returns concatenated content if checks pass.
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

	var concatenatedFlatBuffers []byte // Slice to hold concatenated FlatBuffers
	var previousFID string
	firstRecord := true

	for {
		// Attempt to read the total size prefix.
		totalSizeBuf := make([]byte, 4)
		if _, err := io.ReadFull(stream, totalSizeBuf); err != nil {
			if err == io.EOF { // If EOF, end of data, break out of loop.
				break
			}
			return nil, fmt.Errorf("failed to read total size prefix: %v", err)
		}
		totalSize := binary.LittleEndian.Uint32(totalSizeBuf)

		// Allocate a buffer for the entire FlatBuffer data, including the total size prefix.
		flatBuffer := make([]byte, 4+totalSize) // Include space for the size prefix.
		copy(flatBuffer, totalSizeBuf)          // Copy the size prefix into the FlatBuffer.

		// Read the FlatBuffer data into the slice, starting after the size prefix.
		if _, err := io.ReadFull(stream, flatBuffer[4:]); err != nil {
			return nil, fmt.Errorf("failed to read FlatBuffer data: %v", err)
		}

		// Check the File ID.
		currentFID := FID(flatBuffer)
		if firstRecord {
			previousFID = currentFID
			firstRecord = false
		} else if currentFID != previousFID {
			return nil, fmt.Errorf("mismatched File IDs found in FlatBuffer records")
		}

		// Check that all bytes are present as per the size header.
		if int(totalSize) != len(flatBuffer[4:]) {
			return nil, fmt.Errorf("size header does not match the actual data size")
		}

		// Append the complete FlatBuffer to the concatenated slice if all checks pass.
		concatenatedFlatBuffers = append(concatenatedFlatBuffers, flatBuffer...)
	}

	return concatenatedFlatBuffers, nil // Return the concatenated FlatBuffers if all checks passed.
}
