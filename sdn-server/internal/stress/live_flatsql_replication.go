//go:build stress
// +build stress

package stress

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/peerstore"

	"github.com/spacedatanetwork/sdn-server/internal/protocol"
	"github.com/spacedatanetwork/sdn-server/internal/sds"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
)

const (
	defaultLiveFlatSQLTargetBytes = 64 * 1024 * 1024
	defaultLiveFlatSQLResumeBytes = 16 * 1024 * 1024
	liveFlatSQLSchema             = "OMM.fbs"
	liveFlatSQLProviderID         = "stress-provider"
	liveFlatSQLSourceName         = "OMM"
	liveFlatSQLBatchID            = "stress-batch"
)

// LiveFlatSQLReplicationOptions controls the libp2p FlatSQL payload benchmark.
type LiveFlatSQLReplicationOptions struct {
	TargetBytes     int64
	ProbeBytes      int64
	WireSpeedTarget float64
	WorkDir         string
	SchemaName      string
	ProviderID      string
	SourceName      string
	BatchID         string
}

// LiveFlatSQLReplicationResult is the measured result of one FlatSQL payload
// replication run.
type LiveFlatSQLReplicationResult struct {
	SchemaName              string
	ProviderPeerID          string
	SubscriberPeerID        string
	RecordCount             int
	DownloadedBytes         int64
	WireSpeedBytes          int64
	WireSpeedDuration       time.Duration
	DownloadDuration        time.Duration
	VerificationDuration    time.Duration
	ImportDuration          time.Duration
	ManifestDuration        time.Duration
	WireSpeedBytesPerSecond float64
	DownloadBytesPerSecond  float64
	WireSpeedTarget         float64
	TargetMet               bool
	ImportedRows            int
	LocalRows               int64
	DownloadedShardPath     string
	ShardCID                string
	ShardSHA256             string
	IndexCID                string
}

// LiveFlatSQLRangeResumeOptions controls the deterministic interrupted-shard
// resume check.
type LiveFlatSQLRangeResumeOptions struct {
	InterruptAfterBytes int64
	RangeBytes          int64
}

// LiveFlatSQLRangeResumeResult records a resumed published-shard transfer.
type LiveFlatSQLRangeResumeResult struct {
	LiveFlatSQLReplicationResult
	ResumedFromByte         int64
	RangeRequests           int
	RedownloadedPrefixBytes int64
}

// LiveFlatSQLMultiProviderRangeResult records a single immutable shard
// transfer whose byte ranges were served by more than one libp2p provider.
type LiveFlatSQLMultiProviderRangeResult struct {
	LiveFlatSQLReplicationResult
	ProviderPeerIDs []string
	ProviderBytes   []int64
	RangeRequests   int
}

type liveFlatSQLManifest struct {
	TotalCount int64 `json:"total_count"`
	Segments   []struct {
		CID         string `json:"cid"`
		IndexCID    string `json:"index_cid"`
		ByteCount   int64  `json:"byte_count"`
		ShardSHA256 string `json:"shard_sha256"`
	} `json:"segments"`
}

type liveFlatSQLBatchHeader struct {
	Op           string `json:"op"`
	Status       string `json:"status"`
	SyncProtocol string `json:"sync_protocol"`
	ByteCount    int64  `json:"byte_count"`
	Shards       []struct {
		CID         string `json:"cid"`
		IndexCID    string `json:"index_cid"`
		ByteCount   int64  `json:"byte_count"`
		ShardSHA256 string `json:"shard_sha256"`
	} `json:"shards"`
	Error struct {
		Message string `json:"message"`
	} `json:"error"`
}

type liveFlatSQLProbeHeader struct {
	Op           string `json:"op"`
	Status       string `json:"status"`
	SyncProtocol string `json:"sync_protocol"`
	ProbeBytes   int64  `json:"probe_bytes"`
	PayloadBytes int64  `json:"payload_bytes"`
	Error        struct {
		Message string `json:"message"`
	} `json:"error"`
}

type liveFlatSQLShardHeader struct {
	Op             string `json:"op"`
	Status         string `json:"status"`
	SyncProtocol   string `json:"sync_protocol"`
	CID            string `json:"cid"`
	ByteCount      int64  `json:"byte_count"`
	ByteOffset     int64  `json:"byte_offset"`
	ByteLength     int64  `json:"byte_length"`
	TotalByteCount int64  `json:"total_byte_count"`
	Error          struct {
		Message string `json:"message"`
	} `json:"error"`
}

// RunLiveFlatSQLReplicationBenchmark creates two live libp2p hosts, serves a
// FlatSQL published shard from one host, downloads it over the production sync
// protocol from the other host, verifies the raw shard bytes, and imports them
// into the subscriber FlatSQL store.
func RunLiveFlatSQLReplicationBenchmark(ctx context.Context, opts LiveFlatSQLReplicationOptions) (*LiveFlatSQLReplicationResult, error) {
	opts = normalizeLiveFlatSQLReplicationOptions(opts)
	workDir, cleanup, err := liveFlatSQLWorkDir(opts.WorkDir)
	if err != nil {
		return nil, err
	}
	defer cleanup()

	validator, err := sds.NewValidator(nil)
	if err != nil {
		return nil, fmt.Errorf("create SDS validator: %w", err)
	}
	providerStore, err := storage.NewFlatSQLStore(filepath.Join(workDir, "provider", "db"), validator)
	if err != nil {
		return nil, fmt.Errorf("create provider FlatSQL store: %w", err)
	}
	defer providerStore.Close()
	subscriberStore, err := storage.NewFlatSQLStore(filepath.Join(workDir, "subscriber", "db"), validator)
	if err != nil {
		return nil, fmt.Errorf("create subscriber FlatSQL store: %w", err)
	}
	defer subscriberStore.Close()

	export, err := buildLiveFlatSQLPublishedShard(ctx, providerStore, opts)
	if err != nil {
		return nil, err
	}

	providerHost, subscriberHost, err := startLiveFlatSQLPeers(ctx, providerStore)
	if err != nil {
		return nil, err
	}
	defer providerHost.Close()
	defer subscriberHost.Close()

	manifestStarted := time.Now()
	manifest, err := requestLiveFlatSQLManifest(ctx, subscriberHost, providerHost.ID(), opts)
	if err != nil {
		return nil, err
	}
	manifestDuration := time.Since(manifestStarted)
	cids := make([]string, 0, len(manifest.Segments))
	for _, segment := range manifest.Segments {
		if strings.TrimSpace(segment.CID) != "" {
			cids = append(cids, strings.TrimSpace(segment.CID))
		}
	}
	if len(cids) == 0 {
		return nil, fmt.Errorf("published manifest did not return CID-backed FlatSQL shards")
	}

	wireBytes, wireDuration, err := measureLiveFlatSQLWireSpeed(ctx, subscriberHost, providerHost.ID(), opts.ProbeBytes)
	if err != nil {
		return nil, err
	}
	downloadPath := filepath.Join(workDir, "subscriber", "downloads", export.ShardCID+".fbshard")
	downloadedBytes, batchHeader, downloadDuration, err := downloadLiveFlatSQLShardBatchToFile(ctx, subscriberHost, providerHost.ID(), opts, cids, downloadPath)
	if err != nil {
		return nil, err
	}
	if batchHeader.ByteCount != downloadedBytes {
		return nil, fmt.Errorf("downloaded %d bytes but batch header advertised %d", downloadedBytes, batchHeader.ByteCount)
	}

	verifyStarted := time.Now()
	if err := verifyLiveFlatSQLShardFile(downloadPath, export.ShardSHA256); err != nil {
		return nil, err
	}
	verificationDuration := time.Since(verifyStarted)

	importStarted := time.Now()
	imported, _, err := subscriberStore.ImportDatasetShardFromFiles(downloadPath, export.IndexPath, providerHost.ID().String())
	if err != nil {
		return nil, fmt.Errorf("import replicated FlatSQL shard: %w", err)
	}
	importDuration := time.Since(importStarted)
	localRows, err := subscriberStore.CountRawRecords(storage.RawRecordQuery{SchemaName: opts.SchemaName})
	if err != nil {
		return nil, fmt.Errorf("count replicated FlatSQL rows: %w", err)
	}

	wireSpeed := bytesPerSecond(wireBytes, wireDuration)
	downloadSpeed := bytesPerSecond(downloadedBytes, downloadDuration)
	targetMet := downloadSpeed >= wireSpeed*opts.WireSpeedTarget
	return &LiveFlatSQLReplicationResult{
		SchemaName:              opts.SchemaName,
		ProviderPeerID:          providerHost.ID().String(),
		SubscriberPeerID:        subscriberHost.ID().String(),
		RecordCount:             export.RecordCount,
		DownloadedBytes:         downloadedBytes,
		WireSpeedBytes:          wireBytes,
		WireSpeedDuration:       wireDuration,
		DownloadDuration:        downloadDuration,
		VerificationDuration:    verificationDuration,
		ImportDuration:          importDuration,
		ManifestDuration:        manifestDuration,
		WireSpeedBytesPerSecond: wireSpeed,
		DownloadBytesPerSecond:  downloadSpeed,
		WireSpeedTarget:         opts.WireSpeedTarget,
		TargetMet:               targetMet,
		ImportedRows:            imported,
		LocalRows:               localRows,
		DownloadedShardPath:     downloadPath,
		ShardCID:                export.ShardCID,
		ShardSHA256:             export.ShardSHA256,
		IndexCID:                export.IndexCID,
	}, nil
}

// RunLiveFlatSQLRangeResumeBenchmark proves that a published FlatSQL shard can
// resume from an already-written byte prefix, verify the completed shard, and
// import that shard into the subscriber datastore.
func RunLiveFlatSQLRangeResumeBenchmark(ctx context.Context, opts LiveFlatSQLReplicationOptions, resumeOpts LiveFlatSQLRangeResumeOptions) (*LiveFlatSQLRangeResumeResult, error) {
	opts = normalizeLiveFlatSQLReplicationOptions(opts)
	resumeOpts = normalizeLiveFlatSQLRangeResumeOptions(opts.TargetBytes, resumeOpts)
	workDir, cleanup, err := liveFlatSQLWorkDir(opts.WorkDir)
	if err != nil {
		return nil, err
	}
	defer cleanup()

	validator, err := sds.NewValidator(nil)
	if err != nil {
		return nil, fmt.Errorf("create SDS validator: %w", err)
	}
	providerStore, err := storage.NewFlatSQLStore(filepath.Join(workDir, "provider", "db"), validator)
	if err != nil {
		return nil, fmt.Errorf("create provider FlatSQL store: %w", err)
	}
	defer providerStore.Close()
	subscriberStore, err := storage.NewFlatSQLStore(filepath.Join(workDir, "subscriber", "db"), validator)
	if err != nil {
		return nil, fmt.Errorf("create subscriber FlatSQL store: %w", err)
	}
	defer subscriberStore.Close()

	export, err := buildLiveFlatSQLPublishedShard(ctx, providerStore, opts)
	if err != nil {
		return nil, err
	}

	providerHost, subscriberHost, err := startLiveFlatSQLPeers(ctx, providerStore)
	if err != nil {
		return nil, err
	}
	defer providerHost.Close()
	defer subscriberHost.Close()

	manifestStarted := time.Now()
	manifest, err := requestLiveFlatSQLManifest(ctx, subscriberHost, providerHost.ID(), opts)
	if err != nil {
		return nil, err
	}
	manifestDuration := time.Since(manifestStarted)
	if len(manifest.Segments) == 0 || strings.TrimSpace(manifest.Segments[0].CID) == "" {
		return nil, fmt.Errorf("published manifest did not return a resumable CID-backed FlatSQL shard")
	}
	segment := manifest.Segments[0]
	totalBytes := segment.ByteCount
	if totalBytes <= 0 {
		totalBytes = export.ShardBytes
	}
	if totalBytes <= 0 {
		return nil, fmt.Errorf("published manifest returned no shard byte count")
	}

	wireBytes, wireDuration, err := measureLiveFlatSQLWireSpeed(ctx, subscriberHost, providerHost.ID(), opts.ProbeBytes)
	if err != nil {
		return nil, err
	}
	downloadPath := filepath.Join(workDir, "subscriber", "downloads", export.ShardCID+".resume.fbshard")
	downloadStarted := time.Now()
	firstBytes, firstHeader, err := downloadLiveFlatSQLShardRangeToFile(ctx, subscriberHost, providerHost.ID(), opts, segment.CID, 0, resumeOpts.InterruptAfterBytes, downloadPath)
	if err != nil {
		return nil, err
	}
	if firstHeader.ByteOffset != 0 {
		return nil, fmt.Errorf("first shard range started at %d, want 0", firstHeader.ByteOffset)
	}
	resumeOffset := firstBytes
	rangeRequests := 1
	for resumeOffset < totalBytes {
		length := resumeOpts.RangeBytes
		if remaining := totalBytes - resumeOffset; length <= 0 || length > remaining {
			length = remaining
		}
		readBytes, header, err := downloadLiveFlatSQLShardRangeToFile(ctx, subscriberHost, providerHost.ID(), opts, segment.CID, resumeOffset, length, downloadPath)
		if err != nil {
			return nil, err
		}
		if header.ByteOffset != resumeOffset {
			return nil, fmt.Errorf("resume shard range started at %d, want %d", header.ByteOffset, resumeOffset)
		}
		resumeOffset += readBytes
		rangeRequests++
	}
	downloadDuration := time.Since(downloadStarted)

	verifyStarted := time.Now()
	if err := verifyLiveFlatSQLShardFile(downloadPath, export.ShardSHA256); err != nil {
		return nil, err
	}
	verificationDuration := time.Since(verifyStarted)

	importStarted := time.Now()
	imported, _, err := subscriberStore.ImportDatasetShardFromFiles(downloadPath, export.IndexPath, providerHost.ID().String())
	if err != nil {
		return nil, fmt.Errorf("import resumed FlatSQL shard: %w", err)
	}
	importDuration := time.Since(importStarted)
	localRows, err := subscriberStore.CountRawRecords(storage.RawRecordQuery{SchemaName: opts.SchemaName})
	if err != nil {
		return nil, fmt.Errorf("count resumed FlatSQL rows: %w", err)
	}

	wireSpeed := bytesPerSecond(wireBytes, wireDuration)
	downloadSpeed := bytesPerSecond(totalBytes, downloadDuration)
	result := LiveFlatSQLReplicationResult{
		SchemaName:              opts.SchemaName,
		ProviderPeerID:          providerHost.ID().String(),
		SubscriberPeerID:        subscriberHost.ID().String(),
		RecordCount:             export.RecordCount,
		DownloadedBytes:         totalBytes,
		WireSpeedBytes:          wireBytes,
		WireSpeedDuration:       wireDuration,
		DownloadDuration:        downloadDuration,
		VerificationDuration:    verificationDuration,
		ImportDuration:          importDuration,
		ManifestDuration:        manifestDuration,
		WireSpeedBytesPerSecond: wireSpeed,
		DownloadBytesPerSecond:  downloadSpeed,
		WireSpeedTarget:         opts.WireSpeedTarget,
		TargetMet:               downloadSpeed >= wireSpeed*opts.WireSpeedTarget,
		ImportedRows:            imported,
		LocalRows:               localRows,
		DownloadedShardPath:     downloadPath,
		ShardCID:                export.ShardCID,
		ShardSHA256:             export.ShardSHA256,
		IndexCID:                export.IndexCID,
	}
	return &LiveFlatSQLRangeResumeResult{
		LiveFlatSQLReplicationResult: result,
		ResumedFromByte:              firstBytes,
		RangeRequests:                rangeRequests,
		RedownloadedPrefixBytes:      0,
	}, nil
}

// RunLiveFlatSQLMultiProviderRangeBenchmark proves that the same immutable
// FlatSQL shard can be served by multiple libp2p providers and reassembled by a
// subscriber without changing the verified FlatSQL backing bytes.
func RunLiveFlatSQLMultiProviderRangeBenchmark(ctx context.Context, opts LiveFlatSQLReplicationOptions, resumeOpts LiveFlatSQLRangeResumeOptions) (*LiveFlatSQLMultiProviderRangeResult, error) {
	opts = normalizeLiveFlatSQLReplicationOptions(opts)
	resumeOpts = normalizeLiveFlatSQLRangeResumeOptions(opts.TargetBytes, resumeOpts)
	workDir, cleanup, err := liveFlatSQLWorkDir(opts.WorkDir)
	if err != nil {
		return nil, err
	}
	defer cleanup()

	validator, err := sds.NewValidator(nil)
	if err != nil {
		return nil, fmt.Errorf("create SDS validator: %w", err)
	}
	providerStoreA, err := storage.NewFlatSQLStore(filepath.Join(workDir, "provider-a", "db"), validator)
	if err != nil {
		return nil, fmt.Errorf("create provider A FlatSQL store: %w", err)
	}
	defer providerStoreA.Close()
	providerStoreB, err := storage.NewFlatSQLStore(filepath.Join(workDir, "provider-b", "db"), validator)
	if err != nil {
		return nil, fmt.Errorf("create provider B FlatSQL store: %w", err)
	}
	defer providerStoreB.Close()
	subscriberStore, err := storage.NewFlatSQLStore(filepath.Join(workDir, "subscriber", "db"), validator)
	if err != nil {
		return nil, fmt.Errorf("create subscriber FlatSQL store: %w", err)
	}
	defer subscriberStore.Close()

	tags := storage.SourceTags{
		ProviderID: opts.ProviderID,
		SourceName: opts.SourceName,
		BatchID:    opts.BatchID,
	}
	records, err := generateLiveFlatSQLExportRecords(ctx, opts.TargetBytes, tags)
	if err != nil {
		return nil, err
	}
	exportA, err := exportLiveFlatSQLRecords(providerStoreA, opts, records)
	if err != nil {
		return nil, fmt.Errorf("publish provider A shard: %w", err)
	}
	exportB, err := exportLiveFlatSQLRecords(providerStoreB, opts, records)
	if err != nil {
		return nil, fmt.Errorf("publish provider B shard: %w", err)
	}
	if exportA.ShardCID != exportB.ShardCID || exportA.ShardSHA256 != exportB.ShardSHA256 {
		return nil, fmt.Errorf("multi-provider exports are not identical")
	}

	providerHostA, err := libp2p.New(libp2p.ListenAddrStrings("/ip4/127.0.0.1/tcp/0"))
	if err != nil {
		return nil, fmt.Errorf("create provider A libp2p host: %w", err)
	}
	defer providerHostA.Close()
	providerHostA.SetStreamHandler(protocol.FlatSQLSyncProtocolID, protocol.NewFlatSQLSyncHandler(providerStoreA).HandleStream)

	providerHostB, err := libp2p.New(libp2p.ListenAddrStrings("/ip4/127.0.0.1/tcp/0"))
	if err != nil {
		return nil, fmt.Errorf("create provider B libp2p host: %w", err)
	}
	defer providerHostB.Close()
	providerHostB.SetStreamHandler(protocol.FlatSQLSyncProtocolID, protocol.NewFlatSQLSyncHandler(providerStoreB).HandleStream)

	subscriberHost, err := libp2p.New(libp2p.NoListenAddrs)
	if err != nil {
		return nil, fmt.Errorf("create subscriber libp2p host: %w", err)
	}
	defer subscriberHost.Close()
	providers := []host.Host{providerHostA, providerHostB}
	for _, providerHost := range providers {
		subscriberHost.Peerstore().AddAddrs(providerHost.ID(), providerHost.Addrs(), peerstore.PermanentAddrTTL)
		if err := subscriberHost.Connect(ctx, peer.AddrInfo{ID: providerHost.ID(), Addrs: providerHost.Addrs()}); err != nil {
			return nil, fmt.Errorf("connect subscriber to provider %s: %w", providerHost.ID(), err)
		}
	}

	manifestStarted := time.Now()
	manifest, err := requestLiveFlatSQLManifest(ctx, subscriberHost, providerHostA.ID(), opts)
	if err != nil {
		return nil, err
	}
	manifestDuration := time.Since(manifestStarted)
	if len(manifest.Segments) == 0 || strings.TrimSpace(manifest.Segments[0].CID) == "" {
		return nil, fmt.Errorf("published manifest did not return a CID-backed FlatSQL shard")
	}
	segment := manifest.Segments[0]
	totalBytes := segment.ByteCount
	if totalBytes <= 0 {
		totalBytes = exportA.ShardBytes
	}
	if totalBytes <= 0 {
		return nil, fmt.Errorf("published manifest returned no shard byte count")
	}

	wireBytes, wireDuration, err := measureLiveFlatSQLWireSpeed(ctx, subscriberHost, providerHostA.ID(), opts.ProbeBytes)
	if err != nil {
		return nil, err
	}

	downloadPath := filepath.Join(workDir, "subscriber", "downloads", exportA.ShardCID+".multi-provider.fbshard")
	providerBytes := make([]int64, len(providers))
	downloadStarted := time.Now()
	var offset int64
	rangeRequests := 0
	for offset < totalBytes {
		providerIndex := rangeRequests % len(providers)
		length := resumeOpts.RangeBytes
		if remaining := totalBytes - offset; length <= 0 || length > remaining {
			length = remaining
		}
		readBytes, header, err := downloadLiveFlatSQLShardRangeToFile(ctx, subscriberHost, providers[providerIndex].ID(), opts, segment.CID, offset, length, downloadPath)
		if err != nil {
			return nil, err
		}
		if header.ByteOffset != offset {
			return nil, fmt.Errorf("provider %s returned offset %d, want %d", providers[providerIndex].ID(), header.ByteOffset, offset)
		}
		providerBytes[providerIndex] += readBytes
		offset += readBytes
		rangeRequests++
	}
	downloadDuration := time.Since(downloadStarted)

	verifyStarted := time.Now()
	if err := verifyLiveFlatSQLShardFile(downloadPath, exportA.ShardSHA256); err != nil {
		return nil, err
	}
	verificationDuration := time.Since(verifyStarted)

	importStarted := time.Now()
	imported, _, err := subscriberStore.ImportDatasetShardFromFiles(downloadPath, exportA.IndexPath, providerHostA.ID().String())
	if err != nil {
		return nil, fmt.Errorf("import multi-provider FlatSQL shard: %w", err)
	}
	importDuration := time.Since(importStarted)
	localRows, err := subscriberStore.CountRawRecords(storage.RawRecordQuery{SchemaName: opts.SchemaName})
	if err != nil {
		return nil, fmt.Errorf("count multi-provider FlatSQL rows: %w", err)
	}

	providerPeerIDs := []string{providerHostA.ID().String(), providerHostB.ID().String()}
	wireSpeed := bytesPerSecond(wireBytes, wireDuration)
	downloadSpeed := bytesPerSecond(totalBytes, downloadDuration)
	result := LiveFlatSQLReplicationResult{
		SchemaName:              opts.SchemaName,
		ProviderPeerID:          providerHostA.ID().String(),
		SubscriberPeerID:        subscriberHost.ID().String(),
		RecordCount:             exportA.RecordCount,
		DownloadedBytes:         totalBytes,
		WireSpeedBytes:          wireBytes,
		WireSpeedDuration:       wireDuration,
		DownloadDuration:        downloadDuration,
		VerificationDuration:    verificationDuration,
		ImportDuration:          importDuration,
		ManifestDuration:        manifestDuration,
		WireSpeedBytesPerSecond: wireSpeed,
		DownloadBytesPerSecond:  downloadSpeed,
		WireSpeedTarget:         opts.WireSpeedTarget,
		TargetMet:               downloadSpeed >= wireSpeed*opts.WireSpeedTarget,
		ImportedRows:            imported,
		LocalRows:               localRows,
		DownloadedShardPath:     downloadPath,
		ShardCID:                exportA.ShardCID,
		ShardSHA256:             exportA.ShardSHA256,
		IndexCID:                exportA.IndexCID,
	}
	return &LiveFlatSQLMultiProviderRangeResult{
		LiveFlatSQLReplicationResult: result,
		ProviderPeerIDs:              providerPeerIDs,
		ProviderBytes:                providerBytes,
		RangeRequests:                rangeRequests,
	}, nil
}

func normalizeLiveFlatSQLReplicationOptions(opts LiveFlatSQLReplicationOptions) LiveFlatSQLReplicationOptions {
	if opts.TargetBytes <= 0 {
		opts.TargetBytes = defaultLiveFlatSQLTargetBytes
	}
	if opts.ProbeBytes <= 0 {
		opts.ProbeBytes = opts.TargetBytes
	}
	if opts.WireSpeedTarget <= 0 || opts.WireSpeedTarget > 1 {
		opts.WireSpeedTarget = 0.90
	}
	if strings.TrimSpace(opts.SchemaName) == "" {
		opts.SchemaName = liveFlatSQLSchema
	}
	if strings.TrimSpace(opts.ProviderID) == "" {
		opts.ProviderID = liveFlatSQLProviderID
	}
	if strings.TrimSpace(opts.SourceName) == "" {
		opts.SourceName = liveFlatSQLSourceName
	}
	if strings.TrimSpace(opts.BatchID) == "" {
		opts.BatchID = liveFlatSQLBatchID
	}
	return opts
}

func normalizeLiveFlatSQLRangeResumeOptions(targetBytes int64, opts LiveFlatSQLRangeResumeOptions) LiveFlatSQLRangeResumeOptions {
	if targetBytes <= 0 {
		targetBytes = defaultLiveFlatSQLResumeBytes
	}
	if opts.InterruptAfterBytes <= 0 || opts.InterruptAfterBytes >= targetBytes {
		opts.InterruptAfterBytes = targetBytes / 3
	}
	if opts.InterruptAfterBytes <= 0 {
		opts.InterruptAfterBytes = 1
	}
	if opts.RangeBytes <= 0 {
		opts.RangeBytes = targetBytes - opts.InterruptAfterBytes
	}
	return opts
}

func liveFlatSQLWorkDir(configured string) (string, func(), error) {
	if strings.TrimSpace(configured) != "" {
		if err := os.MkdirAll(configured, 0o700); err != nil {
			return "", func() {}, fmt.Errorf("create work dir: %w", err)
		}
		return configured, func() {}, nil
	}
	tmpDir, err := os.MkdirTemp("", "sdn-live-flatsql-replication-*")
	if err != nil {
		return "", func() {}, fmt.Errorf("create temp work dir: %w", err)
	}
	return tmpDir, func() { _ = os.RemoveAll(tmpDir) }, nil
}

func buildLiveFlatSQLPublishedShard(ctx context.Context, store *storage.FlatSQLStore, opts LiveFlatSQLReplicationOptions) (*storage.DatasetExport, error) {
	tags := storage.SourceTags{
		ProviderID: opts.ProviderID,
		SourceName: opts.SourceName,
		BatchID:    opts.BatchID,
	}
	records, err := generateLiveFlatSQLExportRecords(ctx, opts.TargetBytes, tags)
	if err != nil {
		return nil, err
	}
	return exportLiveFlatSQLRecords(store, opts, records)
}

func exportLiveFlatSQLRecords(store *storage.FlatSQLStore, opts LiveFlatSQLReplicationOptions, records []storage.DatasetExportRecord) (*storage.DatasetExport, error) {
	exportDir := filepath.Join(store.DatasetPublicationOutputDir(), liveFlatSQLPublicationPathComponent(opts.SchemaName))
	export, err := storage.ExportDatasetRecords(exportDir, storage.IndexedRecordQuery{
		SchemaName:          opts.SchemaName,
		ProviderID:          opts.ProviderID,
		SourceName:          opts.SourceName,
		BatchID:             opts.BatchID,
		Limit:               len(records),
		AllowLargeResultSet: true,
		OrderByCID:          true,
	}, records)
	if err != nil {
		return nil, fmt.Errorf("export published FlatSQL shard: %w", err)
	}
	if err := store.UpsertDatasetShardPublication(storage.DatasetShardPublication{
		SchemaName:   opts.SchemaName,
		ProviderID:   opts.ProviderID,
		SourceName:   opts.SourceName,
		BatchID:      opts.BatchID,
		QueryProfile: storage.DatasetPublicationQueryProfile,
		Offset:       0,
		Limit:        export.RecordCount,
		RecordCount:  export.RecordCount,
		ByteCount:    export.ShardBytes,
		ShardCID:     export.ShardCID,
		IndexCID:     export.IndexCID,
		ManifestCID:  "stress-manifest",
		ShardSHA256:  export.ShardSHA256,
		IndexSHA256:  export.IndexSHA256,
		QuerySHA256:  export.QuerySHA256,
		ResultSHA256: export.ResultSHA256,
		PublishedAt:  time.Now().UTC(),
	}); err != nil {
		return nil, fmt.Errorf("register published FlatSQL shard: %w", err)
	}
	return export, nil
}

func generateLiveFlatSQLExportRecords(ctx context.Context, targetBytes int64, tags storage.SourceTags) ([]storage.DatasetExportRecord, error) {
	generator := NewGenerator()
	records := make([]storage.DatasetExportRecord, 0)
	var shardBytes int64
	for batch := range generator.GenerateBatches(ctx, targetBytes) {
		if batch.Err != nil {
			return nil, batch.Err
		}
		for _, record := range batch.Records {
			records = append(records, storage.DatasetExportRecord{
				CID:        record.CID,
				Data:       record.Data,
				SourceTags: tags,
			})
			shardBytes += int64(len(record.Data)) + 4
		}
		if shardBytes >= targetBytes {
			break
		}
	}
	if len(records) == 0 {
		return nil, fmt.Errorf("generated no FlatSQL records")
	}
	return records, nil
}

func startLiveFlatSQLPeers(ctx context.Context, providerStore *storage.FlatSQLStore) (host.Host, host.Host, error) {
	providerHost, err := libp2p.New(libp2p.ListenAddrStrings("/ip4/127.0.0.1/tcp/0"))
	if err != nil {
		return nil, nil, fmt.Errorf("create provider libp2p host: %w", err)
	}
	providerHost.SetStreamHandler(protocol.FlatSQLSyncProtocolID, protocol.NewFlatSQLSyncHandler(providerStore).HandleStream)
	subscriberHost, err := libp2p.New(libp2p.NoListenAddrs)
	if err != nil {
		_ = providerHost.Close()
		return nil, nil, fmt.Errorf("create subscriber libp2p host: %w", err)
	}
	subscriberHost.Peerstore().AddAddrs(providerHost.ID(), providerHost.Addrs(), peerstore.PermanentAddrTTL)
	if err := subscriberHost.Connect(ctx, peer.AddrInfo{ID: providerHost.ID(), Addrs: providerHost.Addrs()}); err != nil {
		_ = subscriberHost.Close()
		_ = providerHost.Close()
		return nil, nil, fmt.Errorf("connect live FlatSQL peers: %w", err)
	}
	return providerHost, subscriberHost, nil
}

func requestLiveFlatSQLManifest(ctx context.Context, client host.Host, target peer.ID, opts LiveFlatSQLReplicationOptions) (*liveFlatSQLManifest, error) {
	stream, err := client.NewStream(ctx, target, protocol.FlatSQLSyncProtocolID)
	if err != nil {
		return nil, fmt.Errorf("open FlatSQL manifest stream: %w", err)
	}
	defer stream.Close()
	if err := writeLiveFlatSQLFrame(stream, map[string]interface{}{
		"op":            "open_manifest",
		"schema":        opts.SchemaName,
		"provider_id":   opts.ProviderID,
		"source_name":   opts.SourceName,
		"batch_id":      opts.BatchID,
		"query_profile": storage.DatasetPublicationQueryProfile,
	}); err != nil {
		return nil, err
	}
	var manifest liveFlatSQLManifest
	if err := readLiveFlatSQLFrame(stream, &manifest); err != nil {
		return nil, err
	}
	if manifest.TotalCount <= 0 || len(manifest.Segments) == 0 {
		return nil, fmt.Errorf("published FlatSQL manifest has no segments")
	}
	return &manifest, nil
}

func measureLiveFlatSQLWireSpeed(ctx context.Context, client host.Host, target peer.ID, probeBytes int64) (int64, time.Duration, error) {
	stream, err := client.NewStream(ctx, target, protocol.FlatSQLSyncProtocolID)
	if err != nil {
		return 0, 0, fmt.Errorf("open FlatSQL wire-speed stream: %w", err)
	}
	defer stream.Close()
	if err := writeLiveFlatSQLFrame(stream, map[string]interface{}{
		"op":          "wire_speed_probe",
		"probe_bytes": probeBytes,
	}); err != nil {
		return 0, 0, err
	}
	var header liveFlatSQLProbeHeader
	if err := readLiveFlatSQLFrame(stream, &header); err != nil {
		return 0, 0, err
	}
	if header.Status == "error" {
		return 0, 0, fmt.Errorf("wire-speed probe failed: %s", header.Error.Message)
	}
	if header.PayloadBytes <= 0 {
		return 0, 0, fmt.Errorf("wire-speed probe returned no payload bytes")
	}
	started := time.Now()
	readBytes, err := io.CopyN(io.Discard, stream, header.PayloadBytes)
	if err != nil {
		return readBytes, time.Since(started), fmt.Errorf("read wire-speed probe payload: %w", err)
	}
	return readBytes, time.Since(started), nil
}

func downloadLiveFlatSQLShardBatchToFile(ctx context.Context, client host.Host, target peer.ID, opts LiveFlatSQLReplicationOptions, cids []string, outputPath string) (int64, liveFlatSQLBatchHeader, time.Duration, error) {
	stream, err := client.NewStream(ctx, target, protocol.FlatSQLSyncProtocolID)
	if err != nil {
		return 0, liveFlatSQLBatchHeader{}, 0, fmt.Errorf("open published FlatSQL shard stream: %w", err)
	}
	defer stream.Close()
	if err := writeLiveFlatSQLFrame(stream, map[string]interface{}{
		"op":            "read_published_shard_batch",
		"schema":        opts.SchemaName,
		"provider_id":   opts.ProviderID,
		"source_name":   opts.SourceName,
		"batch_id":      opts.BatchID,
		"query_profile": storage.DatasetPublicationQueryProfile,
		"cids":          cids,
	}); err != nil {
		return 0, liveFlatSQLBatchHeader{}, 0, err
	}
	var header liveFlatSQLBatchHeader
	if err := readLiveFlatSQLFrame(stream, &header); err != nil {
		return 0, liveFlatSQLBatchHeader{}, 0, err
	}
	if header.Status == "error" {
		return 0, header, 0, fmt.Errorf("published FlatSQL shard download failed: %s", header.Error.Message)
	}
	if header.ByteCount <= 0 {
		return 0, header, 0, fmt.Errorf("published FlatSQL shard batch returned no bytes")
	}
	if err := os.MkdirAll(filepath.Dir(outputPath), 0o700); err != nil {
		return 0, header, 0, fmt.Errorf("create shard download directory: %w", err)
	}
	tempFile, err := os.CreateTemp(filepath.Dir(outputPath), ".flatsql-shard-*.tmp")
	if err != nil {
		return 0, header, 0, fmt.Errorf("create shard download file: %w", err)
	}
	tempPath := tempFile.Name()
	committed := false
	defer func() {
		if !committed {
			_ = os.Remove(tempPath)
		}
	}()

	started := time.Now()
	readBytes, copyErr := io.CopyN(tempFile, stream, header.ByteCount)
	duration := time.Since(started)
	closeErr := tempFile.Close()
	if copyErr != nil {
		return readBytes, header, duration, fmt.Errorf("read published FlatSQL shard payload after %d bytes: %w", readBytes, copyErr)
	}
	if closeErr != nil {
		return readBytes, header, duration, fmt.Errorf("close shard download file: %w", closeErr)
	}
	if err := os.Rename(tempPath, outputPath); err != nil {
		return readBytes, header, duration, fmt.Errorf("commit shard download file: %w", err)
	}
	committed = true
	return readBytes, header, duration, nil
}

func downloadLiveFlatSQLShardRangeToFile(ctx context.Context, client host.Host, target peer.ID, opts LiveFlatSQLReplicationOptions, cid string, offset int64, length int64, outputPath string) (int64, liveFlatSQLShardHeader, error) {
	stream, err := client.NewStream(ctx, target, protocol.FlatSQLSyncProtocolID)
	if err != nil {
		return 0, liveFlatSQLShardHeader{}, fmt.Errorf("open published FlatSQL shard range stream: %w", err)
	}
	defer stream.Close()
	if err := writeLiveFlatSQLFrame(stream, map[string]interface{}{
		"op":            "read_published_shard",
		"schema":        opts.SchemaName,
		"provider_id":   opts.ProviderID,
		"source_name":   opts.SourceName,
		"batch_id":      opts.BatchID,
		"query_profile": storage.DatasetPublicationQueryProfile,
		"cid":           cid,
		"byte_offset":   offset,
		"byte_length":   length,
	}); err != nil {
		return 0, liveFlatSQLShardHeader{}, err
	}
	var header liveFlatSQLShardHeader
	if err := readLiveFlatSQLFrame(stream, &header); err != nil {
		return 0, liveFlatSQLShardHeader{}, err
	}
	if header.Status == "error" {
		return 0, header, fmt.Errorf("published FlatSQL shard range download failed: %s", header.Error.Message)
	}
	if header.ByteCount <= 0 {
		return 0, header, fmt.Errorf("published FlatSQL shard range returned no bytes")
	}
	if header.ByteOffset != offset {
		return 0, header, fmt.Errorf("published FlatSQL shard range offset = %d, want %d", header.ByteOffset, offset)
	}
	if length > 0 && header.ByteCount != length {
		return 0, header, fmt.Errorf("published FlatSQL shard range byte count = %d, want %d", header.ByteCount, length)
	}
	if err := os.MkdirAll(filepath.Dir(outputPath), 0o700); err != nil {
		return 0, header, fmt.Errorf("create shard download directory: %w", err)
	}
	flag := os.O_CREATE | os.O_WRONLY
	if offset == 0 {
		flag |= os.O_TRUNC
	} else {
		info, err := os.Stat(outputPath)
		if err != nil {
			return 0, header, fmt.Errorf("stat shard resume file: %w", err)
		}
		if info.Size() != offset {
			return 0, header, fmt.Errorf("resume file has %d bytes, want %d", info.Size(), offset)
		}
		flag |= os.O_APPEND
	}
	file, err := os.OpenFile(outputPath, flag, 0o600)
	if err != nil {
		return 0, header, fmt.Errorf("open shard resume file: %w", err)
	}
	readBytes, copyErr := io.CopyN(file, stream, header.ByteCount)
	closeErr := file.Close()
	if copyErr != nil {
		return readBytes, header, fmt.Errorf("read published FlatSQL shard range payload after %d bytes: %w", readBytes, copyErr)
	}
	if closeErr != nil {
		return readBytes, header, fmt.Errorf("close shard resume file: %w", closeErr)
	}
	return readBytes, header, nil
}

func verifyLiveFlatSQLShardFile(path string, expectedSHA256 string) error {
	file, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open downloaded FlatSQL shard: %w", err)
	}
	defer file.Close()
	hasher := sha256.New()
	reader := bufio.NewReaderSize(file, 1024*1024)
	offset := int64(0)
	for index := 0; ; index++ {
		var header [4]byte
		n, err := io.ReadFull(reader, header[:])
		if err == io.EOF && n == 0 {
			break
		}
		if err != nil {
			return fmt.Errorf("FlatSQL shard truncated at frame %d header: %w", index, err)
		}
		hasher.Write(header[:])
		length := int64(binary.LittleEndian.Uint32(header[:]))
		offset += 4
		if length < 0 {
			return fmt.Errorf("FlatSQL shard frame %d has negative length", index)
		}
		if _, err := io.CopyN(hasher, reader, length); err != nil {
			return fmt.Errorf("FlatSQL shard truncated at frame %d payload offset %d: %w", index, offset, err)
		}
		offset += length
	}
	if strings.TrimSpace(expectedSHA256) != "" {
		if got := hex.EncodeToString(hasher.Sum(nil)); got != expectedSHA256 {
			return fmt.Errorf("published FlatSQL shard SHA-256 = %s, want %s", got, expectedSHA256)
		}
	}
	return nil
}

func writeLiveFlatSQLFrame(writer io.Writer, payload interface{}) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	if len(data) > math.MaxUint32 {
		return fmt.Errorf("FlatSQL sync frame exceeds uint32 length")
	}
	var header [4]byte
	binary.BigEndian.PutUint32(header[:], uint32(len(data)))
	if _, err := writer.Write(header[:]); err != nil {
		return err
	}
	_, err = writer.Write(data)
	return err
}

func readLiveFlatSQLFrame(reader io.Reader, target interface{}) error {
	var header [4]byte
	if _, err := io.ReadFull(reader, header[:]); err != nil {
		return fmt.Errorf("read FlatSQL sync frame header: %w", err)
	}
	length := binary.BigEndian.Uint32(header[:])
	if length == 0 {
		return fmt.Errorf("empty FlatSQL sync frame")
	}
	if length > 32*1024*1024 {
		return fmt.Errorf("FlatSQL sync frame exceeds 32 MiB")
	}
	payload := make([]byte, length)
	if _, err := io.ReadFull(reader, payload); err != nil {
		return fmt.Errorf("read FlatSQL sync frame payload: %w", err)
	}
	if err := json.Unmarshal(payload, target); err != nil {
		return fmt.Errorf("decode FlatSQL sync frame: %w", err)
	}
	return nil
}

func getLiveFlatSQLTargetBytes() int64 {
	for _, name := range []string{"STRESS_LIVE_FLATSQL_BYTES", "STRESS_FLATSQL_REPLICATION_BYTES"} {
		raw := strings.TrimSpace(os.Getenv(name))
		if raw == "" {
			continue
		}
		value, err := strconv.ParseInt(raw, 10, 64)
		if err == nil && value > 0 {
			return value
		}
	}
	return defaultLiveFlatSQLTargetBytes
}

func getLiveFlatSQLResumeTargetBytes() int64 {
	for _, name := range []string{"STRESS_LIVE_FLATSQL_RESUME_BYTES", "STRESS_FLATSQL_RESUME_BYTES"} {
		raw := strings.TrimSpace(os.Getenv(name))
		if raw == "" {
			continue
		}
		value, err := strconv.ParseInt(raw, 10, 64)
		if err == nil && value > 0 {
			return value
		}
	}
	return defaultLiveFlatSQLResumeBytes
}

func bytesPerSecond(bytes int64, duration time.Duration) float64 {
	if bytes <= 0 || duration <= 0 {
		return 0
	}
	return float64(bytes) / duration.Seconds()
}

func bytesPerSecondToMiB(value float64) float64 {
	return value / (1024 * 1024)
}

func liveFlatSQLPublicationPathComponent(value string) string {
	value = strings.TrimSpace(value)
	value = strings.TrimSuffix(value, ".fbs")
	replacer := strings.NewReplacer("/", "-", "\\", "-", ":", "-", " ", "-")
	value = replacer.Replace(value)
	if value == "" {
		return "dataset"
	}
	return value
}
