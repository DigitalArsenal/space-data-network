//go:build stress
// +build stress

package stress

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/OMM"
	"github.com/spacedatanetwork/sdn-server/internal/sds"
)

// getTargetSize returns the target data size from environment or default (10GB).
func getTargetSize() int64 {
	if envSize := os.Getenv("STRESS_TARGET_SIZE"); envSize != "" {
		if size, err := strconv.ParseInt(envSize, 10, 64); err == nil {
			return size
		}
	}
	return DefaultTargetSize
}

// TestGenerate10GBFlatBuffers tests generating 10GB of OMM FlatBuffers.
func TestGenerate10GBFlatBuffers(t *testing.T) {
	targetSize := getTargetSize()

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Hour)
	defer cancel()

	generator := NewGenerator()

	t.Logf("Starting generation of %.2f GB of FlatBuffers", float64(targetSize)/(1024*1024*1024))
	t.Logf("Using %d workers, %d records per batch", WorkerCount, BatchSize)

	startTime := time.Now()
	batchCount := 0
	lastLogTime := startTime

	for batch := range generator.GenerateBatches(ctx, targetSize) {
		if batch.Err != nil {
			t.Fatalf("Batch generation failed: %v", batch.Err)
		}

		batchCount++

		// Log progress every 5 seconds
		if time.Since(lastLogTime) > 5*time.Second {
			total, count := generator.Stats()
			elapsed := time.Since(startTime)
			rate := float64(total) / elapsed.Seconds() / (1024 * 1024) // MB/s
			progress := float64(total) / float64(targetSize) * 100
			t.Logf("Progress: %.1f%% | %d records | %.2f GB | %.2f MB/s",
				progress, count, float64(total)/(1024*1024*1024), rate)
			lastLogTime = time.Now()
		}

		// Check if we've reached target
		total, _ := generator.Stats()
		if total >= targetSize {
			break
		}
	}

	total, count := generator.Stats()
	elapsed := time.Since(startTime)

	t.Logf("=== Generation Complete ===")
	t.Logf("Total records: %d", count)
	t.Logf("Total size: %.2f GB", float64(total)/(1024*1024*1024))
	t.Logf("Duration: %v", elapsed)
	t.Logf("Rate: %.2f MB/s", float64(total)/elapsed.Seconds()/(1024*1024))
	t.Logf("Batches: %d", batchCount)

	if total < targetSize {
		t.Errorf("Did not reach target size: got %d, want %d", total, targetSize)
	}
}

// TestPinAndTrack10GB tests pinning 10GB of FlatBuffers and tracking CIDs.
func TestPinAndTrack10GB(t *testing.T) {
	targetSize := getTargetSize()

	tmpDir, err := os.MkdirTemp("", "stress-pin-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Create pinner with file-backed tracking
	pinner, err := NewStressPinner(filepath.Join(tmpDir, "pins.idx"))
	if err != nil {
		t.Fatalf("Failed to create pinner: %v", err)
	}
	defer pinner.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Hour)
	defer cancel()

	generator := NewGenerator()
	startTime := time.Now()
	lastLogTime := startTime

	t.Logf("Starting pin tracking for %.2f GB", float64(targetSize)/(1024*1024*1024))

	for batch := range generator.GenerateBatches(ctx, targetSize) {
		if batch.Err != nil {
			t.Fatalf("Batch generation failed: %v", batch.Err)
		}

		for _, record := range batch.Records {
			if err := pinner.Pin(ctx, record.CID, record.Size, 24*time.Hour); err != nil {
				t.Fatalf("Pin failed: %v", err)
			}
		}

		// Log progress every 5 seconds
		if time.Since(lastLogTime) > 5*time.Second {
			count, bytes := pinner.Tracker().Stats()
			elapsed := time.Since(startTime)
			rate := float64(bytes) / elapsed.Seconds() / (1024 * 1024)
			progress := float64(bytes) / float64(targetSize) * 100
			t.Logf("Pinning: %.1f%% | %d CIDs | %.2f GB | %.2f MB/s",
				progress, count, float64(bytes)/(1024*1024*1024), rate)
			lastLogTime = time.Now()
		}

		// Check if we've reached target
		_, bytes := pinner.Tracker().Stats()
		if bytes >= targetSize {
			break
		}
	}

	// Flush to ensure all data is written
	if err := pinner.Tracker().Flush(); err != nil {
		t.Fatalf("Failed to flush tracker: %v", err)
	}

	pinCount, pinBytes := pinner.Tracker().Stats()
	pinDuration := time.Since(startTime)

	t.Logf("=== Pinning Complete ===")
	t.Logf("CIDs pinned: %d", pinCount)
	t.Logf("Total bytes: %.2f GB", float64(pinBytes)/(1024*1024*1024))
	t.Logf("Pin duration: %v", pinDuration)

	// Now verify we can stream back all CIDs
	t.Log("=== Verifying CID Streaming ===")
	streamStart := time.Now()

	cidCh, err := pinner.Tracker().StreamCIDs(ctx)
	if err != nil {
		t.Fatalf("Failed to stream CIDs: %v", err)
	}

	streamedCount := int64(0)
	for range cidCh {
		streamedCount++
		if streamedCount%1000000 == 0 {
			t.Logf("Streamed %d CIDs...", streamedCount)
		}
	}

	streamDuration := time.Since(streamStart)

	if streamedCount != pinCount {
		t.Errorf("Streamed count mismatch: got %d, want %d", streamedCount, pinCount)
	}

	t.Logf("=== Streaming Verification Complete ===")
	t.Logf("Streamed CIDs: %d", streamedCount)
	t.Logf("Stream duration: %v", streamDuration)
	t.Logf("Stream rate: %.0f CIDs/sec", float64(streamedCount)/streamDuration.Seconds())

	// Log index file size
	indexPath := pinner.Tracker().IndexPath()
	if info, err := os.Stat(indexPath); err == nil {
		t.Logf("Index file size: %.2f MB", float64(info.Size())/(1024*1024))
	}
}

// TestIntegrityVerification verifies FlatBuffer data integrity.
func TestIntegrityVerification(t *testing.T) {
	builder := sds.NewOMMBuilder()

	t.Log("Testing FlatBuffer integrity with 10,000 records...")

	for i := 0; i < 10000; i++ {
		noradID := uint32(i + 1)
		objectName := fmt.Sprintf("INTEGRITY-SAT-%d", noradID)

		data := builder.
			WithNoradCatID(noradID).
			WithObjectName(objectName).
			WithMeanMotion(15.5 + float64(i)/10000).
			Build()

		// Verify the data can be deserialized
		omm := OMM.GetSizePrefixedRootAsOMM(data, 0)

		if omm.NORAD_CAT_ID() != noradID {
			t.Errorf("Record %d: NORAD_CAT_ID mismatch: got %d, want %d",
				i, omm.NORAD_CAT_ID(), noradID)
		}

		if string(omm.OBJECT_NAME()) != objectName {
			t.Errorf("Record %d: OBJECT_NAME mismatch: got %s, want %s",
				i, string(omm.OBJECT_NAME()), objectName)
		}
	}

	t.Log("Integrity verification passed for all 10,000 records")
}

// TestCIDDeterminism verifies that CID computation is deterministic.
func TestCIDDeterminism(t *testing.T) {
	builder := sds.NewOMMBuilder()

	t.Log("Testing CID determinism...")

	for i := 0; i < 1000; i++ {
		// Build same data twice with same parameters
		data1 := builder.
			WithNoradCatID(uint32(i)).
			WithObjectName(fmt.Sprintf("TEST-SAT-%d", i)).
			Build()

		data2 := builder.
			WithNoradCatID(uint32(i)).
			WithObjectName(fmt.Sprintf("TEST-SAT-%d", i)).
			Build()

		cid1 := computeCID(data1)
		cid2 := computeCID(data2)

		if cid1 != cid2 {
			t.Errorf("CIDs should be deterministic for record %d: %s != %s", i, cid1, cid2)
		}
	}

	t.Log("CID determinism verified for 1,000 records")
}

// TestTransferBetweenNodes tests data transfer between two SDN nodes.
// This test requires running nodes and is skipped if not configured.
func TestTransferBetweenNodes(t *testing.T) {
	node1Addr := os.Getenv("STRESS_NODE1_ADDR")
	node2Addr := os.Getenv("STRESS_NODE2_ADDR")

	if node1Addr == "" || node2Addr == "" {
		t.Skip("Set STRESS_NODE1_ADDR and STRESS_NODE2_ADDR to run transfer test")
	}

	t.Logf("Transfer test configured:")
	t.Logf("  Node 1: %s", node1Addr)
	t.Logf("  Node 2: %s", node2Addr)

	// TODO: Implement actual node-to-node transfer test
	// This would require:
	// 1. Connect to node1 and generate/pin data
	// 2. Publish PNM tips announcing the content
	// 3. Connect to node2 and verify it received the tips
	// 4. Verify node2 can fetch and pin the content
	// 5. Compare CID lists between nodes

	t.Log("Transfer test placeholder - implement when nodes are running")
}

// TestGeneratorStats tests the generator statistics tracking.
func TestGeneratorStats(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	generator := NewGenerator()

	// Generate a small amount for testing
	targetSize := int64(1024 * 1024) // 1MB

	for batch := range generator.GenerateBatches(ctx, targetSize) {
		if batch.Err != nil {
			t.Fatalf("Batch generation failed: %v", batch.Err)
		}

		total, count := generator.Stats()
		if total >= targetSize {
			t.Logf("Generated %d records totaling %d bytes", count, total)
			break
		}
	}

	total, count := generator.Stats()
	if total == 0 {
		t.Error("Generator should have produced data")
	}
	if count == 0 {
		t.Error("Generator should have counted records")
	}

	t.Logf("Final stats: %d records, %d bytes", count, total)
}

// TestPinTrackerStreaming tests the pin tracker streaming functionality.
func TestPinTrackerStreaming(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "pin-tracker-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	tracker, err := NewStreamingPinTracker(filepath.Join(tmpDir, "test.idx"))
	if err != nil {
		t.Fatalf("Failed to create tracker: %v", err)
	}
	defer tracker.Close()

	// Record some pins
	numPins := 100000
	for i := 0; i < numPins; i++ {
		cid := fmt.Sprintf("bafybei%040d", i)
		if err := tracker.RecordPin(cid, int64(256+i%100)); err != nil {
			t.Fatalf("Failed to record pin: %v", err)
		}
	}

	count, _ := tracker.Stats()
	if count != int64(numPins) {
		t.Errorf("Pin count mismatch: got %d, want %d", count, numPins)
	}

	// Stream CIDs back
	ctx := context.Background()
	cidCh, err := tracker.StreamCIDs(ctx)
	if err != nil {
		t.Fatalf("Failed to stream CIDs: %v", err)
	}

	streamedCount := 0
	for cid := range cidCh {
		expected := fmt.Sprintf("bafybei%040d", streamedCount)
		if cid != expected {
			t.Errorf("CID mismatch at %d: got %s, want %s", streamedCount, cid, expected)
			break
		}
		streamedCount++
	}

	if streamedCount != numPins {
		t.Errorf("Streamed count mismatch: got %d, want %d", streamedCount, numPins)
	}

	t.Logf("Successfully streamed %d CIDs", streamedCount)
}
