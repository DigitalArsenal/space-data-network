package pubsub

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/PNM"
	flatbuffers "github.com/google/flatbuffers/go"
	ps "github.com/libp2p/go-libp2p-pubsub"
)

// TipQueue errors.
var (
	ErrQueueFull     = errors.New("tip queue is full")
	ErrNotSubscribed = errors.New("not subscribed to PNM topic")
	ErrInvalidPNM    = errors.New("invalid PNM message")
	ErrNoTopicMgr    = errors.New("topic manager not set")
)

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
	SchemaType       string    // FILE_ID (e.g., "OMM")
	FileName         string
	MultiformatAddr  string
	Signature        string
	PublishTimestamp time.Time
	ReceivedAt       time.Time
	Fetched          bool
	Pinned           bool
	PinExpiry        time.Time
}

// TipHandler is called when a tip is received.
type TipHandler func(tip *Tip, config ResolvedConfig)

// TipQueue manages PNM-based tip/queue messaging.
type TipQueue struct {
	config   *TipQueueConfig
	topicMgr *TopicManager
	fetcher  ContentFetcher
	pinner   ContentPinner

	subscription *ps.Subscription
	tips         map[string][]*Tip // schema -> pending tips
	pinnedCIDs   map[string]*Tip   // CID -> tip info

	handlers []TipHandler

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

	ctx, cancel := context.WithCancel(context.Background())

	return &TipQueue{
		config:     config,
		tips:       make(map[string][]*Tip),
		pinnedCIDs: make(map[string]*Tip),
		handlers:   make([]TipHandler, 0),
		ctx:        ctx,
		cancel:     cancel,
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

	sub, err := tq.topicMgr.Subscribe("PNM")
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

	pnm := PNM.GetSizePrefixedRootAsPNM(data, 0)

	cid := string(pnm.CID())
	if cid == "" {
		log.Debug("PNM missing CID")
		return
	}

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

// processTip handles auto-fetch and auto-pin based on config.
func (tq *TipQueue) processTip(tip *Tip, config ResolvedConfig) {
	tq.mu.RLock()
	fetcher := tq.fetcher
	pinner := tq.pinner
	tq.mu.RUnlock()

	// Auto-fetch if enabled
	if config.AutoFetch && fetcher != nil {
		go func() {
			ctx, cancel := context.WithTimeout(tq.ctx, tq.config.FetchTimeout)
			defer cancel()

			_, err := fetcher.Fetch(ctx, tip.CID)
			if err != nil {
				log.Warnf("Failed to fetch %s: %v", tip.CID, err)
				return
			}

			tq.mu.Lock()
			tip.Fetched = true
			tq.mu.Unlock()

			log.Debugf("Fetched content: %s", tip.CID)
		}()
	}

	// Auto-pin if enabled
	if config.AutoPin && pinner != nil {
		go func() {
			ctx, cancel := context.WithTimeout(tq.ctx, tq.config.FetchTimeout)
			defer cancel()

			err := pinner.Pin(ctx, tip.CID, config.TTL)
			if err != nil {
				log.Warnf("Failed to pin %s: %v", tip.CID, err)
				return
			}

			tq.mu.Lock()
			tip.Pinned = true
			tip.PinExpiry = time.Now().Add(config.TTL)
			tq.pinnedCIDs[tip.CID] = tip
			tq.mu.Unlock()

			log.Debugf("Pinned content: %s (TTL: %v)", tip.CID, config.TTL)
		}()
	}
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

	return topicMgr.Publish("PNM", data)
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
