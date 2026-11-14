package api

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/config"
)

// DatasetPublicationBatchSource enumerates committed identities from storage.
// Completion remains in the publication series ledger, after announcement.
// Keeping source records and their series is required for restart recovery.
type DatasetPublicationBatchSource interface {
	AutoPublishBatches(context.Context) ([]IngestedBatch, error)
}

func (s *ConcreteDatasetPublicationService) AutoPublishBatches(ctx context.Context) ([]IngestedBatch, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.store.RetryPublicationRecovery()
	rows, err := s.store.SourceBatchProgress()
	if err != nil {
		return nil, err
	}
	out := make([]IngestedBatch, 0, len(rows))
	for _, row := range s.store.RetainedPublicationBatches() {
		out = append(out, IngestedBatch{Schema: row.Schema, ProviderID: row.Provider, SourceName: row.Source, BatchID: row.Batch})
	}
	for _, row := range rows {
		if s.store.RetainsPublicationSource(row.SchemaName, row.ProviderID, row.SourceName) {
			continue
		}
		out = append(out, IngestedBatch{Schema: row.SchemaName, ProviderID: row.ProviderID, SourceName: row.SourceName, BatchID: row.BatchID})
	}
	return out, nil
}

// Matching is independent for each scope; the first rule of that scope wins.
func (p *AutoPublisher) matchScope(schema, provider, source string, full bool) (config.AutoPublishLane, bool) {
	for _, rule := range p.lanes {
		if (rule.PublishScope == "source") != full || normalizeAutoPublishSchema(rule.Schema) != schema {
			continue
		}
		if full {
			if strings.TrimSpace(rule.ProviderID) == provider && strings.TrimSpace(rule.SourceName) == source {
				return rule, true
			}
		} else if (strings.TrimSpace(rule.ProviderID) == "" || strings.EqualFold(strings.TrimSpace(rule.ProviderID), provider)) &&
			(strings.TrimSpace(rule.SourceName) == "" || strings.EqualFold(strings.TrimSpace(rule.SourceName), source)) {
			return rule, true
		}
	}
	return config.AutoPublishLane{}, false
}

func (p *AutoPublisher) isPairedSource(schema, provider, source string) bool {
	rule, batch := p.matchScope(schema, provider, source, false)
	snapshot, _ := p.matchScope(schema, provider, source, true)
	return batch && (rule.PairedPublication || snapshot.PairedPublication || rule.RetainForPublication)
}

type pairedBatchPublication struct {
	order      uint64
	inFlight   bool
	generation uint64
	request    DatasetPublicationRequest
	rule       config.AutoPublishLane
	due        time.Time
	queued     bool
	attempts   int
	retrying   bool
}

func (p *AutoPublisher) wakePublications() {
	select {
	case p.sourceWake <- struct{}{}:
	default:
	}
}

func (p *AutoPublisher) observePairedBatch(rule config.AutoPublishLane, schema, provider, source, batch string, observed bool) {
	req := DatasetPublicationRequest{Schema: schema, ProviderID: provider, SourceName: source, BatchID: batch, MaxShardBytes: rule.MaxShardBytes}
	key := autoPublishBatchKey(req)
	p.mu.Lock()
	state := p.pairedPending[key]
	if state == nil {
		if len(p.pairedPending) >= 4096 {
			p.backlogError = "publication backlog exceeds 4096 batches; stop ingestion and drain publication"
			p.mu.Unlock()
			log.Error(p.backlogError)
			return
		}
		p.pairedSequence++
		state = &pairedBatchPublication{order: p.pairedSequence, request: req, rule: rule, due: p.now().Add(rule.Settle)}
		p.pairedPending[key] = state
	} else if observed && rule.Settle > 0 {
		state.due = p.now().Add(rule.Settle)
	}
	if observed {
		state.generation++
	}
	p.mu.Unlock()
	p.queueReadyPairedBatches()
	p.wakePublications()
}

func (p *AutoPublisher) recoverPairedBatches(ctx context.Context) {
	enabled := false
	for _, rule := range p.lanes {
		key := sourceKey(rule)
		if p.isPairedSource(key.schema, key.provider, key.source) {
			enabled = true
			break
		}
	}
	if !enabled {
		return
	}
	source, ok := p.service.(DatasetPublicationBatchSource)
	if !ok {
		log.Warn("paired auto-publication service has no durable batch source")
		return
	}
	batches, err := source.AutoPublishBatches(ctx)
	if err != nil {
		log.Warnf("recover auto-publication batches: %v", err)
		return
	}
	for _, batch := range batches {
		if ctx.Err() != nil {
			return
		}
		schema := normalizeAutoPublishSchema(batch.Schema)
		if batch.ProviderID == "" || batch.SourceName == "" || batch.BatchID == "" || !p.isPairedSource(schema, batch.ProviderID, batch.SourceName) {
			continue
		}
		req := DatasetPublicationRequest{Schema: schema, ProviderID: batch.ProviderID, SourceName: batch.SourceName, BatchID: batch.BatchID}
		if p.unchangedSinceLastPublication(ctx, req) {
			continue
		}
		rule, _ := p.matchScope(schema, batch.ProviderID, batch.SourceName, false)
		p.observePairedBatch(rule, schema, batch.ProviderID, batch.SourceName, batch.BatchID, false)
	}
}

// The channel is bounded. Identities that do not fit remain pending; storage
// reconstructs them after a process exit even if no observation was delivered.
func (p *AutoPublisher) queueReadyPairedBatches() {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, state := range p.orderedPairedLocked() {
		if state.inFlight || state.queued || p.pairedBatchDueLocked(state).After(p.now()) {
			continue
		}
		select {
		case p.queue <- state.request:
			state.queued = true
		default:
			return
		}
	}
}

func (p *AutoPublisher) publicationDelay() (time.Duration, bool) {
	delay, found := p.sourcePublicationDelay()
	p.mu.Lock()
	defer p.mu.Unlock()
	now := p.now()
	for _, state := range p.pairedPending {
		if state.queued || state.inFlight || !p.firstPairedLocked(state) {
			continue
		}
		d := p.pairedBatchDueLocked(state).Sub(now)
		if d < 0 {
			d = 0
		}
		if !found || d < delay {
			delay, found = d, true
		}
	}
	return delay, found
}

func (p *AutoPublisher) publishPairedBatch(ctx context.Context, req DatasetPublicationRequest) bool {
	key := autoPublishBatchKey(req)
	p.mu.Lock()
	state := p.pairedPending[key]
	if state == nil {
		p.mu.Unlock()
		return p.isPairedSource(req.Schema, req.ProviderID, req.SourceName)
	}
	state.queued = false
	if !p.firstPairedLocked(state) {
		p.mu.Unlock()
		return true
	}
	due := p.pairedBatchDueLocked(state)
	if due.After(p.now()) {
		state.due = due
		p.mu.Unlock()
		return true
	}
	if p.pairedTurns == nil {
		p.pairedTurns = map[sourcePublicationKey]uint64{}
	}
	sourceKey := sourcePublicationKey{req.Schema, req.ProviderID, req.SourceName}
	if state.order > p.pairedTurns[sourceKey] {
		for _, pending := range p.pairedPending {
			if samePairedSource(state, pending) && pending.order > p.pairedTurns[sourceKey] {
				p.pairedTurns[sourceKey] = pending.order
			}
		}
		p.lastPublished[autoPublishLaneKey(state.rule)] = p.now()
	}
	state.inFlight = true
	generation := state.generation
	p.mu.Unlock()
	runCtx, cancel := context.WithTimeout(ctx, autoPublishTimeout)
	defer cancel()
	// A replay is acknowledged only by a successfully completed series.
	unchanged := p.unchangedSinceLastPublication(runCtx, req)
	var result *DatasetPublicationResult
	var err error
	if !unchanged {
		result, err = p.service.PublishDatasetUpdate(runCtx, req)
		if err == nil && result == nil {
			err = fmt.Errorf("publication returned no completion")
		}
	}
	p.mu.Lock()
	state.inFlight = false
	if state.retrying {
		p.retrying--
		state.retrying = false
	}
	if err != nil {
		state.attempts++
		state.retrying = true
		p.retrying++
		state.due = p.now().Add(p.retryBackoff(state.attempts))
	} else {
		if generation == state.generation {
			delete(p.pairedPending, key)
		}
		if unchanged {
			p.unchanged++
		} else {
			p.published++
		}
	}
	p.mu.Unlock()
	if err != nil {
		log.Warnf("auto-publication batch remains pending: %v", err)
	}
	if p.onPublished != nil {
		p.onPublished(req, result, err)
	}
	return true
}

// Caller holds p.mu. Apply the shared rate clock before admitting more work.
func (p *AutoPublisher) pairedBatchDueLocked(state *pairedBatchPublication) time.Time {
	key := sourcePublicationKey{state.request.Schema, state.request.ProviderID, state.request.SourceName}
	if state.order <= p.pairedTurns[key] {
		return state.due
	}
	interval := state.rule.MinInterval
	if interval <= 0 {
		interval = autoPublishDefaultMinInterval
	}
	due := p.lastPublished[autoPublishLaneKey(state.rule)].Add(interval)
	if state.due.After(due) {
		due = state.due
	}
	return due
}

func samePairedSource(a, b *pairedBatchPublication) bool {
	return a.request.Schema == b.request.Schema && a.request.ProviderID == b.request.ProviderID && a.request.SourceName == b.request.SourceName
}
func (p *AutoPublisher) orderedPairedLocked() []*pairedBatchPublication {
	out := make([]*pairedBatchPublication, 0, len(p.pairedPending))
	for _, s := range p.pairedPending {
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].order < out[j].order })
	return out
}
func (p *AutoPublisher) firstPairedLocked(state *pairedBatchPublication) bool {
	for _, other := range p.pairedPending {
		if samePairedSource(state, other) && other.order < state.order {
			return false
		}
	}
	return true
}
