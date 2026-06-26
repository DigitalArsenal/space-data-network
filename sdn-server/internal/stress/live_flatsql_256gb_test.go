//go:build stress
// +build stress

package stress

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

func TestLiveFlatSQLPublishedShardFetchBenchmarkCountsSparseShardBytes(t *testing.T) {
	t.Setenv("SDN_WIRESPEED_TEST", "")
	t.Setenv("SDN_TEST_LINK_GBIT", "")

	const targetBytes int64 = 2 * 1024 * 1024
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	result, err := RunLiveFlatSQLPublishedShardFetchBenchmark(ctx, LiveFlatSQLPublishedShardFetchOptions{
		TargetBytes: targetBytes,
	})
	if err != nil {
		t.Fatalf("RunLiveFlatSQLPublishedShardFetchBenchmark failed: %v", err)
	}
	if result.HeaderBytes != targetBytes || result.DownloadedBytes != targetBytes {
		t.Fatalf("sparse fetch bytes mismatch: header=%d downloaded=%d want=%d", result.HeaderBytes, result.DownloadedBytes, targetBytes)
	}
	if result.ShardCID == "" || result.ProviderPeerID == "" || result.SubscriberPeerID == "" {
		t.Fatalf("sparse fetch did not preserve peer/shard metadata: %#v", result)
	}
}

func TestLiveFlatSQLFetchesPublishedShard256GB(t *testing.T) {
	skipUnlessLiveFlatSQL256GBEnabled(t)

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Hour)
	defer cancel()

	result, err := RunLiveFlatSQLPublishedShardFetchBenchmark(ctx, LiveFlatSQLPublishedShardFetchOptions{
		TargetBytes: liveFlatSQL256GiBBytes,
	})
	if err != nil {
		t.Fatalf("RunLiveFlatSQLPublishedShardFetchBenchmark failed: %v", err)
	}
	if result.DownloadedBytes != liveFlatSQL256GiBBytes {
		t.Fatalf("fetched %d bytes, want %d", result.DownloadedBytes, liveFlatSQL256GiBBytes)
	}
	if result.DownloadBytesPerSecond <= 0 {
		t.Fatalf("fetch reported non-positive throughput: %#v", result)
	}
	if result.ConfiguredGateEnabled {
		t.Logf("configured link: %.2f MiB/s required: %.2f MiB/s target met: %v",
			bytesPerSecondToMiB(result.ConfiguredLinkBytesPerSecond),
			bytesPerSecondToMiB(result.ConfiguredRequiredBytesPerSecond),
			result.ConfiguredTargetMet)
		if result.ConfiguredLinkBytesPerSecond > 0 && !result.ConfiguredTargetMet {
			t.Fatalf("fetch speed %.2f MiB/s is below configured %.0f%% link gate %.2f MiB/s",
				bytesPerSecondToMiB(result.DownloadBytesPerSecond),
				result.WireSpeedTarget*100,
				bytesPerSecondToMiB(result.ConfiguredRequiredBytesPerSecond))
		}
	}
	t.Logf("fetched %.2f GiB in %s (%.2f MiB/s)",
		float64(result.DownloadedBytes)/(1024*1024*1024),
		result.DownloadDuration,
		bytesPerSecondToMiB(result.DownloadBytesPerSecond))
}

func skipUnlessLiveFlatSQL256GBEnabled(t *testing.T) {
	t.Helper()
	if strings.TrimSpace(os.Getenv("STRESS_LIVE_FLATSQL_256GB")) != "1" {
		t.Skip("set STRESS_LIVE_FLATSQL_256GB=1 to fetch the full 256 GiB payload")
	}
}
