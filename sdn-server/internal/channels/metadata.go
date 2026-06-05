package channels

import (
	"encoding/hex"
	"sync"
	"time"
)

type VerifiedMetadata struct {
	ChannelID         string
	PNMCID            string
	PNMFileID         string
	DPMFileID         string
	SignatureType     string
	VerifiedAt        time.Time
	DPMVerifiedAt     time.Time
	ProviderPeer      string
	ProviderPublicKey string
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
		ChannelID:         channel.ChannelID,
		PNMCID:            evidence.CID,
		PNMFileID:         evidence.FileID,
		SignatureType:     evidence.SignatureType,
		VerifiedAt:        time.Now().UTC(),
		ProviderPublicKey: hex.EncodeToString(evidence.ProviderPublicKey),
	}
	if r == nil {
		return metadata
	}
	r.mu.Lock()
	r.metadata[channel.ChannelID] = metadata
	r.mu.Unlock()
	return metadata
}

func (r *VerifiedMetadataRegistry) RecordDPM(channel ChannelID, evidence DPMTrustEvidence) (VerifiedMetadata, bool) {
	if r == nil {
		return VerifiedMetadata{}, false
	}
	r.mu.Lock()
	metadata, ok := r.metadata[channel.ChannelID]
	if ok {
		metadata.DPMFileID = evidence.FileID
		metadata.DPMVerifiedAt = time.Now().UTC()
		if evidence.ProviderPeer != "" {
			metadata.ProviderPeer = evidence.ProviderPeer
		}
		r.metadata[channel.ChannelID] = metadata
	}
	r.mu.Unlock()
	return metadata, ok
}

func (r *VerifiedMetadataRegistry) RecordNativeStream(channel ChannelID, snapshot NativeStreamSnapshot) (VerifiedMetadata, bool) {
	if r == nil {
		return VerifiedMetadata{}, false
	}
	r.mu.Lock()
	metadata, ok := r.metadata[channel.ChannelID]
	if ok {
		metadata.SyncedBytes = int64(snapshot.ByteCount)
		metadata.SyncedRows = snapshot.FrameCount
		metadata.RemoteRows = snapshot.FrameCount
		metadata.LocalRows = snapshot.FrameCount
		metadata.MissingRows = 0
		metadata.PinnedRows = snapshot.FrameCount
		r.metadata[channel.ChannelID] = metadata
	}
	r.mu.Unlock()
	return metadata, ok
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
