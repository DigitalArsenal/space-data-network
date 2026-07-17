package flowrt

// oem_stream.go — the provider $OEM STREAM contract + its one-call pool driver.
//
// A data-source provider module's `pull` method returns, on its
// plugin_invoke_stream output, a length-prefixed stream of aligned-binary SDS
// $OEM records (ephemeris in-memory only, never stored):
//
//	[u32le count]  then  count × ( [u32le len][ non-size-prefixed $OEM ] )
//
// Each record is exactly the byte-shape analysis/od.fit consumes on its "oem"
// input port (file identifier "$OEM" at bytes[4:8]). RunOEMStream splits the stream
// and fits every object through the pool — the single entry point the runner's
// FlowRunEngine calls per provider.

import (
	"context"
	"encoding/binary"
	"fmt"
)

// oemStreamFileID is the SDS $OEM file identifier, at bytes[4:8] of a
// non-size-prefixed OEM FlatBuffer.
const oemStreamFileID = "$OEM"

// SplitOEMStream parses a provider $OEM stream into its per-object $OEM records.
// It validates the framing (count + per-record length within bounds) and that
// every record carries the $OEM file identifier, failing closed on any
// malformation so a corrupt provider response never reaches the fitter. Records
// are copied so they outlive the (reused) provider response buffer.
func SplitOEMStream(b []byte) ([][]byte, error) {
	if len(b) < 4 {
		return nil, fmt.Errorf("flowrt: $OEM stream too short (%d bytes, need >=4 for count)", len(b))
	}
	count := binary.LittleEndian.Uint32(b[0:4])
	off := 4
	records := make([][]byte, 0, count)
	for i := uint32(0); i < count; i++ {
		if off+4 > len(b) {
			return nil, fmt.Errorf("flowrt: $OEM stream truncated reading length of record %d/%d at offset %d", i, count, off)
		}
		n := int(binary.LittleEndian.Uint32(b[off : off+4]))
		off += 4
		if n < 8 || off+n > len(b) {
			return nil, fmt.Errorf("flowrt: $OEM stream record %d has bad length %d at offset %d (stream %d bytes)", i, n, off, len(b))
		}
		rec := b[off : off+n]
		if string(rec[4:8]) != oemStreamFileID {
			return nil, fmt.Errorf("flowrt: $OEM stream record %d is not a $OEM (file id %q)", i, string(rec[4:8]))
		}
		out := make([]byte, n)
		copy(out, rec)
		records = append(records, out)
		off += n
	}
	if off != len(b) {
		return nil, fmt.Errorf("flowrt: $OEM stream has %d trailing bytes after %d records", len(b)-off, count)
	}
	return records, nil
}

// RunOEMStream splits a provider $OEM stream and fits every object through the pool
// (feeder -> od.fit -> store), persisting results via sink. An empty stream (0
// records) is a no-op. This is the single pool-facing entry point the runner calls
// per provider after invoking it.
func RunOEMStream(ctx context.Context, pool *FlowPool, stream []byte, sink StoreSink, cfg OEMBatchConfig) (*OEMBatchResult, error) {
	records, err := SplitOEMStream(stream)
	if err != nil {
		return nil, err
	}
	if len(records) == 0 {
		return &OEMBatchResult{}, nil
	}
	return RunOEMBatch(ctx, pool, records, sink, cfg)
}
