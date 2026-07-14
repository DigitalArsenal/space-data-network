package node

import (
	"context"
	"fmt"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/config"
	"github.com/spacedatanetwork/sdn-server/internal/peers"
	sdnpubsub "github.com/spacedatanetwork/sdn-server/internal/pubsub"
	"github.com/spacedatanetwork/sdn-server/internal/sds"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
)

// --- D4 tunables fold-in: config.TipQueue -> newTipQueueConfig ----------

func TestNewTipQueueConfigAppliesConfiguredTunables(t *testing.T) {
	n := &Node{
		config: &config.Config{
			TipQueue: config.TipQueueConfig{
				MaxFetchBytes:        1 << 20,
				MaxConcurrentFetches: 7,
				MinFetchInterval:     123 * time.Millisecond,
			},
		},
	}
	cfg := n.newTipQueueConfig()
	if cfg.MaxFetchBytes != 1<<20 {
		t.Fatalf("MaxFetchBytes = %d, want %d", cfg.MaxFetchBytes, 1<<20)
	}
	if cfg.MaxConcurrentFetches != 7 {
		t.Fatalf("MaxConcurrentFetches = %d, want 7", cfg.MaxConcurrentFetches)
	}
	if cfg.MinFetchInterval != 123*time.Millisecond {
		t.Fatalf("MinFetchInterval = %v, want 123ms", cfg.MinFetchInterval)
	}
}

func TestNewTipQueueConfigZeroTunablesKeepPubsubDefaults(t *testing.T) {
	n := &Node{config: &config.Config{}}
	cfg := n.newTipQueueConfig()
	if cfg.MaxFetchBytes != sdnpubsub.DefaultMaxFetchBytes {
		t.Fatalf("MaxFetchBytes = %d, want built-in default %d", cfg.MaxFetchBytes, sdnpubsub.DefaultMaxFetchBytes)
	}
	if cfg.MaxConcurrentFetches != sdnpubsub.DefaultMaxConcurrentFetches {
		t.Fatalf("MaxConcurrentFetches = %d, want built-in default %d", cfg.MaxConcurrentFetches, sdnpubsub.DefaultMaxConcurrentFetches)
	}
	if cfg.MinFetchInterval != sdnpubsub.DefaultMinFetchInterval {
		t.Fatalf("MinFetchInterval = %v, want built-in default %v", cfg.MinFetchInterval, sdnpubsub.DefaultMinFetchInterval)
	}
}

// --- storage quota enforcement seam (Task D3) ----------------------------

func newQuotaWiringTestNode(t *testing.T) *Node {
	t.Helper()
	tmpDir := t.TempDir()
	validator, err := sds.NewValidator(nil)
	if err != nil {
		t.Fatalf("NewValidator failed: %v", err)
	}
	store, err := storage.NewFlatSQLStore(filepath.Join(tmpDir, "store"), validator)
	if err != nil {
		t.Fatalf("NewFlatSQLStore failed: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	n := &Node{
		store: store,
		config: &config.Config{
			Storage: config.StorageConfig{Path: filepath.Join(tmpDir, "store")},
		},
		ctx:    ctx,
		cancel: cancel,
	}
	t.Cleanup(func() {
		cancel()
		n.wg.Wait()
		if err := store.Close(); err != nil {
			t.Errorf("close quota wiring test store: %v", err)
		}
	})

	return n
}

func TestNewQuotaWiringTestNodeCleanupWaitsForWorkers(t *testing.T) {
	var workerFinished atomic.Bool

	t.Run("worker", func(t *testing.T) {
		n := newQuotaWiringTestNode(t)
		n.wg.Add(1)
		go func() {
			defer n.wg.Done()
			<-n.ctx.Done()
			time.Sleep(25 * time.Millisecond)
			workerFinished.Store(true)
		}()
	})

	if !workerFinished.Load() {
		t.Fatal("newQuotaWiringTestNode cleanup returned before its worker finished")
	}
}

func TestEnforceStorageQuotaEvictsWhenOverConfiguredCap(t *testing.T) {
	n := newQuotaWiringTestNode(t)

	for i := 0; i < 5; i++ {
		payload := []byte(fmt.Sprintf("filler-record-payload-bytes-%d", i))
		if _, err := n.store.Store("RFM.fbs", payload, "TestPeer", nil); err != nil {
			t.Fatalf("seed record %d failed: %v", i, err)
		}
	}
	before, err := n.store.LiveRecordBytes()
	if err != nil {
		t.Fatalf("LiveRecordBytes failed: %v", err)
	}
	if before == 0 {
		t.Fatal("expected seeded records to contribute live bytes")
	}

	// Absolute byte cap far below what was just seeded -- no Statfs
	// involved, so this is independent of the percentage-resolution path.
	n.config.Storage.MaxSize = "1B"

	n.enforceStorageQuota()

	after, err := n.store.LiveRecordBytes()
	if err != nil {
		t.Fatalf("LiveRecordBytes (after) failed: %v", err)
	}
	if after >= before {
		t.Fatalf("enforceStorageQuota did not evict: live bytes before=%d after=%d", before, after)
	}
}

func TestEnforceStorageQuotaNoOpWhenMaxSizeUnresolvable(t *testing.T) {
	n := newQuotaWiringTestNode(t)
	if _, err := n.store.Store("RFM.fbs", []byte("filler"), "TestPeer", nil); err != nil {
		t.Fatalf("seed record failed: %v", err)
	}
	n.config.Storage.MaxSize = "not-a-size"

	// Must not panic and must not evict (resolution fails, so the
	// function logs a warning and returns without calling into the
	// store's eviction path).
	n.enforceStorageQuota()

	live, err := n.store.LiveRecordBytes()
	if err != nil {
		t.Fatalf("LiveRecordBytes failed: %v", err)
	}
	if live == 0 {
		t.Fatal("expected the seeded record to remain (quota resolution failed, so no eviction should occur)")
	}
}

// TestRunStorageQuotaGCLoopEvictsAndRespectsShutdown is the periodic-loop
// seam test (mirrors the TTL-sweeper/catch-up-loop wiring pattern this
// file's D1/D2 tests already use): one explicit tick must run
// enforceStorageQuota, and the loop must exit promptly once n.ctx is
// cancelled.
func TestRunStorageQuotaGCLoopEvictsAndRespectsShutdown(t *testing.T) {
	n := newQuotaWiringTestNode(t)
	n.config.Storage.MaxSize = "1B"

	for i := 0; i < 5; i++ {
		payload := []byte(fmt.Sprintf("filler-record-payload-bytes-%d", i))
		if _, err := n.store.Store("RFM.fbs", payload, "TestPeer", nil); err != nil {
			t.Fatalf("seed record %d failed: %v", i, err)
		}
	}

	ticks := make(chan time.Time, 1)
	n.wg.Add(1)
	go n.runStorageQuotaGCWithTicks(ticks)
	ticks <- time.Now()

	evicted := waitForCondition(t, 2*time.Second, func() bool {
		live, err := n.store.LiveRecordBytes()
		return err == nil && live == 0
	})
	if !evicted {
		t.Fatal("runStorageQuotaGC did not evict seeded records within the timeout")
	}

	n.cancel()
	done := make(chan struct{})
	go func() {
		n.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("runStorageQuotaGC did not exit within 2s of context cancellation")
	}
}

func TestRunStorageQuotaGCLoopSkipsBufferedTickWhenAlreadyCancelled(t *testing.T) {
	n := newQuotaWiringTestNode(t)
	n.config.Storage.MaxSize = "1B"

	for i := 0; i < 5; i++ {
		payload := []byte(fmt.Sprintf("filler-record-payload-bytes-%d", i))
		if _, err := n.store.Store("RFM.fbs", payload, "TestPeer", nil); err != nil {
			t.Fatalf("seed record %d failed: %v", i, err)
		}
	}
	before, err := n.store.LiveRecordBytes()
	if err != nil {
		t.Fatalf("LiveRecordBytes failed: %v", err)
	}

	ticks := make(chan time.Time, 1)
	ticks <- time.Now()
	n.cancel()
	n.wg.Add(1)
	go n.runStorageQuotaGCWithTicks(ticks)

	done := make(chan struct{})
	go func() {
		n.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("runStorageQuotaGCWithTicks did not exit when started with a cancelled context")
	}

	after, err := n.store.LiveRecordBytes()
	if err != nil {
		t.Fatalf("LiveRecordBytes (after) failed: %v", err)
	}
	if after != before {
		t.Fatalf("cancelled quota loop processed buffered tick: live bytes before=%d after=%d", before, after)
	}
}

// TestMaterializeDatasetPublicationPNMTriggersStorageQuotaEviction proves
// the OTHER Task D3 wiring point: a successful trusted-peer materialization
// dispatches enforceStorageQuota in the background (subscribeFullyTrustedPeer
// / handleTipQueueTip's own ad-hoc n.wg-tracked goroutine pattern), so a
// flood of accepted publications evicts this node's own oldest records
// instead of growing the store unbounded. buildTipMaterializationFixture
// (tipqueue_wiring_test.go, D1) already wires a real store + trusted
// materialization path; this seeds extra "filler" records in a different
// schema before triggering materialization and waits for them to be
// evicted by the post-materialize quota check.
func TestMaterializeDatasetPublicationPNMTriggersStorageQuotaEviction(t *testing.T) {
	n, pnmBytes, providerID := buildTipMaterializationFixture(t)
	if err := n.peerRegistry.AddPeer(&peers.TrustedPeer{ID: providerID, TrustLevel: peers.Trusted}); err != nil {
		t.Fatalf("add trusted provider: %v", err)
	}

	for i := 0; i < 5; i++ {
		payload := []byte(fmt.Sprintf("filler-record-payload-bytes-%d", i))
		if _, err := n.store.Store("RFM.fbs", payload, "filler-peer", nil); err != nil {
			t.Fatalf("seed filler record %d failed: %v", i, err)
		}
	}
	fillerBefore, err := n.store.Query("RFM.fbs", "1 = 1")
	if err != nil {
		t.Fatalf("query filler records: %v", err)
	}
	if len(fillerBefore) != 5 {
		t.Fatalf("seeded filler records = %d, want 5", len(fillerBefore))
	}

	// Tiny absolute cap: whatever materializes plus the filler records is
	// certain to be over cap, without depending on Statfs.
	n.config.Storage.MaxSize = "1B"

	tip := &sdnpubsub.Tip{
		PeerID:     providerID.String(),
		CID:        pnmCID(t, pnmBytes),
		SchemaType: pnmFileID(t, pnmBytes),
		RawPNM:     pnmBytes,
	}
	n.handleTipQueueTip(tip, sdnpubsub.ResolvedConfig{})

	evicted := waitForCondition(t, 2*time.Second, func() bool {
		rows, err := n.store.Query("RFM.fbs", "1 = 1")
		return err == nil && len(rows) < len(fillerBefore)
	})
	if !evicted {
		t.Fatal("materializeDatasetPublicationPNM did not trigger storage quota eviction of pre-existing records")
	}
}
