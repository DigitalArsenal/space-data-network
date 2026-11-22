package main

import (
	"context"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/api"
	"github.com/spacedatanetwork/sdn-server/internal/node"
)

// laneFollows is what the node has received from the producers it follows,
// for the lanes' $DSS and the subscription list.
func laneFollows(n *node.Node) func() []api.LaneFollow {
	return func() []api.LaneFollow {
		statuses := n.FollowStatuses()
		out := make([]api.LaneFollow, 0, len(statuses))
		for _, s := range statuses {
			pending := ""
			if s.Start == node.FollowStartCurrent && s.Cursor == "" && !s.PublicationsOnly {
				pending = node.FollowStartCurrent
			}
			var unreadSince time.Time
			if !s.Unread.IsZero() {
				unreadSince = s.Unread.UTC()
			}
			out = append(out, api.LaneFollow{Schema: s.Schema, ProviderID: s.ProviderID, SourceName: s.SourceName, Producer: s.Producer,
				Since: s.Since, StartsAt: s.Start, Start: pending, Stopped: s.Stopped,
				ReceivedBytes: s.ReceivedBytes, ReceivedRecords: s.ReceivedRecords, StoredRecords: s.StoredRecords, RemovedRecords: s.RemovedRecords,
				ReceiveBytesPerSecond: s.ReceiveBytesPerSecond,
				TransferMs:            s.TransferMs, VerifyMs: s.VerifyMs, StoreMs: s.StoreMs, LastReceivedAt: s.LastReceivedAt,
				Connected: s.Connected, Error: s.LastError,
				CurrentBatch: s.CurrentBatch, StatedBatch: s.Stated.BatchID, StatedRecords: s.Stated.Records, Lag: s.Lag,
				Unread: s.UnreadText(), UnreadSince: unreadSince, KeepPublications: s.KeepPublications, PublicationsOnly: s.PublicationsOnly})
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

func lanePlan(p node.FollowPlan) api.LanePlan {
	return api.LanePlan{Schema: p.Schema, ProviderID: p.ProviderID, SourceName: p.SourceName, Producer: p.Producer, Start: p.Start, Rule: p.Rule,
		BatchID: p.Statement.BatchID, BatchRecords: p.Statement.Records, BatchBytes: p.Statement.BatchBytes,
		LaneRecords: p.Statement.LaneRecords, LaneBytes: p.Statement.LaneBytes, StatementError: p.StatementError,
		FirstReadRecords: p.FirstReadRecords, FirstReadBytes: p.FirstReadBytes, FreeBytes: p.FreeBytes, NeedBytes: p.NeedBytes,
		HeldRecords: p.HeldRecords, RecordCeiling: p.RecordCeiling, ReservedRecords: p.ReservedRecords, ReservedBytes: p.ReservedBytes,
		Refusal: p.Refusal}
}

func followRequest(req api.LaneSubscription) (node.FollowKey, node.FollowOptions) {
	return node.FollowKey{Schema: req.Schema, ProviderID: req.ProviderID, SourceName: req.SourceName, Producer: req.Producer},
		node.FollowOptions{Start: req.Start, Force: req.Force, Rule: req.Retention, AcknowledgeUnread: req.AcknowledgeUnread, Publications: req.Publications}
}

// subscribeLane follows a lane once its first read fits (sync subscribe).
func subscribeLane(n *node.Node) func(context.Context, api.LaneSubscription) (api.LanePlan, error) {
	return func(ctx context.Context, req api.LaneSubscription) (api.LanePlan, error) {
		key, opts := followRequest(req)
		plan, err := n.Subscribe(ctx, key, opts)
		return lanePlan(plan), err
	}
}

// planLaneSubscription answers a subscription's first read.
func planLaneSubscription(n *node.Node) func(context.Context, api.LaneSubscription) (api.LanePlan, error) {
	return func(ctx context.Context, req api.LaneSubscription) (api.LanePlan, error) {
		key, opts := followRequest(req)
		plan, err := n.PlanSubscription(ctx, key, opts)
		return lanePlan(plan), err
	}
}

// ledgerLanes is the removed import gate's lanes (sync subscribe
// --from-ledger).
func ledgerLanes(n *node.Node) func() ([]api.LedgerLane, error) {
	return func() ([]api.LedgerLane, error) {
		lanes, err := n.RemovedImportPlan()
		out := make([]api.LedgerLane, 0, len(lanes))
		for _, l := range lanes {
			out = append(out, api.LedgerLane{Schema: l.Schema, ProviderID: l.ProviderID, SourceName: l.SourceName, Producers: l.Producers,
				Reason: l.Reason, FromLedger: l.FromLedger, FromAllow: l.FromAllow})
		}
		return out, err
	}
}
