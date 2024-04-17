package pubsub

import (
	"context"
	"fmt"

	serverconfig "github.com/DigitalArsenal/space-data-network/serverconfig"
	pubsub "github.com/libp2p/go-libp2p-pubsub"
	"github.com/libp2p/go-libp2p/core/host"
)

// SetupPubSub initializes PubSub and creates a topic and subscription for each standard.
func SetupPubSub(ctx context.Context, h host.Host, pubsubChannelName string) (map[string]*pubsub.Subscription, map[string]*pubsub.Topic, error) {
	ps, err := pubsub.NewGossipSub(ctx, h)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to initialize PubSub: %w", err)
	}

	subscriptions := make(map[string]*pubsub.Subscription)
	topics := make(map[string]*pubsub.Topic)

	for _, standard := range serverconfig.Conf.Info.Standards {
		// Create topic name by concatenating base channel name with standard
		topicName := fmt.Sprintf("%s-%s", pubsubChannelName, standard)
		topic, err := ps.Join(topicName)
		if err != nil {
			fmt.Printf("failed to join topic '%s': %v\n", topicName, err)
			continue
		}

		sub, err := topic.Subscribe()
		if err != nil {
			fmt.Printf("failed to subscribe to topic '%s': %v\n", topicName, err)
			continue
		}

		topics[standard] = topic
		subscriptions[standard] = sub

		if standard == "PNM" {
			go messageRouter(ctx, sub, h.ID())
		}

	}

	return subscriptions, topics, nil
}
