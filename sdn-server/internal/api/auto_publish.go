package api

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/config"
)

// WHY THIS FILE EXISTS (2026-08-04, sdn-rfb-publish-to-consumer-node).
//
// The producer node ingested 5,289 $RFB records from SatNOGS on host-02 and
// host-01 never saw one of them. Nothing was broken in the transport: the
// topic was joined, the peer was linked, the publication machinery existed and
// worked. What was missing was the TRIGGER — the step that turns "a batch
// landed in the store" into "a dataset publication exists for it".
//
// Only the CelesTrak lanes had that step, and they carry it INSIDE the flow: a
// publish_request node builds an HTTP request that POSTs the loopback admin
// route, gated on a flow-config URL. Two consequences followed. Any ingest
// lane whose module has no publish node (every non-CelesTrak lane, including
// SatNOGS RF) can never publish, no matter how it is configured. And the
// in-flow trigger blocks the flow for the whole export: the §19 publish-trigger
// bundle deadlocked the GP flow for 100 minutes with zero store writes and was
// rolled back.
//
// AutoPublisher is the trigger as a HOST CONNECTOR instead. It observes what
// the storage connector already reports (a provenance-tagged batch landed),
// matches it against operator configuration, and runs the SAME dataset
// publication the admin route runs — on its own single-slot queue, never on
// the caller's goroutine, so the wasm ingest hostcall returns immediately.
//
// It decides nothing about the data. Which lanes republish is configuration
// (publishing.auto_publish); an empty list publishes nothing, because absence
// of configuration is not permission to republish a share-alike source.

// autoPublishDefaultMinInterval rate-limits one lane. A publication exports,
// pins and announces a whole shard, so an aggressive ingest timer must not
// become a publication storm.
const autoPublishDefaultMinInterval = 5 * time.Minute

// autoPublishTimeout bounds one publication attempt. The full-catalog export
// on a 2-vCPU producer is minutes, not seconds — this is a runaway guard, not
// a latency budget.
const autoPublishTimeout = 30 * time.Minute

// autoPublishQueueDepth bounds the pending batches. Deeper than one so a burst
// of per-schema batches from one flow run all publish; bounded so a wedged
// publication can never grow memory without limit.
const autoPublishQueueDepth = 16

// IngestedBatch is one provenance-tagged batch as reported by the host's
// storage connector.
//
// It deliberately mirrors caps.IngestObservation by VALUE rather than
// importing it: the publication surface must not depend on the module runtime,
// and the daemon already owns the two-line adaptation where both are wired.
type IngestedBatch struct {
	Schema     string
	ProviderID string
	SourceName string
	BatchID    string
	Inserted   int
}

// AutoPublisher republishes configured ingest lanes as dataset publications.
type AutoPublisher struct {
	service DatasetPublicationService
	lanes   []config.AutoPublishLane

	queue chan DatasetPublicationRequest

	mu sync.Mutex
	// lastPublished is the per-lane rate-limit clock, keyed by the matched
	// lane's identity.
	lastPublished map[string]time.Time
	// publishedBatches remembers the (schema, provider, source, batch) tuples
	// already queued. A flow that re-ingests the SAME batch id (the CelesTrak
	// debounce replay shape) must not republish it.
	publishedBatches map[string]struct{}
	started          bool
	stop             chan struct{}
	done             chan struct{}

	now func() time.Time
	// onPublished reports the outcome of each attempt. Tests use it; the
	// daemon leaves it nil and reads the log.
	onPublished func(DatasetPublicationRequest, *DatasetPublicationResult, error)

	// attempts counts publication attempts per batch key so a failure is
	// retried with backoff instead of being logged and forgotten (PUB-04:
	// 42 of 98 prod publications in a week failed and were never announced,
	// 41 of them "catalog still hydrating" — a transient).
	attempts map[string]int
	// retryBackoff answers how long to wait before attempt n+1 (n >= 1).
	// The daemon uses autoPublishRetryBackoff; tests shorten it.
	retryBackoff func(attempt int) time.Duration
	// counters are the operator-visible outcome tallies (Stats).
	published, retrying, failed int64
}

// AutoPublishStats are the outcome tallies since start: publications that
// succeeded, batches currently waiting for a retry, and batches given up on
// after autoPublishMaxAttempts.
type AutoPublishStats struct {
	Published int64
	Retrying  int64
	Failed    int64
}

// autoPublishMaxAttempts bounds retries of one batch. With the default
// backoff (30 s doubling, capped at 30 min) the last attempt lands about
// 1 h 20 m after the first, comfortably past a catalog hydration.
const autoPublishMaxAttempts = 6

// autoPublishRetryBackoff is the daemon's wait before attempt n+1.
func autoPublishRetryBackoff(attempt int) time.Duration {
	wait := 30 * time.Second
	for i := 1; i < attempt; i++ {
		wait *= 2
		if wait >= 30*time.Minute {
			return 30 * time.Minute
		}
	}
	return wait
}

// NewAutoPublisher builds the trigger for the configured lanes. It returns nil
// when nothing is configured or no publication service exists — a nil
// *AutoPublisher is safe to Observe/Start/Stop, so callers need no branch.
func NewAutoPublisher(service DatasetPublicationService, lanes []config.AutoPublishLane) *AutoPublisher {
	if service == nil {
		return nil
	}
	valid := make([]config.AutoPublishLane, 0, len(lanes))
	for _, lane := range lanes {
		if normalizeAutoPublishSchema(lane.Schema) == "" {
			// A lane without a schema matches everything, which is exactly the
			// fail-open shape this surface must not have. Drop it loudly at
			// startup rather than publish an unnamed lane.
			log.Warnf("publishing.auto_publish entry with no schema ignored (provider %q source %q)",
				lane.ProviderID, lane.SourceName)
			continue
		}
		valid = append(valid, lane)
	}
	if len(valid) == 0 {
		return nil
	}
	return &AutoPublisher{
		service:          service,
		lanes:            valid,
		queue:            make(chan DatasetPublicationRequest, autoPublishQueueDepth),
		lastPublished:    make(map[string]time.Time),
		publishedBatches: make(map[string]struct{}),
		attempts:         make(map[string]int),
		retryBackoff:     autoPublishRetryBackoff,
		stop:             make(chan struct{}),
		done:             make(chan struct{}),
		now:              func() time.Time { return time.Now().UTC() },
	}
}

// Stats reports the outcome tallies. Safe on a nil publisher.
func (p *AutoPublisher) Stats() AutoPublishStats {
	if p == nil {
		return AutoPublishStats{}
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return AutoPublishStats{Published: p.published, Retrying: p.retrying, Failed: p.failed}
}

// Lanes reports the configured lanes (diagnostics; never mutated).
func (p *AutoPublisher) Lanes() []config.AutoPublishLane {
	if p == nil {
		return nil
	}
	return p.lanes
}

// Start runs the single publication worker. Safe to call once; later calls are
// no-ops.
func (p *AutoPublisher) Start(ctx context.Context) {
	if p == nil {
		return
	}
	p.mu.Lock()
	if p.started {
		p.mu.Unlock()
		return
	}
	p.started = true
	p.mu.Unlock()

	go p.run(ctx)
}

// Stop halts the worker and waits for an in-flight publication to finish.
func (p *AutoPublisher) Stop() {
	if p == nil {
		return
	}
	p.mu.Lock()
	started := p.started
	p.mu.Unlock()
	if !started {
		return
	}
	select {
	case <-p.stop:
	default:
		close(p.stop)
	}
	<-p.done
}

func (p *AutoPublisher) run(ctx context.Context) {
	defer close(p.done)
	for {
		select {
		case <-ctx.Done():
			return
		case <-p.stop:
			return
		case req := <-p.queue:
			p.publish(ctx, req)
		}
	}
}

func (p *AutoPublisher) publish(ctx context.Context, req DatasetPublicationRequest) {
	runCtx, cancel := context.WithTimeout(ctx, autoPublishTimeout)
	defer cancel()

	result, err := p.service.PublishDatasetUpdate(runCtx, req)
	batchKey := autoPublishBatchKey(req)
	if err != nil {
		// A failed publication is an operational fact: the batch is stored and
		// the network cannot see it. Most failures are transient (the catalog
		// is still hydrating after a boot, Kubo is restarting), so the batch
		// is retried with backoff and only given up on after
		// autoPublishMaxAttempts — and that is counted, not just logged.
		p.mu.Lock()
		p.attempts[batchKey]++
		attempt := p.attempts[batchKey]
		if attempt < autoPublishMaxAttempts {
			p.retrying++
			p.mu.Unlock()
			wait := p.retryBackoff(attempt)
			log.Warnf("auto-publish failed for %s %s/%s batch %s (attempt %d of %d, retry in %s): %v",
				req.Schema, req.ProviderID, req.SourceName, req.BatchID, attempt, autoPublishMaxAttempts, wait, err)
			go p.requeueAfter(wait, req)
		} else {
			p.failed++
			delete(p.attempts, batchKey)
			p.mu.Unlock()
			log.Errorf("auto-publish gave up on %s %s/%s batch %s after %d attempts: %v",
				req.Schema, req.ProviderID, req.SourceName, req.BatchID, attempt, err)
		}
	} else if result != nil {
		p.mu.Lock()
		p.published++
		delete(p.attempts, batchKey)
		p.mu.Unlock()
		log.Infof("auto-published %s %s/%s batch %s: %d records, manifest %s",
			req.Schema, req.ProviderID, req.SourceName, req.BatchID,
			result.RecordCount, result.ManifestCID)
	}
	if p.onPublished != nil {
		p.onPublished(req, result, err)
	}
}

// ObserveIngest is the storage connector's tap. It NEVER blocks the caller:
// matching and bookkeeping are O(lanes), and the publication itself runs on
// the worker goroutine.
func (p *AutoPublisher) ObserveIngest(batch IngestedBatch) {
	if p == nil {
		return
	}
	if batch.Inserted <= 0 {
		// Nothing new landed. Republishing an unchanged window would announce
		// a shard the network already has.
		return
	}
	schema := normalizeAutoPublishSchema(batch.Schema)
	provider := strings.TrimSpace(batch.ProviderID)
	source := strings.TrimSpace(batch.SourceName)
	batchID := strings.TrimSpace(batch.BatchID)
	if schema == "" || provider == "" || source == "" || batchID == "" {
		// The publication request needs all four to name a window. An
		// incomplete observation is dropped rather than guessed at.
		return
	}

	lane, ok := p.matchLane(schema, provider, source)
	if !ok {
		return
	}

	laneKey := autoPublishLaneKey(lane)
	batchKey := laneKey + "|" + batchID
	now := p.now()

	p.mu.Lock()
	if _, seen := p.publishedBatches[batchKey]; seen {
		p.mu.Unlock()
		return
	}
	minInterval := lane.MinInterval
	if minInterval <= 0 {
		minInterval = autoPublishDefaultMinInterval
	}
	if last, ok := p.lastPublished[laneKey]; ok && now.Sub(last) < minInterval {
		p.mu.Unlock()
		log.Debugf("auto-publish skipped for %s %s/%s: within the %s lane interval",
			schema, provider, source, minInterval)
		return
	}
	p.lastPublished[laneKey] = now
	p.publishedBatches[batchKey] = struct{}{}
	// Bound the dedupe memory: lanes are few and batch ids rotate, so a simple
	// reset at a generous ceiling is enough and cannot leak.
	if len(p.publishedBatches) > 4096 {
		p.publishedBatches = map[string]struct{}{batchKey: {}}
	}
	p.mu.Unlock()

	req := DatasetPublicationRequest{
		Schema:     schema,
		ProviderID: provider,
		SourceName: source,
		BatchID:    batchID,
		// Per-lane byte budget (config: publishing.auto_publish[].max_shard_bytes).
		// Zero falls through to the node default in the publication service.
		MaxShardBytes: lane.MaxShardBytes,
	}
	select {
	case p.queue <- req:
	default:
		// The worker is behind. Give the lane its slot back so the NEXT batch
		// is not also suppressed by the rate limiter.
		p.mu.Lock()
		delete(p.lastPublished, laneKey)
		delete(p.publishedBatches, batchKey)
		p.mu.Unlock()
		log.Warnf("auto-publish queue full; dropped %s %s/%s batch %s",
			schema, provider, source, batchID)
	}
}

func (p *AutoPublisher) matchLane(schema, provider, source string) (config.AutoPublishLane, bool) {
	for _, lane := range p.lanes {
		if normalizeAutoPublishSchema(lane.Schema) != schema {
			continue
		}
		if want := strings.TrimSpace(lane.ProviderID); want != "" && !strings.EqualFold(want, provider) {
			continue
		}
		if want := strings.TrimSpace(lane.SourceName); want != "" && !strings.EqualFold(want, source) {
			continue
		}
		return lane, true
	}
	return config.AutoPublishLane{}, false
}

func autoPublishLaneKey(lane config.AutoPublishLane) string {
	return normalizeAutoPublishSchema(lane.Schema) + "|" +
		strings.ToLower(strings.TrimSpace(lane.ProviderID)) + "|" +
		strings.ToLower(strings.TrimSpace(lane.SourceName))
}

// normalizeAutoPublishSchema accepts either the standard code ("RFB") or the
// schema file ("rfb.fbs") and returns the canonical schema file name.
func normalizeAutoPublishSchema(schema string) string {
	schema = strings.TrimSpace(schema)
	if schema == "" {
		return ""
	}
	upper := strings.ToUpper(schema)
	if strings.HasSuffix(upper, ".FBS") {
		return strings.TrimSuffix(upper, ".FBS") + ".fbs"
	}
	return upper + ".fbs"
}

func autoPublishBatchKey(req DatasetPublicationRequest) string {
	return normalizeAutoPublishSchema(req.Schema) + "|" + req.ProviderID + "|" + req.SourceName + "|" + req.BatchID
}

// requeueAfter puts a failed batch back on the queue once its backoff has
// elapsed, unless the publisher stops first. A full queue drops the retry
// and counts the batch as failed rather than blocking.
func (p *AutoPublisher) requeueAfter(wait time.Duration, req DatasetPublicationRequest) {
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-timer.C:
	case <-p.stop:
		return
	}
	p.mu.Lock()
	p.retrying--
	p.mu.Unlock()
	select {
	case p.queue <- req:
	default:
		p.mu.Lock()
		p.failed++
		delete(p.attempts, autoPublishBatchKey(req))
		p.mu.Unlock()
		log.Warnf("auto-publish queue full; dropped retry of %s %s/%s batch %s",
			req.Schema, req.ProviderID, req.SourceName, req.BatchID)
	}
}
