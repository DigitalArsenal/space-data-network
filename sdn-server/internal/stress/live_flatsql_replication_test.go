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
	if result.ConfiguredGateEnabled {
		t.Logf("configured link: %.2f MiB/s required: %.2f MiB/s target met: %v",
			bytesPerSecondToMiB(result.ConfiguredLinkBytesPerSecond),
			bytesPerSecondToMiB(result.ConfiguredRequiredBytesPerSecond),
			result.ConfiguredTargetMet)
	}

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
	if result.ConfiguredGateEnabled && !result.ConfiguredTargetMet {
		t.Fatalf("download speed %.2f MiB/s is below configured %.0f%% link gate %.2f MiB/s",
			bytesPerSecondToMiB(result.DownloadBytesPerSecond),
			result.WireSpeedTarget*100,
			bytesPerSecondToMiB(result.ConfiguredRequiredBytesPerSecond))
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

func TestLiveFlatSQLConfiguredWireSpeedGateUsesTwoGbitNinetyPercent(t *testing.T) {
	t.Setenv("SDN_WIRESPEED_TEST", "1")
	t.Setenv("SDN_TEST_LINK_GBIT", "2")

	below := evaluateLiveFlatSQLConfiguredWireSpeedGate(224_999_999, 0.90)
	if !below.Enabled {
		t.Fatalf("configured wire-speed gate was not enabled: %#v", below)
	}
	if below.LinkBytesPerSecond != 250_000_000 || below.RequiredBytesPerSecond != 225_000_000 {
		t.Fatalf("configured wire-speed gate bytes mismatch: %#v", below)
	}
	if below.TargetMet {
		t.Fatalf("configured wire-speed gate passed below 1.8 Gbit/s: %#v", below)
	}

	atTarget := evaluateLiveFlatSQLConfiguredWireSpeedGate(225_000_000, 0.90)
	if !atTarget.TargetMet {
		t.Fatalf("configured wire-speed gate failed at 1.8 Gbit/s: %#v", atTarget)
	}
}
