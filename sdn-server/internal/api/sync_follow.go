package api

import (
	"net/http"
	"strings"
	"time"
)

// LaneFollow is one lane this node follows from a producer, as received.
type LaneFollow struct {
	Schema     string    `json:"schema"`
	ProviderID string    `json:"provider_id"`
	SourceName string    `json:"source_name"`
	Producer   string    `json:"producer"`
	Since      time.Time `json:"subscribed_since,omitempty"`
	// StartsAt is where the subscription starts ("current": the producer's
	// current batch, "beginning": the lane's beginning); Start is "current"
	// while it waits for the producer's statement to start there.
	StartsAt string `json:"start"`
	Start    string `json:"pending_start,omitempty"`
	// Stopped is why the running guard stopped the lane before its record
	// ceiling or free disk reserve (the cursor is kept).
	Stopped               string    `json:"stopped,omitempty"`
	ReceivedBytes         int64     `json:"received_bytes"`
	ReceivedRecords       int64     `json:"received_records"`
	StoredRecords         int64     `json:"stored_records"`
	RemovedRecords        int64     `json:"removed_records"`
	ReceiveBytesPerSecond int64     `json:"receive_bytes_per_second"`
	TransferMs            int64     `json:"transfer_ms"`
	VerifyMs              int64     `json:"verify_ms"`
	StoreMs               int64     `json:"store_ms"`
	LastReceivedAt        time.Time `json:"last_received_at,omitempty"`
	Connected             bool      `json:"connected"`
	Error                 string    `json:"last_error,omitempty"`
	// CurrentBatch is the batch held as the producer's current one;
	// StatedBatch and StatedRecords the producer's latest statement; Lag the
	// stated batch's records not held as current.
	CurrentBatch  string `json:"current_batch,omitempty"`
	StatedBatch   string `json:"producer_batch,omitempty"`
	StatedRecords int64  `json:"producer_batch_records"`
	Lag           int64  `json:"lag_records"`
	// Unread says what a refused cursor left unread on a lane whose history
	// this node keeps, until the lane is subscribed from its beginning again
	// or the gap is acknowledged; UnreadSince is when.
	Unread      string    `json:"unread,omitempty"`
	UnreadSince time.Time `json:"unread_since,omitempty"`
	// KeepPublications is set when the subscription keeps the lane's
	// publications (?publications=keep): each is fetched and imported, so
	// this node serves their files.
	KeepPublications bool `json:"keep_publications,omitempty"`
	// PublicationsOnly is set when the lane is imported from its
	// publications alone (?publications=only): no stream runs, so the
	// received counts, the batches and the connection stay empty.
	PublicationsOnly bool `json:"publications_only,omitempty"`
}

// LaneSubscription is a subscription request (sync subscribe).
type LaneSubscription struct {
	Schema, ProviderID, SourceName, Producer string
	// Start is "current", "beginning" or "" (by the lane's rule).
	Start string
	Force bool
	// Retention is the rule the subscription sets on the lane ("": none);
	// it decides the default start.
	Retention string
	// AcknowledgeUnread clears what a refused cursor left unread on the
	// existing subscription (?acknowledge_unread=1).
	AcknowledgeUnread bool
	// Publications is "keep" (each of the lane's publications is fetched and
	// imported, so this node serves its files), "stream", "only" (the lane
	// is imported from its publications alone: no stream, no start), or ""
	// (a new subscription streams; an existing one is left as it is)
	// (?publications=).
	Publications string
}

// LanePlan is a subscription's first read and whether this node takes it.
type LanePlan struct {
	Schema       string `json:"schema"`
	ProviderID   string `json:"provider_id"`
	SourceName   string `json:"source_name"`
	Producer     string `json:"producer"`
	Start        string `json:"start"`
	Rule         string `json:"rule"`
	BatchID      string `json:"producer_batch,omitempty"`
	BatchRecords int64  `json:"producer_batch_records"`
	BatchBytes   int64  `json:"producer_batch_bytes"`
	// LaneRecords and LaneBytes are the lane's batch memberships as the
	// producer counts them: a record two of the lane's batches hold counts
	// in each, so they can be more than the lane's distinct records.
	LaneRecords    int64  `json:"producer_lane_records"`
	LaneBytes      int64  `json:"producer_lane_bytes"`
	StatementError string `json:"statement_error,omitempty"`
	// FirstReadRecords and FirstReadBytes are the batch (start current) or
	// the lane (start beginning).
	FirstReadRecords int64 `json:"first_read_records"`
	FirstReadBytes   int64 `json:"first_read_bytes"`
	FreeBytes        int64 `json:"free_disk_bytes"`
	NeedBytes        int64 `json:"needed_disk_bytes"`
	HeldRecords      int64 `json:"held_records"`
	RecordCeiling    int64 `json:"record_ceiling"`
	// ReservedRecords and ReservedBytes are what this node's followed lanes
	// and publication imports are storing now (store bytes; not counted in
	// FreeBytes).
	ReservedRecords int64  `json:"reserved_records"`
	ReservedBytes   int64  `json:"reserved_bytes"`
	Refusal         string `json:"refusal,omitempty"`
}

// LedgerLane is one lane of the removed import gate and its producers.
type LedgerLane struct {
	Schema     string   `json:"schema"`
	ProviderID string   `json:"provider_id,omitempty"`
	SourceName string   `json:"source_name,omitempty"`
	Producers  []string `json:"producers,omitempty"`
	Reason     string   `json:"not_converted,omitempty"`
	FromLedger bool     `json:"in_ledger"`
	FromAllow  bool     `json:"in_allow_list"`
}

// Routes of the subscription surface (admin).
const (
	SyncPlanPath          = SyncPath + "/plan"
	SyncSubscriptionsPath = SyncPath + "/subscriptions"
	SyncLedgerPath        = SyncPath + "/ledger"
)

// errorStatus is the status a subscription error answers with: 409 when the
// first read was refused, else 400.
func subscribeErrorStatus(plan LanePlan) (int, string) {
	if plan.Refusal != "" {
		return http.StatusConflict, "first_read_refused"
	}
	return http.StatusBadRequest, "bad_request"
}

// handlePlan answers a subscription's first read (GET, admin).
func (h *SyncHandler) handlePlan(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		WriteErrorFrame(w, http.StatusMethodNotAllowed, "method_not_allowed", "Use GET.", 0)
		return
	}
	if h.deps.PlanLaneSubscription == nil {
		WriteErrorFrame(w, http.StatusServiceUnavailable, "unavailable", "This node cannot follow a lane.", 0)
		return
	}
	q := r.URL.Query()
	plan, err := h.deps.PlanLaneSubscription(r.Context(), LaneSubscription{Schema: q.Get("schema"), ProviderID: q.Get("provider_id"),
		SourceName: q.Get("source"), Producer: q.Get("from"), Start: q.Get("start")})
	if err != nil {
		WriteErrorFrame(w, http.StatusBadRequest, "bad_request", "That lane cannot be subscribed: "+err.Error(), 0)
		return
	}
	writeJSON(w, http.StatusOK, plan)
}

// handleSubscriptions lists the followed lanes (GET, admin).
func (h *SyncHandler) handleSubscriptions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		WriteErrorFrame(w, http.StatusMethodNotAllowed, "method_not_allowed", "Use GET.", 0)
		return
	}
	follows := []LaneFollow{}
	if h.deps.LaneFollows != nil {
		follows = append(follows, h.deps.LaneFollows()...)
	}
	writeJSON(w, http.StatusOK, map[string]any{"generated_at": time.Now().UTC().Format(time.RFC3339), "subscriptions": follows})
}

// handleLedger lists the removed import gate's lanes (GET, admin).
func (h *SyncHandler) handleLedger(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		WriteErrorFrame(w, http.StatusMethodNotAllowed, "method_not_allowed", "Use GET.", 0)
		return
	}
	if h.deps.LedgerLanes == nil {
		WriteErrorFrame(w, http.StatusServiceUnavailable, "unavailable", "This node has no import ledger to read.", 0)
		return
	}
	lanes, err := h.deps.LedgerLanes()
	if lanes == nil {
		lanes = []LedgerLane{}
	}
	body := map[string]any{"lanes": lanes}
	if err != nil {
		body["error"] = err.Error()
	}
	writeJSON(w, http.StatusOK, body)
}

// LaneServed is what this node has served to the followers of one lane.
type LaneServed struct {
	Schema, ProviderID, SourceName   string
	Subscribers                      int
	ServedBytes, ServeBytesPerSecond int64
}

// followSyncProtocol names the followed stream in a lane's SYNC_PROTOCOL;
// publicationsSyncProtocol a lane imported from its publications alone.
const (
	followSyncProtocol       = "flatsql-sync read-chunk follow"
	publicationsSyncProtocol = "dataset publications ($PNM)"
)

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
			if f.PublicationsOnly {
				lane.SyncProtocol = publicationsSyncProtocol
			}
			// A followed lane's total is the producer's stated current
			// batch, not a publication row (none is written for it).
			if f.StatedRecords > 0 && uint64(f.StatedRecords) > lane.TotalRows {
				lane.TotalRows = uint64(f.StatedRecords)
			}
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
