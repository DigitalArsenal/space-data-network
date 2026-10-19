package syncwire

import (
	"context"
	"fmt"

	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/QRP"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/protocol"
)

// StreamOpener is what Call needs of a libp2p host.
type StreamOpener interface {
	NewStream(ctx context.Context, p peer.ID, pids ...protocol.ID) (network.Stream, error)
}

// Call opens op's stream to target, writes the one request frame and reads
// the reply's first $QRP frame. On success the caller owns the stream (the
// payload an op sends after its header is still unread) and must close it;
// a $QRP Error reply is returned as a *RemoteError with the stream closed.
func Call(ctx context.Context, h StreamOpener, target peer.ID, op string, req QRPFields) (network.Stream, *QRP.QRP, error) {
	stream, err := h.NewStream(ctx, target, protocol.ID(ProtocolID(op)))
	if err != nil {
		return nil, nil, fmt.Errorf("open %s stream to %s: %w", op, target.ShortString(), err)
	}
	if err := Request(stream, req); err != nil {
		_ = stream.Reset()
		return nil, nil, fmt.Errorf("write %s request: %w", op, err)
	}
	_ = stream.CloseWrite()
	page, _, err := ReadPage(stream)
	if err != nil {
		_ = stream.Close()
		return nil, nil, fmt.Errorf("%s reply: %w", op, err)
	}
	return stream, page, nil
}
