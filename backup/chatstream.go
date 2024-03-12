package node

import (
	"bufio"
	"context"
	"fmt"
	"io"

	"os"

	pubsub "github.com/libp2p/go-libp2p-pubsub"
)

func streamConsoleTo(ctx context.Context, topic *pubsub.Topic) {
	reader := bufio.NewReader(os.Stdin)
	for {
		select {
		case <-ctx.Done():
			fmt.Println("Stopping console stream due to context cancellation.")
			return
		default:
			s, err := reader.ReadString('\n')
			if err != nil {
				if err == io.EOF {
					fmt.Println("EOF received from stdin, stopping console stream.")
					return
				}
				fmt.Printf("Error reading from stdin: %v\n", err)
				continue
			}
			if err := topic.Publish(ctx, []byte(s)); err != nil {
				fmt.Printf("Publish error: %v\n", err)
			}
		}
	}
}

func printMessagesFrom(ctx context.Context, sub *pubsub.Subscription) {
	for {
		msg, err := sub.Next(ctx)
		if err != nil {
			fmt.Printf("Failed to get next message: %v\n", err)
			return
		}
		fmt.Printf("Message from %s: %s\n", msg.ReceivedFrom, string(msg.Data))
	}
}
