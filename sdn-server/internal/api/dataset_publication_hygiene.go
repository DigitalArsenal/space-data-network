package api

// dataset_publication_hygiene.go — what happens after a dataset publication
// succeeds (graph: sdn-publication-hygiene-20260928):
//
//   - the publication is recorded as a SERIES of its lane (schema, provider,
//     source) together with the set fingerprint it exported, which is how
//     auto-publish recognizes an unchanged set and does nothing;
//   - retention keeps the newest publishing.retention.keep_series series of the
//     lane and releases the rest (storage/dataset_publication_series.go): kubo
//     unpins, advertised rows, shard/index/DPM files, staged CAR bundles;
//   - a lane named in publishing.ipns_pointers gets an IPNS record pointing at
//     its current DPM, published through the Kubo RPC (name/publish) — Kubo
//     itself is untouched, this is the SDN layer calling its API.
//
// None of it can fail a publication that already succeeded: the records are
// exported, pinned, signed and announced by then. A hygiene failure is logged
// and the next publication of the lane retries it.

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/config"
	"github.com/spacedatanetwork/sdn-server/internal/sds"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
)

// publicationFileSweepMinAge guards the publication-directory sweep: a file
// younger than this may belong to an export whose row is not written yet.
const publicationFileSweepMinAge = time.Hour

// ipnsPointerPublishTimeout bounds one name/publish call. An online Kubo puts
// the record into the DHT before it answers.
const ipnsPointerPublishTimeout = 3 * time.Minute

type publicationHygiene struct {
	mu        sync.Mutex
	lanes     map[string]*sync.Mutex
	retention config.PublicationRetentionConfig
	pointers  []config.IPNSPointerConfig
	sweepAge  time.Duration
	ipns      *ipnsPointerWorker
}

func newPublicationHygiene() *publicationHygiene {
	return &publicationHygiene{
		lanes:     make(map[string]*sync.Mutex),
		retention: config.PublicationRetentionConfig{KeepSeries: config.DefaultPublicationRetentionKeepSeries},
		sweepAge:  publicationFileSweepMinAge,
		ipns:      newIPNSPointerWorker(),
	}
}

// SetPublicationPolicy installs publishing.retention and
// publishing.ipns_pointers. Without it the service keeps the default series
// count and publishes no IPNS pointer.
func (s *ConcreteDatasetPublicationService) SetPublicationPolicy(retention config.PublicationRetentionConfig, pointers []config.IPNSPointerConfig) {
	if s == nil || s.hygiene == nil {
		return
	}
	s.hygiene.mu.Lock()
	defer s.hygiene.mu.Unlock()
	s.hygiene.retention = retention
	s.hygiene.pointers = append([]config.IPNSPointerConfig(nil), pointers...)
}

func (h *publicationHygiene) lockLane(schema, providerID, sourceName string) func() {
	if h == nil {
		return func() {}
	}
	key := strings.Join([]string{normalizeDatasetPublicationSchema(schema), strings.TrimSpace(providerID), strings.TrimSpace(sourceName)}, "\x00")
	h.mu.Lock()
	lane := h.lanes[key]
	if lane == nil {
		lane = &sync.Mutex{}
		h.lanes[key] = lane
	}
	h.mu.Unlock()
	lane.Lock()
	return lane.Unlock
}

func (h *publicationHygiene) policy() (config.PublicationRetentionConfig, []config.IPNSPointerConfig, time.Duration) {
	if h == nil {
		return config.PublicationRetentionConfig{}, nil, publicationFileSweepMinAge
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.retention, h.pointers, h.sweepAge
}

// publicationFingerprint is the set the request would export, taken before
// the export starts; "" when the request is not scoped to one lane.
func (s *ConcreteDatasetPublicationService) publicationFingerprint(req DatasetPublicationRequest, schema string) string {
	if s == nil || s.store == nil || strings.TrimSpace(req.DatastoreKey) != "" || req.Limit > 0 {
		return ""
	}
	if strings.TrimSpace(req.ProviderID) == "" || strings.TrimSpace(req.SourceName) == "" {
		return ""
	}
	fingerprint, _, err := s.store.DatasetPublicationSetFingerprint(schema, req.ProviderID, req.SourceName, req.BatchID)
	if err != nil {
		log.Warnf("publication fingerprint %s %s/%s: %v", schema, req.ProviderID, req.SourceName, err)
		return ""
	}
	return fingerprint
}

// DatasetPublicationUnchanged reports whether the set a request would export
// is exactly the set its lane's newest publication of the same scope already
// exported. Auto-publish skips such a request: republishing it would export,
// pin and announce what the network already has.
func (s *ConcreteDatasetPublicationService) DatasetPublicationUnchanged(ctx context.Context, req DatasetPublicationRequest) (bool, error) {
	if s == nil || s.store == nil {
		return false, nil
	}
	schema := normalizeDatasetPublicationSchema(req.Schema)
	current := s.publicationFingerprint(req, schema)
	if current == "" {
		return false, nil
	}
	latest, ok, err := s.store.LatestDatasetPublicationSeries(storage.DatasetPublicationLane{
		SchemaName: schema,
		ProviderID: req.ProviderID,
		SourceName: req.SourceName,
	}, req.BatchID)
	if err != nil || !ok {
		return false, err
	}
	return latest.Fingerprint != "" && latest.Fingerprint == current, nil
}

// afterPublication records the series, applies retention and queues the IPNS
// pointer. It never fails the publication.
func (s *ConcreteDatasetPublicationService) afterPublication(ctx context.Context, req DatasetPublicationRequest, schema string, result *DatasetPublicationResult, carCIDs []string, fingerprint string, startedAt time.Time) {
	if s == nil || s.store == nil || result == nil {
		return
	}
	retention, pointers, sweepAge := s.hygiene.policy()
	lane := storage.DatasetPublicationLane{SchemaName: schema, ProviderID: req.ProviderID, SourceName: req.SourceName}

	parts := result.Publications
	if len(parts) == 0 {
		parts = []DatasetPublicationResult{*result}
	}
	series := storage.DatasetPublicationSeries{
		SchemaName:  schema,
		ProviderID:  req.ProviderID,
		SourceName:  req.SourceName,
		BatchID:     req.BatchID,
		RecordCount: result.RecordCount,
		ManifestCID: result.ManifestCID,
		Fingerprint: fingerprint,
		PublishedAt: startedAt,
	}
	windows := make([]storage.DatasetShardPublication, 0, len(parts))
	for i, part := range parts {
		windows = append(windows, storage.DatasetShardPublication{Offset: i, Limit: part.RecordCount, ShardCID: part.ShardCID, IndexCID: part.IndexCID, ManifestCID: part.ManifestCID})
		series.Members = append(series.Members,
			storage.DatasetPublicationSeriesMember{CID: part.ShardCID, Role: storage.PinLedgerRoleShard},
			storage.DatasetPublicationSeriesMember{CID: part.IndexCID, Role: storage.PinLedgerRoleIndex},
			storage.DatasetPublicationSeriesMember{CID: part.ManifestCID, Role: storage.PinLedgerRoleManifest})
	}
	for _, carCID := range carCIDs {
		series.Members = append(series.Members, storage.DatasetPublicationSeriesMember{CID: carCID, Role: storage.PinLedgerRoleShardGroupCAR})
	}
	series.SeriesID = storage.DatasetPublicationSeriesID(lane, req.BatchID, windows)

	if err := s.store.RecordDatasetPublicationSeries(series, s.providerPeerID); err != nil {
		log.Warnf("record %s %s/%s publication series: %v", schema, req.ProviderID, req.SourceName, err)
	} else if keep, snapshots := retention.PolicyFor(schema, req.ProviderID, req.SourceName); keep > 0 {
		policy := storage.DatasetPublicationRetentionPolicy{KeepSeries: keep, SnapshotBatches: snapshots}
		if _, err := s.applyPublicationRetention(ctx, lane, policy); err != nil {
			log.Warnf("publication retention %s %s/%s: %v", schema, req.ProviderID, req.SourceName, err)
		}
	}
	if s.outputDir != "" {
		sweep, err := s.store.SweepDatasetPublicationFiles(s.outputDir, schema, sweepAge, s.now())
		if err != nil {
			log.Warnf("sweep %s publication files: %v", schema, err)
		} else if sweep.FilesRemoved > 0 {
			log.Infof("publication files %s: removed %d unreferenced file(s), %d bytes", schema, sweep.FilesRemoved, sweep.BytesRemoved)
		}
	}

	for _, pointer := range pointers {
		if !config.PublishingLaneMatches(pointer.Schema, pointer.ProviderID, pointer.SourceName, schema, req.ProviderID, req.SourceName) {
			continue
		}
		if strings.TrimSpace(result.ManifestCID) == "" || s.ipfsAPIURL == "" {
			continue
		}
		s.hygiene.ipns.enqueue(ipnsPointerJob{
			APIURL:   s.ipfsAPIURL,
			Key:      strings.TrimSpace(pointer.Key),
			Path:     "/ipfs/" + strings.TrimSpace(result.ManifestCID),
			Lifetime: pointer.Lifetime,
			TTL:      pointer.TTL,
			Lane:     schema + " " + req.ProviderID + "/" + req.SourceName,
		})
	}
}

// applyPublicationRetention runs one retention pass over a lane of this node's
// own publications.
func (s *ConcreteDatasetPublicationService) applyPublicationRetention(ctx context.Context, lane storage.DatasetPublicationLane, policy storage.DatasetPublicationRetentionPolicy) (storage.DatasetPublicationRetentionResult, error) {
	plan, err := s.store.PlanDatasetPublicationRetention(lane, s.providerPeerID, policy)
	if err != nil {
		return storage.DatasetPublicationRetentionResult{}, err
	}
	return s.executePublicationRetention(ctx, lane, plan)
}

// executePublicationRetention unpins a plan's CIDs and records the outcome.
func (s *ConcreteDatasetPublicationService) executePublicationRetention(ctx context.Context, lane storage.DatasetPublicationLane, plan storage.DatasetPublicationRetentionPlan) (storage.DatasetPublicationRetentionResult, error) {
	if len(plan.Unpin) == 0 && len(plan.Shared) == 0 && len(plan.RetireRows) == 0 && len(plan.RetiredSeries) == 0 {
		return storage.DatasetPublicationRetentionResult{Lane: plan.Lane, KeptSeries: len(plan.KeptSeries)}, nil
	}
	unpinned := make(map[string]bool, len(plan.Unpin))
	for _, entry := range plan.Unpin {
		if err := storage.UnpinIPFSCID(ctx, s.ipfsAPIURL, entry.CID); err != nil {
			log.Warnf("publication retention %s: unpin %s %s: %v (retried next pass)", lane.SchemaName, entry.Role, entry.CID, err)
			continue
		}
		unpinned[entry.CID] = true
	}
	result, err := s.store.ApplyDatasetPublicationRetention(plan, s.providerPeerID, unpinned, s.outputDir, s.now())
	if err != nil {
		return result, err
	}
	log.Infof("publication retention %s %s/%s: kept %d series, retired %d; unpinned %d CIDs (%d bytes, %d failed, %d shared kept pinned); %d rows retired; %d files removed (%d bytes)",
		lane.SchemaName, lane.ProviderID, lane.SourceName, result.KeptSeries, result.RetiredSeries,
		result.Unpinned, result.UnpinnedBytes, result.UnpinFailed, result.SharedRetained, result.RowsDeleted,
		result.FilesRemoved, result.BytesRemoved)
	return result, nil
}

// DatasetPublicationRetentionRequest is the body of the loopback admin route
// POST /api/v1/admin/dataset-updates/retention: one lane of this node's own
// publications, dry run unless Apply. KeepSeries/SnapshotBatches override the
// configured policy for this pass only.
type DatasetPublicationRetentionRequest struct {
	Schema          string `json:"schema"`
	ProviderID      string `json:"providerId"`
	SourceName      string `json:"sourceName"`
	KeepSeries      *int   `json:"keepSeries,omitempty"`
	SnapshotBatches *bool  `json:"snapshotBatches,omitempty"`
	Apply           bool   `json:"apply,omitempty"`
}

// DatasetPublicationRetentionPin is one CID a pass releases.
type DatasetPublicationRetentionPin struct {
	CID     string `json:"cid"`
	Role    string `json:"role"`
	BatchID string `json:"batchId"`
	Bytes   int64  `json:"bytes"`
}

// DatasetPublicationRetentionRow is one advertised window a pass retires.
type DatasetPublicationRetentionRow struct {
	BatchID  string `json:"batchId"`
	Offset   int    `json:"offset"`
	Limit    int    `json:"limit"`
	Records  int    `json:"records"`
	ShardCID string `json:"shardCid"`
}

// DatasetPublicationRetentionReport is the plan (and, applied, the outcome).
type DatasetPublicationRetentionReport struct {
	Schema          string                                     `json:"schema"`
	ProviderID      string                                     `json:"providerId"`
	SourceName      string                                     `json:"sourceName"`
	KeepSeries      int                                        `json:"keepSeries"`
	SnapshotBatches bool                                       `json:"snapshotBatches"`
	Applied         bool                                       `json:"applied"`
	AdoptedSeries   int                                        `json:"adoptedSeries"`
	KeptSeries      []string                                   `json:"keptSeries"`
	RetiredSeries   []string                                   `json:"retiredSeries"`
	Unpin           []DatasetPublicationRetentionPin           `json:"unpin"`
	UnpinBytes      int64                                      `json:"unpinBytes"`
	SharedKept      []DatasetPublicationRetentionPin           `json:"sharedKeptPinned"`
	RetireRows      []DatasetPublicationRetentionRow           `json:"retireRows"`
	Result          *storage.DatasetPublicationRetentionResult `json:"result,omitempty"`
}

// DatasetPublicationRetentionRunner runs one retention pass on demand.
type DatasetPublicationRetentionRunner interface {
	RunPublicationRetention(ctx context.Context, req DatasetPublicationRetentionRequest) (*DatasetPublicationRetentionReport, error)
}

// RunPublicationRetention plans (and with Apply runs) one retention pass over
// a lane without publishing: the lane's unrecorded rows are adopted as legacy
// series first, so a lane that has not published since series existed can be
// cleaned too. The dry run reports every CID the pass would unpin.
func (s *ConcreteDatasetPublicationService) RunPublicationRetention(ctx context.Context, req DatasetPublicationRetentionRequest) (*DatasetPublicationRetentionReport, error) {
	if s == nil || s.store == nil {
		return nil, fmt.Errorf("dataset store is unavailable")
	}
	schema := normalizeDatasetPublicationSchema(req.Schema)
	if err := sds.ValidateSchemaName(schema); err != nil {
		return nil, fmt.Errorf("invalid schema: %w", err)
	}
	lane := storage.DatasetPublicationLane{SchemaName: schema, ProviderID: strings.TrimSpace(req.ProviderID), SourceName: strings.TrimSpace(req.SourceName)}
	retention, _, _ := s.hygiene.policy()
	keep, snapshots := retention.PolicyFor(schema, lane.ProviderID, lane.SourceName)
	if req.KeepSeries != nil {
		keep = *req.KeepSeries
	}
	if req.SnapshotBatches != nil {
		snapshots = *req.SnapshotBatches
	}
	if keep <= 0 {
		return nil, fmt.Errorf("keepSeries must be at least 1 for a retention pass (0 keeps everything)")
	}
	policy := storage.DatasetPublicationRetentionPolicy{KeepSeries: keep, SnapshotBatches: snapshots}

	unlock := s.hygiene.lockLane(schema, lane.ProviderID, lane.SourceName)
	defer unlock()
	report := &DatasetPublicationRetentionReport{Schema: schema, ProviderID: lane.ProviderID, SourceName: lane.SourceName, KeepSeries: keep, SnapshotBatches: snapshots}
	if req.Apply {
		adopted, err := s.store.AdoptDatasetPublicationSeries(lane, s.providerPeerID)
		if err != nil {
			return nil, err
		}
		report.AdoptedSeries = adopted
	}
	plan, err := s.planPublicationRetention(lane, policy, !req.Apply)
	if err != nil {
		return nil, err
	}
	report.KeptSeries, report.RetiredSeries = plan.KeptSeries, plan.RetiredSeries
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
	if !req.Apply {
		return report, nil
	}
	result, err := s.executePublicationRetention(ctx, lane, plan)
	if err != nil {
		return report, err
	}
	report.Applied = true
	report.Result = &result
	return report, nil
}

// planPublicationRetention plans a pass. A dry run on a lane with no series
// yet plans against the legacy series adoption WOULD write, without writing.
func (s *ConcreteDatasetPublicationService) planPublicationRetention(lane storage.DatasetPublicationLane, policy storage.DatasetPublicationRetentionPolicy, dryRun bool) (storage.DatasetPublicationRetentionPlan, error) {
	if dryRun {
		return s.store.PlanDatasetPublicationRetentionWithAdoption(lane, s.providerPeerID, policy)
	}
	return s.store.PlanDatasetPublicationRetention(lane, s.providerPeerID, policy)
}

// pruneDatasetShardPublicationsOutsideSeries stops advertising every window of
// the request's scope that the series just published did not produce, and
// removes the files of those windows and of every window the series
// overwrote with new content, when no remaining row serves them. Their pins
// are retention's to release. before is the scope's rows as the series began.
func (s *ConcreteDatasetPublicationService) pruneDatasetShardPublicationsOutsideSeries(req DatasetPublicationRequest, schema string, windows []storage.DatasetShardPublication, before []storage.DatasetShardPublication) error {
	scope := datasetPublicationSourceIdentityFromRequest(req)
	rows, err := s.store.ListDatasetShardPublications(storage.DatasetShardPublicationQuery{
		SchemaName:   schema,
		ProviderID:   scope.ProviderID,
		SourceName:   scope.SourceName,
		BatchID:      scope.BatchID,
		QueryProfile: storage.DatasetPublicationQueryProfile,
	})
	if err != nil {
		return err
	}
	inScope := func(row storage.DatasetShardPublication) bool {
		// The listing narrows by non-empty fields only; the scope is exact.
		return row.ProviderID == scope.ProviderID && row.SourceName == scope.SourceName && row.BatchID == scope.BatchID
	}
	current := make(map[[2]int]bool, len(windows))
	for _, window := range windows {
		current[[2]int{window.Offset, window.Limit}] = true
	}
	now := make(map[[2]int]storage.DatasetShardPublication, len(rows))
	var gone []storage.DatasetShardPublication
	pruned := 0
	for _, row := range rows {
		if !inScope(row) {
			continue
		}
		key := [2]int{row.Offset, row.Limit}
		if current[key] {
			now[key] = row
			continue
		}
		if _, err := s.store.DeleteDatasetShardPublication(row); err != nil {
			return err
		}
		gone = append(gone, row)
		pruned++
	}
	for _, row := range before {
		if !inScope(row) {
			continue
		}
		if replaced, ok := now[[2]int{row.Offset, row.Limit}]; ok && (replaced.ShardSHA256 != row.ShardSHA256 || replaced.IndexSHA256 != row.IndexSHA256) {
			gone = append(gone, row)
		}
	}
	if len(gone) == 0 {
		return nil
	}
	removed, bytes, err := s.store.RemoveDatasetPublicationRowFiles(s.outputDir, schema, gone)
	if err != nil {
		log.Warnf("remove superseded %s window files: %v", schema, err)
	}
	log.Infof("publication %s %s/%s batch %q: %d stale window(s) no longer advertised, %d superseded window file(s) removed (%d bytes)",
		schema, scope.ProviderID, scope.SourceName, scope.BatchID, pruned, removed, bytes)
	return nil
}

// ipnsPointerJob is one pointer to (re)publish.
type ipnsPointerJob struct {
	APIURL   string
	Key      string
	Path     string
	Lifetime time.Duration
	TTL      time.Duration
	Lane     string
}

// ipnsPointerStatus is the outcome of the last publish under one key.
type ipnsPointerStatus struct {
	Path  string
	Name  string
	Err   error
	At    time.Time
	Count int
}

// ipnsPointerWorker publishes pointers off the publication's goroutine: an
// online Kubo answers name/publish only after the DHT put, and a publication
// request (the CelesTrak flow's POST) must not wait on that. Per key only the
// LATEST path is kept, so a burst of series publishes the newest DPM once and
// the pointer can never end on an older one.
type ipnsPointerWorker struct {
	mu      sync.Mutex
	idle    *sync.Cond
	pending map[string]ipnsPointerJob
	order   []string
	running bool
	status  map[string]ipnsPointerStatus
	publish func(ctx context.Context, job ipnsPointerJob) (string, error)
}

func newIPNSPointerWorker() *ipnsPointerWorker {
	w := &ipnsPointerWorker{
		pending: make(map[string]ipnsPointerJob),
		status:  make(map[string]ipnsPointerStatus),
		publish: func(ctx context.Context, job ipnsPointerJob) (string, error) {
			name, _, err := storage.PublishIPNSName(ctx, job.APIURL, job.Key, job.Path, job.Lifetime, job.TTL)
			return name, err
		},
	}
	w.idle = sync.NewCond(&w.mu)
	return w
}

func (w *ipnsPointerWorker) enqueue(job ipnsPointerJob) {
	if w == nil || job.Key == "" || job.Path == "" {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if _, queued := w.pending[job.Key]; !queued {
		w.order = append(w.order, job.Key)
	}
	w.pending[job.Key] = job
	if !w.running {
		w.running = true
		go w.run()
	}
}

func (w *ipnsPointerWorker) run() {
	for {
		w.mu.Lock()
		if len(w.order) == 0 {
			w.running = false
			w.idle.Broadcast()
			w.mu.Unlock()
			return
		}
		key := w.order[0]
		w.order = w.order[1:]
		job := w.pending[key]
		delete(w.pending, key)
		w.mu.Unlock()

		ctx, cancel := context.WithTimeout(context.Background(), ipnsPointerPublishTimeout)
		name, err := w.publish(ctx, job)
		cancel()
		if err != nil {
			log.Warnf("IPNS pointer %s (key %s) -> %s failed: %v", job.Lane, job.Key, job.Path, err)
		} else {
			log.Infof("IPNS pointer %s: /ipns/%s -> %s (key %s)", job.Lane, name, job.Path, job.Key)
		}

		w.mu.Lock()
		status := w.status[job.Key]
		status.Path, status.Name, status.Err, status.At = job.Path, name, err, time.Now().UTC()
		status.Count++
		w.status[job.Key] = status
		w.mu.Unlock()
	}
}

// wait blocks until no pointer is queued or publishing (tests, shutdown).
func (w *ipnsPointerWorker) wait() {
	if w == nil {
		return
	}
	w.mu.Lock()
	for w.running {
		w.idle.Wait()
	}
	w.mu.Unlock()
}

func (w *ipnsPointerWorker) lastStatus(key string) (ipnsPointerStatus, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	status, ok := w.status[key]
	return status, ok
}
