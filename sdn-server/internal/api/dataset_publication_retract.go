package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/sds"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
)

// DatasetPublicationRetractRequest is the body of the loopback admin route
// POST /api/v1/admin/dataset-updates/retract: one lane of this node's own
// publications, withdrawn in one step. A dry run unless Apply.
type DatasetPublicationRetractRequest struct {
	Schema     string `json:"schema"`
	ProviderID string `json:"providerId"`
	SourceName string `json:"sourceName"`
	Apply      bool   `json:"apply,omitempty"`
}

// DatasetPublicationRetractReport is what a retraction would remove (dry run)
// or removed (applied).
type DatasetPublicationRetractReport struct {
	Schema     string `json:"schema"`
	ProviderID string `json:"providerId"`
	SourceName string `json:"sourceName"`
	Applied    bool   `json:"applied"`
	// AlreadyRetracted is true when the lane was marked retracted before this
	// call (a second call only finishes what the first left).
	AlreadyRetracted bool `json:"alreadyRetracted"`
	// RetiredSeries are the lane's recorded publications.
	RetiredSeries []string `json:"retiredSeries"`
	// Records are this node's own records of the lane, by batch.
	Records        int64                            `json:"records"`
	RecordsByBatch map[string]int64                 `json:"recordsByBatch"`
	Unpin          []DatasetPublicationRetentionPin `json:"unpin"`
	UnpinBytes     int64                            `json:"unpinBytes"`
	SharedKept     []DatasetPublicationRetentionPin `json:"sharedKeptPinned"`
	RetireRows     []DatasetPublicationRetentionRow `json:"retireRows"`
	// RecordsRemoved is what the delete removed; a record the lane shares
	// with another lane or producer stays, so it can be below Records.
	RecordsRemoved int64                                      `json:"recordsRemoved,omitempty"`
	Result         *storage.DatasetPublicationRetentionResult `json:"result,omitempty"`
	RetractedAt    *time.Time                                 `json:"retractedAt,omitempty"`
}

// DatasetPublicationRetractRunner retracts a lane on demand.
type DatasetPublicationRetractRunner interface {
	RunPublicationRetract(ctx context.Context, req DatasetPublicationRetractRequest) (*DatasetPublicationRetractReport, error)
}

// handleRetract plans (dry run) or applies the retraction of a lane. Local
// daemon clients only, by the same rule as publish and retention.
func (h *DatasetPublicationHandler) handleRetract(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !requireDatasetUpdatesLocalClient(w, r, "dataset publication retract") {
		return
	}
	runner, ok := h.service.(DatasetPublicationRetractRunner)
	if !ok || h.service == nil {
		writeError(w, http.StatusServiceUnavailable, "dataset publication retract unavailable")
		return
	}
	var req DatasetPublicationRetractRequest
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64*1024))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request: "+err.Error())
		return
	}
	report, err := runner.RunPublicationRetract(r.Context(), req)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, report)
}

// retractProducers are the producer tags of this node's own records of a
// lane: its peer id, and the provider id a record without a producer is
// tagged with. Records stored from another peer's followed lane carry that
// peer's tag and are never touched.
func (s *ConcreteDatasetPublicationService) retractProducers(providerID string) []string {
	var out []string
	for _, producer := range []string{s.providerPeerID, providerID} {
		producer = strings.TrimSpace(producer)
		if producer != "" && !slices.Contains(out, producer) {
			out = append(out, producer)
		}
	}
	return out
}

// RunPublicationRetract withdraws a lane of this node's own publications in
// one step: it unpins every CID the lane's publications hold (the retention
// planner with every series retired), removes the lane's advertised windows
// and files, deletes the lane's records, and marks the lane retracted so the
// node refuses to publish it again and tells a following peer on its next
// quiet page. The dry run reports all of that and changes nothing. A second
// call finishes what a first left (an unpin kubo refused).
func (s *ConcreteDatasetPublicationService) RunPublicationRetract(ctx context.Context, req DatasetPublicationRetractRequest) (*DatasetPublicationRetractReport, error) {
	if s == nil || s.store == nil {
		return nil, fmt.Errorf("dataset store is unavailable")
	}
	schema := normalizeDatasetPublicationSchema(req.Schema)
	if err := sds.ValidateSchemaName(schema); err != nil {
		return nil, fmt.Errorf("invalid schema: %w", err)
	}
	lane := storage.DatasetPublicationLane{SchemaName: schema, ProviderID: strings.TrimSpace(req.ProviderID), SourceName: strings.TrimSpace(req.SourceName)}
	if lane.ProviderID == "" || lane.SourceName == "" {
		return nil, fmt.Errorf("providerId and sourceName are required: a retraction names one lane exactly")
	}
	unlock, err := s.hygiene.lockLane(ctx, schema, lane.ProviderID, lane.SourceName)
	if err != nil {
		return nil, err
	}
	defer unlock()

	report := &DatasetPublicationRetractReport{Schema: schema, ProviderID: lane.ProviderID, SourceName: lane.SourceName, RecordsByBatch: map[string]int64{}}
	if at, marked, err := s.store.DatasetPublicationLaneRetractedAt(lane); err != nil {
		return nil, err
	} else if marked {
		report.AlreadyRetracted = true
		report.RetractedAt = &at
	}
	plan, err := s.store.PlanDatasetPublicationRetention(lane, s.providerPeerID, storage.DatasetPublicationRetentionPolicy{RetractAll: true})
	if err != nil {
		return nil, err
	}
	report.RetiredSeries = plan.RetiredSeries
	for _, entry := range plan.Unpin {
		report.Unpin = append(report.Unpin, DatasetPublicationRetentionPin{CID: entry.CID, Role: entry.Role, BatchID: entry.BatchID, Bytes: entry.ByteCount})
		report.UnpinBytes += entry.ByteCount
	}
	for _, entry := range plan.Shared {
		report.SharedKept = append(report.SharedKept, DatasetPublicationRetentionPin{CID: entry.CID, Role: entry.Role, BatchID: entry.BatchID, Bytes: entry.ByteCount})
	}
	for _, row := range plan.RetireRows {
		report.RetireRows = append(report.RetireRows, DatasetPublicationRetentionRow{BatchID: row.BatchID, Offset: row.Offset, Limit: row.Limit, Records: row.RecordCount, ShardCID: row.ShardCID})
	}
	producers := s.retractProducers(lane.ProviderID)
	for _, producer := range producers {
		counts, err := s.store.FollowLaneBatchCounts(schema, lane.ProviderID, lane.SourceName, producer)
		if err != nil {
			return nil, err
		}
		for batch, n := range counts {
			report.RecordsByBatch[batch] += n
			report.Records += n
		}
	}
	if !req.Apply {
		return report, nil
	}

	// The mark first: from here the lane is not published again and a
	// follower is told, whatever a later step leaves for a second call.
	if err := s.store.MarkDatasetPublicationLaneRetracted(lane, s.now()); err != nil {
		return report, err
	}
	at, _, err := s.store.DatasetPublicationLaneRetractedAt(lane)
	if err != nil {
		return report, err
	}
	report.RetractedAt = &at
	result, err := s.executePublicationRetention(ctx, lane, plan)
	if err != nil {
		return report, err
	}
	report.Result = &result
	s.releaseRedundantShardGroupCARs(ctx, lane)
	batches := make([]string, 0, len(report.RecordsByBatch))
	for batch := range report.RecordsByBatch {
		batches = append(batches, batch)
	}
	sort.Strings(batches)
	for _, producer := range producers {
		for _, batch := range batches {
			cids, err := s.store.FollowLaneBatchCIDs(schema, lane.ProviderID, lane.SourceName, batch, producer)
			if err != nil {
				return report, err
			}
			removed, err := s.store.RemoveFollowLaneRecords(schema, lane.ProviderID, lane.SourceName, producer, func(string) bool { return true }, cids)
			report.RecordsRemoved += removed
			if err != nil {
				return report, err
			}
		}
	}
	// The channel of the lane (one per provider and standard) goes when no
	// other source of the provider still advertises that standard.
	if forgetter, ok := s.channelRecorder.(DatasetPublicationChannelForgetter); ok {
		others, err := s.store.ListDatasetShardPublications(storage.DatasetShardPublicationQuery{SchemaName: schema, ProviderID: lane.ProviderID, QueryProfile: storage.DatasetPublicationQueryProfile})
		if err != nil {
			return report, err
		}
		if len(others) == 0 {
			if err := forgetter.ForgetDatasetPublicationChannel(schema, lane.ProviderID); err != nil {
				return report, err
			}
		}
	}
	report.Applied = true
	log.Infof("publication retract %s %s/%s: %d series retired, %d CIDs unpinned (%d failed), %d records removed of %d", schema, lane.ProviderID, lane.SourceName,
		result.RetiredSeries, result.Unpinned, result.UnpinFailed, report.RecordsRemoved, report.Records)
	return report, nil
}
