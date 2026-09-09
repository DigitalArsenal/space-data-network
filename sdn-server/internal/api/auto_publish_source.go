package api

import (
	"context"
	"strings"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/config"
)

// Only explicitly configured source tuples enter this map. The durable store
// is the restart checkpoint: startup marks each source dirty, and publication
// enumerates that exact source across its original batch identities.
type sourcePublicationKey struct {
	schema, provider, source string
}

type sourcePublicationState struct {
	request     DatasetPublicationRequest
	interval    time.Duration
	dirty       bool
	nextAttempt time.Time
	attempts    int
	retrying    bool
}

func sourceKey(lane config.AutoPublishLane) sourcePublicationKey {
	return sourcePublicationKey{normalizeAutoPublishSchema(lane.Schema), strings.TrimSpace(lane.ProviderID), strings.TrimSpace(lane.SourceName)}
}

// Caller holds p.mu. First matching lane wins, just as in batch scope.
func (p *AutoPublisher) sourceStateLocked(lane config.AutoPublishLane) *sourcePublicationState {
	key := sourceKey(lane)
	if state := p.sources[key]; state != nil {
		return state
	}
	interval := lane.MinInterval
	if interval <= 0 {
		interval = autoPublishDefaultMinInterval
	}
	state := &sourcePublicationState{
		request:  DatasetPublicationRequest{Schema: key.schema, ProviderID: key.provider, SourceName: key.source, FullCatalog: true, MaxShardBytes: lane.MaxShardBytes},
		interval: interval,
	}
	p.sources[key] = state
	return state
}

func (p *AutoPublisher) seedSourcePublicationsLocked() {
	for _, lane := range p.lanes {
		if lane.PublishScope != "source" {
			continue
		}
		key := sourceKey(lane)
		matched, ok := p.matchLane(key.schema, key.provider, key.source)
		if ok && matched.PublishScope == "source" {
			p.sourceStateLocked(matched).dirty = true
		}
	}
}

func (p *AutoPublisher) observeSourceIngest(lane config.AutoPublishLane) {
	p.mu.Lock()
	p.sourceStateLocked(lane).dirty = true
	p.mu.Unlock()
	select {
	case p.sourceWake <- struct{}{}:
	default:
	}
}

func (p *AutoPublisher) sourcePublicationDelay() (time.Duration, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	now := p.now()
	var earliest time.Time
	found := false
	for _, state := range p.sources {
		if state.dirty && (!found || state.nextAttempt.Before(earliest)) {
			earliest = state.nextAttempt
			found = true
		}
	}
	if !found {
		return 0, false
	}
	if !earliest.After(now) {
		return 0, true
	}
	return earliest.Sub(now), true
}

func (p *AutoPublisher) publishReadySource(ctx context.Context) {
	p.mu.Lock()
	now := p.now()
	var selected *sourcePublicationState
	for _, state := range p.sources {
		if state.dirty && !state.nextAttempt.After(now) && (selected == nil || state.nextAttempt.Before(selected.nextAttempt)) {
			selected = state
		}
	}
	if selected == nil {
		p.mu.Unlock()
		return
	}
	// Clear only the event being consumed. Any ingest while the service
	// exports/pins/announces marks it dirty again for a later complete export.
	selected.dirty = false
	selected.nextAttempt = now.Add(selected.interval)
	if selected.retrying {
		selected.retrying = false
		p.retrying--
	}
	req := selected.request
	p.mu.Unlock()

	runCtx, cancel := context.WithTimeout(ctx, autoPublishTimeout)
	result, err := p.service.PublishDatasetUpdate(runCtx, req)
	cancel()

	p.mu.Lock()
	attempts := 0
	if err != nil {
		selected.dirty = true
		selected.attempts++
		selected.retrying = true
		p.retrying++
		// Source state is never discarded on retry exhaustion. Backoff is
		// capped by the existing connector policy, and startup catchup also
		// covers a process stopping while a retry is pending.
		selected.nextAttempt = p.now().Add(p.retryBackoff(selected.attempts))
		attempts = selected.attempts
	} else {
		selected.attempts = 0
		if result != nil {
			p.published++
		}
	}
	p.mu.Unlock()
	if err != nil {
		log.Warnf("auto-publish source %s %s/%s failed (attempt %d, pending retry): %v", req.Schema, req.ProviderID, req.SourceName, attempts, err)
	} else if result != nil {
		log.Infof("auto-published source %s %s/%s: %d records, manifest %s", req.Schema, req.ProviderID, req.SourceName, result.RecordCount, result.ManifestCID)
	}
	if p.onPublished != nil {
		p.onPublished(req, result, err)
	}
}
