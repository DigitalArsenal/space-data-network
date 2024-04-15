package pubsub

import (
	"context"
	"fmt"

	pubsub "github.com/libp2p/go-libp2p-pubsub"
	"github.com/libp2p/go-libp2p/core/host"
)

func SetupPubSub(ctx context.Context, h host.Host, pubsubChannelName string) (*pubsub.Subscription, *pubsub.Topic, error) {
	// Create a cancellable context that inherits from the parent context
	ps, err := pubsub.NewGossipSub(ctx, h)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to initialize PubSub: %w", err)
	}

	topic, err := ps.Join(pubsubChannelName)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to join topic 'space-data-network': %w", err)
	}

	sub, err := topic.Subscribe()
	if err != nil {
		return nil, nil, fmt.Errorf("failed to subscribe to topic: %w", err)
	}

	// Start the message router using the cancellable context
	go messageRouter(ctx, sub, h.ID())

	return sub, topic, nil
}
