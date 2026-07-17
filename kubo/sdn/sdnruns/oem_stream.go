package sdnruns

// oem_stream.go — the provider $OEM STREAM contract for the OD-flow run engine.
//
// A data-source provider module's `pull` method now returns, on its
// plugin_invoke_stream output, a length-prefixed stream of aligned-binary SDS
// $OEM records (ephemeris in-memory only, never stored):
//
//	[u32le count]  then  count × ( [u32le len][ non-size-prefixed $OEM ] )
//
// Each record is exactly the byte-shape analysis/od.fit consumes on its "oem"
// input port (file identifier "$OEM" at bytes[4:8]). The run engine splits this
// into the transient per-object set it feeds to the FlowPool. This mirrors the
// guest emitter in data-source/spacex-starlink-source run_pull and the JS
// parseOemStream in that module's test.

import (
	"encoding/binary"
	"fmt"
)

// oemStreamFileID is the SDS $OEM file identifier, at bytes[4:8] of a
// non-size-prefixed OEM FlatBuffer.
const oemStreamFileID = "$OEM"

// splitOEMStream parses a provider $OEM stream into its per-object $OEM records.
// It validates the framing (count + per-record length within bounds) and that
// every record carries the $OEM file identifier, failing closed on any
// malformation so a corrupt provider response never reaches the fitter.
func splitOEMStream(b []byte) ([][]byte, error) {
	if len(b) < 4 {
		return nil, fmt.Errorf("sdnruns: $OEM stream too short (%d bytes, need >=4 for count)", len(b))
	}
	count := binary.LittleEndian.Uint32(b[0:4])
	off := 4
	records := make([][]byte, 0, count)
	for i := uint32(0); i < count; i++ {
		if off+4 > len(b) {
			return nil, fmt.Errorf("sdnruns: $OEM stream truncated reading length of record %d/%d at offset %d", i, count, off)
		}
		n := int(binary.LittleEndian.Uint32(b[off : off+4]))
		off += 4
		if n < 8 || off+n > len(b) {
			return nil, fmt.Errorf("sdnruns: $OEM stream record %d has bad length %d at offset %d (stream %d bytes)", i, n, off, len(b))
		}
		rec := b[off : off+n]
		if string(rec[4:8]) != oemStreamFileID {
			return nil, fmt.Errorf("sdnruns: $OEM stream record %d is not a $OEM (file id %q)", i, string(rec[4:8]))
		}
		// Copy so the record outlives the (reused) provider response buffer.
		out := make([]byte, n)
		copy(out, rec)
		records = append(records, out)
		off += n
	}
	if off != len(b) {
		return nil, fmt.Errorf("sdnruns: $OEM stream has %d trailing bytes after %d records", len(b)-off, count)
	}
	return records, nil
}
