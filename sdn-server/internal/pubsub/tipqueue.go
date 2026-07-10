package pubsub

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/PNM"
	flatbuffers "github.com/google/flatbuffers/go"
	ps "github.com/libp2p/go-libp2p-pubsub"
	"github.com/spacedatanetwork/sdn-server/internal/channels"
)

// TipQueue errors.
var (
	ErrQueueFull     = errors.New("tip queue is full")
	ErrNotSubscribed = errors.New("not subscribed to PNM topic")
	ErrInvalidPNM    = errors.New("invalid PNM message")
	ErrNoTopicMgr    = errors.New("topic manager not set")

	// ErrFetchTooLarge is the sentinel a ContentFetcher (see ipfsTipFetcher
	// in internal/node) wraps/returns when a tip's content exceeds the
	// configured MaxFetchBytes ceiling (Task D4). dispatchFetch checks for
	// it via errors.Is to count the rejection separately from an ordinary
	// fetch failure and to skip the chained Pin step: oversize content is
	// never pinned.
	ErrFetchTooLarge = errors.New("tip content exceeds max fetch size")
)

const pnmSchema = "PNM.fbs"

// ContentFetcher fetches content by CID.
type ContentFetcher interface {
	Fetch(ctx context.Context, cid string) ([]byte, error)
}

// ContentPinner pins and unpins content.
type ContentPinner interface {
	Pin(ctx context.Context, cid string, ttl time.Duration) error
	Unpin(ctx context.Context, cid string) error
}

// Tip represents a received publish notification.
type Tip struct {
	PeerID           string
	CID              string
	SchemaType       string // FILE_ID (e.g., "OMM")
	FileName         string
	MultiformatAddr  string
	Signature        string
	PublishTimestamp time.Time
	ReceivedAt       time.Time
	Fetched          bool
	Pinned           bool
	PinExpiry        time.Time

	// RawPNM is the complete size-prefixed PNM buffer this tip was parsed
	// from. Callers that need to drive a schema-specific materialization
	// path (e.g. a dataset-publication PNM consumer that re-verifies the
	// signature against a provider key) need the original bytes, not just
	// the parsed fields above, so OnTip handlers get them here rather than
	// having to re-fetch or reconstruct the buffer.
	RawPNM []byte
}

// TipHandler is called when a tip is received.
type TipHandler func(tip *Tip, config ResolvedConfig)

// PNMVerifier rejects untrusted PNM bytes before the queue records or fetches a tip.
type PNMVerifier func(pnmBytes []byte) (channels.PNMTrustEvidence, error)

// TipQueue manages PNM-based tip/queue messaging.
type TipQueue struct {
	config   *TipQueueConfig
	topicMgr *TopicManager
	fetcher  ContentFetcher
	pinner   ContentPinner
	verifier PNMVerifier

	subscription *ps.Subscription
	tips         map[string][]*Tip // schema -> pending tips
	pinnedCIDs   map[string]*Tip   // CID -> tip info

	handlers []TipHandler

	// fetchSem bounds in-flight ContentFetcher.Fetch calls to
	// config.MaxConcurrentFetches (Task D4). Buffered channel used as a
	// counting semaphore; sized once at construction since
	// MaxConcurrentFetches is not hot-reloaded.
	fetchSem chan struct{}
	// fetchLimiter enforces config.MinFetchInterval between the start of
	// consecutive fetches (Task D4).
	fetchLimiter *fetchRateLimiter
	// oversizeRejections counts tips rejected by dispatchFetch because the
	// fetcher reported ErrFetchTooLarge (Task D4). Read via
	// OversizeRejections.
	oversizeRejections int64

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
	mu     sync.RWMutex
}

// NewTipQueue creates a new TipQueue.
func NewTipQueue(config *TipQueueConfig) *TipQueue {
	if config == nil {
		config = NewTipQueueConfig()
	}
	// A config built by hand (or unmarshaled from a source that omits
	// these fields) should not silently disable the D4 resource caps --
	// treat a non-positive value the same as "unset" and fall back to the
	// package defaults, mirroring StartTTLSweeper's existing
	// non-positive-interval-defaults convention below.
	if config.MaxFetchBytes <= 0 {
		config.MaxFetchBytes = DefaultMaxFetchBytes
	}
	if config.MaxConcurrentFetches <= 0 {
		config.MaxConcurrentFetches = DefaultMaxConcurrentFetches
	}
	if config.MinFetchInterval <= 0 {
		config.MinFetchInterval = DefaultMinFetchInterval
	}

	ctx, cancel := context.WithCancel(context.Background())

	return &TipQueue{
		config:       config,
		verifier:     channels.VerifySignedPNMEnvelope,
		tips:         make(map[string][]*Tip),
		pinnedCIDs:   make(map[string]*Tip),
		handlers:     make([]TipHandler, 0),
		fetchSem:     make(chan struct{}, config.MaxConcurrentFetches),
		fetchLimiter: newFetchRateLimiter(config.MinFetchInterval),
		ctx:          ctx,
		cancel:       cancel,
	}
}

// OversizeRejections returns the number of tips rejected so far because
// their fetched content exceeded MaxFetchBytes (Task D4).
func (tq *TipQueue) OversizeRejections() int64 {
	return atomic.LoadInt64(&tq.oversizeRejections)
}

// fetchRateLimiter is a minimal token-bucket-of-one rate limiter: it
// enforces a minimum spacing between successive Wait callers returning,
// which is sufficient for "don't let a burst of tips all fetch at once"
// (Task D4) without pulling in a rate-limiting dependency.
type fetchRateLimiter struct {
	interval time.Duration

	mu   sync.Mutex
	next time.Time
}

func newFetchRateLimiter(interval time.Duration) *fetchRateLimiter {
	return &fetchRateLimiter{interval: interval}
}

// wait blocks until the next fetch slot is available and reserves it, or
// returns early with ctx's error if ctx is done first (e.g. TipQueue.Close
// was called). A non-positive interval disables rate limiting.
func (r *fetchRateLimiter) wait(ctx context.Context) error {
	if r == nil || r.interval <= 0 {
		return nil
	}
	for {
		r.mu.Lock()
		now := time.Now()
		if !now.Before(r.next) {
			r.next = now.Add(r.interval)
			r.mu.Unlock()
			return nil
		}
		wait := r.next.Sub(now)
		r.mu.Unlock()

		timer := time.NewTimer(wait)
		select {
		case <-timer.C:
			// Loop and re-check: another goroutine may have reserved the
			// slot we were waiting on in the meantime.
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		}
	}
}

// SetTopicManager sets the topic manager.
func (tq *TipQueue) SetTopicManager(tm *TopicManager) {
	tq.mu.Lock()
	defer tq.mu.Unlock()
	tq.topicMgr = tm
}

// SetFetcher sets the content fetcher.
func (tq *TipQueue) SetFetcher(fetcher ContentFetcher) {
	tq.mu.Lock()
	defer tq.mu.Unlock()
	tq.fetcher = fetcher
}

// SetPinner sets the content pinner.
func (tq *TipQueue) SetPinner(pinner ContentPinner) {
	tq.mu.Lock()
	defer tq.mu.Unlock()
	tq.pinner = pinner
}

// SetPNMVerifier replaces the trust gate used before queueing received PNMs.
func (tq *TipQueue) SetPNMVerifier(verifier PNMVerifier) {
	tq.mu.Lock()
	defer tq.mu.Unlock()
	tq.verifier = verifier
}

// OnTip registers a handler for received tips.
func (tq *TipQueue) OnTip(handler TipHandler) {
	tq.mu.Lock()
	defer tq.mu.Unlock()
	tq.handlers = append(tq.handlers, handler)
}

// Subscribe starts listening for PNM messages.
func (tq *TipQueue) Subscribe() error {
	tq.mu.Lock()
	defer tq.mu.Unlock()

	if tq.topicMgr == nil {
		return ErrNoTopicMgr
	}

	sub, err := tq.topicMgr.Subscribe(pnmSchema)
	if err != nil {
		return err
	}

	tq.subscription = sub

	tq.wg.Add(1)
	go tq.receiveLoop()

	return nil
}

// receiveLoop processes incoming PNM messages.
func (tq *TipQueue) receiveLoop() {
	defer tq.wg.Done()

	for {
		msg, err := tq.subscription.Next(tq.ctx)
		if err != nil {
			if tq.ctx.Err() != nil {
				return // Context cancelled
			}
			log.Warnf("Error receiving PNM: %v", err)
			continue
		}

		tq.handleMessage(msg)
	}
}

// HandleMessage feeds one already-received pubsub message into the tip
// queue. It is exported so a caller that manages its own topic
// subscription (e.g. a host process that already joined/subscribed the
// topic for other reasons and wants to avoid a second, competing
// subscription to the same libp2p pubsub topic) can drive the queue
// without going through Subscribe/receiveLoop. Subscribe-based and
// HandleMessage-based feeding are mutually exclusive in practice but not
// mutually enforced; callers own that choice.
func (tq *TipQueue) HandleMessage(msg *ps.Message) {
	tq.handleMessage(msg)
}

// handleMessage processes a single PNM message.
func (tq *TipQueue) handleMessage(msg *ps.Message) {
	data := msg.Data
	if len(data) == 0 {
		return
	}

	// Validate PNM
	if !PNM.SizePrefixedPNMBufferHasIdentifier(data) {
		log.Debug("Received message without PNM identifier")
		return
	}
	tq.mu.RLock()
	verifier := tq.verifier
	tq.mu.RUnlock()
	if verifier == nil {
		log.Debug("PNM verifier is not configured")
		return
	}
	if _, err := verifier(data); err != nil {
		log.Debugf("Rejected untrusted PNM: %v", err)
		return
	}

	pnm := PNM.GetSizePrefixedRootAsPNM(data, 0)

	cid := string(pnm.CID())
	if cid == "" {
		log.Debug("PNM missing CID")
		return
	}

	// Dedupe: a CID that is already pinned and not yet past its TTL has
	// already been fetched/pinned/materialized by an earlier tip for the
	// same content. Re-announcements of the same CID (e.g. gossipsub
	// retransmission, or the same publication seen on more than one topic)
	// should not re-trigger fetch/pin/handler work.
	tq.mu.RLock()
	if existing, ok := tq.pinnedCIDs[cid]; ok && (existing.PinExpiry.IsZero() || time.Now().Before(existing.PinExpiry)) {
		tq.mu.RUnlock()
		log.Debugf("Skipping duplicate tip for already-pinned CID %s", cid)
		return
	}
	tq.mu.RUnlock()

	// Extract schema type from FILE_ID
	schemaType := string(pnm.FILE_ID())
	if schemaType == "" {
		schemaType = "unknown"
	}

	// Get peer ID
	peerID := msg.ReceivedFrom.String()

	// Parse timestamp
	var publishTime time.Time
	if ts := pnm.PUBLISH_TIMESTAMP(); len(ts) > 0 {
		publishTime, _ = time.Parse(time.RFC3339, string(ts))
	}
	if publishTime.IsZero() {
		publishTime = time.Now()
	}

	// Create tip
	tip := &Tip{
		PeerID:           peerID,
		CID:              cid,
		SchemaType:       schemaType,
		FileName:         string(pnm.FILE_NAME()),
		MultiformatAddr:  string(pnm.MULTIFORMAT_ADDRESS()),
		Signature:        string(pnm.SIGNATURE()),
		PublishTimestamp: publishTime,
		ReceivedAt:       time.Now(),
		RawPNM:           append([]byte(nil), data...),
	}

	// Resolve config for this peer+schema
	config := tq.config.ResolveConfig(peerID, schemaType)

	// Add to queue
	tq.addTip(tip, config)

	// Notify handlers
	tq.notifyHandlers(tip, config)

	// Process based on config
	tq.processTip(tip, config)
}

// addTip adds a tip to the queue.
func (tq *TipQueue) addTip(tip *Tip, config ResolvedConfig) {
	tq.mu.Lock()
	defer tq.mu.Unlock()

	// Check queue size
	totalTips := 0
	for _, tips := range tq.tips {
		totalTips += len(tips)
	}

	if totalTips >= tq.config.MaxQueueSize {
		// Remove oldest tip from lowest priority schema
		tq.evictOldest()
	}

	tq.tips[tip.SchemaType] = append(tq.tips[tip.SchemaType], tip)
}

// evictOldest removes the oldest tip.
func (tq *TipQueue) evictOldest() {
	var oldestSchema string
	var oldestTime time.Time

	for schema, tips := range tq.tips {
		if len(tips) > 0 {
			if oldestSchema == "" || tips[0].ReceivedAt.Before(oldestTime) {
				oldestSchema = schema
				oldestTime = tips[0].ReceivedAt
			}
		}
	}

	if oldestSchema != "" && len(tq.tips[oldestSchema]) > 0 {
		tq.tips[oldestSchema] = tq.tips[oldestSchema][1:]
	}
}

// notifyHandlers calls all registered handlers.
func (tq *TipQueue) notifyHandlers(tip *Tip, config ResolvedConfig) {
	tq.mu.RLock()
	handlers := make([]TipHandler, len(tq.handlers))
	copy(handlers, tq.handlers)
	tq.mu.RUnlock()

	for _, handler := range handlers {
		handler(tip, config)
	}
}

// processTip handles auto-fetch and auto-pin based on config, subject to
// the Task D4 resource caps: MaxFetchBytes (per-fetch size ceiling,
// enforced by the ContentFetcher adapter itself -- see ipfsTipFetcher in
// internal/node), MaxConcurrentFetches (in-flight fetch concurrency), and
// MinFetchInterval (minimum spacing between fetch starts). The
// concurrency/rate caps only gate Fetch: a pin-only tip (AutoFetch=false,
// AutoPin=true, e.g. a schema/source override for durability-only pinning)
// is not subject to either, since Pin never reads content into this
// process to measure against MaxFetchBytes and issues a single lightweight
// RPC rather than a bulk transfer here.
func (tq *TipQueue) processTip(tip *Tip, config ResolvedConfig) {
	tq.mu.RLock()
	fetcher := tq.fetcher
	pinner := tq.pinner
	tq.mu.RUnlock()

	if config.AutoFetch && fetcher != nil {
		// When both AutoFetch and AutoPin are enabled (the default), Pin
		// is chained INSIDE dispatchFetch after a successful, in-cap
		// fetch rather than fired as an independent goroutine: this is
		// what makes "oversize content is never pinned" true instead of a
		// race between two unrelated goroutines.
		go tq.dispatchFetch(tip, config, fetcher, pinner)
		return
	}

	if config.AutoPin && pinner != nil {
		go tq.runPin(tip, config, pinner)
	}
}

// dispatchFetch waits for a rate-limit slot and a concurrency slot (both
// bounded by tq.ctx, so TipQueue.Close unblocks any queued dispatch
// instead of leaking a goroutine), fetches, and on success chains straight
// into Pin (if AutoPin is also enabled). A fetch rejected for being
// oversize (ErrFetchTooLarge) is counted and logged, and never reaches
// Pin.
func (tq *TipQueue) dispatchFetch(tip *Tip, config ResolvedConfig, fetcher ContentFetcher, pinner ContentPinner) {
	if err := tq.fetchLimiter.wait(tq.ctx); err != nil {
		return // TipQueue is shutting down
	}

	select {
	case tq.fetchSem <- struct{}{}:
		defer func() { <-tq.fetchSem }()
	case <-tq.ctx.Done():
		return
	}

	ctx, cancel := context.WithTimeout(tq.ctx, tq.config.FetchTimeout)
	defer cancel()

	_, err := fetcher.Fetch(ctx, tip.CID)
	if err != nil {
		if errors.Is(err, ErrFetchTooLarge) {
			atomic.AddInt64(&tq.oversizeRejections, 1)
			log.Warnf("Rejected oversize fetch for %s: %v", tip.CID, err)
		} else {
			log.Warnf("Failed to fetch %s: %v", tip.CID, err)
		}
		return
	}

	tq.mu.Lock()
	tip.Fetched = true
	tq.mu.Unlock()

	log.Debugf("Fetched content: %s", tip.CID)

	if config.AutoPin && pinner != nil {
		tq.runPin(tip, config, pinner)
	}
}

// runPin pins tip.CID and records the resulting TTL bookkeeping. Called
// either as its own goroutine (pin-only tips) or synchronously from
// dispatchFetch after a successful in-cap fetch.
func (tq *TipQueue) runPin(tip *Tip, config ResolvedConfig, pinner ContentPinner) {
	ctx, cancel := context.WithTimeout(tq.ctx, tq.config.FetchTimeout)
	defer cancel()

	if err := pinner.Pin(ctx, tip.CID, config.TTL); err != nil {
		log.Warnf("Failed to pin %s: %v", tip.CID, err)
		return
	}

	tq.mu.Lock()
	tip.Pinned = true
	tip.PinExpiry = time.Now().Add(config.TTL)
	tq.pinnedCIDs[tip.CID] = tip
	tq.mu.Unlock()

	log.Debugf("Pinned content: %s (TTL: %v)", tip.CID, config.TTL)
}

// PublishTip creates and broadcasts a PNM for pinned content.
func (tq *TipQueue) PublishTip(ctx context.Context, opts PublishOptions) error {
	tq.mu.RLock()
	topicMgr := tq.topicMgr
	tq.mu.RUnlock()

	if topicMgr == nil {
		return ErrNoTopicMgr
	}

	// Build PNM
	builder := flatbuffers.NewBuilder(512)

	var addrOffset, timestampOffset, cidOffset flatbuffers.UOffsetT
	var fileNameOffset, fileIDOffset, sigOffset, sigTypeOffset flatbuffers.UOffsetT

	if opts.MultiformatAddr != "" {
		addrOffset = builder.CreateString(opts.MultiformatAddr)
	}

	timestamp := time.Now().UTC().Format(time.RFC3339)
	timestampOffset = builder.CreateString(timestamp)

	if opts.CID != "" {
		cidOffset = builder.CreateString(opts.CID)
	}

	if opts.FileName != "" {
		fileNameOffset = builder.CreateString(opts.FileName)
	}

	if opts.SchemaType != "" {
		fileIDOffset = builder.CreateString(opts.SchemaType)
	}

	if opts.Signature != "" {
		sigOffset = builder.CreateString(opts.Signature)
	}

	if opts.SignatureType != "" {
		sigTypeOffset = builder.CreateString(opts.SignatureType)
	}

	PNM.PNMStart(builder)
	if addrOffset != 0 {
		PNM.PNMAddMULTIFORMAT_ADDRESS(builder, addrOffset)
	}
	PNM.PNMAddPUBLISH_TIMESTAMP(builder, timestampOffset)
	if cidOffset != 0 {
		PNM.PNMAddCID(builder, cidOffset)
	}
	if fileNameOffset != 0 {
		PNM.PNMAddFILE_NAME(builder, fileNameOffset)
	}
	if fileIDOffset != 0 {
		PNM.PNMAddFILE_ID(builder, fileIDOffset)
	}
	if sigOffset != 0 {
		PNM.PNMAddSIGNATURE(builder, sigOffset)
	}
	if sigTypeOffset != 0 {
		PNM.PNMAddSIGNATURE_TYPE(builder, sigTypeOffset)
	}
	pnm := PNM.PNMEnd(builder)
	PNM.FinishSizePrefixedPNMBuffer(builder, pnm)

	data := make([]byte, len(builder.FinishedBytes()))
	copy(data, builder.FinishedBytes())

	return topicMgr.Publish(pnmSchema, data)
}

// PublishOptions contains options for publishing a tip.
type PublishOptions struct {
	MultiformatAddr string
	CID             string
	FileName        string
	SchemaType      string
	Signature       string
	SignatureType   string
}

// GetTips returns pending tips for a schema type.
func (tq *TipQueue) GetTips(schemaType string) []*Tip {
	tq.mu.RLock()
	defer tq.mu.RUnlock()

	tips := tq.tips[schemaType]
	result := make([]*Tip, len(tips))
	copy(result, tips)
	return result
}

// GetAllTips returns all pending tips.
func (tq *TipQueue) GetAllTips() map[string][]*Tip {
	tq.mu.RLock()
	defer tq.mu.RUnlock()

	result := make(map[string][]*Tip)
	for schema, tips := range tq.tips {
		result[schema] = make([]*Tip, len(tips))
		copy(result[schema], tips)
	}
	return result
}

// GetPinnedCIDs returns all currently pinned CIDs.
func (tq *TipQueue) GetPinnedCIDs() map[string]*Tip {
	tq.mu.RLock()
	defer tq.mu.RUnlock()

	result := make(map[string]*Tip)
	for cid, tip := range tq.pinnedCIDs {
		result[cid] = tip
	}
	return result
}

// ClearTips clears tips for a schema type.
func (tq *TipQueue) ClearTips(schemaType string) {
	tq.mu.Lock()
	defer tq.mu.Unlock()

	delete(tq.tips, schemaType)
}

// ClearAllTips clears all pending tips.
func (tq *TipQueue) ClearAllTips() {
	tq.mu.Lock()
	defer tq.mu.Unlock()

	tq.tips = make(map[string][]*Tip)
}

// RemoveTip removes a specific tip by CID.
func (tq *TipQueue) RemoveTip(cid string) bool {
	tq.mu.Lock()
	defer tq.mu.Unlock()

	for schema, tips := range tq.tips {
		for i, tip := range tips {
			if tip.CID == cid {
				tq.tips[schema] = append(tips[:i], tips[i+1:]...)
				return true
			}
		}
	}
	return false
}

// Config returns the configuration.
func (tq *TipQueue) Config() *TipQueueConfig {
	return tq.config
}

// QueueSize returns the total number of tips in the queue.
func (tq *TipQueue) QueueSize() int {
	tq.mu.RLock()
	defer tq.mu.RUnlock()

	total := 0
	for _, tips := range tq.tips {
		total += len(tips)
	}
	return total
}

// StartTTLSweeper begins a background loop that unpins content whose TTL
// (Tip.PinExpiry, set when the tip was auto-pinned) has elapsed. Kubo/IPFS
// pins have no native TTL, so this is the mechanism that actually enforces
// ResolvedConfig.TTL: SetPinner must be called with a real ContentPinner
// before this has any effect. Safe to call at most once per TipQueue; the
// loop stops when Close is called. A non-positive interval defaults to one
// minute.
func (tq *TipQueue) StartTTLSweeper(interval time.Duration) {
	if interval <= 0 {
		interval = time.Minute
	}
	tq.wg.Add(1)
	go tq.ttlSweepLoop(interval)
}

func (tq *TipQueue) ttlSweepLoop(interval time.Duration) {
	defer tq.wg.Done()

	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-tq.ctx.Done():
			return
		case <-ticker.C:
			tq.sweepExpiredPins()
		}
	}
}

// sweepExpiredPins unpins every currently-tracked CID whose PinExpiry has
// elapsed and removes it from the pinned set. Exported indirectly via
// StartTTLSweeper; kept callable directly (unexported) for tests that want
// to assert sweep behavior without waiting on a ticker.
func (tq *TipQueue) sweepExpiredPins() {
	tq.mu.RLock()
	pinner := tq.pinner
	now := time.Now()
	expired := make([]string, 0)
	for cidValue, tip := range tq.pinnedCIDs {
		if tip != nil && !tip.PinExpiry.IsZero() && now.After(tip.PinExpiry) {
			expired = append(expired, cidValue)
		}
	}
	tq.mu.RUnlock()

	if pinner == nil || len(expired) == 0 {
		return
	}

	for _, cidValue := range expired {
		ctx, cancel := context.WithTimeout(tq.ctx, tq.config.FetchTimeout)
		err := pinner.Unpin(ctx, cidValue)
		cancel()
		if err != nil {
			log.Warnf("Failed to unpin expired content %s: %v", cidValue, err)
			continue
		}
		tq.mu.Lock()
		delete(tq.pinnedCIDs, cidValue)
		tq.mu.Unlock()
		log.Debugf("Unpinned expired content: %s", cidValue)
	}
}

// Close stops the TipQueue.
func (tq *TipQueue) Close() error {
	tq.cancel()

	tq.mu.Lock()
	if tq.subscription != nil {
		tq.subscription.Cancel()
	}
	tq.mu.Unlock()

	tq.wg.Wait()
	return nil
}
