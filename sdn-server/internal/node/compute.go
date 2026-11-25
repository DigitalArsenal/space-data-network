package node

import (
	"context"
	"encoding/binary"
	"io"
	"runtime/debug"
	"time"

	"github.com/libp2p/go-libp2p/core/network"
	libp2pprotocol "github.com/libp2p/go-libp2p/core/protocol"

	"github.com/spacedatanetwork/sdn-server/internal/compute"
	"github.com/spacedatanetwork/sdn-server/internal/license"
	"github.com/spacedatanetwork/sdn-server/internal/modulert"
	"github.com/spacedatanetwork/sdn-server/internal/syncwire"
)

// ComputeInvokeProtocolID is the libp2p stream protocol for subscription-gated
// remote WASM compute offload over FlatBuffers.
const ComputeInvokeProtocolID libp2pprotocol.ID = "/space-data-network/compute-invoke/1.0.0"

const (
	maxP2PComputeRequestBytes = 16 << 20
	computeStreamReadTimeout  = 30 * time.Second
)

// ComputeService returns the node's generic compute offload service.
func (n *Node) ComputeService() *compute.Service {
	if n == nil {
		return nil
	}
	return n.computeSvc
}

// EntitlementStore returns the node's FlatSQL-backed entitlement store.
func (n *Node) EntitlementStore() *license.EntitlementStore {
	if n == nil {
		return nil
	}
	return n.entitlements
}

func (n *Node) initComputeService() {
	if n == nil {
		return
	}
	n.computeSvc = compute.NewService(
		compute.PluginManagerResolver{Manager: n.plugins},
		n.store,
		n.validator,
		n.entitlements,
		func() *license.GrantPolicyConfig {
			if n.pluginRegistry != nil {
				return n.pluginRegistry.GrantPolicyConfig()
			}
			return &license.GrantPolicyConfig{}
		},
	)
	if n.host != nil {
		n.host.SetStreamHandler(ComputeInvokeProtocolID, n.handleComputeInvokeStream)
	}
}

func (n *Node) handleComputeInvokeStream(s network.Stream) {
	defer s.Close()
	defer func() {
		if recovered := recover(); recovered != nil {
			log.Errorf("Compute invoke stream from %s dropped: handler panicked: %v\n%s",
				s.Conn().RemotePeer(), recovered, debug.Stack())
			_ = s.Reset()
		}
	}()

	if n == nil || n.computeSvc == nil {
		_, _ = s.Write(syncwire.ErrorFrame(syncwire.QRPStatusError, "compute_unavailable", "compute service unavailable", 503, 0))
		return
	}

	_ = s.SetReadDeadline(time.Now().Add(computeStreamReadTimeout))

	peerID := ""
	if conn := s.Conn(); conn != nil {
		peerID = conn.RemotePeer().String()
	}

	ent, authErr := n.computeSvc.AuthorizePeerID(peerID)
	if authErr != nil {
		_, _ = s.Write(syncwire.ErrorFrame(syncwire.QRPStatusError, authErr.Code, authErr.Message, authErr.HTTPStatus, 0))
		return
	}

	// Read either a [u32le len][payload] frame or raw stream up to maxP2PComputeRequestBytes.
	reqBytes, err := readBoundedComputeStreamRequest(s, maxP2PComputeRequestBytes)
	if err != nil {
		_, _ = s.Write(syncwire.ErrorFrame(syncwire.QRPStatusError, "bad_request", err.Error(), 400, 0))
		return
	}

	ctx, cancel := context.WithTimeout(n.ctx, 2*time.Minute)
	defer cancel()

	_, invokeErr := n.computeSvc.Invoke(ctx, ent, "", reqBytes, func(_ compute.InvokeMetadata, frame modulert.InvokeOutputFrame) error {
		_, werr := s.Write(compute.WriteSizePrefixedFrame(frame.Payload))
		return werr
	})
	if invokeErr != nil {
		_, _ = s.Write(syncwire.ErrorFrame(syncwire.QRPStatusError, invokeErr.Code, invokeErr.Message, invokeErr.HTTPStatus, 0))
	}
}

func readBoundedComputeStreamRequest(r io.Reader, maxBytes int64) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(r, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maxBytes {
		return nil, io.ErrShortBuffer
	}
	if len(data) >= 12 {
		prefix := binary.LittleEndian.Uint32(data[:4])
		if int64(prefix) == int64(len(data))-4 {
			return data, nil
		}
	}
	return data, nil
}
