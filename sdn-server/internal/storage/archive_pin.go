package storage

// archive_pin.go — the immutable archive-pin plane (Hermes ruling 2026-07-24,
// graph task pin-archive-plane): the THIRD storage plane, distinct from the
// live FlatSQL DB and from the replication/heads plane
// (sdn_dataset_shard_publications + feed-head announcements).
//
// An archive materializes an arbitrary IndexedRecordQuery selection through
// the EXISTING deterministic export path (ExportDatasetWindow, which already
// persists the canonical query JSON) into a signed shard + index + DPM
// provenance manifest, pins all three CIDs, and records them in the pin
// ledger as role='archive' with TTL=0 — permanent.
//
// Plane isolation, structurally enforced:
//   - Archives are pin-ledger-ONLY. They are NEVER registered in
//     sdn_dataset_shard_publications (UpsertDatasetShardPublication refuses
//     ArchiveQueryProfile), so the supersede path (SupersedeSourceBatches,
//     which deletes superseded publications' cached shard/index FILES) and
//     the feed catch-up/replication machinery can never see, retire, or
//     re-materialize them.
//   - Archive artifacts are written under their own sibling directory
//     ("dataset-archives", ArchiveOutputDir) — no publication file sweep
//     (SupersedeSourceBatches, RemoveStaleShardGroupCARFiles) ever computes
//     a path inside it, and stream-file compaction (CompactStreams) only
//     rewrites flatsql-streams/*.flatsql + the record-catalog journal, so
//     the archive plane is untouched by compaction by construction: even if
//     every live row an archive was selected from is later evicted and
//     compacted away, the archive shard still carries the record bytes.
//   - The TipQueue TTL sweep skips role='archive' CIDs via the pin-role
//     resolver (pubsub.TipQueue.SetPinRoleResolver), and the shard-group CAR
//     retirement path skips role='archive' ledger entries explicitly.
//
// Boundary (task ruling): export/pin here is HOST STORAGE CONNECTOR work.
// WASM modules own only the selection policy — they hand over a query via a
// query capability; they never drive pinning or ledger writes directly.
//
// Provenance: the archive DPM embeds the canonical replay query
// (QUERY.CANONICAL_QUERY / QUERY_SHA256 / RESULT_SHA256), the producer's
// Ed25519 signature (PROVIDER_SIGNATURE), and one OTHER-kind content-addressed
// DPMAsset per source feed head (CID = the source feed head's DPM manifest
// CID, DATA_ROOT = the feed-head chain value, FILE_ID = the source feed key)
// — existing DPM v1.0.6 fields only, no schema extension.

import (
	"context"
	"crypto/ed25519"
	"fmt"
	"path/filepath"
	"strings"
	"time"
)

// PinLedgerRoleArchive is the pin-ledger role of the immutable archive plane.
// Entries with this role are permanent (TTL=0): every TTL/supersede sweep
// MUST skip them.
const PinLedgerRoleArchive = "archive"

// ArchiveQueryProfile marks archive-plane pin-ledger entries. It is also the
// query-profile value UpsertDatasetShardPublication structurally refuses, so
// an archive can never be registered in sdn_dataset_shard_publications.
const ArchiveQueryProfile = "archive-pin-v1"

// archivePlaneDirName is the sibling directory (next to "dataset-publications")
// holding archive shard/index/manifest files. Publication file sweeps never
// compute paths inside it.
const archivePlaneDirName = "dataset-archives"

// ArchiveSourceFeedHead names one source feed head an archive selection was
// materialized from. ManifestCID is the head publication's DPM manifest CID;
// FeedHead is the feed's chain-head value at archive time.
type ArchiveSourceFeedHead struct {
	SchemaName   string
	ProviderID   string
	SourceName   string
	BatchID      string
	QueryProfile string
	FeedHead     string
	ManifestCID  string
}

// ArchiveDatasetOptions controls one archive materialization.
type ArchiveDatasetOptions struct {
	// ArchiveID is the stable dataset identifier for this archive (DPM
	// DATASET_ID and the pin-ledger batch key). Required.
	ArchiveID string
	// OutputDir overrides the archive plane directory (default
	// ArchiveOutputDir()).
	OutputDir      string
	ProviderPeerID string
	ProviderEPMCID string
	SigningKey     ed25519.PrivateKey
	SchemaHash     string
	PublishedAt    time.Time
	// SourceFeedHeads is the feed-head provenance embedded in the manifest.
	// nil = derive automatically from the store's current shard publications
	// matching the filter (ArchiveSourceFeedHeads).
	SourceFeedHeads []ArchiveSourceFeedHead
	// IPFSAPIURL, when set, pins shard+index+manifest through Kubo. When
	// empty the artifacts are still exported, signed, and ledgered (the
	// caller owns pinning).
	IPFSAPIURL string
}

// DatasetArchive reports one materialized archive.
type DatasetArchive struct {
	Export     *DatasetExport
	Manifest   *DatasetPublicationManifest
	PinEntries []PinLedgerEntry
}

// ArchiveOutputDir is the archive plane's on-disk root, a sibling of
// DatasetPublicationOutputDir.
func (s *FlatSQLStore) ArchiveOutputDir() string {
	if s == nil {
		return ""
	}
	return filepath.Join(filepath.Dir(s.basePath), archivePlaneDirName)
}

// ArchiveSourceFeedHeads derives the current feed-head provenance for a
// selection: the newest shard publication per (provider, source, batch) group
// of the filter's schema, carrying its feed-head chain value and DPM manifest
// CID.
func (s *FlatSQLStore) ArchiveSourceFeedHeads(filter IndexedRecordQuery) ([]ArchiveSourceFeedHead, error) {
	if s == nil {
		return nil, fmt.Errorf("store is required")
	}
	if strings.TrimSpace(filter.SchemaName) == "" {
		return nil, fmt.Errorf("schema name is required")
	}
	publications, err := s.ListDatasetShardPublications(DatasetShardPublicationQuery{
		SchemaName:   filter.SchemaName,
		ProviderID:   filter.ProviderID,
		SourceName:   filter.SourceName,
		BatchID:      filter.BatchID,
		QueryProfile: DatasetPublicationQueryProfile,
	})
	if err != nil {
		return nil, fmt.Errorf("list source publications for archive provenance: %w", err)
	}
	type headKey struct{ providerID, sourceName, batchID string }
	newest := map[headKey]DatasetShardPublication{}
	order := make([]headKey, 0)
	for _, pub := range publications {
		key := headKey{pub.ProviderID, pub.SourceName, pub.BatchID}
		existing, seen := newest[key]
		if !seen {
			order = append(order, key)
		}
		if !seen || pub.PublishedAt.After(existing.PublishedAt) ||
			(pub.PublishedAt.Equal(existing.PublishedAt) && pub.FeedSequence > existing.FeedSequence) {
			newest[key] = pub
		}
	}
	heads := make([]ArchiveSourceFeedHead, 0, len(order))
	for _, key := range order {
		pub := newest[key]
		heads = append(heads, ArchiveSourceFeedHead{
			SchemaName:   pub.SchemaName,
			ProviderID:   pub.ProviderID,
			SourceName:   pub.SourceName,
			BatchID:      pub.BatchID,
			QueryProfile: pub.QueryProfile,
			FeedHead:     pub.FeedHead,
			ManifestCID:  pub.ManifestCID,
		})
	}
	return heads, nil
}

// dpmAuxiliaryAssetForFeedHead maps one source feed head onto an OTHER-kind
// DPM asset using existing DPMAsset fields: CID = the head DPM manifest CID,
// DATA_ROOT = the feed-head chain value, FILE_ID = the source feed key.
func dpmAuxiliaryAssetForFeedHead(head ArchiveSourceFeedHead) DPMAuxiliaryAsset {
	feedKey := strings.Join([]string{
		strings.TrimSpace(head.SchemaName),
		strings.TrimSpace(head.ProviderID),
		strings.TrimSpace(head.SourceName),
		strings.TrimSpace(head.BatchID),
		strings.TrimSpace(head.QueryProfile),
	}, ":")
	return DPMAuxiliaryAsset{
		CID:        strings.TrimSpace(head.ManifestCID),
		FileName:   "SOURCE_FEED_HEAD",
		FileID:     feedKey,
		DataRoot:   strings.TrimSpace(head.FeedHead),
		SchemaName: strings.TrimSpace(head.SchemaName),
	}
}

// ArchiveDatasetSelection materializes one query-selected archive: export via
// the existing deterministic path, sign a DPM provenance manifest embedding
// the canonical query + source feed-head CIDs, optionally pin all three
// artifacts through Kubo, and record them as permanent (role='archive',
// TTL=0) pin-ledger entries. It never writes sdn_dataset_shard_publications.
func (s *FlatSQLStore) ArchiveDatasetSelection(ctx context.Context, filter IndexedRecordQuery, opts ArchiveDatasetOptions) (*DatasetArchive, error) {
	if s == nil {
		return nil, fmt.Errorf("store is required")
	}
	opts.ArchiveID = strings.TrimSpace(opts.ArchiveID)
	if opts.ArchiveID == "" {
		return nil, fmt.Errorf("archive id is required")
	}
	if strings.ContainsAny(opts.ArchiveID, "/\\") {
		// The archive ID becomes part of the immutable manifest file name.
		return nil, fmt.Errorf("archive id must not contain path separators")
	}
	opts.ProviderPeerID = strings.TrimSpace(opts.ProviderPeerID)
	if opts.ProviderPeerID == "" {
		return nil, fmt.Errorf("provider peer id is required")
	}
	if len(opts.SigningKey) != ed25519.PrivateKeySize {
		return nil, fmt.Errorf("ed25519 signing key is required")
	}
	if opts.PublishedAt.IsZero() {
		opts.PublishedAt = time.Now().UTC()
	}
	outputDir := strings.TrimSpace(opts.OutputDir)
	if outputDir == "" {
		outputDir = s.ArchiveOutputDir()
	}

	heads := opts.SourceFeedHeads
	if heads == nil {
		derived, err := s.ArchiveSourceFeedHeads(filter)
		if err != nil {
			return nil, err
		}
		heads = derived
	}
	auxiliaryAssets := make([]DPMAuxiliaryAsset, 0, len(heads))
	for _, head := range heads {
		if strings.TrimSpace(head.ManifestCID) == "" {
			return nil, fmt.Errorf("source feed head for %s/%s/%s has no manifest CID", head.SchemaName, head.ProviderID, head.SourceName)
		}
		auxiliaryAssets = append(auxiliaryAssets, dpmAuxiliaryAssetForFeedHead(head))
	}

	export, err := s.ExportDatasetWindow(outputDir, filter)
	if err != nil {
		return nil, fmt.Errorf("archive export: %w", err)
	}
	manifest, err := BuildSignedDatasetPublicationManifest(outputDir, DatasetPublicationManifestOptions{
		Export:          export,
		DatasetID:       opts.ArchiveID,
		UpdateID:        opts.PublishedAt.UTC().Format("20060102T150405.000000000Z"),
		ProviderPeerID:  opts.ProviderPeerID,
		ProviderEPMCID:  opts.ProviderEPMCID,
		PublishedAt:     opts.PublishedAt,
		SigningKey:      opts.SigningKey,
		SchemaHash:      opts.SchemaHash,
		AuxiliaryAssets: auxiliaryAssets,
	})
	if err != nil {
		return nil, fmt.Errorf("archive manifest: %w", err)
	}

	if strings.TrimSpace(opts.IPFSAPIURL) != "" {
		if _, err := PublishDatasetExportToIPFS(ctx, opts.IPFSAPIURL, export); err != nil {
			return nil, fmt.Errorf("pin archive export: %w", err)
		}
		if _, err := PublishDatasetPublicationManifestToIPFS(ctx, opts.IPFSAPIURL, manifest); err != nil {
			return nil, fmt.Errorf("pin archive manifest: %w", err)
		}
	}

	providerPublicKey := ""
	if pubKey, ok := opts.SigningKey.Public().(ed25519.PublicKey); ok {
		providerPublicKey = fmt.Sprintf("%x", []byte(pubKey))
	}
	entries := []PinLedgerEntry{
		{CID: export.ShardCID, ByteHash: export.ShardSHA256, RowCount: int64(export.RecordCount), ByteCount: export.ShardBytes},
		{CID: export.IndexCID, ByteHash: export.IndexSHA256, ByteCount: export.IndexBytes},
		{CID: manifest.CID, ByteHash: manifest.SHA256, ByteCount: manifest.ByteLength},
	}
	recorded := make([]PinLedgerEntry, 0, len(entries))
	for _, entry := range entries {
		entry.SchemaName = export.SchemaName
		entry.ProviderPeerID = opts.ProviderPeerID
		entry.ProviderPublicKey = providerPublicKey
		entry.BatchID = opts.ArchiveID
		entry.QueryProfile = ArchiveQueryProfile
		entry.Role = PinLedgerRoleArchive
		entry.SnapshotID = manifest.CID
		entry.Head = export.QuerySHA256
		entry.TTL = 0 // permanent: archive-plane pins are never swept
		entry.VerificationState = "verified"
		entry.VerifiedAt = opts.PublishedAt
		entry.UpdatedAt = opts.PublishedAt
		if err := s.UpsertPinLedgerEntry(entry); err != nil {
			return nil, fmt.Errorf("record archive pin ledger %s: %w", entry.CID, err)
		}
		recorded = append(recorded, entry)
	}
	return &DatasetArchive{Export: export, Manifest: manifest, PinEntries: recorded}, nil
}

// IsArchivePinnedCID reports whether cidValue is held by the immutable
// archive plane (a role='archive' pin-ledger row). Sweeps consult this to
// skip permanent pins. A ledger read failure is treated as PROTECTED: wrongly
// skipping an unpin only delays reclamation, while wrongly unpinning an
// archive is permanent loss.
func (s *FlatSQLStore) IsArchivePinnedCID(cidValue string) bool {
	if s == nil {
		return false
	}
	cidValue = strings.TrimSpace(cidValue)
	if cidValue == "" {
		return false
	}
	entries, err := s.ListPinLedgerEntries(PinLedgerQuery{CID: cidValue, Role: PinLedgerRoleArchive})
	if err != nil {
		log.Warnf("archive pin lookup for %s failed; treating as archive-protected until the ledger is readable: %v", cidValue, err)
		return true
	}
	return len(entries) > 0
}
