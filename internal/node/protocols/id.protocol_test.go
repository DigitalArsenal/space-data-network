package protocols_test

import (
	"context"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/event"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/p2p/host/eventbus"
	"github.com/stretchr/testify/require"

	"github.com/DigitalArsenal/space-data-network/internal/node/protocols"
)

func setupHost(t *testing.T) host.Host {
	h, err := libp2p.New()
	require.NoError(t, err)
	return h
}

func TestPNMExchange(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	h1 := setupHost(t) // Assuming setupHost is a function that initializes a libp2p host for testing
	defer h1.Close()

	h2 := setupHost(t) // Same assumption as above
	defer h2.Close()

	protocols.SetupPNMExchange(h1) // Assuming this sets up the necessary protocol handlers on the host
	protocols.SetupPNMExchange(h2)

	h1ConnSub, err := h1.EventBus().Subscribe(new(event.EvtPeerConnectednessChanged), eventbus.BufSize(16))
	if err != nil {
		t.Fatal(err)
	}
	defer h1ConnSub.Close()

	h2ConnSub, err := h2.EventBus().Subscribe(new(event.EvtPeerConnectednessChanged), eventbus.BufSize(16))
	if err != nil {
		t.Fatal(err)
	}
	defer h2ConnSub.Close()

	err = h1.Connect(ctx, *host.InfoFromHost(h2))
	require.NoError(t, err)

	select {
	case <-h1ConnSub.Out():
	case <-ctx.Done():
		t.Fatal("Timed out waiting for connection event from h1")
	}

	select {
	case <-h2ConnSub.Out():
	case <-ctx.Done():
		t.Fatal("Timed out waiting for connection event from h2")
	}

	// Assuming RequestPNM initiates a protocol message exchange and returns an error if something goes wrong
	err = protocols.RequestPNM(ctx, h1, h2.ID())
	require.NoError(t, err)

	// Verify that h2 received the "PNM" message
	// This part depends on the implementation of your protocol handlers.
	// For example, you could have a variable in the protocol handler that gets set when a PNM message is received.
	// Alternatively, you could emit an event on the EventBus when a PNM message is received and subscribe to that event here.
}
