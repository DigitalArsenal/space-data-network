package api

import (
	"strings"
	"time"
)

// LaneFollow is one lane this node follows from a producer, as received.
type LaneFollow struct {
	Schema, ProviderID, SourceName, Producer string
	ReceivedBytes, ReceivedRecords           int64
	ReceiveBytesPerSecond                    int64
	TransferMs, VerifyMs, StoreMs            int64
	LastReceivedAt                           time.Time
	Connected                                bool
	Error                                    string
}

// LaneServed is what this node has served to the followers of one lane.
type LaneServed struct {
	Schema, ProviderID, SourceName   string
	Subscribers                      int
	ServedBytes, ServeBytesPerSecond int64
}

// followSyncProtocol names the followed stream in a lane's SYNC_PROTOCOL.
const followSyncProtocol = "flatsql-sync read-chunk follow"

// laneFollowStatus fills a lane's $DSS from what the node received from the
// lane's followed producers and served to the lane's followers.
func (h *SyncHandler) laneFollowStatus(lane *SyncLane) {
	match := func(schema, provider, source string) bool {
		return storeSchemaName(schema) == lane.Key.schema && strings.TrimSpace(provider) == lane.Key.providerID &&
			strings.TrimSpace(source) == lane.Key.sourceName
	}
	if h.deps.LaneFollows != nil {
		for _, f := range h.deps.LaneFollows() {
			if !match(f.Schema, f.ProviderID, f.SourceName) {
				continue
			}
			lane.Subscribed = true
			lane.SyncProtocol = followSyncProtocol
			if lane.ProviderPeerID == "" {
				lane.ProviderPeerID = f.Producer
			}
			lane.DownloadedBytes += uint64(max(f.ReceivedBytes, 0))
			lane.DownloadSpeed += uint64(max(f.ReceiveBytesPerSecond, 0))
			lane.TransferMs += uint64(max(f.TransferMs, 0))
			lane.VerifyMs += uint64(max(f.VerifyMs, 0))
			lane.StoreMs += uint64(max(f.StoreMs, 0))
			lane.SyncedRows += uint64(max(f.ReceivedRecords, 0))
			if !f.LastReceivedAt.IsZero() {
				at := f.LastReceivedAt.UTC().Format(time.RFC3339)
				if at > lane.LastSyncedAt {
					lane.LastSyncedAt = at
				}
			}
			if f.Error != "" && !f.Connected && lane.Error == "" {
				lane.Error = f.Error
			}
		}
	}
	if h.deps.LaneServed != nil {
		for _, s := range h.deps.LaneServed() {
			if !match(s.Schema, s.ProviderID, s.SourceName) {
				continue
			}
			lane.SubscriberCount += uint32(max(s.Subscribers, 0))
			lane.ServedBytes += uint64(max(s.ServedBytes, 0))
			lane.ServeSpeed += uint64(max(s.ServeBytesPerSecond, 0))
		}
	}
}
