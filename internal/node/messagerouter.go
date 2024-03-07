package node

import (
	"context"
	"fmt"

	pubsub "github.com/libp2p/go-libp2p-pubsub"
)

func messageRouter(ctx context.Context, sub *pubsub.Subscription) {
	for {
		msg, err := sub.Next(ctx)
		if err != nil {
			fmt.Printf("Failed to get next message: %v\n", err)
			return
		}
		fmt.Printf("Message from %s: %s\n", msg.ReceivedFrom, string(msg.Data))
	}
}
