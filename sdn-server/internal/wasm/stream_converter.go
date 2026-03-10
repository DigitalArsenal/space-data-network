package wasm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"io"
)

// StreamConverter wraps a FlatcModule to provide streaming JSON↔FlatBuffer
// conversion. It processes records one at a time, converting between formats
// using the WASM-compiled FlatBuffers compiler.
//
// Usage pattern (publish pipeline):
//
//	converter := wasm.NewStreamConverter(flatc, schemaID)
//	records, errs := converter.JSONStreamToFlatBuffers(ctx, httpBody)
//	for _, rec := range records {
//	    store.Put(rec.Binary)
//	}
//
// Usage pattern (query response):
//
//	converter.FlatBuffersToJSONStream(ctx, storedRecords, responseWriter)
type StreamConverter struct {
	flatc    *FlatcModule
	schemaID int
}

// NewStreamConverter creates a converter bound to a specific schema.
// The schemaID must be obtained from flatc.AddSchema() or flatc.GetSchemaID().
func NewStreamConverter(flatc *FlatcModule, schemaID int) *StreamConverter {
	return &StreamConverter{
		flatc:    flatc,
		schemaID: schemaID,
	}
}

// ConvertedRecord holds the result of converting a single record.
type ConvertedRecord struct {
	// Binary is the FlatBuffer binary representation.
	Binary []byte

	// SourceJSON is the original JSON (kept for debugging/logging).
	SourceJSON []byte
}

// JSONStreamToFlatBuffers reads newline-delimited JSON from a reader and
// converts each line to FlatBuffer binary format via flatc-wasm.
//
// Processing continues past individual record errors (best-effort).
// Returns all successfully converted records and any per-record errors.
func (sc *StreamConverter) JSONStreamToFlatBuffers(ctx context.Context, reader io.Reader) ([]ConvertedRecord, []error) {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 0, 64*1024), 256*1024)

	var records []ConvertedRecord
	var errs []error
	lineNum := 0

	for scanner.Scan() {
		lineNum++
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}

		binaryData, err := sc.flatc.JSONToBinary(ctx, sc.schemaID, line)
		if err != nil {
			errs = append(errs, fmt.Errorf("line %d: %w", lineNum, err))
			continue
		}

		jsonCopy := make([]byte, len(line))
		copy(jsonCopy, line)

		records = append(records, ConvertedRecord{
			Binary:     binaryData,
			SourceJSON: jsonCopy,
		})
	}

	if err := scanner.Err(); err != nil {
		errs = append(errs, fmt.Errorf("stream read: %w", err))
	}

	return records, errs
}

// FlatBuffersToJSONStream converts FlatBuffer binary records to
// newline-delimited JSON written to the provided writer.
func (sc *StreamConverter) FlatBuffersToJSONStream(ctx context.Context, records [][]byte, writer io.Writer) (int, []error) {
	var errs []error
	written := 0

	for i, binaryData := range records {
		jsonData, err := sc.flatc.BinaryToJSON(ctx, sc.schemaID, binaryData)
		if err != nil {
			errs = append(errs, fmt.Errorf("record %d: %w", i, err))
			continue
		}

		if _, err := writer.Write(jsonData); err != nil {
			errs = append(errs, fmt.Errorf("write record %d: %w", i, err))
			return written, errs
		}
		if _, err := writer.Write([]byte("\n")); err != nil {
			errs = append(errs, fmt.Errorf("write newline %d: %w", i, err))
			return written, errs
		}
		written++
	}

	return written, errs
}

// FlatBufferBatchToWire serializes multiple FlatBuffer records into a single
// length-prefixed wire-format payload for libp2p streams.
//
// Wire format:
//
//	[record_count: uint32 LE]
//	[record_0_len: uint32 LE][record_0_data: N bytes]
//	[record_1_len: uint32 LE][record_1_data: N bytes]
//	...
func FlatBufferBatchToWire(records [][]byte) []byte {
	totalSize := 4
	for _, r := range records {
		totalSize += 4 + len(r)
	}

	buf := make([]byte, totalSize)
	offset := 0

	binary.LittleEndian.PutUint32(buf[offset:], uint32(len(records)))
	offset += 4

	for _, r := range records {
		binary.LittleEndian.PutUint32(buf[offset:], uint32(len(r)))
		offset += 4
		copy(buf[offset:], r)
		offset += len(r)
	}

	return buf
}

// WireToFlatBufferBatch deserializes a wire-format payload back into
// individual FlatBuffer records. See FlatBufferBatchToWire for format.
func WireToFlatBufferBatch(data []byte) ([][]byte, error) {
	if len(data) < 4 {
		return nil, fmt.Errorf("payload too short: need at least 4 bytes, got %d", len(data))
	}

	count := binary.LittleEndian.Uint32(data[:4])
	if count > 10000 {
		return nil, fmt.Errorf("record count %d exceeds maximum 10000", count)
	}

	offset := 4
	records := make([][]byte, 0, count)

	for i := uint32(0); i < count; i++ {
		if offset+4 > len(data) {
			return nil, fmt.Errorf("truncated at record %d header", i)
		}
		recLen := binary.LittleEndian.Uint32(data[offset:])
		offset += 4

		if offset+int(recLen) > len(data) {
			return nil, fmt.Errorf("truncated at record %d data (need %d, have %d)", i, recLen, len(data)-offset)
		}

		record := make([]byte, recLen)
		copy(record, data[offset:offset+int(recLen)])
		offset += int(recLen)

		records = append(records, record)
	}

	return records, nil
}
