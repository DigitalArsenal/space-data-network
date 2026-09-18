package node

import (
	"crypto/tls"

	"github.com/libp2p/go-libp2p"

	"github.com/spacedatanetwork/sdn-server/internal/libp2ptransports"
)

// HostTransportOptions returns the transports the full node registers.
//
// The list itself lives in internal/libp2ptransports, a leaf package with no
// cgo in its dependency graph, so cmd/js-interop-host can build its host from
// the SAME definition without dragging in the node's WasmEdge binding. See
// that package's doc comment.
func HostTransportOptions(wsTLS *tls.Config) []libp2p.Option {
	return libp2ptransports.Options(wsTLS)
}
