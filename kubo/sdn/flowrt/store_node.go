package flowrt

// store_node.go — the OD-flow STORE node as a HOST-MODEL handler (no C++ guest
// module, no JSON control plane). The flow routes a node's aligned-binary SDS
// output ($OMM / $OCM / $OBD) into a store node; this handler persists each frame:
//   - the SDS TYPE is read from the record's OWN file_identifier ($XXX) — the
//     record is self-describing, so the control plane carries NO type field;
//   - the SOURCE lane is the node's configured value (a capability primitive of
//     the store node, not pipeline data);
//   - the (content-addressed, non-size-prefixed) record is written via StoreSink.
// This satisfies the owner rule "the store path is FlatBuffer/meta-less too — no
// JSON at any hop": the frame IS the record, the type is intrinsic, the source is
// config. sdn/sdnstore.Store satisfies StoreSink directly.

import (
	"context"
	"fmt"

	"github.com/ipfs/go-cid"
)

// StoreSink persists a content-addressed SDS record under (source, sdsType).
// *sdnstore.Store satisfies this interface.
type StoreSink interface {
	Store(ctx context.Context, source, sdsType string, fb []byte) (cid.Cid, error)
}

// NewStoreHandler returns a host-model flow-node handler that persists every SDS
// record frame it receives. `source` is the store lane (the node's config). The
// record type is derived from each frame's file_identifier; frames without a valid
// size-prefixed SDS identifier are rejected (fail-closed).
func NewStoreHandler(sink StoreSink, source string) func(context.Context, *InvocationArgs) (*InvocationResult, error) {
	return func(ctx context.Context, args *InvocationArgs) (*InvocationResult, error) {
		for _, f := range args.Frames {
			sdsType, ok := sdsTypeFromSizePrefixed(f.Bytes)
			if !ok {
				return &InvocationResult{StatusCode: 1},
					fmt.Errorf("flowrt store: frame on node %q is not a size-prefixed SDS record (len=%d)", args.NodeID, len(f.Bytes))
			}
			// Persist the content-addressed (non-size-prefixed) record: strip the
			// 4-byte little-endian size prefix, mirroring sdnruns runner.go's sized[4:].
			if _, err := sink.Store(ctx, source, sdsType, f.Bytes[4:]); err != nil {
				return &InvocationResult{StatusCode: 1},
					fmt.Errorf("flowrt store %s/%s: %w", source, sdsType, err)
			}
		}
		return &InvocationResult{StatusCode: 0}, nil
	}
}

// sdsTypeFromSizePrefixed reads the 3-letter SDS type from a size-prefixed
// FlatBuffer's file_identifier. Layout: [u32le size][FlatBuffer], and the FB's
// file_identifier "$XXX" sits at buffer offset 4..8 — i.e. bytes[8:12] here, with
// bytes[8]=='$'. Returns ("OMM", true) for a "$OMM" record.
func sdsTypeFromSizePrefixed(b []byte) (string, bool) {
	if len(b) < 12 || b[8] != '$' {
		return "", false
	}
	for _, c := range b[9:12] {
		if c < 'A' || c > 'Z' {
			return "", false
		}
	}
	return string(b[9:12]), true
}
