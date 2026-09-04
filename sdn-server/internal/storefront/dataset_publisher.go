package storefront

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/spacedatanetwork/sdn-server/internal/storage"
)

// ListingDatasetAsset is one pinned shard of a one-time listing's record set:
// the CID Kubo serves it under, the SHA-256 and length of the shard bytes on
// disk, and the index that rides with it.
type ListingDatasetAsset struct {
	Schema     string
	CID        string
	SHA256     string
	ByteLength uint64
	IndexCID   string
	ShardPath  string
}

// ListingDatasetPublisher turns a listing's stored records into a pinned
// dataset shard the DPM can advertise. Before this seam the storefront hashed
// the record set in memory into a raw-leaf CID that nothing stored or pinned,
// so every one-time listing advertised bytes no node could fetch (PUB-03).
type ListingDatasetPublisher interface {
	PublishListingDataset(ctx context.Context, listingID, updateID string, filter storage.IndexedRecordQuery, records []storage.DatasetExportRecord) (*ListingDatasetAsset, error)
}

// KuboListingDatasetPublisher is the daemon's publisher: the records go
// through the same export pipeline as dataset publications and archives (a
// shard file plus its index under the node's publication directory) and are
// pinned through Kubo. The CID it returns is the one Kubo serves; the
// SHA-256 is of the shard bytes, so a fetched shard verifies whatever
// chunking produced the CID.
type KuboListingDatasetPublisher struct {
	IPFSAPIURL string
	OutputDir  string
}

// PublishListingDataset exports and pins one schema's records for a listing.
// Each (listing, schema, update) gets its own immutable export directory.
func (p *KuboListingDatasetPublisher) PublishListingDataset(ctx context.Context, listingID, updateID string, filter storage.IndexedRecordQuery, records []storage.DatasetExportRecord) (*ListingDatasetAsset, error) {
	if p == nil || strings.TrimSpace(p.IPFSAPIURL) == "" {
		return nil, errors.New("publishing a stored-records listing needs Kubo to pin the shard: set admin.ipfs_api_url (or run from a bundle, which manages Kubo)")
	}
	if strings.TrimSpace(p.OutputDir) == "" {
		return nil, errors.New("listing dataset publisher needs an output directory")
	}
	if strings.TrimSpace(filter.SchemaName) == "" {
		return nil, errors.New("listing dataset publisher needs a schema")
	}
	dir := filepath.Join(p.OutputDir, listingPathComponent(listingID), listingPathComponent(filter.SchemaName), listingPathComponent(updateID))
	export, err := storage.ExportDatasetRecords(dir, filter, records)
	if err != nil {
		return nil, fmt.Errorf("export listing %s records: %w", filter.SchemaName, err)
	}
	published, err := storage.PublishDatasetExportToIPFS(ctx, p.IPFSAPIURL, export)
	if err != nil {
		return nil, fmt.Errorf("pin listing %s shard: %w", filter.SchemaName, err)
	}
	return &ListingDatasetAsset{
		Schema:     filter.SchemaName,
		CID:        published.ShardCID,
		SHA256:     export.ShardSHA256,
		ByteLength: uint64(export.ShardBytes),
		IndexCID:   published.IndexCID,
		ShardPath:  export.ShardPath,
	}, nil
}

// listingPathComponent keeps identifiers on one directory level.
func listingPathComponent(value string) string {
	value = strings.TrimSpace(strings.TrimSuffix(value, ".fbs"))
	value = strings.NewReplacer("/", "-", "\\", "-", ":", "-", " ", "-", "..", "-").Replace(value)
	if value == "" {
		return "unnamed"
	}
	return value
}
