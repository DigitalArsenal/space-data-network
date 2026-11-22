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
//   - a failed publication of a whole batch X of the node's own store: a
//     check of X (storage.CheckFailedBatchPublication) finds that a completed
//     publication of the whole of X exported the set X holds now (the
//     fingerprints are equal), or that X holds no records any more (they
//     were superseded, expired or retracted). A publication of any other
//     batch never clears X, whatever records it shares with X; a check reads
//     X only (one fingerprint of X and one read of its series rows).
//   - any other failed scope (a head of a batch or of the lane, a
//     publication of the whole lane, an announcement of existing
//     publications, a batch of a datastore lane or of a store whose batches
//     cannot be checked): the same operation on the same scope completes
//     after it, and read the lane after the failed one did (a publication of
//     the whole lane also covers a failed head of the lane).
//   - the lane is retracted (dataset-updates/retract): every scope of the
//     lane goes at the retraction, since the lane is never published again.
//
// X is checked at each failure of X, after each completed publication of the
// whole of X, when a supersede or an expiry removed records of X's lane, and
// at start once the node reports ready (StartPublicationAlertChecks). A
// completed publication of another batch, or of the whole lane, prompts no
// check of X. At most one check per (lane, batch) is pending at a time, so
// the worker's queue is bounded by the failed scopes.
//
// A completed publication of the whole of X that read the lane after the
// failed one did removes X's scope when it is reported, before its callers
// are answered, with no store read; the worker then checks X, and when the
// set X holds is not the one that publication exported (records arrived
// while it ran), the scope is recorded again and the alert raised again,
// saying how many records the publication did not export. When that check
// fails (a store error), the scope is recorded again too, the alert saying X
// could not be checked, until a later check of X confirms it.
//
// The alert clears when its lane has no failed scope left; the scope's
// removal and the clear are one step under the lock the raise takes, so a
// failure raised meanwhile is never cleared with them. At most
// maxFailedBatchesPerLane batches of a lane are tracked; a failure beyond
// them leaves a scope that only a completed publication of the whole lane
// removes, and the alert says so.
//
// Checks read the store, so they run off the publication's path: on one
// worker per service that runs one job at a time, on a context the node's
// stop cancels (StopPublications waits for it within its bound). A
// publication that completes reads no store on its path, but it takes the
// persist lock, which a failure, a check and a retraction hold across their
// row writes, and it deletes the rows of the non-batch scopes it removes.

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
// is one check (one fingerprint of the batch) at each of its events.
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

// snapshotGuardStep is the step at which the snapshot guard refuses a batch
// (dataset_publication.go).
const snapshotGuardStep = "snapshot guard"

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
	// guarded: the snapshot guard refused the batch.
	guarded bool
	// The last check: not yet checked, a check that failed, or what it
	// found.
	checked  bool
	checkErr error
	check    storage.FailedBatchCheck
}

// countable reports whether the scope is removed by a check of its batch.
func (f *failedScope) countable(store *storage.FlatSQLStore) bool {
	return store != nil && strings.TrimSpace(f.req.DatastoreKey) == "" && !errors.Is(f.checkErr, storage.ErrBatchCoverageUnsupported)
}

// laneAlert is a lane's failed scopes and the text of its last failure.
type laneAlert struct {
	scopes map[failedScopeKey]*failedScope
	last   string
}

// checkKey names a failed batch of a lane.
type checkKey struct{ lane, batch string }

// pendingCheck is a queued check of a batch that has not started.
type pendingCheck struct {
	// after names the event that prompted it, as restateOrClearLocked takes
	// it.
	after string
	// verify is the request of a completed publication of the whole batch
	// that removed the batch's scope: the check records the scope again when
	// the batch's set is not the one that publication exported.
	verify *DatasetPublicationRequest
}

// publicationAlertState is the service's failed scopes and the worker that
// checks their batches.
type publicationAlertState struct {
	// mu guards the fields below, and every Raise, Restate and Clear of a
	// publication_failed alert is made under it. It is never held across
	// I/O.
	mu    sync.Mutex
	lanes map[string]*laneAlert
	gen   uint64
	// pending is the queued check of each batch that has not started.
	pending map[checkKey]*pendingCheck
	// events is, per batch, the generation of its last failure or completed
	// publication of the whole batch: a check that started before it
	// decides nothing (the check that event queued does).
	events map[checkKey]uint64
	// checking is the batch whose check runs (the zero key for none).
	checking checkKey
	// restored is the batches restored at start, checked once the node
	// reports ready (StartPublicationAlertChecks).
	restored []checkKey
	queue    []func(context.Context)
	running  bool
	// persist serializes each change of the failed scopes: the store write
	// of its row and the change in memory.
	persist sync.Mutex
}

// SetAlerts binds the node's operational alert registry, and has the store
// tell the service when a supersede or an expiry removed records of a lane.
func (s *ConcreteDatasetPublicationService) SetAlerts(alerts *ops.Registry) {
	if s == nil {
		return
	}
	s.alerts = alerts
	if s.store != nil && s.runs != nil {
		s.store.SetLaneRecordsRemoved(s.laneRecordsRemoved)
	}
}

// RestorePublicationAlerts raises the publication_failed alert of every lane
// with a failed scope in the store, as the node stopped with it. It reads one
// table and checks nothing: the checks of the restored batches wait for
// StartPublicationAlertChecks. The node calls it at start, after SetAlerts.
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
	defer st.persist.Unlock()
	st.mu.Lock()
	defer st.mu.Unlock()
	for _, row := range rows {
		la := st.lane(row.Lane)
		la.scopes[failedScopeKey{row.BatchID, row.Operation}] = &failedScope{
			req: DatasetPublicationRequest{
				Schema: row.SchemaName, ProviderID: row.ProviderID, SourceName: row.SourceName,
				DatastoreKey: row.DatastoreKey, BatchID: row.BatchID,
			},
			failedAt: row.FailedAt,
			guarded:  strings.Contains(row.Reason, fmt.Sprintf("at step %q", snapshotGuardStep)),
		}
		la.last = row.Reason
		if row.Operation == opBatch {
			st.restored = append(st.restored, checkKey{row.Lane, row.BatchID})
		}
	}
	lanes := make([]string, 0, len(st.lanes))
	for lane := range st.lanes {
		lanes = append(lanes, lane)
	}
	sort.Strings(lanes)
	for _, lane := range lanes {
		la := st.lanes[lane]
		text := la.last + " (" + s.scopesText(lane, la) + ")"
		log.Warnf("Dataset publication %s: %d failed scope(s) of the last run are unpublished; the publication_failed alert is raised again: %s", lane, len(la.scopes), text)
		s.alerts.Raise(ops.KindPublicationFailed, lane, ops.SeverityError, text)
	}
}

// StartPublicationAlertChecks queues the checks of the failed batches
// RestorePublicationAlerts restored. The node calls it once it reports
// ready, so the checks delay neither its readiness nor an update's health
// trial.
func (s *ConcreteDatasetPublicationService) StartPublicationAlertChecks() {
	if s == nil || s.runs == nil {
		return
	}
	st := &s.runs.alert
	st.mu.Lock()
	defer st.mu.Unlock()
	keys := st.restored
	st.restored = nil
	if len(keys) > 0 {
		log.Infof("Dataset publications: the node is ready; checking %d failed batch(es) of the last run", len(keys))
	}
	for _, key := range keys {
		s.enqueueCheckLocked(key, "", nil)
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

// event records an event of a failed batch: a check that started before it
// decides nothing. st.mu is held.
func (st *publicationAlertState) event(key checkKey) {
	if st.events == nil {
		st.events = make(map[checkKey]uint64)
	}
	st.gen++
	st.events[key] = st.gen
}

// scopeKeyFor is the scope a failure of batch with op records in a lane that
// tracks la: the overflow scope for a batch beyond the bound. st.mu is held.
func scopeKeyFor(la *laneAlert, batch, op string) failedScopeKey {
	key := failedScopeKey{batch, op}
	if batch != "" {
		if _, tracked := la.scopesOrNil()[key]; !tracked && la.batchScopes() >= maxFailedBatchesPerLane {
			return failedScopeKey{"", opOverflow}
		}
	}
	return key
}

// recordScopeRow writes a failed scope's row. st.persist is held.
func (s *ConcreteDatasetPublicationService) recordScopeRow(lane string, key failedScopeKey, req DatasetPublicationRequest, failedAt time.Time, text string) {
	if s.store == nil {
		return
	}
	if err := s.store.RecordDatasetPublicationFailedScope(storage.DatasetPublicationFailedScope{
		Lane: lane, BatchID: key.batch, Operation: key.op,
		SchemaName: normalizeDatasetPublicationSchema(req.Schema), ProviderID: strings.TrimSpace(req.ProviderID),
		SourceName: strings.TrimSpace(req.SourceName), DatastoreKey: strings.TrimSpace(req.DatastoreKey),
		FailedAt: failedAt, Reason: text,
	}); err != nil {
		log.Errorf("Dataset publication %s batch %q: the failed scope could not be recorded, so a restart does not raise its alert again: %v", lane, key.batch, err)
	}
}

// putScopeLocked records a failed scope in memory. st.mu is held.
func (st *publicationAlertState) putScopeLocked(lane string, key failedScopeKey, req DatasetPublicationRequest, failedAt time.Time, stamp uint64, guarded bool, text string) *laneAlert {
	la := st.lane(lane)
	scope := la.scopes[key]
	if scope == nil {
		scope = &failedScope{}
		la.scopes[key] = scope
	}
	scoped := req
	scoped.BatchID = key.batch
	scope.req, scope.failedAt, scope.guarded = scoped, failedAt, guarded
	scope.stamp = max(scope.stamp, stamp)
	scope.checked, scope.checkErr, scope.check = false, nil, storage.FailedBatchCheck{}
	la.last = text
	return la
}

// failPublication records the failed scope of a failed publication of req,
// which read its lane at stamp and failed at step, and raises the lane's
// alert with text.
func (s *ConcreteDatasetPublicationService) failPublication(req DatasetPublicationRequest, lane string, stamp uint64, step, text string) {
	if s.runs == nil {
		s.alerts.Raise(ops.KindPublicationFailed, lane, ops.SeverityError, text)
		return
	}
	st := &s.runs.alert
	batch, op := failedOperation(req)
	st.persist.Lock()
	defer st.persist.Unlock()

	st.mu.Lock()
	key := scopeKeyFor(st.lanes[lane], batch, op)
	st.mu.Unlock()

	failedAt := s.now().UTC()
	s.recordScopeRow(lane, key, req, failedAt, text)

	st.mu.Lock()
	defer st.mu.Unlock()
	la := st.putScopeLocked(lane, key, req, failedAt, stamp, step == snapshotGuardStep, text)
	if key.op == opOverflow {
		log.Warnf("Dataset publication %s: batch %q failed while %d failed batches of the lane are tracked; it is not checked, and the alert stays until a publication of the whole lane completes", lane, batch, maxFailedBatchesPerLane)
	}
	s.alerts.Raise(ops.KindPublicationFailed, lane, ops.SeverityError, text+" ("+s.scopesText(lane, la)+")")
	if key.op == opBatch && la.scopes[key].countable(s.store) {
		ck := checkKey{lane, key.batch}
		st.event(ck)
		s.enqueueCheckLocked(ck, "", nil)
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
// (failedOperation) that read the lane before it. A completed publication of
// the whole of a failed batch also queues a check of that batch. It reads no
// store on the caller's path.
func (s *ConcreteDatasetPublicationService) publicationCompleted(req DatasetPublicationRequest, lane string, stamp uint64) {
	if s.runs == nil {
		return
	}
	st := &s.runs.alert
	batch, op := failedOperation(req)
	st.mu.Lock()
	ck := checkKey{lane, batch}
	_, pending := st.pending[ck]
	failed := len(st.lanes[lane].scopesOrNil()) > 0 || pending || (st.checking == ck && batch != "")
	st.mu.Unlock()
	if !failed {
		return
	}
	s.clearSameOperation(req, lane, batch, op, stamp, fmt.Sprintf("dataset publication %s: after the publication of batch %q, ", lane, batch))
}

// clearSameOperation removes the failed scopes of lane that a completed
// operation op of req on batch, which read the lane at stamp, covers.
func (s *ConcreteDatasetPublicationService) clearSameOperation(req DatasetPublicationRequest, lane, batch, op string, stamp uint64, after string) {
	st := &s.runs.alert
	st.persist.Lock()
	defer st.persist.Unlock()
	st.mu.Lock()
	la := st.lanes[lane]
	datastore := strings.TrimSpace(req.DatastoreKey)
	var gone []failedScopeKey
	cleared := false
	if ck := (checkKey{lane, batch}); op == opBatch && datastore == "" && la.scopesOrNil()[failedScopeKey{batch, opBatch}] == nil {
		// The batch's scope went with an earlier publication of the whole
		// batch whose check is queued or running: that check read the batch
		// before this publication, so this one queues its own.
		if _, queued := st.pending[ck]; queued || st.checking == ck {
			verify := req
			verify.BatchID = batch
			st.event(ck)
			s.enqueueCheckLocked(ck, after, &verify)
		}
	}
	for key, scope := range la.scopesOrNil() {
		if key.op == opBatch && op == opBatch && key.batch == batch && scope.countable(s.store) && datastore == "" {
			// A completed publication of the whole failed batch: its scope
			// goes now if that publication read the lane after the failed
			// one did, and the batch is checked either way.
			ck := checkKey{lane, batch}
			st.event(ck)
			if scope.stamp >= stamp {
				s.enqueueCheckLocked(ck, after, nil)
				continue
			}
			verify := scope.req
			s.enqueueCheckLocked(ck, after, &verify)
			// The row stays until the check confirms it, so a stop before
			// the check leaves the scope to the next start's check.
			cleared = true
			delete(la.scopes, key)
			log.Infof("Dataset publication %s: failed scope %q of batch %q is published: a publication of the whole batch completed after it; the batch is checked against what it exported", lane, key.op, key.batch)
			continue
		}
		if scope.stamp >= stamp {
			continue
		}
		covers := false
		switch {
		case key.op == opBatch:
			// A batch that cannot be checked (a datastore lane, a store
			// whose batches are not checked): the same publication of it.
			covers = op == opBatch && key.batch == batch && !scope.countable(s.store) && datastore == strings.TrimSpace(scope.req.DatastoreKey)
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
	if len(gone) > 0 || cleared {
		s.restateOrClearLocked(lane, la, after, "the failed operations completed again")
	}
	st.mu.Unlock()
	s.deleteScopeRows(lane, gone)
}

// laneRecordsRemoved queues a check of every failed batch of the lanes a
// supersede or an expiry removed records of (storage.SetLaneRecordsRemoved;
// providerID or sourceName "" stands for any). It does not block.
func (s *ConcreteDatasetPublicationService) laneRecordsRemoved(schemaName, providerID, sourceName string) {
	if s == nil || s.runs == nil {
		return
	}
	schemaName = normalizeDatasetPublicationSchema(schemaName)
	st := &s.runs.alert
	st.mu.Lock()
	defer st.mu.Unlock()
	lanes := make([]string, 0, len(st.lanes))
	for lane := range st.lanes {
		lanes = append(lanes, lane)
	}
	sort.Strings(lanes)
	for _, lane := range lanes {
		for key, scope := range st.lanes[lane].scopes {
			req := scope.req
			if key.op != opBatch || !scope.countable(s.store) || normalizeDatasetPublicationSchema(req.Schema) != schemaName ||
				(providerID != "" && strings.TrimSpace(req.ProviderID) != providerID) || (sourceName != "" && strings.TrimSpace(req.SourceName) != sourceName) {
				continue
			}
			s.enqueueCheckLocked(checkKey{lane, key.batch}, fmt.Sprintf("dataset publication %s: after records of the lane were removed, ", lane), nil)
		}
	}
}

// laneRetracted removes every failed scope of a lane retracted by this node
// (RunPublicationRetract): a retracted lane is never published again. Its
// queued checks decide nothing after it.
func (s *ConcreteDatasetPublicationService) laneRetracted(lane string) {
	if s == nil || s.runs == nil {
		return
	}
	st := &s.runs.alert
	st.persist.Lock()
	defer st.persist.Unlock()
	st.mu.Lock()
	// A queued check of the lane finds neither a scope nor a publication to
	// verify; the running one, if of the lane, decides nothing.
	for key, pending := range st.pending {
		if key.lane == lane {
			pending.verify = nil
		}
	}
	for key := range st.events {
		if key.lane == lane && key != st.checking {
			delete(st.events, key)
		}
	}
	if st.checking.lane == lane {
		st.event(st.checking)
	}
	la := st.lanes[lane]
	gone := map[failedScopeKey]bool{}
	for key := range la.scopesOrNil() {
		gone[key] = true
	}
	if la != nil {
		la.scopes = map[failedScopeKey]*failedScope{}
		log.Infof("Dataset publication %s: the lane is retracted; its %d failed scope(s) are removed", lane, len(gone))
		s.restateOrClearLocked(lane, la, "", "the lane is retracted")
	}
	st.mu.Unlock()
	// Every row of the lane goes, those kept for a check of a publication
	// of the whole batch included.
	if s.store != nil {
		if rows, err := s.store.DatasetPublicationFailedScopes(); err != nil {
			log.Errorf("Dataset publication %s: the failed scopes of the retracted lane could not be read, so a restart may raise its alert again: %v", lane, err)
		} else {
			for _, row := range rows {
				if row.Lane == lane {
					gone[failedScopeKey{row.BatchID, row.Operation}] = true
				}
			}
		}
	}
	keys := make([]failedScopeKey, 0, len(gone))
	for key := range gone {
		keys = append(keys, key)
	}
	s.deleteScopeRows(lane, keys)
}

// restateOrClearLocked clears the lane's alert when it has no failed scope
// left, or restates it: after, when set, names the event that prompted it;
// otherwise the text of the lane's last failure leads. st.mu is held.
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
			log.Errorf("Dataset publication %s: the published scope %q of batch %q could not be removed from the store, so a restart raises its alert again until a check finds it published: %v", lane, key.op, key.batch, err)
		}
	}
}

// enqueueCheckLocked queues a check of a failed batch, unless one is queued
// and has not started (that one then names this event, and verifies what
// this one verifies). st.mu is held.
func (s *ConcreteDatasetPublicationService) enqueueCheckLocked(key checkKey, after string, verify *DatasetPublicationRequest) {
	st := &s.runs.alert
	if st.pending == nil {
		st.pending = make(map[checkKey]*pendingCheck)
	}
	if queued, ok := st.pending[key]; ok {
		if after != "" {
			queued.after = after
		}
		if verify != nil {
			queued.verify = verify
		}
		return
	}
	st.pending[key] = &pendingCheck{after: after, verify: verify}
	s.enqueueAlertJobLocked(func(ctx context.Context) { s.checkFailedBatch(ctx, key) })
}

// checkFailedBatch checks a failed batch from the store, removes its scope
// when it is published or holds no records, records its scope again when a
// publication of the whole batch that removed it did not export what the
// batch holds now, and clears or restates the alert.
func (s *ConcreteDatasetPublicationService) checkFailedBatch(ctx context.Context, key checkKey) {
	st := &s.runs.alert
	st.mu.Lock()
	pending := st.pending[key]
	delete(st.pending, key)
	if pending == nil {
		pending = &pendingCheck{}
	}
	ev := st.events[key]
	scopeKey := failedScopeKey{key.batch, opBatch}
	scope := st.lanes[key.lane].scopesOrNil()[scopeKey]
	var req DatasetPublicationRequest
	switch {
	case scope != nil:
		req = scope.req
	case pending.verify != nil:
		req = *pending.verify
	default:
		st.mu.Unlock()
		return
	}
	countable := (&failedScope{req: req}).countable(s.store) && (scope == nil || scope.countable(s.store))
	if countable {
		st.checking = key
	}
	st.mu.Unlock()
	if !countable {
		return
	}
	defer func() {
		st.mu.Lock()
		st.checking = checkKey{}
		st.mu.Unlock()
	}()

	check, err := s.store.CheckFailedBatchPublication(ctx, normalizeDatasetPublicationSchema(req.Schema), req.ProviderID, req.SourceName, key.batch)
	if ctx.Err() != nil {
		return
	}
	if err == nil && scope == nil {
		err = injectedVerifyCheckFault(key.lane, key.batch)
	}
	if err != nil {
		log.Warnf("Dataset publication %s: batch %q could not be checked (its failed scope stays in the store, and the next start checks it again): %v", key.lane, key.batch, err)
	}
	published := err == nil && (check.Records == 0 || check.Published)

	st.persist.Lock()
	defer st.persist.Unlock()
	st.mu.Lock()
	next := st.pending[key]
	if st.events[key] != ev {
		// Failed again, published whole again or retracted since this check
		// started: the check that event queued decides (a retraction queues
		// none).
		if next == nil {
			delete(st.events, key)
		}
		st.mu.Unlock()
		return
	}
	if next != nil {
		// A newer check of the batch (records of the lane were removed)
		// decides, and verifies what this one verified.
		if next.verify == nil {
			next.verify = pending.verify
		}
		st.mu.Unlock()
		return
	}
	delete(st.events, key)
	la := st.lanes[key.lane]
	scope = la.scopesOrNil()[scopeKey]
	if scope == nil {
		if pending.verify == nil || published {
			st.mu.Unlock()
			if pending.verify != nil {
				log.Infof("Dataset publication %s: batch %q is published as it is now (%d records)", key.lane, key.batch, check.Records)
				s.deleteScopeRows(key.lane, []failedScopeKey{scopeKey})
			}
			return
		}
		// The publication that removed the scope did not export what the
		// batch holds now, or the batch could not be checked against it:
		// the scope is recorded again, and stays until a check confirms.
		text := fmt.Sprintf("dataset publication %s: the publication of the whole of batch %q completed, but the batch changed while it ran and is not published as it is now", key.lane, key.batch)
		if err != nil {
			text = fmt.Sprintf("dataset publication %s: the publication of the whole of batch %q completed, but the check of the batch against what it exported failed", key.lane, key.batch)
		}
		newKey := scopeKeyFor(la, key.batch, opBatch)
		st.mu.Unlock()
		failedAt := s.now().UTC()
		s.recordScopeRow(key.lane, newKey, req, failedAt, text)
		st.mu.Lock()
		defer st.mu.Unlock()
		la = st.putScopeLocked(key.lane, newKey, req, failedAt, 0, false, text)
		if restored := la.scopes[newKey]; newKey.op == opBatch {
			restored.checked, restored.checkErr, restored.check = true, err, check
		}
		if err != nil {
			log.Warnf("Dataset publication %s: batch %q could not be checked against its completed publication of the whole batch: the failed scope is recorded again and the publication_failed alert raised again", key.lane, key.batch)
		} else {
			log.Warnf("Dataset publication %s: batch %q holds %d records, and its last completed publication of the whole batch exported %d: the failed scope is recorded again and the publication_failed alert raised again", key.lane, key.batch, check.Records, check.PublishedRecords)
		}
		s.alerts.Raise(ops.KindPublicationFailed, key.lane, ops.SeverityError, text+" ("+s.scopesText(key.lane, la)+")")
		return
	}
	scope.checked, scope.checkErr, scope.check = true, err, check
	var gone []failedScopeKey
	why := "checked"
	if published {
		gone = append(gone, scopeKey)
		delete(la.scopes, scopeKey)
		why = fmt.Sprintf("a completed publication of the whole of failed batch %q exported the %d records it holds", key.batch, check.Records)
		if check.Records == 0 {
			why = fmt.Sprintf("failed batch %q holds no records any more (they were superseded, expired or retracted)", key.batch)
		}
		log.Infof("Dataset publication %s: %s", key.lane, why)
	}
	s.restateOrClearLocked(key.lane, la, pending.after, why)
	st.mu.Unlock()
	s.deleteScopeRows(key.lane, gone)
}

// enqueueAlertJobLocked runs job after the jobs before it, on the worker,
// unless the node is stopping. st.mu is held.
func (s *ConcreteDatasetPublicationService) enqueueAlertJobLocked(job func(context.Context)) {
	runs := s.runs
	if runs == nil {
		return
	}
	st := &runs.alert
	st.queue = append(st.queue, job)
	if st.running {
		return
	}
	runs.mu.Lock()
	if runs.stopped {
		runs.mu.Unlock()
		st.queue = nil
		st.pending = nil
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
				st.pending = nil
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
		scope := la.scopes[key]
		if key.op == opBatch && !scope.guarded {
			remedy = publicationRemedy
		}
		switch key.op {
		case opBatch:
			parts = append(parts, batchScopeText(s.store, key.batch, scope))
		case opBatchHead:
			parts = append(parts, fmt.Sprintf("a publication of a head of batch %q failed (it stays until a publication of a head of the batch completes)", key.batch))
		case opLane:
			parts = append(parts, "a publication of the whole lane failed (it stays until a publication of the whole lane completes)")
		case opLaneHead:
			parts = append(parts, "a publication of the lane's newest records failed (it stays until a publication of the lane completes)")
		case opAnnounce:
			parts = append(parts, fmt.Sprintf("an announcement of the existing publications of batch %q failed (it stays until such an announcement completes)", key.batch))
		case opOverflow:
			parts = append(parts, fmt.Sprintf("more batches failed than the %d the lane tracks; those are not checked, and the alert stays until a publication of the whole lane (no batch, \"fullCatalog\" or a \"chunkSize\") completes", maxFailedBatchesPerLane))
		}
	}
	return strings.Join(parts, "; ") + remedy
}

// batchScopeText is the alert's text of a failed batch.
func batchScopeText(store *storage.FlatSQLStore, batch string, scope *failedScope) string {
	if scope.guarded {
		return fmt.Sprintf("batch %q is unpublished: the snapshot guard refused it, and it refuses a publication of the batch again while the batch is under the guard's floor; it clears when the batch holds no records any more (a newer batch supersedes it on a replace-current lane), or when a publication of the whole batch completes once the guard no longer refuses it (min_record_ratio / min_records lowered or removed for this lane)", batch)
	}
	switch {
	case !scope.countable(store):
		why := "it is not checked on a datastore lane"
		if scope.checkErr != nil {
			why = scope.checkErr.Error()
		}
		return fmt.Sprintf("batch %q is unpublished (%s; it stays until a publication of the whole batch completes)", batch, why)
	case !scope.checked:
		return fmt.Sprintf("batch %q is unpublished (it is being checked)", batch)
	case scope.checkErr != nil:
		return fmt.Sprintf("batch %q is unpublished (it could not be checked: %v)", batch, scope.checkErr)
	case scope.check.Series == 0:
		return fmt.Sprintf("batch %q is unpublished: no publication of the whole batch completed; it holds %s", batch, recordsText(scope.check.Records))
	case scope.check.Records > scope.check.PublishedRecords:
		return fmt.Sprintf("batch %q is unpublished: %d of its %s arrived after its last completed publication of the whole batch and are in no publication of it", batch, scope.check.Records-scope.check.PublishedRecords, recordsText(scope.check.Records))
	default:
		return fmt.Sprintf("batch %q is unpublished: its records changed after its last completed publication of the whole batch (%d now, %d published)", batch, scope.check.Records, scope.check.PublishedRecords)
	}
}

// recordsText is "1 record" or "<n> records".
func recordsText(n int64) string {
	if n == 1 {
		return "1 record"
	}
	return fmt.Sprintf("%d records", n)
}
