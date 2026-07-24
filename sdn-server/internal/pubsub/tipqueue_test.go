package pubsub

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/PNM"
	flatbuffers "github.com/google/flatbuffers/go"
	ps "github.com/libp2p/go-libp2p-pubsub"
	pb "github.com/libp2p/go-libp2p-pubsub/pb"
	"github.com/libp2p/go-libp2p/core/peer"
)

// waitUntil polls cond until it returns true or timeout elapses, returning
// cond's final value. Mirrors internal/node's waitForCondition helper for
// the same async-goroutine-settling pattern, kept local to this package
// since pubsub and node are independent modules-under-test here.
func waitUntil(t *testing.T, timeout time.Duration, cond func() bool) bool {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return cond()
}

// mockFetcher implements ContentFetcher for testing.
type mockFetcher struct {
	mu      sync.Mutex
	fetched map[string]bool
	data    map[string][]byte
	delay   time.Duration
}

func newMockFetcher() *mockFetcher {
	return &mockFetcher{
		fetched: make(map[string]bool),
		data:    make(map[string][]byte),
	}
}

func (m *mockFetcher) Fetch(ctx context.Context, cid string) ([]byte, error) {
	if m.delay > 0 {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(m.delay):
		}
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	m.fetched[cid] = true
	return m.data[cid], nil
}

func (m *mockFetcher) WasFetched(cid string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.fetched[cid]
}

// mockPinner implements ContentPinner for testing.
type mockPinner struct {
	mu     sync.Mutex
	pinned map[string]time.Duration
}

type recordingSchemaPublisher struct {
	published []string
	payloads  map[string][]byte
}

func (p *recordingSchemaPublisher) Publish(schema string, data []byte) error {
	if p.payloads == nil {
		p.payloads = make(map[string][]byte)
	}
	p.published = append(p.published, schema)
	p.payloads[schema] = append([]byte(nil), data...)
	return nil
}

func newMockPinner() *mockPinner {
	return &mockPinner{
		pinned: make(map[string]time.Duration),
	}
}

func (m *mockPinner) Pin(ctx context.Context, cid string, ttl time.Duration) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.pinned[cid] = ttl
	return nil
}

func (m *mockPinner) Unpin(ctx context.Context, cid string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.pinned, cid)
	return nil
}

func (m *mockPinner) IsPinned(cid string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.pinned[cid]
	return ok
}

func (m *mockPinner) GetTTL(cid string) time.Duration {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.pinned[cid]
}

func TestNewTipQueue(t *testing.T) {
	tq := NewTipQueue(nil)
	if tq == nil {
		t.Fatal("NewTipQueue returned nil")
	}
	if tq.config == nil {
		t.Error("Config should not be nil")
	}
	if tq.tips == nil {
		t.Error("Tips map should be initialized")
	}
}

func TestNewTipQueueWithConfig(t *testing.T) {
	config := NewTipQueueConfig()
	config.DefaultAutoFetch = true
	config.MaxQueueSize = 500

	tq := NewTipQueue(config)
	if tq.config.DefaultAutoFetch != true {
		t.Error("Config not applied correctly")
	}
	if tq.config.MaxQueueSize != 500 {
		t.Error("MaxQueueSize not applied correctly")
	}
}

func TestTipQueueSetters(t *testing.T) {
	tq := NewTipQueue(nil)

	fetcher := newMockFetcher()
	pinner := newMockPinner()

	tq.SetFetcher(fetcher)
	tq.SetPinner(pinner)

	// Test that setters work (internal state)
	tq.mu.RLock()
	if tq.fetcher == nil {
		t.Error("Fetcher not set")
	}
	if tq.pinner == nil {
		t.Error("Pinner not set")
	}
	tq.mu.RUnlock()
}

func TestTipQueueOnTip(t *testing.T) {
	tq := NewTipQueue(nil)

	var receivedTip *Tip
	var receivedConfig ResolvedConfig

	tq.OnTip(func(tip *Tip, config ResolvedConfig) {
		receivedTip = tip
		receivedConfig = config
	})

	// Simulate receiving a tip
	tip := &Tip{
		PeerID:     "peer1",
		CID:        "bafytest123",
		SchemaType: "OMM",
	}
	config := ResolvedConfig{AutoFetch: true}

	tq.notifyHandlers(tip, config)

	if receivedTip == nil {
		t.Error("Handler was not called")
	}
	if receivedTip.CID != "bafytest123" {
		t.Errorf("Tip CID mismatch: got %s", receivedTip.CID)
	}
	if !receivedConfig.AutoFetch {
		t.Error("Config not passed correctly")
	}
}

func TestTipQueueAddTip(t *testing.T) {
	tq := NewTipQueue(nil)

	tip := &Tip{
		PeerID:     "peer1",
		CID:        "bafytest123",
		SchemaType: "OMM",
		ReceivedAt: time.Now(),
	}
	config := ResolvedConfig{}

	tq.addTip(tip, config)

	tips := tq.GetTips("OMM")
	if len(tips) != 1 {
		t.Errorf("Expected 1 tip, got %d", len(tips))
	}
	if tips[0].CID != "bafytest123" {
		t.Error("Tip not stored correctly")
	}
}

func TestTipQueueMaxSize(t *testing.T) {
	config := NewTipQueueConfig()
	config.MaxQueueSize = 3

	tq := NewTipQueue(config)

	// Add tips up to max
	for i := 0; i < 5; i++ {
		tip := &Tip{
			PeerID:     "peer1",
			CID:        "cid" + string(rune('0'+i)),
			SchemaType: "OMM",
			ReceivedAt: time.Now().Add(time.Duration(i) * time.Second),
		}
		tq.addTip(tip, ResolvedConfig{})
	}

	// Should have evicted oldest
	if tq.QueueSize() > 3 {
		t.Errorf("Queue size should not exceed max: got %d", tq.QueueSize())
	}
}

func TestTipQueueGetAllTips(t *testing.T) {
	tq := NewTipQueue(nil)

	tq.addTip(&Tip{CID: "cid1", SchemaType: "OMM", ReceivedAt: time.Now()}, ResolvedConfig{})
	tq.addTip(&Tip{CID: "cid2", SchemaType: "OMM", ReceivedAt: time.Now()}, ResolvedConfig{})
	tq.addTip(&Tip{CID: "cid3", SchemaType: "EPM", ReceivedAt: time.Now()}, ResolvedConfig{})

	allTips := tq.GetAllTips()

	if len(allTips["OMM"]) != 2 {
		t.Errorf("Expected 2 OMM tips, got %d", len(allTips["OMM"]))
	}
	if len(allTips["EPM"]) != 1 {
		t.Errorf("Expected 1 EPM tip, got %d", len(allTips["EPM"]))
	}
}

func TestTipQueueClearTips(t *testing.T) {
	tq := NewTipQueue(nil)

	tq.addTip(&Tip{CID: "cid1", SchemaType: "OMM", ReceivedAt: time.Now()}, ResolvedConfig{})
	tq.addTip(&Tip{CID: "cid2", SchemaType: "EPM", ReceivedAt: time.Now()}, ResolvedConfig{})

	tq.ClearTips("OMM")

	if len(tq.GetTips("OMM")) != 0 {
		t.Error("OMM tips should be cleared")
	}
	if len(tq.GetTips("EPM")) != 1 {
		t.Error("EPM tips should still exist")
	}
}

func TestTipQueueClearAllTips(t *testing.T) {
	tq := NewTipQueue(nil)

	tq.addTip(&Tip{CID: "cid1", SchemaType: "OMM", ReceivedAt: time.Now()}, ResolvedConfig{})
	tq.addTip(&Tip{CID: "cid2", SchemaType: "EPM", ReceivedAt: time.Now()}, ResolvedConfig{})

	tq.ClearAllTips()

	if tq.QueueSize() != 0 {
		t.Errorf("Queue should be empty, got %d", tq.QueueSize())
	}
}

func TestTipQueueRemoveTip(t *testing.T) {
	tq := NewTipQueue(nil)

	tq.addTip(&Tip{CID: "cid1", SchemaType: "OMM", ReceivedAt: time.Now()}, ResolvedConfig{})
	tq.addTip(&Tip{CID: "cid2", SchemaType: "OMM", ReceivedAt: time.Now()}, ResolvedConfig{})

	removed := tq.RemoveTip("cid1")
	if !removed {
		t.Error("Expected tip to be removed")
	}

	tips := tq.GetTips("OMM")
	if len(tips) != 1 {
		t.Errorf("Expected 1 tip remaining, got %d", len(tips))
	}
	if tips[0].CID != "cid2" {
		t.Error("Wrong tip remaining")
	}

	removed = tq.RemoveTip("nonexistent")
	if removed {
		t.Error("Should not remove nonexistent tip")
	}
}

func TestTipQueueProcessTipAutoFetch(t *testing.T) {
	tq := NewTipQueue(nil)
	fetcher := newMockFetcher()
	tq.SetFetcher(fetcher)

	tip := &Tip{
		CID:        "bafytest123",
		SchemaType: "OMM",
	}
	config := ResolvedConfig{
		AutoFetch: true,
	}

	tq.processTip(tip, config)

	// Give async goroutine time to run
	time.Sleep(50 * time.Millisecond)

	if !fetcher.WasFetched("bafytest123") {
		t.Error("Content should have been fetched")
	}
}

func TestTipQueueProcessTipAutoPin(t *testing.T) {
	tq := NewTipQueue(nil)
	pinner := newMockPinner()
	tq.SetPinner(pinner)

	tip := &Tip{
		CID:        "bafytest456",
		SchemaType: "OMM",
	}
	config := ResolvedConfig{
		AutoPin: true,
		TTL:     1 * time.Hour,
	}

	tq.processTip(tip, config)

	// Give async goroutine time to run
	time.Sleep(50 * time.Millisecond)

	if !pinner.IsPinned("bafytest456") {
		t.Error("Content should have been pinned")
	}
	if pinner.GetTTL("bafytest456") != 1*time.Hour {
		t.Errorf("Pin TTL should be 1h, got %v", pinner.GetTTL("bafytest456"))
	}
}

func TestTipQueueProcessTipNoAutoFetch(t *testing.T) {
	tq := NewTipQueue(nil)
	fetcher := newMockFetcher()
	tq.SetFetcher(fetcher)

	tip := &Tip{
		CID:        "bafytest789",
		SchemaType: "OMM",
	}
	config := ResolvedConfig{
		AutoFetch: false, // Disabled
	}

	tq.processTip(tip, config)

	time.Sleep(50 * time.Millisecond)

	if fetcher.WasFetched("bafytest789") {
		t.Error("Content should NOT have been fetched")
	}
}

func TestTipQueueHandleMessageRejectsUnsignedPNMBeforeTrustingTip(t *testing.T) {
	config := NewTipQueueConfig()
	config.DefaultAutoFetch = true
	config.DefaultAutoPin = true

	tq := NewTipQueue(config)
	fetcher := newMockFetcher()
	pinner := newMockPinner()
	tq.SetFetcher(fetcher)
	tq.SetPinner(pinner)

	handlerCalled := false
	tq.OnTip(func(tip *Tip, config ResolvedConfig) {
		handlerCalled = true
	})

	tq.handleMessage(&ps.Message{
		Message:      &pb.Message{Data: buildTestPNM(t, "bafyunsigned", "DPM")},
		ReceivedFrom: peer.ID("12D3KooWUnsignedPublisher"),
	})
	time.Sleep(50 * time.Millisecond)

	if tq.QueueSize() != 0 {
		t.Fatalf("unsigned PNM should not be queued, got %d queued tips", tq.QueueSize())
	}
	if handlerCalled {
		t.Fatal("unsigned PNM should not notify tip handlers")
	}
	if fetcher.WasFetched("bafyunsigned") {
		t.Fatal("unsigned PNM should not be fetched")
	}
	if pinner.IsPinned("bafyunsigned") {
		t.Fatal("unsigned PNM should not be pinned")
	}
}

func TestTipQueueConfigIntegration(t *testing.T) {
	config := NewTipQueueConfig()

	// Set schema default for OMM
	config.SetSchemaDefault("OMM", &SchemaConfig{
		AutoFetch: true,
		AutoPin:   true,
		TTL:       2 * time.Hour,
	})

	// Set source override
	config.SetSourceOverride("trusted-peer", &SourceConfig{
		Trusted: true,
		TTL:     DurationPtr(4 * time.Hour),
	})

	tq := NewTipQueue(config)
	fetcher := newMockFetcher()
	pinner := newMockPinner()
	tq.SetFetcher(fetcher)
	tq.SetPinner(pinner)

	// Simulate tip from trusted peer
	tip := &Tip{
		PeerID:     "trusted-peer",
		CID:        "bafytrusted",
		SchemaType: "OMM",
		ReceivedAt: time.Now(),
	}
	resolved := tq.config.ResolveConfig(tip.PeerID, tip.SchemaType)

	tq.addTip(tip, resolved)
	tq.processTip(tip, resolved)

	time.Sleep(50 * time.Millisecond)

	// Should auto-fetch (from schema default)
	if !fetcher.WasFetched("bafytrusted") {
		t.Error("Should have auto-fetched based on schema default")
	}

	// Should auto-pin with source TTL override
	if !pinner.IsPinned("bafytrusted") {
		t.Error("Should have auto-pinned")
	}
	if pinner.GetTTL("bafytrusted") != 4*time.Hour {
		t.Errorf("TTL should be 4h from source override, got %v", pinner.GetTTL("bafytrusted"))
	}
}

func TestBuildPNMMessage(t *testing.T) {
	builder := flatbuffers.NewBuilder(256)

	cidOffset := builder.CreateString("bafytest123")
	fileIDOffset := builder.CreateString("OMM")
	timestampOffset := builder.CreateString("2024-01-15T12:00:00Z")

	PNM.PNMStart(builder)
	PNM.PNMAddCID(builder, cidOffset)
	PNM.PNMAddFILE_ID(builder, fileIDOffset)
	PNM.PNMAddPUBLISH_TIMESTAMP(builder, timestampOffset)
	pnm := PNM.PNMEnd(builder)
	PNM.FinishSizePrefixedPNMBuffer(builder, pnm)

	data := builder.FinishedBytes()

	if !PNM.SizePrefixedPNMBufferHasIdentifier(data) {
		t.Error("PNM should have identifier")
	}

	parsed := PNM.GetSizePrefixedRootAsPNM(data, 0)
	if string(parsed.CID()) != "bafytest123" {
		t.Errorf("CID mismatch: got %s", parsed.CID())
	}
	if string(parsed.FILE_ID()) != "OMM" {
		t.Errorf("FILE_ID mismatch: got %s", parsed.FILE_ID())
	}
}

func TestPublishDatasetUpdatePNMPublishesExplicitlyDeclaredSchemaTopics(t *testing.T) {
	pnmBytes := buildTestPNM(t, "bafymanifest", "DPM")
	publisher := &recordingSchemaPublisher{}

	err := PublishDatasetUpdatePNM(context.Background(), publisher, DatasetUpdateAnnouncement{
		PNM:     pnmBytes,
		Schemas: []string{"CAT.fbs", "OMM", "CAT.fbs", "MPE", "SPW.fbs"},
	})
	if err != nil {
		t.Fatalf("PublishDatasetUpdatePNM failed: %v", err)
	}

	want := []string{"PNM.fbs", "CAT.fbs", "OMM.fbs", "MPE.fbs", "SPW.fbs"}
	if !reflect.DeepEqual(publisher.published, want) {
		t.Fatalf("published schemas = %#v, want %#v", publisher.published, want)
	}
	for _, schema := range want {
		if !reflect.DeepEqual(publisher.payloads[schema], pnmBytes) {
			t.Fatalf("payload for %s did not match original PNM", schema)
		}
	}
}

func TestPublishDatasetUpdatePNMRejectsInvalidPNM(t *testing.T) {
	publisher := &recordingSchemaPublisher{}

	err := PublishDatasetUpdatePNM(context.Background(), publisher, DatasetUpdateAnnouncement{
		PNM:     []byte("not a pnm"),
		Schemas: []string{"OMM.fbs"},
	})
	if err == nil {
		t.Fatalf("expected invalid PNM error")
	}
	if len(publisher.published) != 0 {
		t.Fatalf("published invalid PNM to %#v", publisher.published)
	}
}

func TestPublishDatasetFeedHeadPublishesSchemaScopedTopic(t *testing.T) {
	publisher := &recordingTopicPublisher{}

	err := PublishDatasetFeedHead(context.Background(), publisher, DatasetFeedHeadAnnouncement{
		Schema:       "OMM.fbs",
		ProviderID:   "space-data-network-02",
		SourceName:   "celestrak-gp",
		QueryProfile: "dataset-publication-offset-v1",
		Offset:       5000,
		Limit:        5000,
		FeedSequence: 2,
		PreviousHead: "head-1",
		FeedHead:     "head-2",
		ManifestCID:  "bafymanifest",
		PNMCID:       "bafypnm",
		PublishedAt:  time.Unix(1_778_436_060, 0).UTC(),
	})
	if err != nil {
		t.Fatalf("PublishDatasetFeedHead failed: %v", err)
	}

	wantTopic := DatasetFeedHeadTopic("OMM.fbs")
	if !reflect.DeepEqual(publisher.topics, []string{wantTopic}) {
		t.Fatalf("topics = %#v, want %q", publisher.topics, wantTopic)
	}
	var payload DatasetFeedHeadAnnouncement
	if err := json.Unmarshal(publisher.payloads[wantTopic], &payload); err != nil {
		t.Fatalf("decode feed head payload: %v", err)
	}
	if payload.Offset != 5000 || payload.Limit != 5000 || payload.FeedSequence != 2 || payload.PreviousHead != "head-1" || payload.FeedHead != "head-2" || payload.ManifestCID != "bafymanifest" {
		t.Fatalf("unexpected feed head payload: %+v", payload)
	}
}

func TestParseDatasetFeedHeadAnnouncementNormalizesAndValidates(t *testing.T) {
	payload := []byte(`{
		"message_type": "sdn.dataset.feed_head.v1",
		"schema": "OMM",
		"provider_id": "space-data-network-02",
		"source_name": "celestrak-gp",
		"query_profile": "dataset-publication-offset-v1",
		"offset": 10000,
		"limit": 5000,
		"feed_sequence": 3,
		"previous_head": "head-2",
		"feed_head": "head-3",
		"manifest_cid": "bafymanifest"
	}`)

	ann, err := ParseDatasetFeedHeadAnnouncement(payload)
	if err != nil {
		t.Fatalf("ParseDatasetFeedHeadAnnouncement failed: %v", err)
	}
	if ann.MessageType != DatasetFeedHeadMessageType || ann.Schema != "OMM.fbs" || ann.Offset != 10000 || ann.Limit != 5000 || ann.FeedSequence != 3 || ann.FeedHead != "head-3" {
		t.Fatalf("unexpected parsed feed head: %+v", ann)
	}
}

func TestParseDatasetFeedHeadAnnouncementRejectsInvalidPayload(t *testing.T) {
	_, err := ParseDatasetFeedHeadAnnouncement([]byte(`{
		"message_type": "sdn.dataset.other",
		"schema": "OMM.fbs",
		"query_profile": "dataset-publication-offset-v1",
		"feed_sequence": 1,
		"feed_head": "head-1"
	}`))
	if err == nil {
		t.Fatalf("expected invalid feed head payload to be rejected")
	}
}

func buildTestPNM(t *testing.T, cid, fileID string) []byte {
	t.Helper()

	builder := flatbuffers.NewBuilder(256)
	cidOffset := builder.CreateString(cid)
	fileIDOffset := builder.CreateString(fileID)
	timestampOffset := builder.CreateString(time.Now().UTC().Format(time.RFC3339))

	PNM.PNMStart(builder)
	PNM.PNMAddCID(builder, cidOffset)
	PNM.PNMAddFILE_ID(builder, fileIDOffset)
	PNM.PNMAddPUBLISH_TIMESTAMP(builder, timestampOffset)
	pnm := PNM.PNMEnd(builder)
	PNM.FinishSizePrefixedPNMBuffer(builder, pnm)
	return append([]byte(nil), builder.FinishedBytes()...)
}

// buildSignedTestPNM builds a PNM buffer that clears the default verifier's
// structural checks (SIGNATURE_TYPE == "Ed25519", SIGNATURE decodes to 64
// bytes) without asserting anything about the signature's cryptographic
// validity — mirrors internal/channels' own verifier test helper.
func buildSignedTestPNM(t *testing.T, cid, fileID string) []byte {
	t.Helper()

	signature := make([]byte, 64)
	for i := range signature {
		signature[i] = byte(i + 1)
	}

	builder := flatbuffers.NewBuilder(256)
	cidOffset := builder.CreateString(cid)
	fileIDOffset := builder.CreateString(fileID)
	timestampOffset := builder.CreateString(time.Now().UTC().Format(time.RFC3339))
	sigTypeOffset := builder.CreateString("Ed25519")
	sigOffset := builder.CreateString(hex.EncodeToString(signature))

	PNM.PNMStart(builder)
	PNM.PNMAddCID(builder, cidOffset)
	PNM.PNMAddFILE_ID(builder, fileIDOffset)
	PNM.PNMAddPUBLISH_TIMESTAMP(builder, timestampOffset)
	PNM.PNMAddSIGNATURE_TYPE(builder, sigTypeOffset)
	PNM.PNMAddSIGNATURE(builder, sigOffset)
	pnm := PNM.PNMEnd(builder)
	PNM.FinishSizePrefixedPNMBuffer(builder, pnm)
	return append([]byte(nil), builder.FinishedBytes()...)
}

func TestTipQueueHandleMessagePublicWrapperMatchesInternalHandler(t *testing.T) {
	tq := NewTipQueue(nil)

	var received *Tip
	tq.OnTip(func(tip *Tip, _ ResolvedConfig) {
		received = tip
	})

	tq.HandleMessage(&ps.Message{
		Message:      &pb.Message{Data: buildSignedTestPNM(t, "bafypublicwrapper", "OMM")},
		ReceivedFrom: peer.ID("12D3KooWPublicWrapperPeer"),
	})

	if received == nil {
		t.Fatal("exported HandleMessage did not drive the same handling path as handleMessage")
	}
	if received.CID != "bafypublicwrapper" {
		t.Fatalf("tip CID = %q, want bafypublicwrapper", received.CID)
	}
}

func TestTipQueueHandleMessageCarriesRawPNMToHandlers(t *testing.T) {
	tq := NewTipQueue(nil)
	pnmBytes := buildSignedTestPNM(t, "bafyraw", "OMM")

	var received *Tip
	tq.OnTip(func(tip *Tip, _ ResolvedConfig) {
		received = tip
	})

	tq.handleMessage(&ps.Message{
		Message:      &pb.Message{Data: pnmBytes},
		ReceivedFrom: peer.ID("12D3KooWRawPNMPeer"),
	})

	if received == nil {
		t.Fatal("handler was not called")
	}
	if !reflect.DeepEqual(received.RawPNM, pnmBytes) {
		t.Fatalf("RawPNM = %x, want %x", received.RawPNM, pnmBytes)
	}
}

func TestTipQueueHandleMessageDedupesAlreadyPinnedCID(t *testing.T) {
	config := NewTipQueueConfig()
	config.DefaultAutoFetch = true
	config.DefaultAutoPin = true

	tq := NewTipQueue(config)
	fetcher := newMockFetcher()
	pinner := newMockPinner()
	tq.SetFetcher(fetcher)
	tq.SetPinner(pinner)

	handlerCalls := 0
	tq.OnTip(func(tip *Tip, _ ResolvedConfig) {
		handlerCalls++
	})

	// Seed pinnedCIDs as though an earlier tip for this CID already ran
	// through processTip and is still within its TTL.
	tq.mu.Lock()
	tq.pinnedCIDs["bafydedupe"] = &Tip{CID: "bafydedupe", PinExpiry: time.Now().Add(time.Hour)}
	tq.mu.Unlock()

	tq.handleMessage(&ps.Message{
		Message:      &pb.Message{Data: buildSignedTestPNM(t, "bafydedupe", "OMM")},
		ReceivedFrom: peer.ID("12D3KooWDedupePeer"),
	})
	time.Sleep(50 * time.Millisecond)

	if handlerCalls != 0 {
		t.Fatalf("handler called %d times, want 0 for a duplicate already-pinned CID", handlerCalls)
	}
	if tq.QueueSize() != 0 {
		t.Fatalf("queue size = %d, want 0: duplicate tip should not be queued", tq.QueueSize())
	}
	if fetcher.WasFetched("bafydedupe") {
		t.Fatal("duplicate already-pinned CID should not be re-fetched")
	}
}

func TestTipQueueHandleMessageProcessesExpiredPinAgain(t *testing.T) {
	config := NewTipQueueConfig()
	config.DefaultAutoFetch = true
	config.DefaultAutoPin = true

	tq := NewTipQueue(config)
	fetcher := newMockFetcher()
	pinner := newMockPinner()
	tq.SetFetcher(fetcher)
	tq.SetPinner(pinner)

	// A tip whose TTL has already elapsed is not a duplicate: a fresh
	// announcement of the same CID should be processed normally.
	tq.mu.Lock()
	tq.pinnedCIDs["bafyexpired"] = &Tip{CID: "bafyexpired", PinExpiry: time.Now().Add(-time.Hour)}
	tq.mu.Unlock()

	handlerCalls := 0
	tq.OnTip(func(tip *Tip, _ ResolvedConfig) {
		handlerCalls++
	})

	tq.handleMessage(&ps.Message{
		Message:      &pb.Message{Data: buildSignedTestPNM(t, "bafyexpired", "OMM")},
		ReceivedFrom: peer.ID("12D3KooWExpiredPeer"),
	})
	time.Sleep(50 * time.Millisecond)

	if handlerCalls != 1 {
		t.Fatalf("handler called %d times, want 1 for an expired-then-renewed CID", handlerCalls)
	}
	if !fetcher.WasFetched("bafyexpired") {
		t.Fatal("expired-then-renewed CID should be fetched again")
	}
}

func TestTipQueueSweepExpiredPinsUnpinsAndRemoves(t *testing.T) {
	tq := NewTipQueue(nil)
	pinner := newMockPinner()
	tq.SetPinner(pinner)

	tq.mu.Lock()
	tq.pinnedCIDs["bafystale"] = &Tip{CID: "bafystale", PinExpiry: time.Now().Add(-time.Minute)}
	tq.pinnedCIDs["bafyfresh"] = &Tip{CID: "bafyfresh", PinExpiry: time.Now().Add(time.Hour)}
	tq.mu.Unlock()
	// Seed the mock pinner directly so Unpin has something to remove.
	_ = pinner.Pin(context.Background(), "bafystale", time.Minute)
	_ = pinner.Pin(context.Background(), "bafyfresh", time.Hour)

	tq.sweepExpiredPins()

	if pinner.IsPinned("bafystale") {
		t.Fatal("expired pin should have been unpinned by the sweep")
	}
	if !pinner.IsPinned("bafyfresh") {
		t.Fatal("non-expired pin should not have been touched by the sweep")
	}
	remaining := tq.GetPinnedCIDs()
	if _, ok := remaining["bafystale"]; ok {
		t.Fatal("expired CID should have been removed from the tracked pinned set")
	}
	if _, ok := remaining["bafyfresh"]; !ok {
		t.Fatal("non-expired CID should still be tracked")
	}
}

func TestTipQueueStartTTLSweeperStopsOnClose(t *testing.T) {
	tq := NewTipQueue(nil)
	tq.StartTTLSweeper(10 * time.Millisecond)

	// Give the sweeper a couple of ticks before closing, then assert Close
	// returns promptly (i.e. the sweeper goroutine actually exits instead
	// of leaking past ctx cancellation).
	time.Sleep(30 * time.Millisecond)

	done := make(chan error, 1)
	go func() { done <- tq.Close() }()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Close returned error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Close did not return; TTL sweeper goroutine may have leaked")
	}
}

func TestTipQueueClose(t *testing.T) {
	tq := NewTipQueue(nil)

	err := tq.Close()
	if err != nil {
		t.Errorf("Close returned error: %v", err)
	}
}

func TestTipQueueGetPinnedCIDs(t *testing.T) {
	tq := NewTipQueue(nil)

	// Manually add pinned CIDs for testing
	tq.mu.Lock()
	tq.pinnedCIDs["cid1"] = &Tip{CID: "cid1", SchemaType: "OMM"}
	tq.pinnedCIDs["cid2"] = &Tip{CID: "cid2", SchemaType: "EPM"}
	tq.mu.Unlock()

	pinned := tq.GetPinnedCIDs()
	if len(pinned) != 2 {
		t.Errorf("Expected 2 pinned CIDs, got %d", len(pinned))
	}
	if _, ok := pinned["cid1"]; !ok {
		t.Error("cid1 should be in pinned map")
	}
}

func TestTipQueueConcurrency(t *testing.T) {
	tq := NewTipQueue(nil)

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				tip := &Tip{
					CID:        "cid" + string(rune(id)) + string(rune(j)),
					SchemaType: "OMM",
					ReceivedAt: time.Now(),
				}
				tq.addTip(tip, ResolvedConfig{})
				tq.GetTips("OMM")
				tq.QueueSize()
			}
		}(i)
	}
	wg.Wait()
}

func TestEvictOldest(t *testing.T) {
	tq := NewTipQueue(nil)

	// Add tips with different times
	now := time.Now()
	tq.tips["OMM"] = []*Tip{
		{CID: "old", ReceivedAt: now.Add(-2 * time.Hour)},
		{CID: "new", ReceivedAt: now},
	}
	tq.tips["EPM"] = []*Tip{
		{CID: "oldest", ReceivedAt: now.Add(-3 * time.Hour)},
	}

	tq.evictOldest()

	// Should evict from EPM (oldest tip)
	if len(tq.tips["EPM"]) != 0 {
		t.Error("Should have evicted oldest tip from EPM")
	}
	if len(tq.tips["OMM"]) != 2 {
		t.Error("OMM tips should be unchanged")
	}
}

// --- D4: resource caps on auto-ingest fetches --------------------------

// capTestFetcher simulates a ContentFetcher whose adapter (e.g.
// internal/node's ipfsTipFetcher) has already applied the MaxFetchBytes
// size cap: a CID marked oversize returns ErrFetchTooLarge, everything
// else returns its configured bytes.
type capTestFetcher struct {
	mu       sync.Mutex
	oversize map[string]bool
	data     map[string][]byte
	fetched  map[string]bool
}

func newCapTestFetcher() *capTestFetcher {
	return &capTestFetcher{
		oversize: make(map[string]bool),
		data:     make(map[string][]byte),
		fetched:  make(map[string]bool),
	}
}

func (f *capTestFetcher) Fetch(ctx context.Context, cid string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.fetched[cid] = true
	if f.oversize[cid] {
		return nil, fmt.Errorf("%w: cid %s exceeded cap", ErrFetchTooLarge, cid)
	}
	return f.data[cid], nil
}

func (f *capTestFetcher) WasFetched(cid string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.fetched[cid]
}

func TestTipQueueDispatchFetchRejectsOversizeWithoutPinningButUndersizeFlows(t *testing.T) {
	config := NewTipQueueConfig()
	config.DefaultAutoFetch = true
	config.DefaultAutoPin = true

	tq := NewTipQueue(config)
	fetcher := newCapTestFetcher()
	fetcher.oversize["bafyoversize"] = true
	fetcher.data["bafyundersize"] = []byte("ok")
	pinner := newMockPinner()
	tq.SetFetcher(fetcher)
	tq.SetPinner(pinner)

	over := &Tip{CID: "bafyoversize", SchemaType: "OMM"}
	under := &Tip{CID: "bafyundersize", SchemaType: "OMM"}
	cfg := ResolvedConfig{AutoFetch: true, AutoPin: true, TTL: time.Hour}

	tq.processTip(over, cfg)
	tq.processTip(under, cfg)

	if !waitUntil(t, 2*time.Second, func() bool {
		return pinner.IsPinned("bafyundersize") && tq.OversizeRejections() >= 1
	}) {
		t.Fatalf("undersize tip not pinned and/or oversize rejection not counted in time: pinned=%v rejections=%d",
			pinner.IsPinned("bafyundersize"), tq.OversizeRejections())
	}

	if pinner.IsPinned("bafyoversize") {
		t.Fatal("oversize content must not be pinned")
	}
	if tq.OversizeRejections() != 1 {
		t.Fatalf("OversizeRejections = %d, want exactly 1", tq.OversizeRejections())
	}
	if pinner.GetTTL("bafyundersize") != time.Hour {
		t.Fatalf("undersize pin TTL = %v, want 1h", pinner.GetTTL("bafyundersize"))
	}

	// tip.Fetched/tip.Pinned are mutated under tq.mu by dispatchFetch and
	// runPin; read them the same way TestTipQueueSetters reads TipQueue's
	// own internal state, rather than racing on the bare struct fields.
	tq.mu.RLock()
	overFetched, overPinned := over.Fetched, over.Pinned
	underFetched, underPinned := under.Fetched, under.Pinned
	tq.mu.RUnlock()

	if overFetched {
		t.Fatal("oversize tip must not be marked Fetched")
	}
	if overPinned {
		t.Fatal("oversize tip must not be marked Pinned")
	}
	if !underFetched {
		t.Fatal("undersize tip should be marked Fetched")
	}
	if !underPinned {
		t.Fatal("undersize tip should be marked Pinned")
	}
}

// blockingFetcher blocks every Fetch call until release is closed, and
// tracks how many calls are simultaneously executing (peak) so tests can
// assert TipQueue never exceeds MaxConcurrentFetches in-flight fetches.
type blockingFetcher struct {
	release chan struct{}

	mu        sync.Mutex
	inFlight  int
	peak      int
	completed int
}

func newBlockingFetcher() *blockingFetcher {
	return &blockingFetcher{release: make(chan struct{})}
}

func (f *blockingFetcher) Fetch(ctx context.Context, cid string) ([]byte, error) {
	f.mu.Lock()
	f.inFlight++
	if f.inFlight > f.peak {
		f.peak = f.inFlight
	}
	f.mu.Unlock()

	select {
	case <-f.release:
	case <-ctx.Done():
		f.mu.Lock()
		f.inFlight--
		f.mu.Unlock()
		return nil, ctx.Err()
	}

	f.mu.Lock()
	f.inFlight--
	f.completed++
	f.mu.Unlock()
	return []byte("ok"), nil
}

func (f *blockingFetcher) InFlight() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.inFlight
}

func (f *blockingFetcher) Peak() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.peak
}

func (f *blockingFetcher) Completed() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.completed
}

func TestTipQueueDispatchFetchBoundsConcurrency(t *testing.T) {
	config := NewTipQueueConfig()
	config.MaxConcurrentFetches = 2
	// A tiny (but non-zero, so NewTipQueue does not normalize it back to
	// the package default) interval isolates this test from rate
	// limiting: fetch starts are barely spaced, so concurrency -- the
	// blockingFetcher holding its slot until released -- is the only
	// thing capping how many run at once.
	config.MinFetchInterval = time.Millisecond
	config.FetchTimeout = 5 * time.Second

	tq := NewTipQueue(config)
	fetcher := newBlockingFetcher()
	tq.SetFetcher(fetcher)

	const n = 6
	cfg := ResolvedConfig{AutoFetch: true}
	for i := 0; i < n; i++ {
		tq.processTip(&Tip{CID: fmt.Sprintf("cid-%d", i), SchemaType: "OMM"}, cfg)
	}

	if !waitUntil(t, 2*time.Second, func() bool {
		return fetcher.InFlight() == config.MaxConcurrentFetches
	}) {
		t.Fatalf("in-flight fetches never reached the concurrency cap of %d (got %d); dispatch may not be bounding concurrency",
			config.MaxConcurrentFetches, fetcher.InFlight())
	}

	// Give any incorrectly-unbounded dispatch a further moment to prove
	// itself before checking peak.
	time.Sleep(100 * time.Millisecond)
	if peak := fetcher.Peak(); peak > config.MaxConcurrentFetches {
		t.Fatalf("peak concurrent fetches = %d, want <= %d", peak, config.MaxConcurrentFetches)
	}

	close(fetcher.release)

	if !waitUntil(t, 2*time.Second, func() bool {
		return fetcher.Completed() == n
	}) {
		t.Fatalf("not all %d fetches completed after release (completed=%d)", n, fetcher.Completed())
	}

	if peak := fetcher.Peak(); peak != config.MaxConcurrentFetches {
		t.Fatalf("peak concurrent fetches = %d, want exactly %d with %d tips queued against a cap of %d",
			peak, config.MaxConcurrentFetches, n, config.MaxConcurrentFetches)
	}
}

// timestampFetcher records the wall-clock time each Fetch call starts, so
// tests can assert TipQueue spaces fetch starts by at least
// MinFetchInterval instead of firing a burst all at once.
type timestampFetcher struct {
	mu     sync.Mutex
	starts []time.Time
}

func (f *timestampFetcher) Fetch(ctx context.Context, cid string) ([]byte, error) {
	f.mu.Lock()
	f.starts = append(f.starts, time.Now())
	f.mu.Unlock()
	return []byte("ok"), nil
}

func (f *timestampFetcher) Count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.starts)
}

func (f *timestampFetcher) Starts() []time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]time.Time, len(f.starts))
	copy(out, f.starts)
	return out
}

func TestTipQueueDispatchFetchBoundsRate(t *testing.T) {
	config := NewTipQueueConfig()
	config.MaxConcurrentFetches = 100 // effectively unbounded: isolate rate limiting
	config.MinFetchInterval = 100 * time.Millisecond
	config.FetchTimeout = 5 * time.Second

	tq := NewTipQueue(config)
	fetcher := &timestampFetcher{}
	tq.SetFetcher(fetcher)

	const n = 4
	cfg := ResolvedConfig{AutoFetch: true}
	start := time.Now()
	for i := 0; i < n; i++ {
		tq.processTip(&Tip{CID: fmt.Sprintf("cid-%d", i), SchemaType: "OMM"}, cfg)
	}

	if !waitUntil(t, 3*time.Second, func() bool {
		return fetcher.Count() == n
	}) {
		t.Fatalf("not all %d fetches completed (got %d)", n, fetcher.Count())
	}

	// The whole burst must NOT complete within a single interval window:
	// spacing n fetch starts by MinFetchInterval takes at least
	// (n-1)*MinFetchInterval.
	minExpected := time.Duration(n-1) * config.MinFetchInterval
	if elapsed := time.Since(start); elapsed < minExpected {
		t.Fatalf("burst of %d tips fetched in %v, want >= %v (rate limiting should space fetch starts by %v instead of firing them all at once)",
			n, elapsed, minExpected, config.MinFetchInterval)
	}

	starts := fetcher.Starts()
	sort.Slice(starts, func(i, j int) bool { return starts[i].Before(starts[j]) })
	const jitterTolerance = 20 * time.Millisecond
	for i := 1; i < len(starts); i++ {
		gap := starts[i].Sub(starts[i-1])
		if gap < config.MinFetchInterval-jitterTolerance {
			t.Fatalf("fetch %d started only %v after fetch %d, want >= ~%v (MinFetchInterval)", i, gap, i-1, config.MinFetchInterval)
		}
	}
}

func TestTipQueueProcessTipStillFetchesAndPinsWhenBothEnabled(t *testing.T) {
	// D1/D2 regression guard: the default config (AutoFetch=AutoPin=true)
	// must still result in a fetched + pinned tip once dispatchFetch
	// chains Pin after a successful fetch (Task D4 restructured this from
	// two independent goroutines into one sequential chain).
	tq := NewTipQueue(nil)
	fetcher := newMockFetcher()
	fetcher.data["bafyboth"] = []byte("payload")
	pinner := newMockPinner()
	tq.SetFetcher(fetcher)
	tq.SetPinner(pinner)

	tip := &Tip{CID: "bafyboth", SchemaType: "OMM"}
	cfg := ResolvedConfig{AutoFetch: true, AutoPin: true, TTL: 30 * time.Minute}

	tq.processTip(tip, cfg)

	if !waitUntil(t, 2*time.Second, func() bool {
		return fetcher.WasFetched("bafyboth") && pinner.IsPinned("bafyboth")
	}) {
		t.Fatalf("expected fetch+pin to both complete: fetched=%v pinned=%v", fetcher.WasFetched("bafyboth"), pinner.IsPinned("bafyboth"))
	}
	if pinner.GetTTL("bafyboth") != 30*time.Minute {
		t.Fatalf("pin TTL = %v, want 30m", pinner.GetTTL("bafyboth"))
	}
}

func TestErrFetchTooLargeIsDistinguishableViaErrorsIs(t *testing.T) {
	wrapped := fmt.Errorf("adapter rejected cid: %w", ErrFetchTooLarge)
	if !errors.Is(wrapped, ErrFetchTooLarge) {
		t.Fatal("wrapped oversize error should satisfy errors.Is(err, ErrFetchTooLarge)")
	}
}
