package pubsub

import (
	"context"
	"fmt"

	"github.com/DigitalArsenal/space-data-network/internal/node/sds_utils"
	"github.com/DigitalArsenal/space-data-network/internal/spacedatastandards/PNM"
	pubsub "github.com/libp2p/go-libp2p-pubsub"
	"github.com/libp2p/go-libp2p/core/peer"
)

type Plugin interface {
	Test(*pubsub.Message, *PNM.PNM) bool
	Main(*pubsub.Message, *PNM.PNM)
}

var plugins = []Plugin{
	&ServerEPM{},
}

func messageRouter(ctx context.Context, sub *pubsub.Subscription, selfID peer.ID) {
	if sub == nil {
		fmt.Println("Error: subscription is nil")
		return
	}

	for {
		m, err := sub.Next(ctx)
		if err != nil {
			// Check if the context has been canceled
			if ctx.Err() != nil {
				fmt.Println("Context is done, stopping message processing")
				fmt.Println(err.Error())
				return
			}
			fmt.Println("Error reading next message: ", err.Error())
			continue
		}

		// Skip processing if the message is from the node itself
		if m.ReceivedFrom == selfID {
			//fmt.Println("Skipping message from self")
			continue
		}

		pnm, _ := sds_utils.DeserializePNM(ctx, m.Data)
		for _, plugin := range plugins {
			if plugin.Test(m, pnm) {
				plugin.Main(m, pnm)
			}
		}
	}
}
