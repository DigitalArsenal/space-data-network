package channels

import (
	"sync"
	"time"
)

type VerifiedMetadata struct {
	ChannelID         string
	PNMCID            string
	PNMFileID         string
	SignatureType     string
	VerifiedAt        time.Time
	ProviderPeer      string
	LocalRows         int
	RemoteRows        int
	SyncedRows        int
	MissingRows       int
	PinnedRows        int
	SyncedBytes       int64
	ThroughputBPS     int64
	WireUtilization   *float64
	LastImportFailure string
}

type VerifiedMetadataRegistry struct {
	mu       sync.RWMutex
	metadata map[string]VerifiedMetadata
}

func NewVerifiedMetadataRegistry() *VerifiedMetadataRegistry {
	return &VerifiedMetadataRegistry{metadata: make(map[string]VerifiedMetadata)}
}

func (r *VerifiedMetadataRegistry) RecordPNM(channel ChannelID, evidence PNMTrustEvidence) VerifiedMetadata {
	metadata := VerifiedMetadata{
		ChannelID:     channel.ChannelID,
		PNMCID:        evidence.CID,
		PNMFileID:     evidence.FileID,
		SignatureType: evidence.SignatureType,
		VerifiedAt:    time.Now().UTC(),
	}
	if r == nil {
		return metadata
	}
	r.mu.Lock()
	r.metadata[channel.ChannelID] = metadata
	r.mu.Unlock()
	return metadata
}

func (r *VerifiedMetadataRegistry) Get(channel ChannelID) (VerifiedMetadata, bool) {
	if r == nil {
		return VerifiedMetadata{}, false
	}
	r.mu.RLock()
	metadata, ok := r.metadata[channel.ChannelID]
	r.mu.RUnlock()
	return metadata, ok
}
