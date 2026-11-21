package api

// dataset_publication_alert.go — the publication_failed alert of a lane, as a
// function of what the store holds.
//
// Every publication that fails records a failed scope of its lane: the batch
// it named ("" for none) and its operation (failedOperation). The scope is
// written to the store (storage.RecordDatasetPublicationFailedScope) before
// the alert is raised, so a restart raises the alert again at start
// (RestorePublicationAlerts). A scope goes, and its row with it, when:
//
//   - a failed publication of a whole batch of the node's own store: a
//     recount from the store (storage.UnpublishedBatchRecords) finds none of
//     the records the batch holds now unpublished. Either every one of them
//     is in a completed publication's export, or the batch holds none any
//     more (they expired, or a replace-current lane superseded them). The
//     batch's records are recounted after each failure in the lane, after
//     each completed publication of a whole batch or of the whole lane, and
//     at start. Nothing is carried from one count to the next. A completed
//     publication of the whole batch itself that read the lane after the
//     failed one did exported every record the batch held then, and removes
//     the scope when it is reported, before its callers are answered.
//   - any other failed scope (a head of a batch or of the lane, a
//     publication of the whole lane, an announcement of existing
//     publications, a batch of a datastore lane or of a store whose records
//     cannot be counted): the same operation on the same scope completes
//     after it, and read the lane after the failed one did (a publication of
//     the whole lane also covers a failed head of the lane).
//
// The alert clears when its lane has no failed scope left; the scope's
// removal and the clear are one step under the lock the raise takes, so a
// failure raised meanwhile is never cleared with them. At most
// maxFailedBatchesPerLane batches of a lane are tracked; a failure beyond
// them leaves a scope that only a completed publication of the whole lane
// removes, and the alert says so.
//
// Counting reads the store, so it runs off the publication's path: on one
// worker per service that runs one job at a time, on a context the node's
// stop cancels (StopPublications waits for it within its bound). A
// publication that completes takes no lock that I/O holds.

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/ops"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
)

// maxFailedBatchesPerLane bounds the failed batches tracked per lane: each
// costs a recount of its records at every event of its lane.
const maxFailedBatchesPerLane = 16

// The operations a failed scope names.
const (
	opBatch     = "publish batch"
	opBatchHead = "publish batch head"
	opLane      = "publish lane"
	opLaneHead  = "publish lane head"
	opAnnounce  = "announce existing"
	opOverflow  = "failed batches beyond the bound"
)

// publicationRemedy closes the text of an alert that names a failed batch:
// what an operator does once the auto-publisher's retries of it are spent
// (auto_publish.go autoPublishMaxAttempts).
const publicationRemedy = `; a publication of a failed batch whole (POST /api/v1/admin/dataset-updates/publish with its "batchId") publishes its records and clears it`

// failedOperation is the scope and operation of a publication request.
func failedOperation(req DatasetPublicationRequest) (batch, op string) {
	batch = strings.TrimSpace(req.BatchID)
	switch {
	case req.AnnounceExisting:
		return batch, opAnnounce
	case batch != "" && req.Limit > 0:
		return batch, opBatchHead
	case batch != "":
		return batch, opBatch
	case req.Limit <= 0 && (req.FullCatalog || req.ChunkSize > 0):
		return "", opLane
	default:
		return "", opLaneHead
	}
}

type failedScopeKey struct{ batch, op string }

// failedScope is one failed scope of a lane.
type failedScope struct {
	req      DatasetPublicationRequest
	failedAt time.Time
	// stamp orders the failed publication's read of its lane among the
	// service's publications (publicationRuns.readStamp); 0 for a scope
	// restored at start, which every publication since read after.
	stamp uint64
	// gen changes at every failure of the scope: a count read before it
	// says nothing of the scope after it.
	gen uint64
	// The last count: not yet counted, a count that failed, or its figures.
	counted            bool
	countErr           error
	unpublished, total int64
}

// countable reports whether the scope is removed by a recount.
func (f *failedScope) countable(store *storage.FlatSQLStore) bool {
	return store != nil && strings.TrimSpace(f.req.DatastoreKey) == "" && !errors.Is(f.countErr, storage.ErrBatchCoverageUnsupported)
}

// laneAlert is a lane's failed scopes and the text of its last failure.
type laneAlert struct {
	scopes map[failedScopeKey]*failedScope
	last   string
}

// publicationAlertState is the service's failed scopes and the worker that
// counts their records.
type publicationAlertState struct {
	// mu guards the fields below, and every Raise, Restate and Clear of a
	// publication_failed alert is made under it. It is never held across
	// I/O.
	mu    sync.Mutex
	lanes map[string]*laneAlert
	gen   uint64
	// pending is, per lane, the event a queued recount that has not started
	// names ("" for none): one is queued per lane at a time.
	pending map[string]*string
	queue   []func(context.Context)
	running bool
	// persist serializes each change of the failed scopes: the store write
	// of its row and the change in memory.
	persist sync.Mutex
}

// SetAlerts binds the node's operational alert registry.
func (s *ConcreteDatasetPublicationService) SetAlerts(alerts *ops.Registry) {
	if s != nil {
		s.alerts = alerts
	}
}

// RestorePublicationAlerts raises the publication_failed alert of every lane
// with a failed scope in the store, as the node stopped with it, and counts
// the lanes' failed batches again. The node calls it at start, after
// SetAlerts.
func (s *ConcreteDatasetPublicationService) RestorePublicationAlerts() {
	if s == nil || s.runs == nil || s.store == nil {
		return
	}
	rows, err := s.store.DatasetPublicationFailedScopes()
	if err != nil {
		log.Errorf("Dataset publications: the failed scopes of the last run could not be read, so their publication_failed alerts are not raised: %v", err)
		return
	}
	if len(rows) == 0 {
		return
	}
	st := &s.runs.alert
	st.persist.Lock()
	st.mu.Lock()
	for _, row := range rows {
		la := st.lane(row.Lane)
		st.gen++
		la.scopes[failedScopeKey{row.BatchID, row.Operation}] = &failedScope{
			req: DatasetPublicationRequest{
				Schema: row.SchemaName, ProviderID: row.ProviderID, SourceName: row.SourceName,
				DatastoreKey: row.DatastoreKey, BatchID: row.BatchID,
			},
			failedAt: row.FailedAt,
			gen:      st.gen,
		}
		la.last = row.Reason
	}
	lanes := make([]string, 0, len(st.lanes))
	for lane, la := range st.lanes {
		lanes = append(lanes, lane)
		text := la.last + " (" + s.scopesText(lane, la) + ")"
		log.Warnf("Dataset publication %s: %d failed scope(s) of the last run are unpublished; the publication_failed alert is raised again: %s", lane, len(la.scopes), text)
		s.alerts.Raise(ops.KindPublicationFailed, lane, ops.SeverityError, text)
	}
	st.mu.Unlock()
	st.persist.Unlock()
	sort.Strings(lanes)
	for _, lane := range lanes {
		s.enqueueRecount(lane, "")
	}
}

// lane is a lane's alert state, made when missing. st.mu is held.
func (st *publicationAlertState) lane(lane string) *laneAlert {
	if st.lanes == nil {
		st.lanes = make(map[string]*laneAlert)
	}
	la := st.lanes[lane]
	if la == nil {
		la = &laneAlert{scopes: make(map[failedScopeKey]*failedScope)}
		st.lanes[lane] = la
	}
	return la
}

// failPublication records the failed scope of a failed publication of req,
// which read its lane at stamp, and raises the lane's alert with text.
func (s *ConcreteDatasetPublicationService) failPublication(req DatasetPublicationRequest, lane string, stamp uint64, text string) {
	if s.runs == nil {
		s.alerts.Raise(ops.KindPublicationFailed, lane, ops.SeverityError, text)
		return
	}
	st := &s.runs.alert
	batch, op := failedOperation(req)
	key := failedScopeKey{batch, op}
	st.persist.Lock()
	defer st.persist.Unlock()

	st.mu.Lock()
	if batch != "" {
		la := st.lanes[lane]
		if _, tracked := la.scopesOrNil()[key]; !tracked && la.batchScopes() >= maxFailedBatchesPerLane {
			key = failedScopeKey{"", opOverflow}
		}
	}
	st.mu.Unlock()

	failedAt := s.now().UTC()
	if s.store != nil {
		if err := s.store.RecordDatasetPublicationFailedScope(storage.DatasetPublicationFailedScope{
			Lane: lane, BatchID: key.batch, Operation: key.op,
			SchemaName: normalizeDatasetPublicationSchema(req.Schema), ProviderID: strings.TrimSpace(req.ProviderID),
			SourceName: strings.TrimSpace(req.SourceName), DatastoreKey: strings.TrimSpace(req.DatastoreKey),
			FailedAt: failedAt, Reason: text,
		}); err != nil {
			log.Errorf("Dataset publication %s batch %q: the failed scope could not be recorded, so a restart does not raise its alert again: %v", lane, batch, err)
		}
	}

	st.mu.Lock()
	la := st.lane(lane)
	scope := la.scopes[key]
	if scope == nil {
		scope = &failedScope{}
		la.scopes[key] = scope
	}
	st.gen++
	scoped := req
	scoped.BatchID = key.batch
	scope.req, scope.failedAt, scope.gen = scoped, failedAt, st.gen
	scope.stamp = max(scope.stamp, stamp)
	scope.counted, scope.countErr, scope.unpublished, scope.total = false, nil, 0, 0
	la.last = text
	if key.op == opOverflow {
		log.Warnf("Dataset publication %s: batch %q failed while %d failed batches of the lane are tracked; it is not counted, and the alert stays until a publication of the whole lane completes", lane, batch, maxFailedBatchesPerLane)
	}
	countable := key.op == opBatch && scope.countable(s.store)
	s.alerts.Raise(ops.KindPublicationFailed, lane, ops.SeverityError, text+" ("+s.scopesText(lane, la)+")")
	st.mu.Unlock()
	if countable {
		s.enqueueRecount(lane, "")
	}
}

func (la *laneAlert) scopesOrNil() map[failedScopeKey]*failedScope {
	if la == nil {
		return nil
	}
	return la.scopes
}

// batchScopes is how many batches of the lane have a failed scope.
func (la *laneAlert) batchScopes() int {
	if la == nil {
		return 0
	}
	n := 0
	for key := range la.scopes {
		if key.batch != "" {
			n++
		}
	}
	return n
}

// publicationCompleted removes what a completed publication of req, which
// read its lane at stamp, covers: the failed scopes of the same operation
// (failedOperation) that read the lane before it, and, for a publication of
// a whole batch or of the whole lane, the failed batches a recount then
// finds published. It reads no store on the caller's path.
func (s *ConcreteDatasetPublicationService) publicationCompleted(req DatasetPublicationRequest, lane string, stamp uint64) {
	if s.runs == nil {
		return
	}
	st := &s.runs.alert
	st.mu.Lock()
	failed := len(st.lanes[lane].scopesOrNil()) > 0
	st.mu.Unlock()
	if !failed {
		return
	}
	batch, op := failedOperation(req)
	whole := op == opLane || (op == opBatch && strings.TrimSpace(req.DatastoreKey) == "")
	after := fmt.Sprintf("dataset publication %s: after the publication of batch %q, ", lane, batch)
	// The same operation is settled here, before the publication's callers
	// are answered: it reads no store (one row delete per scope it clears).
	s.clearSameOperation(lane, batch, op, stamp, after)
	if whole {
		s.enqueueRecount(lane, after)
	}
}

// clearSameOperation removes the failed scopes of lane that a completed
// operation op on batch, which read the lane at stamp, covers.
func (s *ConcreteDatasetPublicationService) clearSameOperation(lane, batch, op string, stamp uint64, after string) {
	st := &s.runs.alert
	st.persist.Lock()
	defer st.persist.Unlock()
	st.mu.Lock()
	la := st.lanes[lane]
	var gone []failedScopeKey
	for key, scope := range la.scopesOrNil() {
		if scope.stamp >= stamp {
			continue
		}
		covers := false
		switch {
		case key.op == opBatch:
			// A completed publication of the whole batch that read the lane
			// after the failed one exported every record the batch held
			// then; any other batch's publication covers it only through a
			// recount.
			covers = op == opBatch && key.batch == batch
		case op == opLane:
			covers = key.op == opLane || key.op == opLaneHead || key.op == opOverflow
		default:
			covers = key.op == op && key.batch == batch
		}
		if covers {
			gone = append(gone, key)
			delete(la.scopes, key)
			log.Infof("Dataset publication %s: failed scope %q of batch %q is published: the same operation completed after it (%s)", lane, key.op, key.batch, op)
		}
	}
	if len(gone) > 0 {
		s.restateOrClearLocked(lane, la, after, "the failed operations completed again")
	}
	st.mu.Unlock()
	s.deleteScopeRows(lane, gone)
}

// restateOrClearLocked clears the lane's alert when it has no failed scope
// left, or restates it: after, when set, names the publication that
// prompted it; otherwise the text of the lane's last failure leads. st.mu is
// held.
func (s *ConcreteDatasetPublicationService) restateOrClearLocked(lane string, la *laneAlert, after, why string) {
	st := &s.runs.alert
	if len(la.scopes) == 0 {
		delete(st.lanes, lane)
		log.Infof("Dataset publication %s: no failed scope of the lane is unpublished (%s); its publication_failed alert clears", lane, why)
		heldAlertClear(lane)
		s.alerts.Clear(ops.KindPublicationFailed, lane)
		return
	}
	text := la.last + " (" + s.scopesText(lane, la) + ")"
	if after != "" {
		text = after + s.scopesText(lane, la)
	}
	log.Warnf("Dataset publication %s: %d failed scope(s) are still unpublished: %s", lane, len(la.scopes), text)
	s.alerts.Restate(ops.KindPublicationFailed, lane, text)
}

// deleteScopeRows removes the store rows of a lane's scopes. st.persist is
// held.
func (s *ConcreteDatasetPublicationService) deleteScopeRows(lane string, keys []failedScopeKey) {
	if s.store == nil {
		return
	}
	for _, key := range keys {
		if err := s.store.DeleteDatasetPublicationFailedScope(lane, key.batch, key.op); err != nil {
			log.Errorf("Dataset publication %s: the published scope %q of batch %q could not be removed from the store, so a restart raises its alert again until a count finds it published: %v", lane, key.op, key.batch, err)
		}
	}
}

// enqueueRecount queues a recount of the lane's failed batches, unless one
// is queued and has not started; after names the event, as
// restateOrClearLocked takes it.
func (s *ConcreteDatasetPublicationService) enqueueRecount(lane, after string) {
	st := &s.runs.alert
	st.mu.Lock()
	if st.pending == nil {
		st.pending = make(map[string]*string)
	}
	if event, queued := st.pending[lane]; queued {
		*event = after
		st.mu.Unlock()
		return
	}
	event := after
	st.pending[lane] = &event
	st.mu.Unlock()
	s.enqueueAlertJob(func(ctx context.Context) {
		st.mu.Lock()
		after := *st.pending[lane]
		delete(st.pending, lane)
		st.mu.Unlock()
		s.recountLane(ctx, lane, after)
	})
}

// recountLane counts, from the store, the unpublished records of each of the
// lane's failed batches that can be counted, removes the batches with none,
// and clears or restates the alert.
func (s *ConcreteDatasetPublicationService) recountLane(ctx context.Context, lane, after string) {
	st := &s.runs.alert
	type count struct {
		key                failedScopeKey
		req                DatasetPublicationRequest
		gen                uint64
		unpublished, total int64
		err                error
	}
	var counts []count
	st.mu.Lock()
	for key, scope := range st.lanes[lane].scopesOrNil() {
		if key.op == opBatch && scope.countable(s.store) {
			counts = append(counts, count{key: key, req: scope.req, gen: scope.gen})
		}
	}
	st.mu.Unlock()
	if len(counts) == 0 {
		return
	}
	sort.Slice(counts, func(i, j int) bool { return counts[i].key.batch < counts[j].key.batch })
	for i := range counts {
		c := &counts[i]
		started := time.Now()
		c.unpublished, c.total, c.err = s.store.UnpublishedBatchRecords(ctx, normalizeDatasetPublicationSchema(c.req.Schema), c.req.ProviderID, c.req.SourceName, c.key.batch)
		if ctx.Err() != nil {
			return
		}
		if c.err != nil {
			log.Warnf("Dataset publication %s: the records of failed batch %q could not be counted, so it stays unpublished: %v", lane, c.key.batch, c.err)
		} else {
			log.Infof("Dataset publication %s: failed batch %q: %d of its %d records are in no completed publication of the lane (counted in %s)", lane, c.key.batch, c.unpublished, c.total, time.Since(started).Round(time.Millisecond))
		}
	}

	st.persist.Lock()
	defer st.persist.Unlock()
	st.mu.Lock()
	la := st.lanes[lane]
	var gone []failedScopeKey
	var reasons []string
	for _, c := range counts {
		scope := la.scopesOrNil()[c.key]
		if scope == nil || scope.gen != c.gen {
			// Published meanwhile, or failed again: the count queued by
			// that failure decides.
			continue
		}
		scope.counted, scope.countErr, scope.unpublished, scope.total = true, c.err, c.unpublished, c.total
		if c.err != nil || c.unpublished > 0 {
			continue
		}
		gone = append(gone, c.key)
		delete(la.scopes, c.key)
		reason := fmt.Sprintf("every record of failed batch %q (%d) is in a completed publication of the lane", c.key.batch, c.total)
		if c.total == 0 {
			reason = fmt.Sprintf("failed batch %q holds no records any more (they expired or were superseded)", c.key.batch)
		}
		log.Infof("Dataset publication %s: %s", lane, reason)
		reasons = append(reasons, reason)
	}
	if la != nil {
		why := strings.Join(reasons, "; ")
		if why == "" {
			why = "counted"
		}
		s.restateOrClearLocked(lane, la, after, why)
	}
	st.mu.Unlock()
	s.deleteScopeRows(lane, gone)
}

// enqueueAlertJob runs job after the jobs before it, on the worker, unless
// the node is stopping.
func (s *ConcreteDatasetPublicationService) enqueueAlertJob(job func(context.Context)) {
	runs := s.runs
	if runs == nil {
		return
	}
	st := &runs.alert
	st.mu.Lock()
	defer st.mu.Unlock()
	st.queue = append(st.queue, job)
	if st.running {
		return
	}
	runs.mu.Lock()
	if runs.stopped {
		runs.mu.Unlock()
		st.queue = nil
		return
	}
	runs.active.Add(1)
	runs.mu.Unlock()
	st.running = true
	go func() {
		defer runs.active.Done()
		for {
			st.mu.Lock()
			if len(st.queue) == 0 || runs.life.Err() != nil {
				st.queue = nil
				st.running = false
				st.mu.Unlock()
				return
			}
			next := st.queue[0]
			st.queue = st.queue[1:]
			st.mu.Unlock()
			next(runs.life)
		}
	}()
}

// scopesText names a lane's failed scopes for its alert, ending with what an
// operator does. st.mu is held.
func (s *ConcreteDatasetPublicationService) scopesText(lane string, la *laneAlert) string {
	keys := make([]failedScopeKey, 0, len(la.scopes))
	for key := range la.scopes {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].batch != keys[j].batch {
			return keys[i].batch < keys[j].batch
		}
		return keys[i].op < keys[j].op
	})
	parts := make([]string, 0, len(keys))
	remedy := ""
	for _, key := range keys {
		if key.op == opBatch {
			remedy = publicationRemedy
		}
		scope := la.scopes[key]
		switch key.op {
		case opBatch:
			switch {
			case !scope.countable(s.store):
				why := "its records are not counted on a datastore lane"
				if scope.countErr != nil {
					why = scope.countErr.Error()
				}
				parts = append(parts, fmt.Sprintf("batch %q is unpublished (%s; it stays until a publication of the whole batch completes)", key.batch, why))
			case !scope.counted:
				parts = append(parts, fmt.Sprintf("batch %q is unpublished (its records are being counted)", key.batch))
			case scope.countErr != nil:
				parts = append(parts, fmt.Sprintf("batch %q is unpublished (its records could not be counted: %v)", key.batch, scope.countErr))
			default:
				parts = append(parts, fmt.Sprintf("batch %q is unpublished: %d of its %d records are in no completed publication of the lane", key.batch, scope.unpublished, scope.total))
			}
		case opBatchHead:
			parts = append(parts, fmt.Sprintf("a publication of a head of batch %q failed (it stays until a publication of a head of the batch completes)", key.batch))
		case opLane:
			parts = append(parts, "a publication of the whole lane failed (it stays until a publication of the whole lane completes)")
		case opLaneHead:
			parts = append(parts, "a publication of the lane's newest records failed (it stays until a publication of the lane completes)")
		case opAnnounce:
			parts = append(parts, fmt.Sprintf("an announcement of the existing publications of batch %q failed (it stays until such an announcement completes)", key.batch))
		case opOverflow:
			parts = append(parts, fmt.Sprintf("more batches failed than the %d the lane tracks; those are not counted, and the alert stays until a publication of the whole lane (no batch, \"fullCatalog\" or a \"chunkSize\") completes", maxFailedBatchesPerLane))
		}
	}
	return strings.Join(parts, "; ") + remedy
}
