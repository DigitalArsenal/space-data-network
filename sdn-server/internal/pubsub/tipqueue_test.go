package pubsub

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/PNM"
	flatbuffers "github.com/google/flatbuffers/go"
)

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
