package main

import (
	"github.com/spacedatanetwork/sdn-server/internal/api"
	"github.com/spacedatanetwork/sdn-server/internal/node"
)

// laneFollows is what the node has received from the producers it follows,
// for the lanes' $DSS.
func laneFollows(n *node.Node) func() []api.LaneFollow {
	return func() []api.LaneFollow {
		statuses := n.FollowStatuses()
		out := make([]api.LaneFollow, 0, len(statuses))
		for _, s := range statuses {
			out = append(out, api.LaneFollow{Schema: s.Schema, ProviderID: s.ProviderID, SourceName: s.SourceName, Producer: s.Producer,
				ReceivedBytes: s.ReceivedBytes, ReceivedRecords: s.ReceivedRecords, ReceiveBytesPerSecond: s.ReceiveBytesPerSecond,
				TransferMs: s.TransferMs, VerifyMs: s.VerifyMs, StoreMs: s.StoreMs, LastReceivedAt: s.LastReceivedAt,
				Connected: s.Connected, Error: s.LastError})
		}
		return out
	}
}

// laneServed is what the node has served to its followers, for the lanes'
// $DSS.
func laneServed(n *node.Node) func() []api.LaneServed {
	return func() []api.LaneServed {
		served := n.ServedFollows()
		out := make([]api.LaneServed, 0, len(served))
		for lane, s := range served {
			out = append(out, api.LaneServed{Schema: lane.Schema, ProviderID: lane.ProviderID, SourceName: lane.SourceName,
				Subscribers: s.Subscribers, ServedBytes: s.ServedBytes, ServeBytesPerSecond: s.ServeBytesPerSecond})
		}
		return out
	}
}
