//go:build stress
// +build stress

package stress

import (
	"context"
	"testing"
	"time"
)

func TestLiveFlatSQLReplicationBenchmarkMeetsWireSpeedGate(t *testing.T) {
	targetBytes := getLiveFlatSQLTargetBytes()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	result, err := RunLiveFlatSQLReplicationBenchmark(ctx, LiveFlatSQLReplicationOptions{
		TargetBytes:     targetBytes,
		ProbeBytes:      targetBytes,
		WireSpeedTarget: 0.90,
	})
	if err != nil {
		t.Fatalf("RunLiveFlatSQLReplicationBenchmark failed: %v", err)
	}

	t.Logf("wire speed: %.2f MiB/s", bytesPerSecondToMiB(result.WireSpeedBytesPerSecond))
	t.Logf("download: %.2f MiB/s", bytesPerSecondToMiB(result.DownloadBytesPerSecond))
	t.Logf("downloaded: %.2f MiB across %d FlatSQL rows", float64(result.DownloadedBytes)/(1024*1024), result.RecordCount)
	t.Logf("imported rows: %d", result.ImportedRows)

	if result.DownloadedBytes < targetBytes {
		t.Fatalf("downloaded %d bytes, want at least %d", result.DownloadedBytes, targetBytes)
	}
	if result.ImportedRows != result.RecordCount {
		t.Fatalf("imported %d rows, want %d", result.ImportedRows, result.RecordCount)
	}
	if !result.TargetMet {
		t.Fatalf("download speed %.2f MiB/s is below %.0f%% of measured wire speed %.2f MiB/s",
			bytesPerSecondToMiB(result.DownloadBytesPerSecond),
			result.WireSpeedTarget*100,
			bytesPerSecondToMiB(result.WireSpeedBytesPerSecond))
	}
}

func TestLiveFlatSQLReplicationRangeResumeKeepsVerifiedPrefixAndImportsRows(t *testing.T) {
	targetBytes := getLiveFlatSQLResumeTargetBytes()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	result, err := RunLiveFlatSQLRangeResumeBenchmark(ctx, LiveFlatSQLReplicationOptions{
		TargetBytes:     targetBytes,
		ProbeBytes:      targetBytes,
		WireSpeedTarget: 0.90,
	}, LiveFlatSQLRangeResumeOptions{
		InterruptAfterBytes: targetBytes / 3,
	})
	if err != nil {
		t.Fatalf("RunLiveFlatSQLRangeResumeBenchmark failed: %v", err)
	}

	t.Logf("resumed from byte: %d", result.ResumedFromByte)
	t.Logf("range requests: %d", result.RangeRequests)
	t.Logf("download: %.2f MiB/s", bytesPerSecondToMiB(result.DownloadBytesPerSecond))
	t.Logf("downloaded: %.2f MiB across %d FlatSQL rows", float64(result.DownloadedBytes)/(1024*1024), result.RecordCount)
	t.Logf("imported rows: %d", result.ImportedRows)

	if result.ResumedFromByte <= 0 {
		t.Fatal("resume gate did not preserve a verified byte prefix")
	}
	if result.RedownloadedPrefixBytes != 0 {
		t.Fatalf("resume redownloaded %d prefix bytes, want 0", result.RedownloadedPrefixBytes)
	}
	if result.DownloadedBytes < targetBytes {
		t.Fatalf("downloaded %d bytes, want at least %d", result.DownloadedBytes, targetBytes)
	}
	if result.ImportedRows != result.RecordCount {
		t.Fatalf("imported %d rows, want %d", result.ImportedRows, result.RecordCount)
	}
	if result.LocalRows != int64(result.RecordCount) {
		t.Fatalf("local rows = %d, want %d", result.LocalRows, result.RecordCount)
	}
}

func TestLiveFlatSQLReplicationCanAlternateRangesAcrossTwoProviders(t *testing.T) {
	targetBytes := getLiveFlatSQLResumeTargetBytes()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	result, err := RunLiveFlatSQLMultiProviderRangeBenchmark(ctx, LiveFlatSQLReplicationOptions{
		TargetBytes: targetBytes,
		ProbeBytes:  targetBytes,
	}, LiveFlatSQLRangeResumeOptions{
		RangeBytes: targetBytes / 4,
	})
	if err != nil {
		t.Fatalf("RunLiveFlatSQLMultiProviderRangeBenchmark failed: %v", err)
	}

	t.Logf("range requests: %d", result.RangeRequests)
	t.Logf("provider bytes: %v", result.ProviderBytes)
	t.Logf("downloaded: %.2f MiB across %d FlatSQL rows", float64(result.DownloadedBytes)/(1024*1024), result.RecordCount)

	if len(result.ProviderPeerIDs) != 2 {
		t.Fatalf("provider count = %d, want 2", len(result.ProviderPeerIDs))
	}
	if len(result.ProviderBytes) != 2 || result.ProviderBytes[0] <= 0 || result.ProviderBytes[1] <= 0 {
		t.Fatalf("multi-provider range replication did not use both providers: %v", result.ProviderBytes)
	}
	if result.ImportedRows != result.RecordCount {
		t.Fatalf("imported %d rows, want %d", result.ImportedRows, result.RecordCount)
	}
	if result.LocalRows != int64(result.RecordCount) {
		t.Fatalf("local rows = %d, want %d", result.LocalRows, result.RecordCount)
	}
}
